package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/sebastianrcnt/atto/provider"
)

func TestReadActiveMatchesLoad(t *testing.T) {
	t.Setenv("ATTO_DIR", t.TempDir())
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint(legacy), func(t *testing.T) {
			w := New("/work")
			w.Append(Entry{Type: TypeMessage, Message: &provider.Message{Role: "user", Content: "start"}})
			root := w.Leaf()
			for i := range 5 {
				w.Append(Entry{Type: TypeModel, Provider: "p", Model: fmt.Sprint(i)})
				w.Append(Entry{Type: TypeEffort, Effort: "high"})
				w.Append(Entry{Type: TypeContext, LongContext: i%2 == 0})
				w.Append(Entry{Type: TypeName, Name: fmt.Sprint(i)})
				w.Append(Entry{Type: TypeGoal, Goal: json.RawMessage(`{"text":"goal"}`)})
				w.Append(Entry{Type: TypeMessage, Message: &provider.Message{Role: "assistant", Content: strings.Repeat("answer", 100)}, Usage: &provider.Usage{PromptTokens: i + 1, CachedTokens: i, CompletionTokens: 2}})
				w.Append(Entry{Type: TypeCompaction, Notes: "notes", Replacement: []provider.Message{{Role: "user", Content: "notes"}}})
				w.Branch(root)
			}
			w.Append(Entry{Type: TypeMessage, Message: &provider.Message{Role: "user", Content: "active"}})
			w.Append(Entry{Type: TypeCompaction, Replacement: []provider.Message{{Role: "user", Content: "active notes"}}})
			w.Append(Entry{Type: TypeMessage, Message: &provider.Message{Role: "assistant", Content: "end"}})
			w.Close()
			if legacy {
				h, entries, err := Load(w.Path)
				if err != nil {
					t.Fatal(err)
				}
				f, err := os.Create(w.Path)
				if err != nil {
					t.Fatal(err)
				}
				enc := json.NewEncoder(f)
				enc.Encode(h)
				for _, e := range entries {
					e.ID, e.Parent = "", ""
					enc.Encode(e)
				}
				f.Close()
			}
			h, entries, err := Load(w.Path)
			if err != nil {
				t.Fatal(err)
			}
			got, err := ReadActive(w.Path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Header, h) || !reflect.DeepEqual(got.Entries, Active(entries)) {
				t.Fatal("active branch differs from Load")
			}
			context, err := ReadContext(w.Path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(context.Entries, Context(Active(entries))) {
				t.Fatal("context differs from Load")
			}
			state := map[string]Entry{}
			var total, last provider.Usage
			model, usageModel := "", ""
			for _, e := range entries {
				if e.Type == TypeModel {
					model = e.Provider + "/" + e.Model
				}
				if e.Usage != nil {
					x := *e.Usage
					total.PromptTokens += x.PromptTokens
					total.CachedTokens += x.CachedTokens
					total.CompletionTokens += x.CompletionTokens
					total.CacheWriteTokens += x.CacheWriteTokens
					total.Cost += x.Cost
					last, usageModel = x, model
				}
				if e.Type == TypeModel || e.Type == TypeEffort || e.Type == TypeContext || e.Type == TypeName || e.Type == TypeGoal {
					state[e.Type] = Entry{Type: e.Type, ID: e.ID, Parent: e.Parent, Provider: e.Provider, Model: e.Model, Effort: e.Effort, Name: e.Name, LongContext: e.LongContext, Goal: e.Goal}
				}
			}
			if len(got.State) != len(state) {
				t.Fatal("snapshots are not bounded")
			}
			for _, e := range got.State {
				if !reflect.DeepEqual(e, state[e.Type]) {
					t.Fatal("session-wide snapshot differs")
				}
			}
			if got.Usage != total || got.LastUsage != last || got.UsageModel != usageModel {
				t.Fatal("usage scan differs")
			}
		})
	}
}

// BenchmarkOpenSessionHeap reports retained heap, not just allocation traffic.
// Each file has 49 MiB of old text and 1 MiB on the active tail. Display
// necessarily keeps old active text; context-only readers do not.
func BenchmarkOpenSessionHeap(b *testing.B) {
	for _, compacted := range []bool{false, true} {
		name := "branched"
		if compacted {
			name = "compacted"
		}
		b.Run(name, func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "large.jsonl")
			f, err := os.Create(path)
			if err != nil {
				b.Fatal(err)
			}
			enc := json.NewEncoder(f)
			enc.Encode(Entry{Type: TypeSession, ID: "session"})
			text := strings.Repeat("x", 1024*1024)
			for i := range 49 {
				enc.Encode(Entry{Type: TypeMessage, ID: fmt.Sprint(i), Parent: fmt.Sprint(i - 1), Message: &provider.Message{Role: "assistant", Content: text}})
			}
			marker := Entry{Type: TypeBranch, ID: "branch"}
			if compacted {
				marker.Type, marker.Parent = TypeCompaction, "48"
				marker.Replacement = []provider.Message{{Role: "user", Content: "notes"}}
			}
			enc.Encode(marker)
			enc.Encode(Entry{Type: TypeMessage, ID: "active", Parent: "branch", Message: &provider.Message{Role: "assistant", Content: text}})
			f.Close()
			for _, mode := range []string{"load", "display", "context"} {
				b.Run(mode, func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						runtime.GC()
						var before, after runtime.MemStats
						runtime.ReadMemStats(&before)
						var entries []Entry
						switch mode {
						case "load":
							_, x, err := Load(path)
							if err != nil {
								b.Fatal(err)
							}
							entries = x
						case "display", "context":
							read := ReadActive
							if mode == "context" {
								read = ReadContext
							}
							x, err := read(path)
							if err != nil {
								b.Fatal(err)
							}
							entries = x.Entries
						}
						runtime.GC()
						runtime.ReadMemStats(&after)
						runtime.KeepAlive(entries)
						b.ReportMetric(float64(after.HeapAlloc)-float64(before.HeapAlloc), "retained-B")
					}
				})
			}
		})
	}
}
