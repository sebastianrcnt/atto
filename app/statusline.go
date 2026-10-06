package app

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/shell"
	"github.com/sebastianrcnt/atto/tui"
)

// statusInput is the JSON a statusLine command receives on stdin. Field
// names follow Claude Code's status line input where one exists.
type statusInput struct {
	HookEventName  string `json:"hook_event_name"`
	SessionID      string `json:"session_id"`
	SessionName    string `json:"session_name,omitempty"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	Version        string `json:"version"`
	Model          struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
		Provider    string `json:"provider"`
	} `json:"model"`
	Workspace struct {
		CurrentDir string `json:"current_dir"`
		ProjectDir string `json:"project_dir"`
	} `json:"workspace"`
	ContextWindow struct {
		UsedTokens       int  `json:"used_tokens"`
		Size             int  `json:"context_window_size"`
		UsedPercentage   int  `json:"used_percentage"`
		AutoCompactLimit int  `json:"auto_compact_limit"`
		Long             bool `json:"long"`
	} `json:"context_window"`
	Effort    string `json:"effort"`
	GitBranch string `json:"git_branch,omitempty"`
	Busy      bool   `json:"busy"`
	Memory    struct {
		RSSBytes int64 `json:"rss_bytes"`
	} `json:"memory"`
	Cache struct {
		LastInputTokens  int `json:"last_input_tokens"`
		LastCachedTokens int `json:"last_cached_tokens"`
		InputTokens      int `json:"input_tokens"`  // session total
		CachedTokens     int `json:"cached_tokens"` // session total
		OutputTokens     int `json:"output_tokens"` // session total
	} `json:"cache"`
}

func (a *App) statusInput() statusInput {
	m, effort := a.agent.Current()
	var in statusInput
	in.HookEventName = "Status"
	in.SessionID = a.sess.ID
	in.SessionName = a.sessName
	in.TranscriptPath = a.sess.Path
	in.Cwd = a.cwd
	in.Version = Version
	in.Model.ID = m.Model.ID
	in.Model.DisplayName = m.Model.DisplayName()
	in.Model.Provider = m.ProviderName
	in.Workspace.CurrentDir = a.cwd
	in.Workspace.ProjectDir = a.cwd
	in.ContextWindow.UsedTokens = a.ctxTokens
	in.ContextWindow.Size = m.Model.ContextWindow
	if m.Model.ContextWindow > 0 {
		in.ContextWindow.UsedPercentage = a.ctxTokens * 100 / m.Model.ContextWindow
	}
	in.ContextWindow.AutoCompactLimit, _ = a.agent.CompactionLimit()
	in.ContextWindow.Long = a.agent.LongContext()
	in.Effort = effort
	in.GitBranch = a.gitBranch
	in.Busy = a.busy
	in.Memory.RSSBytes = rssBytes.Load()
	in.Cache.LastInputTokens = a.usage.last.PromptTokens
	in.Cache.LastCachedTokens = a.usage.last.CachedTokens
	in.Cache.InputTokens, in.Cache.CachedTokens, in.Cache.OutputTokens = a.usage.input, a.usage.cached, a.usage.output
	return in
}

// gitBranch finds the branch checked out in dir or a parent, reading
// .git/HEAD directly so it costs no process.
func gitBranch(dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		gitPath := filepath.Join(d, ".git")
		if st, err := os.Stat(gitPath); err == nil {
			if !st.IsDir() { // worktree: "gitdir: <path>"
				b, err := os.ReadFile(gitPath)
				if err != nil {
					return ""
				}
				gitPath = strings.TrimSpace(strings.TrimPrefix(string(b), "gitdir:"))
				if !filepath.IsAbs(gitPath) {
					gitPath = filepath.Join(d, gitPath)
				}
			}
			head, err := os.ReadFile(filepath.Join(gitPath, "HEAD"))
			if err != nil {
				return ""
			}
			h := strings.TrimSpace(string(head))
			if ref, ok := strings.CutPrefix(h, "ref: refs/heads/"); ok {
				return ref
			}
			if len(h) >= 7 {
				return h[:7] // detached
			}
			return ""
		}
		if d == filepath.Dir(d) {
			return ""
		}
	}
}

// startStatusLine runs background refreshes: memory and git branch every
// 2s, and the custom statusLine command when its input changes.
func (a *App) startStatusLine(cfg *config.StatusLine) {
	a.statusWake = make(chan struct{}, 1)
	startMemoryMonitor(2*time.Second, a.quit, func() {
		branch := gitBranch(a.cwd)
		a.ui.Do(func() { a.gitBranch = branch })
		a.statusTrigger()
	})
	if cfg == nil || cfg.Command == "" {
		return
	}
	go a.statusLoop(cfg)
}

// statusTrigger asks the custom status line to refresh (debounced).
func (a *App) statusTrigger() {
	if a.statusWake == nil {
		return
	}
	select {
	case a.statusWake <- struct{}{}:
	default:
	}
}

func statusRefreshDuration(seconds int) time.Duration {
	return time.Duration(min(int64(seconds), int64((1<<63-1)/time.Second))) * time.Second
}

func (a *App) statusLoop(cfg *config.StatusLine) {
	var last []byte
	var refresh <-chan time.Time
	if cfg.RefreshInterval > 0 {
		t := time.NewTicker(statusRefreshDuration(cfg.RefreshInterval))
		defer t.Stop()
		refresh = t.C
	}
	for {
		forced := false
		select {
		case <-a.quit:
			return
		case <-a.statusWake:
		case <-refresh:
			forced = true
		}
		time.Sleep(300 * time.Millisecond) // debounce, as Claude Code does
		var in statusInput
		a.ui.Do(func() { in = a.statusInput() })
		input, _ := json.Marshal(in)
		if !forced && bytes.Equal(input, last) {
			continue
		}
		last = input
		lines, err := runStatusCommand(cfg.Command, input, a.cwd)
		a.ui.Do(func() {
			if err != nil {
				a.statusLines = []string{tui.FG(1, "statusLine: "+err.Error())}
			} else {
				a.statusLines = lines
			}
		})
	}
}

func runStatusCommand(command string, input []byte, cwd string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := shell.Command(ctx, command)
	cmd.Dir = cwd
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	s := strings.TrimRight(string(out), "\n")
	if s == "" {
		return nil, nil
	}
	return strings.Split(s, "\n"), nil
}

// renderStatus draws the custom status line if configured, otherwise the
// built-in one:
//
//	◆ Orca Local · medium  ━━─────── 12% 31k/262k · cache 93%    name · ~/proj (main) · 18MB
//
// The built-in line takes a second row when one is too narrow (see
// builtinStatus). Only characters with an unambiguous width (box drawing renders as one
// column everywhere the editor rules do) so CJK terminals line up.
func (a *App) renderStatus(width int) []string {
	a.paneSync()
	// The goal indicator goes at the right end of the first row, as codex's
	// footer shows it; the row gives up room for it. On a terminal too narrow
	// for both, it takes a row of its own.
	ind := a.goalIndicator()
	rowWidth, own := width, false
	if ind != "" {
		if rowWidth = width - tui.VisibleWidth(ind) - 2; rowWidth < minStatusWithGoal {
			rowWidth, own = width, true
		}
	}
	var out []string
	if a.statusCmd {
		for i, l := range a.statusLines {
			w := width
			if i == 0 {
				w = rowWidth
			}
			out = append(out, tui.Truncate(" "+l, w, "…"))
		}
	} else {
		out = a.builtinStatus(rowWidth, width)
	}
	if len(out) > 0 {
		out[0] = a.withToast(out[0], rowWidth)
		if ind != "" && !own {
			out[0] += strings.Repeat(" ", max(1, width-1-tui.VisibleWidth(out[0])-tui.VisibleWidth(ind))) + ind
		}
	}
	// Transient indicators go on their own line so custom output is untouched.
	flags := a.extensionStatus()
	if ind != "" && own {
		flags = append(flags, ind)
	}
	if a.jobCount > 0 {
		flags = append(flags, tui.FG(2, fmt.Sprintf("● %d job%s running (/jobs)", a.jobCount, plural(a.jobCount))))
	}
	if a.timerCount > 0 {
		flags = append(flags, tui.FG(4, fmt.Sprintf("⏱ %d timer%s (/timers)", a.timerCount, plural(a.timerCount))))
	}
	if r := a.remoteStatus(); r != "" {
		flags = append(flags, r)
	}
	if len(flags) > 0 {
		out = append(out, tui.Truncate(" "+strings.Join(flags, tui.Dim(" · ")), width, "…"))
	}
	return out
}

// minStatusWithGoal is the room the status row needs to share it with the
// goal indicator (the model, the context bar).
const minStatusWithGoal = 24

func contextBar(pct, cells int) string {
	filled := min(cells, (pct*cells+50)/100)
	return tui.FG(6, strings.Repeat("━", filled)) + strings.Repeat("─", cells-filled)
}

// compactTokens is pi's footer notation: 950, 1.2k, 12k, 1.2M, 12M.
func compactTokens(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprint(n)
	case n < 10000:
		return strings.Replace(fmt.Sprintf("%.1fk", float64(n)/1e3), ".0k", "k", 1)
	case n < 1000000:
		return fmt.Sprintf("%dk", (n+500)/1000)
	case n < 10000000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	}
	return fmt.Sprintf("%dM", (n+500000)/1000000)
}

// priced reports whether the model has prices (local models have none).
func priced(m config.Model) bool {
	c := m.Cost
	return c != nil && (c.Input > 0 || c.Output > 0 || c.CacheRead > 0 || c.CacheWrite > 0)
}

// statusItem is one piece of the built-in status line. Items are dropped
// when even two rows cannot hold them, lowest drop level first (see the
// drop levels below); level 0 is never dropped.
type statusItem struct {
	text  string
	pre   string // separator before it (spaces); a dot by default
	drop  int
	right bool
	w     int // visible width of text
}

// statusRun is the width of items joined with their separators.
func statusRun(its []statusItem, sepW int) int {
	w := 0
	for i, it := range its {
		if i > 0 {
			w += cmp.Or(len(it.pre), sepW)
		}
		w += it.w
	}
	return w
}

func joinStatus(b *strings.Builder, its []statusItem, sep string) {
	for i, it := range its {
		if i > 0 {
			b.WriteString(cmp.Or(it.pre, sep))
		}
		b.WriteString(it.text)
	}
}

// Drop levels, least important first, as pi's footer gives way: memory,
// cache totals, cache hit rate, token totals, cost, the
// directory, the context size, the effort.
const (
	dropMem = iota + 1
	dropCacheTotals
	dropCacheRate
	dropTokens
	dropCost
	dropPath
	dropCtxSize
	dropEffort
)

// minPath is the room the directory needs to be worth showing.
const minPath = 14

// builtinStatus is atto's default status line, with pi's footer items:
//
//	◆ model · effort  ━━─── 12% 31k/262k · cache 93% · ↑12k ↓3.4k · R80k W2k · $0.123   ~/proj (main) · 18MB
//
// The left side is the model and the usage, the right side where we are.
// The first row is first columns wide (the goal indicator takes the rest
// of width). When one row cannot hold everything, the right side moves to
// a second row, still at the right, and the left items that do not fit the
// first row start the second:
//
//	◆ model · effort  ━━─── 12% 31k/262k · cache 93% · ↑12k ↓3.4k
//	R80k W2k · $0.123                          ~/proj (main) · 18MB
//
// Items are dropped only when two rows cannot hold them.
func (a *App) builtinStatus(first, width int) []string {
	m, effort := a.agent.Current()
	// The rows only change with what they are made of, which is far less
	// often than frames are drawn, so keep them until it changes.
	limit, _ := a.agent.CompactionLimit()
	k := statusKey{
		first: first, width: width, effort: effort,
		compactLimit: limit, long: a.agent.LongContext(), surcharge: a.surcharge(m),
		name: a.models.DisplayName(m), subscription: m.Provider.Subscription, priced: priced(m.Model),
		ctxWindow: m.Model.ContextWindow, maxTokens: m.Model.MaxTokens,
		reasoning: m.Model.Reasoning != nil && *m.Model.Reasoning, hasLevels: len(m.Model.Levels()) > 0,
		usage: a.usage, ctxTokens: a.ctxTokens, sessName: a.sessName, mem: fmtBytes(rssBytes.Load()),
		branch: a.gitBranch, cwd: a.cwd,
	}
	if c := &a.statusRows; c.rows != nil && c.key == k {
		return slices.Clone(c.rows)
	}
	rows := a.buildStatus(m, effort, first, width)
	a.statusRows.key, a.statusRows.rows = k, slices.Clone(rows)
	return rows
}

// statusKey is everything builtinStatus reads.
type statusKey struct {
	compactLimit          int
	long                  bool
	surcharge             string
	first, width          int
	effort, name          string
	subscription, priced  bool
	ctxWindow, maxTokens  int
	reasoning             bool
	hasLevels             bool // what buildStatus reads of the efforts
	usage                 usageStats
	ctxTokens             int
	sessName, mem, branch string
	cwd                   string
}

// statusCache is builtinStatus' rows for key.
type statusCache struct {
	key  statusKey
	rows []string
}

func (a *App) buildStatus(m config.ModelRef, effort string, first, width int) []string {
	sep := tui.Dim(" · ")
	u := &a.usage

	items := []statusItem{{text: tui.FG(6, "◆ ") + a.models.DisplayName(m)}}
	if effort != "" && len(m.Model.Levels()) > 0 {
		items = append(items, statusItem{text: effortStyle(effort), drop: dropEffort})
	}
	if cw := m.Model.ContextWindow; cw > 0 {
		pct := a.ctxTokens * 100 / cw
		style := tui.Dim
		if limit, _ := a.agent.CompactionLimit(); limit > 0 && a.ctxTokens*100/limit >= 80 {
			style = func(s string) string { return tui.FG(3, s) } // nearing auto-compaction
		}
		label := fmt.Sprintf(" %d%%", pct)
		if a.agent.LongContext() {
			label += " long"
		}
		items = append(items,
			statusItem{text: style(contextBar(pct, 10) + label), pre: "  "},
			statusItem{text: style(fmt.Sprintf("%s/%s", tui.FormatTokens(a.ctxTokens), tui.FormatTokens(cw))), pre: " ", drop: dropCtxSize})
	}
	if c := u.cacheLabel(); c != "" {
		items = append(items, statusItem{text: tui.Dim(c), drop: dropCacheRate})
	}
	var io, rw []string
	if n := u.fresh(); n > 0 {
		io = append(io, "↑"+compactTokens(n))
	}
	if u.output > 0 {
		io = append(io, "↓"+compactTokens(u.output))
	}
	// Cache reads are left out: a long conversation reads its whole prefix
	// back on every request, so their total says little (the hit rate does).
	if u.cacheWrite > 0 {
		rw = append(rw, "W"+compactTokens(u.cacheWrite))
	}
	if len(io) > 0 {
		items = append(items, statusItem{text: tui.Dim(strings.Join(io, " ")), drop: dropTokens})
	}
	if len(rw) > 0 {
		items = append(items, statusItem{text: tui.Dim(strings.Join(rw, " ")), drop: dropCacheTotals})
	}
	// Only a model with prices has a cost; a local model's session is free.
	// On a subscription the prices only estimate what the usage would cost
	// over the API.
	if priced(m.Model) || u.cost > 0 {
		cost := fmt.Sprintf("$%.3f", u.cost) + a.surcharge(m)
		if m.Provider.Subscription {
			cost = "≈" + cost
		}
		items = append(items, statusItem{text: tui.Dim(cost), drop: dropCost})
	}

	items = append(items, statusItem{text: tui.Dim(fmtBytes(rssBytes.Load())), drop: dropMem, right: true})
	for i := range items {
		items[i].w = tui.VisibleWidth(items[i].text)
	}

	branch := ""
	if a.gitBranch != "" {
		branch = " (" + a.gitBranch + ")"
	}
	where := shortPath(a.cwd)
	whereW, branchW, sepW := tui.VisibleWidth(where+branch), tui.VisibleWidth(branch), tui.VisibleWidth(sep)

	// row lays out one row w wide: l at the left, the directory (when
	// path) and r at the right. ok is false when they do not fit; the
	// directory is shortened from the front to the room that remains.
	row := func(l, r []statusItem, w int, path bool) (s string, ok bool) {
		lw, rw := statusRun(l, sepW), statusRun(r, sepW)
		gapMin := 0
		if len(l) > 0 {
			gapMin = 2
		}
		dir := ""
		if path {
			room := w - 1 - lw - gapMin
			if len(r) > 0 {
				room -= sepW + rw
			}
			if room < min(minPath, whereW) {
				return "", false
			}
			dir = tui.Dim(compressPath(where, room-branchW) + branch)
			if len(r) > 0 {
				dir += sep
			}
			rw += tui.VisibleWidth(dir)
		}
		if len(l) == 0 && rw == 0 {
			return "", true
		}
		var b strings.Builder
		b.WriteByte(' ')
		joinStatus(&b, l, sep)
		gap := w - 1 - lw - rw
		if rw == 0 {
			return tui.Truncate(b.String(), w, "…"), gap >= 0
		}
		if gap < gapMin {
			return tui.Truncate(b.String(), w, "…"), false
		}
		b.WriteString(strings.Repeat(" ", gap))
		b.WriteString(dir)
		joinStatus(&b, r, sep)
		return b.String(), true
	}

	var left, right []statusItem
	keep := func(cut int) {
		left, right = left[:0], right[:0]
		for _, it := range items {
			switch {
			case it.drop != 0 && it.drop <= cut:
			case it.right:
				right = append(right, it)
			default:
				left = append(left, it)
			}
		}
	}

	keep(0)
	if s, ok := row(left, right, first, true); ok {
		return []string{s}
	}
	for cut := 0; ; cut++ {
		keep(cut)
		// The first row takes the left items in order while they fit (the
		// model always); the rest start the second.
		n, w := 1, left[0].w
		for ; n < len(left); n++ {
			add := cmp.Or(len(left[n].pre), sepW) + left[n].w
			if 1+w+add > first {
				break
			}
			w += add
		}
		second, ok := row(left[n:], right, width, cut < dropPath)
		if !ok && cut < dropEffort {
			continue
		}
		var b strings.Builder
		b.WriteByte(' ')
		joinStatus(&b, left[:n], sep)
		out := []string{tui.Truncate(b.String(), first, "…")}
		if second != "" {
			out = append(out, second)
		}
		return out
	}
}

// compressPath shortens p to at most w columns by dropping leading
// directories: "~/a/b/c/d" -> "…/c/d".
func compressPath(p string, w int) string {
	if tui.VisibleWidth(p) <= w {
		return p
	}
	parts := strings.Split(p, "/")
	for i := 1; i < len(parts); i++ {
		c := "…/" + strings.Join(parts[i:], "/")
		if tui.VisibleWidth(c) <= w {
			return c
		}
	}
	return tui.Truncate(parts[len(parts)-1], max(1, w), "…")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// surcharge uses the last request's prices, even after a model switch.
func (a *App) surcharge(m config.ModelRef) string {
	cost := a.usage.lastCost
	if cost == nil {
		cost = m.Model.Cost
	}
	if x := cost.InputMultiplier(a.usage.last.PromptTokens); x > 1 {
		return fmt.Sprintf(" ×%.0f", x)
	}
	return ""
}
