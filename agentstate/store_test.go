package agentstate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/session"
)

// fixture is a dir with the format marker in place.
func fixture(t *testing.T) {
	t.Helper()
	t.Setenv(config.EnvDir, t.TempDir())
	if err := WriteMarker("test", ""); err != nil {
		t.Fatal(err)
	}
}

var clock = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// agentAt makes a state created n seconds after the epoch of the tests.
func agentAt(n int, parent, name, id string) State {
	return State{Name: name, Parent: parent, Session: id, Created: clock.Add(time.Duration(n) * time.Second)}
}

func mustCreate(t *testing.T, s State) State {
	t.Helper()
	if err := Create(s); err != nil {
		t.Fatal(err)
	}
	got, err := Load(s.Session)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestRecordsAreFilesNamedBySessionWithSummaryFirst(t *testing.T) {
	fixture(t)
	root := mustCreate(t, State{Name: "tests", Session: "aaaa1111", Created: clock, SpawnCwd: "/src/p", Project: "/src/p"})
	child := mustCreate(t, agentAt(1, root.Session, "lint", "bbbb2222"))
	if root.Root != "aaaa1111" || root.Depth != 0 || root.Path != "/root" || root.Origin != session.OriginExternal || root.Lifecycle != Open {
		t.Fatalf("root %+v", root)
	}
	if child.Root != "aaaa1111" || child.Depth != 1 || child.Path != "/root/lint" || child.Origin != session.OriginAgent {
		t.Fatalf("child %+v", child)
	}
	// One flat directory, no per-parent directories, no _up or _closed.
	ents, _ := os.ReadDir(config.AgentStateDir())
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if got := strings.Join(names, " "); got != ".format aaaa1111.json bbbb2222.json" {
		t.Fatalf("layout: %s", got)
	}
	raw, _ := os.ReadFile(filepath.Join(config.AgentStateDir(), "bbbb2222.json"))
	if !strings.HasPrefix(string(raw), "{\n  \"summary\": {") {
		t.Fatalf("the summary is not the first field:\n%s", raw)
	}
	// The inventory decodes the summary alone.
	sum, ok := readSummary(filepath.Join(config.AgentStateDir(), "bbbb2222.json"))
	if !ok || sum.Parent != "aaaa1111" || sum.Name != "lint" || sum.Root != "aaaa1111" {
		t.Fatalf("summary %+v", sum)
	}
}

func TestNamesAreUniqueAmongLiveChildrenOfOneParent(t *testing.T) {
	fixture(t)
	a := mustCreate(t, State{Name: "tests", Session: "aaaa1111", Created: clock})
	// Roots may share a name.
	mustCreate(t, State{Name: "tests", Session: "aaaa2222", Created: clock.Add(time.Second)})
	b := mustCreate(t, agentAt(2, a.Session, "lint", "bbbb1111"))
	if err := Create(agentAt(3, a.Session, "lint", "bbbb2222")); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Fatalf("duplicate sibling: %v", err)
	}
	// The same name under another parent is free.
	mustCreate(t, agentAt(4, "aaaa2222", "lint", "bbbb3333"))
	// Closing frees the label for reuse; the ID stays reserved.
	if err := MarkClosed(b.Session, "/archive/b"); err != nil {
		t.Fatal(err)
	}
	mustCreate(t, agentAt(5, a.Session, "lint", "bbbb4444"))
	if _, err := Load(b.Session); err != nil {
		t.Fatalf("closed record kept: %v", err)
	}
	if err := Create(agentAt(6, a.Session, "x", b.Session)); err == nil {
		t.Fatal("a closed agent's ID was reused")
	}
	if err := Create(State{Name: "../x", Parent: a.Session, Session: "cccc1111"}); err == nil {
		t.Fatal("bad name saved")
	}
	if err := Create(State{Name: "ok", Parent: a.Session, Session: "../cccc"}); err == nil {
		t.Fatal("bad session ID saved")
	}
}

func TestInventoryQueries(t *testing.T) {
	fixture(t)
	r1 := mustCreate(t, State{Name: "tests", Session: "aaaa1111", Created: clock, Project: "/p1"})
	r2 := mustCreate(t, State{Name: "docs", Session: "aaaa2222", Created: clock.Add(time.Second), Project: "/p2"})
	c1 := mustCreate(t, agentAt(2, r1.Session, "lint", "bbbb1111"))
	c2 := mustCreate(t, agentAt(3, c1.Session, "deep", "bbbb2222"))
	mustCreate(t, agentAt(4, r2.Session, "x", "bbbb3333"))
	if err := MarkClosed("bbbb3333", ""); err != nil {
		t.Fatal(err)
	}
	ids := func(l []State) string {
		var s []string
		for _, x := range l {
			s = append(s, x.Session)
		}
		return strings.Join(s, ",")
	}
	if got := ids(Children(r1.Session)); got != "bbbb1111" {
		t.Fatalf("children %s", got)
	}
	if got := ids(Children(r2.Session)); got != "" {
		t.Fatalf("closed children are not live: %s", got)
	}
	if got := ids(Tree(r1.Session)); got != "bbbb1111,bbbb2222" {
		t.Fatalf("tree %s", got)
	}
	if got := ids(All()); got != "aaaa1111,aaaa2222,bbbb1111,bbbb2222" {
		t.Fatalf("all %s", got)
	}
	if got := ids(AllWithClosed()); !strings.Contains(got, "bbbb3333") {
		t.Fatalf("with closed %s", got)
	}
	if got := ids(ExternalRoots("/p1", false)); got != "aaaa1111" {
		t.Fatalf("roots in p1: %s", got)
	}
	if got := ids(ExternalRoots("", false)); got != "aaaa1111,aaaa2222" {
		t.Fatalf("roots anywhere: %s", got)
	}
	if got := ids(ExternalRoots("/p2", true)); got != "aaaa2222" {
		t.Fatalf("roots in p2: %s", got)
	}
	if Root(c2.Session) != r1.Session || Depth(c2.Session) != 2 || PathOf(c2.Session) != "/root/lint/deep" {
		t.Fatalf("position %s %d %s", Root(c2.Session), Depth(c2.Session), PathOf(c2.Session))
	}
	if p, name, ok := ParentOf(c2.Session); !ok || p != c1.Session || name != "deep" {
		t.Fatalf("parent %s %s %v", p, name, ok)
	}
	// An ordinary session roots its own tree by implication.
	if Root("plain") != "plain" || Depth("plain") != 0 || PathOf("plain") != "/root" || IsAgent("plain") {
		t.Fatal("ordinary session")
	}
}

func TestInventoryFollowsRewrittenAndRemovedFiles(t *testing.T) {
	fixture(t)
	s := mustCreate(t, State{Name: "a", Session: "aaaa1111", Created: clock})
	if len(All()) != 1 {
		t.Fatal("not listed")
	}
	s.Lifecycle = Closing
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	if sum, _ := summaryOf(s.Session); sum.Lifecycle != Closing {
		t.Fatalf("cached summary is stale: %+v", sum)
	}
	Discard(s.Session)
	if len(All()) != 0 || len(AllWithClosed()) != 0 {
		t.Fatal("a discarded agent is listed")
	}
}

func TestTurnStatus(t *testing.T) {
	fixture(t)
	s := mustCreate(t, State{Name: "a", Parent: "p1", Session: "c1c1c1c1", Created: clock})
	if s.Latest().Status != Idle {
		t.Fatalf("%+v", s.Latest())
	}
	// A turn without a job never started.
	s.Turns = 1
	if st := s.Latest(); st.Status != Failed {
		t.Fatalf("status %+v", st)
	}
	_ = SaveTurn(s.Session, Turn{N: 1, Status: Done, Started: time.Now().Add(-time.Minute), Ended: time.Now()})
	s.Job = 99 // a job that doesn't exist: the turn's own record is kept only for a live one
	if st := s.Latest(); st.Status != Failed {
		t.Fatalf("status %+v", st)
	}
	// The job belongs to the parent, or to the agent itself for a root.
	if owner, id := s.JobRef(); owner != "p1" || id != 99 {
		t.Fatalf("job ref %s %d", owner, id)
	}
	root := State{Name: "r", Session: "rrrr1111", Job: 4}
	root.JobOwner = root.Session
	if owner, id := root.JobRef(); owner != "rrrr1111" || id != 4 {
		t.Fatalf("root job ref %s %d", owner, id)
	}
	// A turn recorded running with a finished job is corrected by the job.
	write(t, filepath.Join(config.Dir(), "jobs", "p2", "1", "job.json"), `{"kind":"agent","id":1,"session":"p2","status":"exited","exitCode":0,"started":"2026-01-01T00:00:00Z","ended":"2026-01-01T00:00:05Z"}`)
	if j, err := jobs.Get("p2", 1); err != nil || j.Active() {
		t.Fatalf("job %+v %v", j, err)
	}
	s2 := State{Name: "b", Parent: "p2", Session: "c2c2c2c2", Turns: 1, Job: 1}
	_ = SaveTurn(s2.Session, Turn{N: 1, Status: Running, Started: time.Now()})
	if st := s2.Latest(); st.Status != Failed || !strings.Contains(st.Error, "exited with code 0") {
		t.Fatalf("a turn whose process ended without saying how: %+v", st)
	}
}

func TestAncestryFailsClosed(t *testing.T) {
	fixture(t)
	root := mustCreate(t, State{Name: "r", Session: "aaaa1111", Created: clock})
	a := mustCreate(t, agentAt(1, root.Session, "a", "bbbb1111"))
	b := mustCreate(t, agentAt(2, a.Session, "b", "bbbb2222"))
	chain, err := Ancestry(b.Session)
	if err != nil || strings.Join(chain, ",") != "bbbb2222,bbbb1111,aaaa1111" {
		t.Fatalf("chain %v %v", chain, err)
	}
	// An agent under an ordinary session ends there; so does an ordinary session.
	o := mustCreate(t, agentAt(3, "ordinary1", "o", "cccc1111"))
	if chain, err := Ancestry(o.Session); err != nil || len(chain) != 2 {
		t.Fatalf("ordinary parent: %v %v", chain, err)
	}
	if chain, err := Ancestry("ordinary1"); err != nil || len(chain) != 1 {
		t.Fatalf("ordinary: %v %v", chain, err)
	}
	// A cycle is found, not followed.
	x := mustCreate(t, agentAt(4, "dddd2222", "x", "dddd1111"))
	y := mustCreate(t, agentAt(5, x.Session, "y", "dddd2222"))
	_ = y
	if _, err := Ancestry("dddd1111"); !errors.Is(err, ErrBrokenTree) || !strings.Contains(err.Error(), "loop") {
		t.Fatalf("cycle: %v", err)
	}
	if release, err := StartWork("dddd1111"); err == nil {
		release()
		t.Fatal("work started below a cycle")
	}
	// A record that disagrees with its parents about root or depth.
	bad := mustCreate(t, agentAt(6, a.Session, "bad", "eeee1111"))
	bad.Depth = 5
	if err := Save(bad); err != nil {
		t.Fatal(err)
	}
	if _, err := Ancestry(bad.Session); !errors.Is(err, ErrBrokenTree) {
		t.Fatalf("depth mismatch: %v", err)
	}
	// A parent whose own header says it is an agent, but whose record is gone.
	t.Setenv("ATTO_DIR", config.Dir()) // the same dir
	w := session.NewManaged("/work", func(id string) session.AgentMeta {
		return session.AgentMeta{Version: 1, RootSessionID: id, Path: "/root", Name: "lost"}
	})
	w.Append(session.Entry{Type: session.TypeName, Name: "lost"})
	w.Close()
	mustCreate(t, agentAt(7, w.ID, "orphan", "ffff1111"))
	if _, err := Ancestry("ffff1111"); !errors.Is(err, ErrBrokenTree) || !strings.Contains(err.Error(), "no record") {
		t.Fatalf("missing parent record: %v", err)
	}
}

func TestResolveInsideATree(t *testing.T) {
	fixture(t)
	mustCreate(t, State{Name: "r", Session: "aaaa1111", Created: clock})
	mustCreate(t, agentAt(1, "aaaa1111", "tests", "bbbb1111"))
	mustCreate(t, agentAt(2, "bbbb1111", "lint", "cccc1111"))
	mustCreate(t, agentAt(3, "ordinary1", "tests", "dddd1111"))
	for _, tc := range []struct{ name, from, addr, want, err string }{
		{"child name", "aaaa1111", "tests", "bbbb1111", ""},
		{"relative path", "aaaa1111", "tests/lint", "cccc1111", ""},
		{"path from the root", "cccc1111", "/root/tests", "bbbb1111", ""},
		{"the root itself", "cccc1111", "/root", "aaaa1111", ""},
		{"parent", "cccc1111", "..", "bbbb1111", ""},
		{"parent of a root", "aaaa1111", "..", "", "no parent agent"},
		{"parent that is an ordinary session", "dddd1111", "..", "ordinary1", ""},
		{"unknown", "aaaa1111", "zz", "", "no such agent"},
		{"id inside the tree", "aaaa1111", "@cccc1111", "cccc1111", ""},
		{"id prefix", "aaaa1111", "@cccc11", "cccc1111", ""},
		{"id of the root from below", "cccc1111", "@aaaa1111", "aaaa1111", ""},
		{"id of the ordinary root", "dddd1111", "@ordinary1", "ordinary1", ""},
		{"id in another tree", "aaaa1111", "@dddd1111", "", "no such agent"},
		{"short id", "aaaa1111", "@cccc1", "", "at least 6 characters"},
		{"same name in another tree", "ordinary1", "tests", "dddd1111", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, err := Resolve(tc.from, tc.addr)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("%+v: %v", target, err)
				}
				return
			}
			if err != nil || target.Session != tc.want {
				t.Fatalf("%+v: %v", target, err)
			}
		})
	}
	// Roots carry a state; an ordinary session does not.
	if tg, _ := Resolve("cccc1111", "/root"); tg.State == nil || tg.Path != "/root" {
		t.Fatalf("managed root: %+v", tg)
	}
	if tg, _ := Resolve("dddd1111", ".."); tg.State != nil {
		t.Fatalf("ordinary root has no state: %+v", tg)
	}
}

func TestResolveOutsideProjectsAndAmbiguity(t *testing.T) {
	fixture(t)
	mustCreate(t, State{Name: "panes", Session: "aaaa1111", Created: clock, Project: "/p1"})
	mustCreate(t, State{Name: "panes", Session: "aaaa2222", Created: clock.Add(time.Minute), Project: "/p1"})
	mustCreate(t, State{Name: "solo", Session: "aaaa3333", Created: clock, Project: "/p1"})
	mustCreate(t, State{Name: "solo", Session: "aaaa4444", Created: clock, Project: "/p2"})
	mustCreate(t, agentAt(1, "aaaa3333", "lint", "bbbb1111"))
	// A unique name selects the root; a path descends into it.
	if tg, err := ResolveOutside("/p1", "solo"); err != nil || tg.Session != "aaaa3333" || tg.State == nil {
		t.Fatalf("%+v %v", tg, err)
	}
	if tg, err := ResolveOutside("/p1", "solo/lint"); err != nil || tg.Session != "bbbb1111" {
		t.Fatalf("%+v %v", tg, err)
	}
	// /root/NAME is a spelling of the project selector.
	if tg, err := ResolveOutside("/p1", "/root/solo/lint"); err != nil || tg.Session != "bbbb1111" {
		t.Fatalf("%+v %v", tg, err)
	}
	// Another project's root is not visible.
	if tg, err := ResolveOutside("/p2", "solo"); err != nil || tg.Session != "aaaa4444" {
		t.Fatalf("%+v %v", tg, err)
	}
	if _, err := ResolveOutside("/p2", "panes"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other project: %v", err)
	}
	// Duplicates list the candidates with their IDs.
	_, err := ResolveOutside("/p1", "panes")
	if err == nil || !strings.Contains(err.Error(), "2 agents named panes") || !strings.Contains(err.Error(), "@aaaa1111") || !strings.Contains(err.Error(), "@aaaa2222") {
		t.Fatalf("ambiguity: %v", err)
	}
	// Closing one of them makes the name unique again.
	if err := MarkClosed("aaaa1111", ""); err != nil {
		t.Fatal(err)
	}
	if tg, err := ResolveOutside("/p1", "panes"); err != nil || tg.Session != "aaaa2222" {
		t.Fatalf("%+v %v", tg, err)
	}
	for _, addr := range []string{"/root", ".."} {
		if _, err := ResolveOutside("/p1", addr); err == nil || !strings.Contains(err.Error(), "-session") {
			t.Fatalf("%s: %v", addr, err)
		}
	}
	// Outside spellings.
	solo, _ := Load("aaaa3333")
	lint, _ := Load("bbbb1111")
	if Label(solo, true) != "solo" || Label(lint, true) != "solo/lint" || Label(lint, false) != "/root/lint" {
		t.Fatalf("labels %q %q %q", Label(solo, true), Label(lint, true), Label(lint, false))
	}
}

func TestResolveIDs(t *testing.T) {
	fixture(t)
	mustCreate(t, State{Name: "a", Session: "abcdef12", Created: clock})
	mustCreate(t, agentAt(1, "abcdef12", "b", "abcdef123456"))
	mustCreate(t, State{Name: "a", Session: "abcdef34", Created: clock})
	// An exact ID wins over another ID it prefixes.
	if tg, err := Resolve("", "@abcdef12"); err != nil || tg.Session != "abcdef12" {
		t.Fatalf("%+v %v", tg, err)
	}
	// Outside atto any tree can be reached; a prefix shared by several is ambiguous.
	if _, err := Resolve("", "@abcdef"); err == nil || !strings.Contains(err.Error(), "ambiguous") ||
		!strings.Contains(err.Error(), "@abcdef12 ") || !strings.Contains(err.Error(), "@abcdef34 ") {
		t.Fatalf("ambiguity: %v", err)
	}
	if tg, err := Resolve("", "@abcdef3"); err != nil || tg.Session != "abcdef34" {
		t.Fatalf("%+v %v", tg, err)
	}
	// Inside a tree only that tree counts.
	if tg, err := Resolve("abcdef12", "@abcdef12345"); err != nil || tg.Session != "abcdef123456" {
		t.Fatalf("%+v %v", tg, err)
	}
	if _, err := Resolve("abcdef12", "@abcdef34"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other tree: %v", err)
	}
	if ShortID("abcdef123456") != "abcdef12" || ShortID("abcdef12") != "abcdef12" {
		t.Fatal("short ID")
	}
}

// A closed ID is reported closed: it keeps its tree, never follows a reused
// name, and still counts toward prefix ambiguity.
func TestClosedIDsAreNotRevived(t *testing.T) {
	fixture(t)
	a := mustCreate(t, State{Name: "tests", Session: "abcdef12", Created: clock})
	b := mustCreate(t, agentAt(1, a.Session, "lint", "12345678"))
	for _, s := range []State{b, a} {
		if err := MarkClosed(s.Session, "/archived/"+s.Session); err != nil {
			t.Fatal(err)
		}
	}
	mustCreate(t, State{Name: "tests", Session: "abcdef34", Created: clock.Add(time.Hour)})
	for _, addr := range []string{"@abcdef12", "@12345678", "@123456"} {
		for _, from := range []string{"", "abcdef12"} {
			if _, err := Resolve(from, addr); !errors.Is(err, ErrClosed) || !strings.Contains(err.Error(), "archived transcript") {
				t.Fatalf("%s from %q: %v", addr, from, err)
			}
		}
		if _, err := Resolve("other", addr); !errors.Is(err, ErrNotFound) {
			t.Fatalf("closed agent exposed outside its tree: %v", err)
		}
	}
	if _, err := Resolve("", "@abcdef"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("closed IDs count toward ambiguity: %v", err)
	}
	got, _ := Load(a.Session)
	if got.Lifecycle != Closed || got.Archive != "/archived/abcdef12" || got.Closed.IsZero() {
		t.Fatalf("closed record %+v", got)
	}
	if len(All()) != 1 {
		t.Fatalf("closed records leaked into the live inventory: %+v", All())
	}
}
