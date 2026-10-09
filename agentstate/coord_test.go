package agentstate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/config"
)

func TestInterruptTargetsOneTurn(t *testing.T) {
	fixture(t)
	if Interrupted("aaaa1111", 1) {
		t.Fatal("turn interrupted without a request")
	}
	if err := RequestInterrupt("aaaa1111", 1); err != nil {
		t.Fatal(err)
	}
	if !Interrupted("aaaa1111", 1) || Interrupted("aaaa1111", 2) {
		t.Fatal("request did not target exactly its turn")
	}
	if err := RequestInterrupt("aaaa1111", 2); err != nil {
		t.Fatal(err)
	}
	if Interrupted("aaaa1111", 1) || !Interrupted("aaaa1111", 2) {
		t.Fatal("new request did not replace the old one")
	}
	if RequestInterrupt("../bad", 1) == nil || RequestInterrupt("aaaa1111", 0) == nil {
		t.Fatal("invalid interrupt request accepted")
	}
	// Closing the agent drops a pending request.
	mustCreate(t, State{Name: "a", Session: "aaaa1111", Created: clock})
	if err := MarkClosed("aaaa1111", ""); err != nil {
		t.Fatal(err)
	}
	if Interrupted("aaaa1111", 2) {
		t.Fatal("request survived closing the agent")
	}
	path, content := InterruptRequestPath("aaaa1111", 3)
	if !strings.HasSuffix(path, "aaaa1111.turn.json.interrupt") || content != "3" {
		t.Fatalf("%s %s", path, content)
	}
}

func TestTreeGateRefusesWorkBelowClosedOrClosingAgents(t *testing.T) {
	fixture(t)
	// An ordinary session roots the tree.
	a := mustCreate(t, agentAt(0, "root1", "a", "aaaa1111"))
	b := mustCreate(t, agentAt(1, a.Session, "b", "bbbb1111"))
	release, err := StartWork(b.Session)
	if err != nil {
		t.Fatal(err)
	}
	release()
	closeTree, err := CloseTree("root1")
	if err != nil {
		t.Fatal(err)
	}
	closeTree()
	if r, err := StartWork(b.Session); err == nil {
		r()
		t.Fatal("work started below a closed ancestor")
	}
	OpenTree("root1")
	if r, err := StartWork(b.Session); err != nil {
		t.Fatal(err)
	} else {
		r()
	}
	// A closing agent admits nothing below it.
	if err := MarkClosing(a.Session); err != nil {
		t.Fatal(err)
	}
	if r, err := StartWork(b.Session); err == nil || !strings.Contains(err.Error(), "closing") {
		if r != nil {
			r()
		}
		t.Fatalf("work below a closing agent: %v", err)
	}
	// The locks live under .coord, not in the namespace of records.
	if _, err := os.Stat(filepath.Join(config.AgentStateDir(), ".coord", "trees", "root1", ".tree.lock")); err != nil {
		t.Fatal(err)
	}
	for _, e := range AllWithClosed() {
		if e.Session == "" {
			t.Fatal("coordination file taken for a record")
		}
	}
}

func TestTreeLockSerializesAndTurnLocksArePerAgent(t *testing.T) {
	fixture(t)
	mustCreate(t, State{Name: "r", Session: "aaaa1111", Created: clock})
	r1, err := LockTree("aaaa1111")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan func(), 1)
	go func() {
		r, _ := LockTree("aaaa1111")
		got <- r
	}()
	select {
	case <-got:
		t.Fatal("two holders of one tree lock")
	case <-time.After(100 * time.Millisecond):
	}
	r1()
	select {
	case r := <-got:
		r()
	case <-time.After(2 * time.Second):
		t.Fatal("the lock was not handed over")
	}
	// One agent runs one turn at a time; other agents are not held up.
	t1, err := LockTurn("aaaa1111")
	if err != nil {
		t.Fatal(err)
	}
	other, err := LockTurn("bbbb1111")
	if err != nil {
		t.Fatal(err)
	}
	other()
	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Go(func() {
		r, _ := LockTurn("aaaa1111")
		close(done)
		r()
	})
	select {
	case <-done:
		t.Fatal("a second turn of one agent")
	case <-time.After(100 * time.Millisecond):
	}
	t1()
	wg.Wait()
}

func TestSpawnJournalFindsOnlyAbandonedSpawns(t *testing.T) {
	fixture(t)
	live, err := BeginSpawn(SpawnEntry{ID: "aaaa1111", Name: "a", Worktree: "/wt/a", Branch: "atto/aaaa1111"})
	if err != nil {
		t.Fatal(err)
	}
	if got := StaleSpawns(); len(got) != 0 {
		t.Fatalf("a running spawn is not stale: %+v", got)
	}
	if _, err := BeginSpawn(SpawnEntry{ID: "aaaa1111"}); err == nil {
		t.Fatal("one spawn per ID")
	}
	if err := live.Update(SpawnEntry{ID: "aaaa1111", Name: "a", Worktree: "/wt/a2"}); err != nil {
		t.Fatal(err)
	}
	// A crash leaves the entry behind, unlocked.
	unlock(live.f)
	live.f.Close()
	stale := StaleSpawns()
	if len(stale) != 1 || stale[0].ID != "aaaa1111" || stale[0].Worktree != "/wt/a2" {
		t.Fatalf("stale %+v", stale)
	}
	if Published("aaaa1111") {
		t.Fatal("published without a record")
	}
	ClearSpawn("aaaa1111")
	if got := StaleSpawns(); len(got) != 0 {
		t.Fatalf("cleared %+v", got)
	}
	done, err := BeginSpawn(SpawnEntry{ID: "bbbb1111", Name: "b"})
	if err != nil {
		t.Fatal(err)
	}
	done.Done()
	if got := StaleSpawns(); len(got) != 0 {
		t.Fatalf("a finished spawn leaves nothing: %+v", got)
	}
}

func TestFormatMarker(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	// Nothing there: the first agent command stamps the marker.
	if l, _, err := Detect(); err != nil || l != LayoutEmpty {
		t.Fatalf("empty: %v %v", l, err)
	}
	if err := Ready(); err != nil {
		t.Fatal(err)
	}
	m, ok, err := ReadMarker()
	if err != nil || !ok || m.Version != FormatVersion {
		t.Fatalf("marker %+v %v %v", m, ok, err)
	}
	if err := Ready(); err != nil {
		t.Fatal(err)
	}
	// A newer atto's marker makes agent commands refuse.
	if err := os.WriteFile(markerPath(), []byte(`{"version":99}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Ready(); !errors.Is(err, ErrNewerFormat) || !strings.Contains(err.Error(), "upgrade") {
		t.Fatalf("newer: %v", err)
	}
}

func TestOldLayoutIsDetected(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T){
		"parent directory": func(t *testing.T) {
			write(t, filepath.Join(config.AgentStateDir(), "p", "a.json"), `{"name":"a","parent":"p","session":"c1"}`)
		},
		"only _closed": func(t *testing.T) {
			write(t, filepath.Join(config.AgentStateDir(), "_closed", "c1.json"), `{"session":"c1","root":"p","path":"/root/a"}`)
		},
		"subagents directory": func(t *testing.T) {
			write(t, filepath.Join(config.Dir(), "subagents", "p", "a.json"), `{"name":"a","parent":"p","session":"c1"}`)
		},
		"a record without the marker": func(t *testing.T) {
			write(t, filepath.Join(config.AgentStateDir(), "c1.json"), `{"name":"a","session":"c1"}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(config.EnvDir, t.TempDir())
			setup(t)
			if l, why, err := Detect(); err != nil || l != LayoutLegacy || why == "" {
				t.Fatalf("detect: %v %q %v", l, why, err)
			}
			if err := Ready(); !errors.Is(err, ErrNeedsMigration) || !strings.Contains(err.Error(), "atto agent migrate") {
				t.Fatalf("ready: %v", err)
			}
		})
	}
	// With the marker, leftovers of the old layout do not matter to Detect.
	t.Setenv(config.EnvDir, t.TempDir())
	write(t, filepath.Join(config.AgentStateDir(), "p", "a.json"), `{}`)
	if err := WriteMarker("t", ""); err != nil {
		t.Fatal(err)
	}
	if l, _, _ := Detect(); l != LayoutCurrent {
		t.Fatalf("layout %v", l)
	}
}

func TestScanLegacy(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	st := config.AgentStateDir()
	write(t, filepath.Join(st, "p1", "a.json"), `{"name":"a","parent":"p1","session":"aaaa1111","created":"2026-01-01T00:00:02Z","turns":1,"job":3}`)
	write(t, filepath.Join(st, "p1", "a.turn.json"), `{"turn":1,"status":"done"}`)
	write(t, filepath.Join(st, "p1", "a.lock"), ``)
	write(t, filepath.Join(st, "aaaa1111", "b.json"), `{"name":"b","session":"bbbb1111","created":"2026-01-01T00:00:01Z"}`)
	write(t, filepath.Join(st, "_up", "aaaa1111.json"), `{"parent":"p1","name":"a"}`)
	write(t, filepath.Join(st, "_closed", "cccc1111.json"), `{"session":"cccc1111","root":"p1","path":"/root/gone"}`)
	write(t, filepath.Join(st, "p2", "bad.json"), `not json`)
	// The old name of the directory, with a record also found in the new one.
	write(t, filepath.Join(config.Dir(), "subagents", "p1", "a.json"), `{"name":"a","parent":"p1","session":"aaaa1111","task":"stale"}`)
	write(t, filepath.Join(config.Dir(), "subagents", "p3", "old.json"), `{"name":"old","session":"dddd1111"}`)
	l, err := ScanLegacy()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, a := range l.Agents {
		ids = append(ids, a.State.Session+":"+a.State.Parent+"/"+a.State.Name)
	}
	if got := strings.Join(ids, " "); got != "dddd1111:p3/old bbbb1111:aaaa1111/b aaaa1111:p1/a" {
		t.Fatalf("agents %s", got)
	}
	for _, a := range l.Agents {
		if a.State.Session == "aaaa1111" && (a.Turn == nil || a.Turn.Status != Done || a.State.Task != "" || len(a.Files) != 2) {
			t.Fatalf("agent a read from agent-state first: %+v", a)
		}
	}
	if len(l.Tombs) != 1 || l.Tombs[0].Path != "/root/gone" {
		t.Fatalf("tombs %+v", l.Tombs)
	}
	if len(l.Problems) != 1 || !strings.Contains(l.Problems[0], "bad.json") {
		t.Fatalf("problems %v", l.Problems)
	}
	if len(l.Remove) < 6 {
		t.Fatalf("remove %v", l.Remove)
	}
}
