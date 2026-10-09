package cli

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sebastianrcnt/atto/agentstate"
	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/jobs"
	"github.com/sebastianrcnt/atto/provider"
	"github.com/sebastianrcnt/atto/session"
)

func savedIDAgent(t *testing.T, parent, name string) agentstate.State {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	w := session.NewAgent(cwd, parent)
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", Content: "saved answer"}})
	w.Close()
	if err := w.Err(); err != nil {
		t.Fatal(err)
	}
	st := agentstate.State{Parent: parent, Name: name, Session: w.ID, Preset: "general", Model: "fake/m", Cwd: cwd, Task: "task", Created: time.Now()}
	if err := agentstate.Save(st); err != nil {
		t.Fatal(err)
	}
	return st
}

// savedRoot saves an agent started from a shell, in the working directory's project.
func savedRoot(t *testing.T, name string) agentstate.State {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	project := externalProject(cwd)
	w := session.NewManaged(cwd, func(id string) session.AgentMeta {
		return session.AgentMeta{Version: 1, RootSessionID: id, Path: "/root", Name: name, Project: project, Origin: session.OriginExternal}
	})
	w.Append(session.Entry{Type: session.TypeMessage, Message: &provider.Message{Role: "assistant", Content: "saved answer"}})
	w.Close()
	if err := w.Err(); err != nil {
		t.Fatal(err)
	}
	st := agentstate.State{Name: name, Session: w.ID, Preset: "general", Model: "fake/m", Cwd: cwd, Project: project, Origin: session.OriginExternal, Task: "task", Created: time.Now()}
	if err := agentstate.Save(st); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestAgentIDInEveryAddressCommand(t *testing.T) {
	agentServer(t, func(int, string) string { return textAnswer("task completed") })
	t.Chdir(t.TempDir())
	enableAgents(t, "")
	for _, sub := range []string{"send", "task", "next", "steer", "wait", "wait-any", "report", "read", "show", "interrupt", "stop", "close", "rm"} {
		t.Run(sub, func(t *testing.T) {
			st := savedIDAgent(t, "root-"+sub, "tests")
			t.Cleanup(func() { jobs.KillAll(st.Parent); jobs.KillAll(st.Session) })
			addr := "@" + st.Session
			args := []string{sub, addr}
			if sub == "send" || sub == "task" || sub == "next" || sub == "steer" {
				args = append(args, "hello")
			}
			out, err := runAgent(t, args...)
			switch sub {
			case "interrupt", "stop":
				if err == nil || !strings.Contains(err.Error(), "is idle, not running") {
					t.Fatalf("%q: %v", out, err)
				}
			default:
				if err != nil {
					t.Fatalf("%q: %v", out, err)
				}
			}
			switch sub {
			case "task", "next":
				if _, err := runAgent(t, "wait", addr, "-timeout", "30s"); err != nil {
					t.Fatal(err)
				}
			case "send", "steer":
				evs := events.Drain(st.Session)
				if len(evs) != 1 || !strings.Contains(evs[0].Text, "hello") {
					t.Fatalf("events: %+v", evs)
				}
			case "wait", "wait-any", "report", "read", "show":
				if !strings.Contains(out, "saved answer") {
					t.Fatalf("report: %q", out)
				}
			case "close", "rm":
				if !strings.Contains(out, "closed agent /root/tests") {
					t.Fatalf("close: %q", out)
				}
				if _, err := runAgent(t, "report", addr); !errors.Is(err, agentstate.ErrClosed) {
					t.Fatalf("removed: %v", err)
				}
				if p, err := session.Find(st.Session); err != nil || !isArchived(p) {
					t.Fatalf("archive: %s %v", p, err)
				}
				if out, err := sessionShowForID(st.Session); err != nil || !strings.Contains(out, st.Session) {
					t.Fatalf("archived transcript: %q %v", out, err)
				}
			}
		})
	}
}

// Keep archived reading on the existing sessions command, not the live-agent API.
func sessionShowForID(id string) (string, error) {
	var out strings.Builder
	err := RunSessions([]string{"show", id}, &out)
	return out.String(), err
}

func TestAgentOutsideIDAcrossProjectsAndListAll(t *testing.T) {
	agentServer(t, func(int, string) string { return textAnswer("done") })
	enableAgents(t, "")
	var agents []agentstate.State
	for _, project := range []string{t.TempDir(), t.TempDir()} {
		t.Chdir(project)
		agents = append(agents, savedRoot(t, "same-name"))
	}
	// A third directory has no agents started from a shell, yet can reach both exact agents.
	t.Chdir(t.TempDir())
	for _, st := range agents {
		for _, sub := range []string{"wait", "report"} {
			out, err := runAgent(t, sub, "@"+st.Session[:6], "-json")
			var report struct{ Session, Message string }
			if err != nil || json.Unmarshal([]byte(out), &report) != nil || report.Session != st.Session || report.Message != "saved answer" {
				t.Fatalf("%s: %q %v", sub, out, err)
			}
		}
		if _, err := runAgent(t, "send", "@"+st.Session, "specific", "-session", "unrelated"); err != nil {
			t.Fatal(err)
		}
		if evs := events.Drain(st.Session); len(evs) != 1 {
			t.Fatalf("send did not reach exact agent: %+v", evs)
		}
	}
	// -all includes every tree of an agent started from a shell, not regular model-run trees.
	unrelated := savedIDAgent(t, "regular-root", "regular")
	out, err := runAgent(t, "list", "-all")
	if err != nil || !strings.Contains(out, "ID") || !strings.Contains(out, "ADDRESS") {
		t.Fatalf("list: %q %v", out, err)
	}
	for _, st := range agents {
		if !strings.Contains(out, "@"+st.Session) {
			t.Fatalf("ID missing: %q", out)
		}
	}
	if strings.Contains(out, unrelated.Session) {
		t.Fatalf("non-external agent listed: %q", out)
	}
	if out, err := runAgent(t, "list"); err != nil || out != "no agents\n" {
		t.Fatalf("names still project-relative: %q %v", out, err)
	}
	for _, st := range agents {
		if _, err := runAgent(t, "close", "@"+st.Session); err != nil {
			t.Fatal(err)
		}
		if path, err := session.Find(st.Session); err != nil || !isArchived(path) {
			t.Fatalf("other project's agent not archived: %s %v", path, err)
		}
	}
}

func TestAgentInsideIDScopeCannotBeOverridden(t *testing.T) {
	agentServer(t, func(int, string) string { return "" })
	t.Chdir(t.TempDir())
	enableAgents(t, "")
	own := savedIDAgent(t, "own-root", "own")
	other := savedIDAgent(t, "other-root", "other")
	for _, env := range []string{"ATTO_SESSION_ID", config.EnvAgent, config.EnvLegacyAgent} {
		t.Run(env, func(t *testing.T) {
			caller := "1"
			if env == "ATTO_SESSION_ID" {
				caller = own.Session
			}
			t.Setenv(env, caller)
			parent := own.Session
			if env == "ATTO_SESSION_ID" {
				parent = "other-root"
			} // flag cannot escape actual caller's tree
			if _, err := runAgent(t, "report", "@"+other.Session, "-session", parent); !errors.Is(err, agentstate.ErrNotFound) {
				t.Fatalf("cross-tree ID: %v", err)
			}
			if _, err := runAgent(t, "report", "@"+own.Session, "-session", parent); err != nil {
				t.Fatal("within-tree ID:", err)
			}
			if _, err := runAgent(t, "list", "-all", "-session", "other-root"); err == nil || !strings.Contains(err.Error(), "external callers only") {
				t.Fatalf("-all inside: %v", err)
			}
		})
	}
}

func TestAgentSpawnPrintsIDAddressAndListID(t *testing.T) {
	agentServer(t, func(int, string) string { return textAnswer("done") })
	t.Chdir(t.TempDir())
	enableAgents(t, "")
	out, err := runAgent(t, "spawn", "panes", "work", "-session", "root")
	if err != nil {
		t.Fatal(err)
	}
	st, err := agentstate.LoadChild("root", "panes")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { jobs.KillAll(st.Parent); jobs.KillAll(st.Session) })
	if !strings.Contains(out, "agent /root/panes started (@"+st.Session+", session "+st.Session+",") || !strings.Contains(out, "project ") || !strings.Contains(out, "job 1)") {
		t.Fatalf("spawn: %q", out)
	}
	if out, err := runAgent(t, "list", "-session", "root"); err != nil || !strings.Contains(out, "ID") || !strings.Contains(out, "@"+st.Session) {
		t.Fatalf("list: %q %v", out, err)
	}
	if _, err := runAgent(t, "wait", "@"+st.Session, "-timeout", "30s"); err != nil {
		t.Fatal(err)
	}
}

func TestAgentClosedIDInEveryAddressCommand(t *testing.T) {
	agentServer(t, func(int, string) string { return "" })
	t.Chdir(t.TempDir())
	enableAgents(t, "")
	st := savedIDAgent(t, "root", "tests")
	if _, err := runAgent(t, "close", "@"+st.Session); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"send", "task", "next", "steer", "wait", "wait-any", "report", "read", "show", "interrupt", "stop", "close", "rm"} {
		args := []string{sub, "@" + st.Session}
		if sub == "send" || sub == "task" || sub == "next" || sub == "steer" {
			args = append(args, "hello")
		}
		if _, err := runAgent(t, args...); !errors.Is(err, agentstate.ErrClosed) {
			t.Fatalf("%s: %v", sub, err)
		}
	}
}
