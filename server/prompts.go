package server

import (
	"context"
	"fmt"
	"slices"

	"github.com/sebastianrcnt/atto/extensions"
	"github.com/sebastianrcnt/atto/mcp"
)

// Prompts are questions the runtime asks: an extension's select, confirm
// or input, MCP server approvals and the goal's confirmations. A prompt is
// the runtime's, not a client's: prompt/open goes to every client, any of
// them may answer, the first valid answer wins (prompt/closed says by
// whom) and the question's callback runs once, on the lane. With no client
// attached a prompt waits. While one is open, automatic work (events, the
// queue, goal turns) waits too. One opens at a time; further questions wait in order. Detaching clients
// never supplies a default answer.

// openPrompt is the open prompt.
type openPrompt struct {
	wire   Prompt
	choose func(i int)  // select: picks wire.Options[i]
	submit func(string) // input
	cancel func()       // Esc, or closed unanswered: the default answer
	origin string       // extension, mcp or goal
	ext    string       // the extension asking
}

// ask opens p, or queues it behind the current question.
func (t *thread) ask(p *openPrompt) {
	if t.closing {
		p.cancel()
		return
	}
	if p.wire.ID == "" {
		t.promptSeq++
		p.wire.ID = fmt.Sprintf("%s-p%d", t.id, t.promptSeq)
	}
	if t.prompt != nil {
		t.prompts = append(t.prompts, p)
		return
	}
	p.wire.Origin = p.origin
	t.prompt = p
	t.publish("prompt/open", map[string]any{"prompt": p.wire})
}

// closePrompt closes the open prompt id: how is answered, cancelled or
// closed, by the client that answered ("" for the runtime).
func (t *thread) closePrompt(how, by string) *openPrompt {
	p := t.prompt
	if p == nil {
		return nil
	}
	t.prompt = nil
	t.publish("prompt/closed", map[string]any{"id": p.wire.ID, "how": how, "by": by})
	return p
}

// answerPrompt applies client's answer to prompt id.
func (t *thread) answerPrompt(client, id string, ans PromptAnswer) error {
	p := t.prompt
	if p == nil || p.wire.ID != id {
		return &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf("prompt %q is not open", id), Data: &ErrorData{Reason: ReasonStalePrompt}}
	}
	switch {
	case ans.Cancel:
		t.closePrompt("cancelled", client)
		if p.wire.ClientID != "" {
			t.publish("prompt/clientAnswered", map[string]any{"clientId": p.wire.ClientID, "requestId": p.wire.RequestID, "answer": PromptAnswer{Cancel: true}})
		}
		p.cancel()
	case p.wire.Kind == PromptSelect:
		if ans.Index == nil {
			return invalid("index is required for a select prompt")
		}
		i := *ans.Index
		if i < 0 || i >= len(p.wire.Options) {
			return invalid("index %d is out of range (%d options)", i, len(p.wire.Options))
		}
		t.closePrompt("answered", client)
		p.choose(i)
	default:
		if ans.Text == nil {
			return invalid("text is required for an input prompt")
		}
		t.closePrompt("answered", client)
		p.submit(oneLine(*ans.Text))
	}
	t.afterPrompt()
	return nil
}

// afterPrompt lets what waited for the prompt go on.
func (t *thread) afterPrompt() {
	if len(t.prompts) > 0 {
		next := t.prompts[0]
		t.prompts = t.prompts[1:]
		t.ask(next)
		return
	}
	t.deliverEvents()
	t.maybeSendNextQueued()
	t.askMCPApprovals()
}

// cancelPrompt closes the open prompt unanswered (the session ends or
// reloads); its question gets its default answer.
func (t *thread) cancelPrompt() {
	for _, p := range t.prompts {
		p.cancel()
	}
	t.prompts = nil
	if p := t.closePrompt("closed", ""); p != nil {
		p.cancel()
	}
}

// choice asks a two-way question; the first option is the default.
func (t *thread) choice(title, subtitle, origin string, yes, no PromptOption, onYes func()) {
	t.ask(&openPrompt{
		wire:   Prompt{Kind: PromptSelect, Title: title, Subtitle: subtitle, Options: []PromptOption{yes, no}},
		origin: origin,
		choose: func(i int) {
			if i == 0 {
				onYes()
			}
		},
		cancel: func() {},
	})
}

// askExtension is an extension's question (extensions.Host.Ask).
func (t *thread) askExtension(ext string, q extensions.Question, answer func(any)) {
	def := func() {
		if q.Kind == "confirm" {
			answer(false)
			return
		}
		answer(nil)
	}
	p := &openPrompt{origin: "extension", ext: ext, cancel: def}
	switch q.Kind {
	case "select", "confirm":
		p.wire = Prompt{Kind: PromptSelect, Title: q.Title, Subtitle: "(" + ext + ")", Confirm: q.Kind == "confirm"}
		if q.Kind == "confirm" {
			p.wire.Options = []PromptOption{{Label: "Yes"}, {Label: "No"}}
		}
		for _, o := range q.Options {
			p.wire.Options = append(p.wire.Options, PromptOption{Label: o})
		}
		p.choose = func(i int) {
			if q.Kind == "confirm" {
				answer(i == 0)
				return
			}
			answer(p.wire.Options[i].Label)
		}
	default:
		p.wire = Prompt{Kind: PromptInput, Title: q.Title, Subtitle: "(" + ext + ")"}
		p.submit = func(text string) { answer(text) }
	}
	t.ask(p)
}

// Choices of the MCP approval prompt.
const (
	mcpAllow    = "Allow"
	mcpDeny     = "Deny"
	mcpAllowAll = "Allow all for this project"
)

// askMCPApprovals asks, one server after another, about the project MCP
// servers that wait for approval. Servers asked about once are not asked
// again while the runtime lives.
func (t *thread) askMCPApprovals() {
	if t.mcp == nil || t.prompt != nil || t.closing || t.readOnly != "" {
		return
	}
	infos, _ := t.mcp.Servers(context.Background())
	for _, in := range infos {
		if in.Status != mcp.NeedsApproval {
			continue
		}
		key := in.Name + "#" + in.Hash
		if t.mcpAsked[key] {
			continue
		}
		if t.mcpAsked == nil {
			t.mcpAsked = map[string]bool{}
		}
		t.mcpAsked[key] = true
		t.askMCPApproval(in)
		return
	}
}

func (t *thread) askMCPApproval(in mcp.Info) {
	choices := []string{mcpAllow, mcpDeny, mcpAllowAll}
	p := &openPrompt{origin: "mcp", wire: Prompt{Kind: PromptSelect,
		Title: fmt.Sprintf("This project wants to start MCP server %s: %s. Allow?", in.Name, in.Target)}}
	for _, c := range choices {
		p.wire.Options = append(p.wire.Options, PromptOption{Label: c})
	}
	p.choose = func(i int) {
		var err error
		switch choices[i] {
		case mcpAllow:
			err = t.mcp.Approve(in.Name)
		case mcpAllowAll:
			err = t.mcp.ApproveAll(in.Name)
		case mcpDeny:
			err = t.mcp.Deny(in.Name)
		}
		if err != nil {
			t.errorNotice(err)
		}
	}
	p.cancel = func() {} // not now: the server stays unapproved
	t.ask(p)
}

// cancelExtensionPrompts disposes questions whose extension is being reloaded.
// Other runtime questions keep their place and cannot be auto-answered.
func (t *thread) cancelExtensionPrompts() {
	t.prompts = slices.DeleteFunc(t.prompts, func(p *openPrompt) bool {
		if p.origin != "extension" {
			return false
		}
		p.cancel()
		return true
	})
	if p := t.prompt; p != nil && p.origin == "extension" {
		t.closePrompt("closed", "")
		p.cancel()
		if len(t.prompts) > 0 {
			next := t.prompts[0]
			t.prompts = t.prompts[1:]
			t.ask(next)
		}
	}
}

// openClientPrompt makes a terminal picker a server object too. The runtime
// arbitrates answers; its owner applies the chosen UI action asynchronously.
func (t *thread) openClientPrompt(client string, wire Prompt) (Prompt, error) {
	if client == "" || wire.RequestID == "" {
		return Prompt{}, invalid("client and requestId are required")
	}
	if wire.Kind != PromptSelect && wire.Kind != PromptInput {
		return Prompt{}, invalid("kind is select or input")
	}
	wire.ID, wire.ClientID, wire.Origin = "", client, "client"
	respond := func(ans PromptAnswer) {
		t.publish("prompt/clientAnswered", map[string]any{"clientId": client, "requestId": wire.RequestID, "answer": ans})
	}
	p := &openPrompt{wire: wire, origin: "client", choose: func(i int) { respond(PromptAnswer{Index: &i}) }, submit: func(text string) { respond(PromptAnswer{Text: &text}) }, cancel: func() {}}
	t.ask(p)
	return p.wire, nil
}

func (t *thread) withdrawClientPrompt(client, request string) {
	t.prompts = slices.DeleteFunc(t.prompts, func(p *openPrompt) bool {
		return p.wire.ClientID == client && (request == "" || p.wire.RequestID == request)
	})
	if p := t.prompt; p != nil && p.wire.ClientID == client && (request == "" || p.wire.RequestID == request) {
		t.closePrompt("closed", client)
		t.afterPrompt()
	}
}
