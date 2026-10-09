// Package session persists conversations as append-only JSONL files under
// ~/.atto/sessions/YYYY/MM/DD/<YYYYMMDD-HHMMSS>-<id>.jsonl.
//
// The first line is a "session" header. Every later line is one Entry,
// written and flushed as it happens, so a crash loses at most the line
// being written.
//
// Entries form a tree, as in pi: each has an id and the id of its parent
// (see tree.go). The last entry in the file is the active leaf, and the
// conversation is the path from the root to it. Going back to an earlier
// point appends a "branch" entry whose parent is that point, so the file
// stays append-only and every branch is kept. Replaying the path in order
// reconstructs the conversation: a "compaction" entry replaces all earlier
// messages with the handoff notes.
package session

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/sebastianrcnt/atto/fsutil"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/provider"
)

const Version = 1

// Entry types.
const (
	TypeSession    = "session"
	TypeMessage    = "message"
	TypeCompaction = "compaction"
	TypeModel      = "model"
	TypeEffort     = "effort"
	TypeContext    = "context"
	TypeName       = "name"
	TypeGoal       = "goal"
	TypeLabel      = "label"  // a bookmark on another entry (/tree shift+l)
	TypeBranch     = "branch" // moves the active leaf to its parent (/tree)
	// TypeBranchSummary moves the active leaf like "branch" and carries a
	// summary of the branch left behind (pi's branch_summary), which the
	// model sees on the new branch.
	TypeBranchSummary = "branch_summary"
	// TypeBashExecution is a shell command the user ran with "!" or "!!"
	// in the prompt; unless excluded, the model sees it as a user message.
	TypeBashExecution = "bash_execution"
	// TypeBlockDisplay is display-only data an extension set on an
	// assistant message's text or reasoning block: a status suffix and a
	// replacement text the block shows. It never reaches the model.
	TypeBlockDisplay = "block_display"
	// TypeExtText is a block of text an extension added to the transcript
	// (ctx.ui.showText): display only, it never reaches the model.
	TypeExtText = "ext_text"
)

// Blocks of an assistant message, as TypeBlockDisplay entries name them.
const (
	BlockText      = "text"
	BlockReasoning = "reasoning"
)

// BlockID is the ID extensions and front ends use for a block of the
// assistant message recorded as entry entryID in session sessionID. It is
// stable for the life of the session file, and unlike entry IDs (which an
// old file numbers by position) cannot collide between sessions.
func BlockID(sessionID, entryID, block string) string {
	return sessionID + "." + entryID + ":" + block
}

// Entry is one line of a session file. Fields are used according to Type.
type Entry struct {
	Type string    `json:"type"`
	Time time.Time `json:"time"`

	// ID is the session ID on the header and the entry's own ID on every
	// other line. Parent is the entry this one follows ("" for a root).
	// Files written before entries had IDs are linked on Load.
	ID     string `json:"id,omitempty"`
	Parent string `json:"parentId,omitempty"`

	// session header
	Version       int    `json:"version,omitempty"`
	Cwd           string `json:"cwd,omitempty"`
	ParentSession string `json:"parentSession,omitempty"` // path of the session this was forked from
	GitBranch     string `json:"gitBranch,omitempty"`     // branch checked out in Cwd when the session began ("HEAD" if detached)
	// AgentOf is the ID of the session an agent session works for (atto
	// agent). Such sessions are left out of default listings.
	AgentOf  string `json:"agentOf,omitempty"`
	External bool   `json:"external,omitempty"` // lightweight external orchestration parent

	// message
	Message    *provider.Message `json:"message,omitempty"`
	Usage      *provider.Usage   `json:"usage,omitempty"`      // assistant messages
	ThinkingMs int64             `json:"thinkingMs,omitempty"` // assistant messages
	Tool       *ToolMeta         `json:"tool,omitempty"`       // tool messages

	// compaction: Replacement is the full message history after compaction.
	Replacement  []provider.Message `json:"replacement,omitempty"`
	Notes        string             `json:"notes,omitempty"`
	TokensBefore int                `json:"tokensBefore,omitempty"`
	TokensAfter  int                `json:"tokensAfter,omitempty"` // the estimate right after
	ElapsedMs    int64              `json:"elapsedMs,omitempty"`   // how long writing the notes took
	Auto         bool               `json:"auto,omitempty"`
	Reason       string             `json:"reason,omitempty"` // a cap lowered the trigger: price-tier or setting
	Cap          int                `json:"cap,omitempty"`    // that cap, in tokens
	Finish       string             `json:"finish,omitempty"` // how the notes' answer ended: stop, length...

	// context
	LongContext bool `json:"longContext,omitempty"`

	// model / effort
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`

	// name
	Name string `json:"name,omitempty"`

	// goal: a snapshot of the session's goal (null when cleared)
	Goal json.RawMessage `json:"goal,omitempty"`

	// label: Label names TargetID ("" clears it)
	TargetID string `json:"targetId,omitempty"`
	Label    string `json:"label,omitempty"`

	// branch, branch_summary: the leaf the user navigated away from
	FromID string `json:"fromId,omitempty"`

	// branch_summary: the summary of the branch from FromID back to where
	// it meets the new one (ElapsedMs is how long writing it took)
	Summary string `json:"summary,omitempty"`

	// bash_execution
	Bash *BashExec `json:"bash,omitempty"`

	// block_display: the block Block (BlockText or BlockReasoning) of the
	// assistant message TargetID, and what extension Ext shows there: a
	// short Status for the block's header and a Display text that replaces
	// what the block shows. Each entry is the extension's whole state for
	// the block (both empty clears it); the latest wins.
	Block   string `json:"block,omitempty"`
	Ext     string `json:"ext,omitempty"`
	Status  string `json:"status,omitempty"`
	Display string `json:"display,omitempty"`

	// ext_text: extension Ext showed the text in Display under Title;
	// Lang says how to colour it ("diff") and Preview how many lines show
	// while it is collapsed (0: the default).
	Title   string `json:"title,omitempty"`
	Lang    string `json:"lang,omitempty"`
	Preview int    `json:"preview,omitempty"`
}

// BashExec is a command the user ran with "!" (or "!!", which keeps it
// out of the model's context).
type BashExec struct {
	Command string `json:"command"`
	// Output is the command's output as shown and as sent to the model:
	// tidied, and cut in the middle when long (Truncated; the whole of it
	// is in FullOutputPath).
	Output         string `json:"output,omitempty"`
	ExitCode       int    `json:"exitCode"`
	Cancelled      bool   `json:"cancelled,omitempty"`
	Truncated      bool   `json:"truncated,omitempty"`
	FullOutputPath string `json:"fullOutputPath,omitempty"`
	// Exclude keeps the command and its output from the model ("!!").
	Exclude    bool  `json:"excludeFromContext,omitempty"`
	DurationMs int64 `json:"durationMs,omitempty"`
}

// ToolMeta records how a tool call went, for redisplay on resume.
type ToolMeta struct {
	Description string `json:"description,omitempty"`
	ExitCode    int    `json:"exitCode"`
	DurationMs  int64  `json:"durationMs"`
	TimedOut    bool   `json:"timedOut,omitempty"`
	Canceled    bool   `json:"canceled,omitempty"`
	// Job is the background job the command became, and Background why
	// (agent.BackgroundRequested, ...).
	Job        int    `json:"job,omitempty"`
	Background string `json:"background,omitempty"`
}

// Writer appends entries to a session file. The file is created lazily on
// the first entry so empty sessions leave nothing behind.
type Writer struct {
	mu       sync.Mutex
	ID       string
	Path     string
	cwd      string
	created  time.Time
	parent   string // ParentSession for the header
	branch   string // GitBranch for the header
	agentOf  string // AgentOf for the header
	external bool
	leaf     string // ID of the last entry: the parent of the next one
	hasLeaf  bool   // leaf is known; else read from the file on open
	f        *os.File
	err      error
	closed   bool // Close ran: later writes open the file, write and close it again
	// readOnly, when set, is why nothing is written (see lock.go).
	readOnly string
}

// SetReadOnly makes w drop every entry, for a session another process is
// writing; why is shown to the user.
func (w *Writer) SetReadOnly(why string) {
	w.mu.Lock()
	w.readOnly = why
	w.mu.Unlock()
}

// ReadOnly returns why w writes nothing, "" if it does.
func (w *Writer) ReadOnly() string {
	if w == nil {
		return ""
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.readOnly
}

func newID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// New prepares a writer for a fresh session in cwd.
func New(cwd string) *Writer {
	now := time.Now()
	id := newID()
	path := filepath.Join(config.SessionsDir(), now.Format("2006/01/02"), now.Format("20060102-150405")+"-"+id+".jsonl")
	return &Writer{ID: id, Path: path, cwd: cwd, created: now, branch: GitBranch(cwd)}
}

// NewExternal prepares a lightweight parent for external orchestration.
func NewExternal(cwd string) *Writer {
	w := New(cwd)
	w.external = true
	return w
}

// NewAgent is New for the session of an agent working for parent.
func NewAgent(cwd, parent string) *Writer {
	w := New(cwd)
	w.agentOf = parent
	return w
}

// Resume returns a writer that appends to an existing session file. New
// entries continue from its last entry; SetLeaf(Leaf(entries)) saves
// reading the file again to find it.
func Resume(path string, h Entry) *Writer {
	return &Writer{ID: h.ID, Path: path, cwd: h.Cwd, created: h.Time}
}

// SetLeaf sets the entry the next appended entry follows.
func (w *Writer) SetLeaf(id string) {
	w.mu.Lock()
	w.leaf, w.hasLeaf = id, true
	w.mu.Unlock()
}

// Leaf returns the ID of the last entry written (the active leaf).
func (w *Writer) Leaf() string {
	if w == nil {
		return ""
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.leaf
}

func (w *Writer) open() error {
	if underRoot(w.Path, config.ArchivedDir()) || strings.HasSuffix(w.Path, ".zst") {
		w.err = fmt.Errorf("archived sessions are read-only; unarchive first")
		return w.err
	}
	if w.f != nil || w.err != nil {
		return w.err
	}
	if err := fsutil.PrivateDirs(config.Dir(), filepath.Dir(w.Path)); err != nil {
		w.err = err
		return err
	}
	_, statErr := os.Stat(w.Path)
	f, err := os.OpenFile(w.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		w.err = err
		return err
	}
	if err := fsutil.PrivateFile(f); err != nil {
		f.Close()
		w.err = err
		return err
	}
	w.f = f
	if statErr == nil {
		// Existing file: if a crash left a partial last line, terminate it so
		// new entries start on their own line.
		if st, err := f.Stat(); err == nil && st.Size() > 0 {
			last := make([]byte, 1)
			if r, err := os.Open(w.Path); err == nil {
				_, _ = r.ReadAt(last, st.Size()-1)
				r.Close()
			}
			if last[0] != '\n' {
				_, _ = f.Write([]byte{'\n'})
			}
		}
		if !w.hasLeaf {
			if _, nodes, err := scanActive(w.Path, true); err == nil {
				w.leaf = ""
				if len(nodes) > 0 {
					w.leaf = nodes[len(nodes)-1].ID
				}
				w.hasLeaf = true
			}
		}
	}
	if statErr != nil { // new file: write the header
		return w.write(Entry{Type: TypeSession, Time: w.created, Version: Version, ID: w.ID, Cwd: w.cwd, ParentSession: w.parent, GitBranch: w.branch, AgentOf: w.agentOf, External: w.external})
	}
	return nil
}

func (w *Writer) write(e Entry) error {
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = w.f.Write(append(line, '\n'))
	return err
}

// Append writes e as a child of the active leaf, which it becomes. It
// stamps the time if unset. Errors are sticky and reported by Err;
// persistence never interrupts the conversation.
func (w *Writer) Append(e Entry) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.appendLocked(e, nil)
}

// Branch moves the active leaf to the entry with ID to ("" for before the
// first entry), recording a "branch" entry there so the move survives a
// resume. Later entries continue from it; the old branch stays in the file.
func (w *Writer) Branch(to string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.appendLocked(Entry{Type: TypeBranch}, &to)
}

// BranchSummary is Branch with a summary of the branch being left: e (of
// type TypeBranchSummary, with Summary set) is written as a child of to
// and becomes the leaf, as in pi.
func (w *Writer) BranchSummary(to string, e Entry) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	e.Type = TypeBranchSummary
	w.appendLocked(e, &to)
}

// appendLocked writes e as a child of parent, or of the leaf if nil.
func (w *Writer) appendLocked(e Entry, parent *string) {
	if w.readOnly != "" {
		return
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	if err := w.open(); err != nil { // finds the leaf of a resumed file
		return
	}
	e.ID, e.Parent = newEntryID(), w.leaf
	if parent != nil {
		e.Parent, e.FromID = *parent, w.leaf
	}
	err := w.write(e)
	if w.closed { // a late write (an extension's session_end, say) keeps no handle open
		w.f.Close()
		w.f = nil
	}
	if err != nil {
		if w.err == nil {
			w.err = err
		}
		return
	}
	w.leaf, w.hasLeaf = e.ID, true
}

func (w *Writer) Err() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

func (w *Writer) Close() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	if w.f != nil {
		w.f.Close()
		w.f = nil
	}
}

// Load reads a session file: the header and the entries after it.
// A truncated last line (from a crash mid-write) is ignored.
func Load(path string) (Entry, []Entry, error) {
	f, err := Open(path)
	if err != nil {
		return Entry{}, nil, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	var header Entry
	var entries []Entry
	for first := true; ; first = false {
		line, err := r.ReadBytes('\n')
		if err != nil && err != io.EOF {
			return Entry{}, nil, err
		}
		if len(line) == 0 && err == io.EOF {
			break
		}
		var e Entry
		if parseErr := json.Unmarshal(line, &e); parseErr != nil {
			if first {
				return Entry{}, nil, fmt.Errorf("%s: invalid session header: %w", path, parseErr)
			}
			continue
		}
		if first {
			if e.Type != TypeSession {
				return Entry{}, nil, fmt.Errorf("%s: not an atto session", path)
			}
			header = e
			continue
		}
		entries = append(entries, e)
	}

	if header.Type != TypeSession {
		return Entry{}, nil, fmt.Errorf("%s: missing session header", path)
	}
	link(entries)
	return header, entries, nil
}

// Summary describes a stored session for the resume picker.
type Summary struct {
	Path        string
	ID          string
	Name        string // from the latest "name" entry
	Model       string // provider/id from the latest "model" entry
	Archived    bool
	Cwd         string
	Created     time.Time
	Updated     time.Time
	Preview     string // first user message
	LastMessage string // bounded preview of the active branch's last assistant message
	Messages    int    // user + assistant messages
	Branch      string // git branch when the session began; "" if unknown
	Size        int64  // file size in bytes
	Running     int    // pid of the background process writing it; 0 if none
	AgentOf     string // the session an agent session works for
	External    bool
}

// List returns sessions, newest first. If cwd is non-empty only sessions
// started in that directory are returned. archived selects archived
// sessions instead of active ones. Worker sessions are left out: they
// are reached through atto agent (or by ID).
func List(cwd string, archived bool) ([]Summary, error) {
	return list(cwd, archived, false)
}

// ListAll is List including agent sessions, even before their first message.
// It is intended for views of the complete session tree.
func ListAll(cwd string, archived bool) ([]Summary, error) {
	return list(cwd, archived, true)
}

func list(cwd string, archived, agents bool) ([]Summary, error) {
	var out []Summary
	root := config.SessionsDir()
	if archived {
		root = config.ArchivedDir()
	}
	seen := make(map[string]bool)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !IsSessionFile(path) {
			return nil
		}
		if archived && archiveShadowed(path) {
			return nil
		}
		seen[path] = true
		s, err := listSummaryMode(path, agents)
		if err != nil || (cwd != "" && !SameDir(s.Cwd, cwd)) || (s.Preview == "" && !s.External && !(agents && s.AgentOf != "")) || (!agents && s.AgentOf != "") {
			return nil
		}
		s.Archived = archived
		out = append(out, s)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	forgetSummaries(root, seen)
	saveDiskSummaries(false)
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}

// SameDir compares two directory paths the way the OS does: Windows paths
// are case-insensitive, so "C:\\Users\\me\\desktop" (as cmd's "cd desktop"
// leaves it) is the same directory as "...\\Desktop".
func SameDir(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// Summarize reads one session file's summary, as List does.
func Summarize(path string) (Summary, error) { return listSummaryMode(path, true) }

// Rename gives the session at path a name, as a "name" entry.
func Rename(path, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("name is empty")
	}
	h, err := ReadHeader(path)
	if err != nil {
		return err
	}
	w := Resume(path, h)
	w.Append(Entry{Type: TypeName, Name: name})
	w.Close()
	return w.Err()
}

// Latest returns the most recently updated conversation for cwd, excluding
// lightweight external parents.
func Latest(cwd string) (Summary, bool) {
	l, err := List(cwd, false)
	if err == nil {
		for _, s := range l {
			if !s.External {
				return s, true
			}
		}
	}
	return Summary{}, false
}
