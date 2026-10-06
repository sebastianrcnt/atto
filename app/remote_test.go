package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agent"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/tui"
)

// remoteModel answers by the last message: "block" waits until the
// request goes away, "tool" runs a long command, a command's result and
// anything else get "answer to <text>".
type remoteModel struct {
	*httptest.Server
	mu      sync.Mutex
	blocked int
}

func newRemoteModel(t *testing.T) *remoteModel {
	m := &remoteModel{}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(raw, &body)
		last := body.Messages[len(body.Messages)-1]
		text, _ := last.Content.(string)
		// An interrupted "block" stays in the history, and requests merge it
		// into the user message after it.
		text = strings.TrimPrefix(text, "block\n\n")
		switch {
		case last.Role == "user" && text == "block":
			m.mu.Lock()
			m.blocked++
			m.mu.Unlock()
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		case last.Role == "user" && text == "fail":
			http.Error(w, `{"error":{"message":"invalid request"}}`, http.StatusBadRequest)
			return
		case last.Role == "user" && text == "tool":
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"bash","arguments":"{\"description\":\"Wait\",\"command\":\"sleep 30\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
		default:
			if last.Role == "tool" {
				text = "tool"
			}
			j, _ := json.Marshal("answer to " + text)
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s},\"finish_reason\":\"stop\"}]}\n\n", j)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(m.Close)
	return m
}

func (m *remoteModel) blocks() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.blocked
}

// remoteApp is an App talking to model, with /remote on 127.0.0.1 on a
// free port.
func remoteApp(t *testing.T, model *remoteModel) *App {
	t.Helper()
	t.Setenv("ATTO_DIR", t.TempDir())
	writeTestFile(t, config.ModelsPath(), `{"providers":{"t":{"baseUrl":"`+model.URL+`","models":[`+
		`{"id":"m","contextWindow":100000},{"id":"m2","contextWindow":100000,"reasoning":true}]}}}`)
	models, err := config.LoadModels()
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := models.Find("t", "m")
	cwd := t.TempDir()
	a := &App{ui: tui.New(nullTerm{}), models: models, agent: agent.New(ref, "", cwd), tools: map[string]*toolBlock{}, cwd: cwd, quit: make(chan struct{})}
	a.build()
	a.remoteHost = "127.0.0.1"
	a.remotePort = new(int) // any free port
	a.newSession("")
	t.Cleanup(func() {
		a.ui.Do(func() {
			if a.cancel != nil {
				a.cancel()
			}
			a.stopRemote()
		})
		waitIdle(t, a)
		jobs.KillAll(a.sess.ID)
		a.sess.Close()
	})
	return a
}

type rmsg struct {
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
	Result map[string]any `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type remoteClient struct {
	t           *testing.T
	base, token string
}

func (a *App) remoteClient(t *testing.T) remoteClient {
	var c remoteClient
	a.ui.Do(func() { c = remoteClient{t, "http://" + a.remote.addr, a.remote.token} })
	return c
}

func (c remoteClient) post(method string, params any) (*http.Response, error) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, _ := http.NewRequest("POST", c.base+"/rpc", strings.NewReader(string(b)))
	req.Header.Set("Authorization", "Bearer "+c.token)
	return http.DefaultClient.Do(req)
}

func (c remoteClient) call(method string, params any) rmsg {
	c.t.Helper()
	resp, err := c.post(method, params)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		c.t.Fatalf("%s: HTTP %d", method, resp.StatusCode)
	}
	var m rmsg
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return m
}

func (c remoteClient) must(method string, params any) map[string]any {
	c.t.Helper()
	m := c.call(method, params)
	if m.Error != nil {
		c.t.Fatalf("%s: %s", method, m.Error.Message)
	}
	return m.Result
}

// events follows the event stream; the channel closes when it ends.
func (c remoteClient) events(from float64) <-chan rmsg {
	c.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	c.t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/events?token=%s&lastEventId=%d", c.base, c.token, int64(from)), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	ch := make(chan rmsg, 1000)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var m rmsg
				_ = json.Unmarshal([]byte(data), &m)
				ch <- m
			}
		}
	}()
	return ch
}

// until collects notifications until one with method (and, when given,
// matching ok) arrives.
func until(t *testing.T, ch <-chan rmsg, method string, ok func(rmsg) bool) (rmsg, []rmsg) {
	t.Helper()
	var seen []rmsg
	timeout := time.After(10 * time.Second)
	for {
		select {
		case m, open := <-ch:
			if !open {
				t.Fatalf("event stream ended waiting for %s", method)
			}
			seen = append(seen, m)
			if m.Method == method && (ok == nil || ok(m)) {
				return m, seen
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %s", method)
		}
	}
}

func item(m rmsg) map[string]any { it, _ := m.Params["item"].(map[string]any); return it }

func remoteUserBlocks(a *App) []*userBlock {
	var out []*userBlock
	a.ui.Do(func() {
		for _, c := range a.ui.Body.Children {
			if g, ok := c.(gap); ok {
				if u, ok := g.Component.(*userBlock); ok {
					out = append(out, u)
				}
			}
		}
	})
	return out
}

func TestRemoteLiveSession(t *testing.T) {
	model := newRemoteModel(t)
	a := remoteApp(t, model)
	runTurn(t, a, "first")

	a.ui.Do(func() { a.cmdRemote("on") })
	c := a.remoteClient(t)
	text := bodyText(a)
	if !strings.Contains(text, c.base+"/#token="+c.token) || !strings.Contains(text, "█") || !strings.Contains(text, "/remote off") {
		t.Fatalf("the notice should show the link and its QR code:\n%s", text)
	}

	// The token is required.
	if r, _ := http.Post(c.base+"/rpc", "application/json", strings.NewReader(`{"method":"initialize"}`)); r.StatusCode != 401 {
		t.Fatalf("no token: %d", r.StatusCode)
	}
	if r, _ := (remoteClient{t, c.base, "0123"}).post("initialize", nil); r.StatusCode != 401 {
		t.Fatalf("wrong token: %d", r.StatusCode)
	}
	var sess string
	a.ui.Do(func() { sess = a.sess.ID })
	if init := c.must("initialize", nil); init["live"] != true || init["threadId"] != sess {
		t.Fatalf("initialize %v", init)
	}

	// Replay: the conversation so far.
	read := c.must("thread/read", nil)
	var types []string
	for _, it := range read["items"].([]any) {
		m := it.(map[string]any)
		types = append(types, m["type"].(string)+":"+fmt.Sprint(m["text"]))
	}
	if got := strings.Join(types, ","); got != "userMessage:first,agentMessage:answer to first" || read["threadId"] != sess || read["model"] != "t/m" {
		t.Fatalf("thread/read items %s (%v)", got, read)
	}
	eid, _ := read["eventId"].(float64) // omitted while 0
	ev := c.events(eid)
	within(t, a, "the client to connect", func() bool { return a.remote.clients == 1 })
	var status string
	a.ui.Do(func() { status = plainLines(a.renderStatus(100)) })
	if !strings.Contains(status, "remote · 1 connected") {
		t.Fatalf("status line %q", status)
	}

	// Sent from the phone: a turn, streamed, and marked in the terminal.
	if r := c.must("turn/start", map[string]any{"threadId": sess, "input": "hello"}); r["status"] != "started" {
		t.Fatalf("turn/start %v", r)
	}
	done, seen := until(t, ev, "turn/completed", nil)
	var got []string
	for _, m := range seen {
		if it := item(m); it != nil {
			got = append(got, m.Method+" "+it["type"].(string))
		} else if m.Method == "item/delta" {
			got = append(got, m.Method)
		}
	}
	want := "item/started userMessage,item/completed userMessage,item/started agentMessage,item/delta,item/completed agentMessage"
	if strings.Join(got, ",") != want || done.Params["status"] != "completed" || done.Params["threadId"] != sess {
		t.Fatalf("notifications %v (%v)", got, done.Params)
	}
	ub := remoteUserBlocks(a)
	if last := ub[len(ub)-1]; last.text != "hello" || !last.remote || ub[0].remote {
		t.Fatalf("user blocks: last %+v, first remote %v", last, ub[0].remote)
	}
	if lines := plainLines(ub[len(ub)-1].Render(60)); !strings.Contains(lines, "from remote") {
		t.Fatalf("no remote mark:\n%s", lines)
	}
	// The terminal's notices come along, in place, on a later read too.
	again := c.must("thread/read", nil)["items"].([]any)
	if last := again[len(again)-1].(map[string]any); last["type"] != "notice" || !strings.HasPrefix(last["text"].(string), "Worked for") ||
		again[len(again)-2].(map[string]any)["text"] != "answer to hello" {
		t.Fatalf("items after the turn: %v", again)
	}

	// While a turn runs, input steers it; Esc from the phone interrupts,
	// and the steer goes out at once, as in the terminal.
	c.must("turn/start", map[string]any{"input": "block"})
	within(t, a, "the blocking request", func() bool { return model.blocks() == 1 })
	if r := c.must("turn/start", map[string]any{"input": "more"}); r["status"] != "steered" {
		t.Fatalf("send while busy: %v", r)
	}
	c.must("turn/interrupt", nil)
	if m, _ := until(t, ev, "turn/completed", nil); m.Params["status"] != "interrupted" {
		t.Fatalf("interrupt: %v", m.Params)
	}
	if m, _ := until(t, ev, "item/completed", func(m rmsg) bool { return item(m)["type"] == "agentMessage" }); item(m)["text"] != "answer to more" {
		t.Fatalf("steer after interrupt: %v", item(m))
	}
	until(t, ev, "turn/completed", nil)
	ub = remoteUserBlocks(a)
	if last := ub[len(ub)-1]; last.text != "more" || !last.remote {
		t.Fatalf("the steer's message %+v", last)
	}

	// Ctrl+B from the phone, with no command running.
	if m := c.call("turn/background", nil); m.Error == nil {
		t.Fatal("background with nothing running")
	}
	// (Moving a command to the background needs atto's shell host, which
	// these tests don't run; the server's live session test covers the call.)

	// Model and effort, from the phone and seen by the terminal.
	if r := c.must("thread/setModel", map[string]any{"model": "t/m2"}); r["model"] != "t/m2" {
		t.Fatalf("setModel %v", r)
	}
	if m, _ := until(t, ev, "thread/updated", nil); m.Params["thread"].(map[string]any)["model"] != "t/m2" {
		t.Fatalf("thread/updated %v", m.Params)
	}
	if r := c.must("thread/setEffort", map[string]any{"effort": "low"}); r["effort"] != "low" {
		t.Fatalf("setEffort %v", r)
	}
	var cur config.ModelRef
	var effort string
	a.ui.Do(func() { cur, effort = a.model(), a.effort() })
	if cur.Model.ID != "m2" || effort != "low" {
		t.Fatalf("terminal has %s/%s", cur.Model.ID, effort)
	}
	if m := c.call("thread/setEffort", map[string]any{"effort": "ludicrous"}); m.Error == nil {
		t.Fatal("unknown effort accepted")
	}

	// /clear in the terminal: the clients follow to the new session.
	a.ui.Do(func() { a.cmdClear("") })
	m, _ := until(t, ev, "thread/switched", nil)
	var now string
	a.ui.Do(func() { now = a.sess.ID })
	if m.Params["threadId"] != now || m.Params["previousThreadId"] != sess || now == sess {
		t.Fatalf("switched %v (now %s, was %s)", m.Params, now, sess)
	}
	r := c.must("thread/read", map[string]any{"threadId": now})
	items, _ := r["items"].([]any)
	if r["threadId"] != now || len(items) != 1 || items[0].(map[string]any)["text"] != "Started a new conversation." {
		t.Fatalf("after /clear, only the terminal's notice: %v", r)
	}
	if m := c.call("turn/start", map[string]any{"threadId": sess, "input": "x"}); m.Error == nil {
		t.Fatal("a message for the old session went through")
	}

	// /remote off closes every connection.
	a.ui.Do(func() { a.cmdRemote("off") })
	timeout := time.After(5 * time.Second)
	for open := true; open; {
		select {
		case _, open = <-ev:
		case <-timeout:
			t.Fatal("event stream still open after /remote off")
		}
	}
	if _, err := c.post("initialize", nil); err == nil {
		t.Fatal("server still answers after /remote off")
	}
	a.ui.Do(func() { status = plainLines(a.renderStatus(100)) })
	if strings.Contains(status, "remote") {
		t.Fatalf("status line still says remote: %q", status)
	}
}

func TestRemoteTokenPerStart(t *testing.T) {
	a := remoteApp(t, newRemoteModel(t))
	a.ui.Do(func() { a.cmdRemote("") })
	first := a.remoteClient(t)
	first.must("initialize", nil)
	a.ui.Do(func() { a.cmdRemote("") }) // on already: shows it again
	if again := a.remoteClient(t); again.token != first.token {
		t.Fatal("/remote while on made a new token")
	}
	a.ui.Do(func() {
		a.cmdRemote("off")
		a.cmdRemote("on")
	})
	second := a.remoteClient(t)
	if second.token == first.token {
		t.Fatal("the token was reused")
	}
	second.must("initialize", nil)
	if r, err := (remoteClient{t, second.base, first.token}).post("initialize", nil); err != nil || r.StatusCode != 401 {
		t.Fatalf("the old token should be refused: %v %v", r, err)
	}

	a.ui.Do(func() { a.cmdRemote("on nope") })
	if !strings.Contains(bodyText(a), "Not a port: nope") {
		t.Fatal("bad port accepted")
	}
	a.ui.Do(func() { a.cmdRemote("off"); a.cmdRemote("off") })
	if !strings.Contains(bodyText(a), "Remote control is off.") {
		t.Fatal("/remote off twice")
	}
}

// Pending steers show on the phone and can be taken back there; the last
// turn can be rolled back from it as /tree would.
func TestRemotePendingAndRollback(t *testing.T) {
	model := newRemoteModel(t)
	a := remoteApp(t, model)
	runTurn(t, a, "first")
	a.ui.Do(func() { a.cmdRemote("on") })
	c := a.remoteClient(t)
	read := c.must("thread/read", nil)
	if read["modelName"] != "m" || read["usage"] == nil {
		t.Fatalf("status line info %v", read)
	}
	eid, _ := read["eventId"].(float64)
	ev := c.events(eid)

	if r := c.must("turn/start", map[string]any{"input": "block"}); r["status"] != "started" {
		t.Fatalf("turn/start %v", r)
	}
	if m, _ := until(t, ev, "turn/started", nil); m.Params["startedAt"] == nil {
		t.Fatalf("turn/started %v", m.Params)
	}
	within(t, a, "the blocking request", func() bool { return model.blocks() == 1 })
	c.must("turn/start", map[string]any{"input": "more"})
	pending := func(want string) func(rmsg) bool {
		return func(m rmsg) bool {
			p, _ := m.Params["pending"].(map[string]any)
			s, _ := p["steers"].([]any)
			return fmt.Sprint(s) == want
		}
	}
	until(t, ev, "turn/pending", pending("[more]"))
	if p := c.must("thread/read", nil)["pending"].(map[string]any); fmt.Sprint(p["steers"]) != "[more]" {
		t.Fatalf("thread/read pending %v", p)
	}
	c.must("turn/unsteer", map[string]any{"input": "more"})
	until(t, ev, "turn/pending", pending("[]"))
	if m := c.call("turn/unsteer", map[string]any{"input": "more"}); m.Error == nil {
		t.Fatal("unsteered twice")
	}
	var steers []string
	a.ui.Do(func() { steers = a.agent.DrainSteers() })
	if len(steers) != 0 {
		t.Fatalf("the agent still has %v", steers)
	}

	// Rolling back while busy is refused; once idle it goes back to before
	// the last message, and gives it back.
	if m := c.call("thread/rollback", nil); m.Error == nil || !strings.Contains(m.Error.Message, "running") {
		t.Fatalf("rollback while busy: %+v", m.Error)
	}
	c.must("turn/interrupt", nil)
	until(t, ev, "turn/completed", nil)
	waitIdle(t, a)
	r := c.must("thread/rollback", nil)
	if r["input"] != "block" {
		t.Fatalf("rollback input %v", r["input"])
	}
	until(t, ev, "thread/switched", nil)
	var users []string
	for _, it := range c.must("thread/read", nil)["items"].([]any) {
		if m := it.(map[string]any); m["type"] == "userMessage" {
			users = append(users, m["text"].(string))
		}
	}
	if strings.Join(users, ",") != "first" {
		t.Fatalf("user messages after rollback %v", users)
	}
}
