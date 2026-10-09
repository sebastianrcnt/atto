package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"runtime"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/session"
	"github.com/sebastianrcnt/atto/shell"
)

type statusLineRequest struct {
	command string
	cwd     string
	input   map[string]any
	refresh int
}

// statusLine takes its input on the lane but executes the configured command
// off it. There is no client-supplied command or arbitrary filesystem path.
func (t *thread) statusLine() (any, error) {
	settings, err := config.LoadSettings()
	if err != nil {
		return nil, err
	}
	cfg := settings.StatusLine
	if cfg == nil || cfg.Command == "" {
		return map[string]any{"configured": false, "lines": []string{}}, nil
	}
	info := t.info()
	used := 0
	if info.ContextWindow > 0 {
		used = info.ContextTokens * 100 / info.ContextWindow
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	lastInput, lastCached := info.Usage.LastInputTokens, info.Usage.LastCachedInputTokens
	input := map[string]any{
		"hook_event_name": "Status", "session_id": t.id, "session_name": t.name, "transcript_path": t.sess.Path,
		"cwd": t.cwd, "version": t.s.Version, "effort": info.Effort, "busy": info.Busy,
		"model":     map[string]any{"id": t.model().Model.ID, "display_name": info.ModelName, "provider": t.model().ProviderName},
		"workspace": map[string]any{"current_dir": t.cwd, "project_dir": t.cwd},
		"context_window": map[string]any{"used_tokens": info.ContextTokens, "context_window_size": info.ContextWindow,
			"used_percentage": used, "auto_compact_limit": info.AutoCompactLimit, "long": info.LongContext},
		"git_branch": session.GitBranch(t.cwd), "memory": map[string]any{"heap_bytes": mem.HeapInuse},
		"cache": map[string]any{"last_input_tokens": lastInput, "last_cached_tokens": lastCached,
			"input_tokens": info.Usage.InputTokens, "cached_tokens": info.Usage.CachedInputTokens, "output_tokens": info.Usage.OutputTokens},
	}
	return statusLineRequest{command: cfg.Command, cwd: t.cwd, input: input, refresh: cfg.RefreshInterval}, nil
}

// boundedOutput drains the whole pipe while keeping a small display prefix.
// A status command cannot consume unbounded server memory by printing forever.
type boundedOutput struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (w *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := max(0, w.limit-w.Len())
	if keep := min(n, remaining); keep > 0 {
		_, _ = w.Buffer.Write(p[:keep])
	}
	w.truncated = w.truncated || n > remaining
	return n, nil
}

func runStatusLine(ctx context.Context, r statusLineRequest) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	input, _ := json.Marshal(r.input)
	cmd := shell.Command(ctx, r.command)
	cmd.Dir, cmd.Stdin = r.cwd, bytes.NewReader(input)
	out := &boundedOutput{limit: 16 << 10}
	cmd.Stdout, cmd.Stderr = out, io.Discard
	tree := shell.NewTree(cmd)
	defer tree.Close()
	defer tree.Kill()
	if err := tree.Run(); err != nil {
		return nil, err
	}
	lines := []string{}
	if text := strings.TrimRight(out.String(), "\r\n"); text != "" {
		lines = strings.Split(text, "\n")
	}
	return map[string]any{"configured": true, "lines": lines, "refreshInterval": r.refresh, "truncated": out.truncated}, nil
}
