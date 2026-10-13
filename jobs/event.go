package jobs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sebastianrcnt/atto/events"
	"github.com/sebastianrcnt/atto/fsutil"
)

// postEvent tells the session how the job ended.
func postEvent(j Job, detail string) error {
	var text, title string
	switch {
	case j.Monitor != nil:
		what := "condition met"
		if j.ExitCode != nil && *j.ExitCode != 0 {
			what = "gave up"
		}
		text = fmt.Sprintf("Monitor %d (%q) %s after %s: %s\nFull log: atto job output %d", j.ID, j.Label(), what, j.Runtime(), detail, j.ID)
		title = fmt.Sprintf("◎ monitor %d %s: %s", j.ID, what, j.Label())
	case j.Status == Failed:
		text = fmt.Sprintf("Background job %d (%q) failed to start: %s", j.ID, j.Label(), detail)
		title = fmt.Sprintf("● job %d failed to start: %s", j.ID, j.Label())
	default:
		tail, _ := Tail(j.Session, j.ID, eventTail)
		text = fmt.Sprintf("Background job %d (%q) exited with code %d after %s.", j.ID, j.Label(), *j.ExitCode, j.Runtime())
		if strings.TrimSpace(tail) != "" {
			text += "\nLast output:\n" + tail
		}
		text += fmt.Sprintf("\nFull output: atto job output %d", j.ID)
		title = fmt.Sprintf("● job %d exited (%d) after %s: %s", j.ID, *j.ExitCode, j.Runtime(), j.Label())
	}
	if err := events.Push(j.Session, events.Event{Source: j.Kind(), Text: text, Title: title, Quiet: j.QuietExit}); err != nil {
		return fmt.Errorf("posting job %d exit event: %w", j.ID, err)
	}
	return nil
}

// reportSupervisionError uses independent channels: the job log and session
// inbox. Either may be unavailable with a full disk; return both errors too.
func reportSupervisionError(dir string, j Job, cause error) error {
	text := fmt.Sprintf("Background job %d (%q) supervision failed: %v. The recorded state may be stale; check the log before restarting it.", j.ID, j.Label(), cause)
	logErr := appendFailureLog(dir, text)
	eventErr := events.Push(j.Session, events.Event{Source: j.Kind(), Text: text,
		Title: fmt.Sprintf("job %d supervision failed: %s", j.ID, j.Label()), Quiet: j.Silent || j.QuietExit})
	return errors.Join(logErr, eventErr)
}

func appendFailureLog(dir, text string) error {
	f, err := os.OpenFile(filepath.Join(dir, "output.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if err := fsutil.PrivateFile(f); err != nil {
		return errors.Join(err, f.Close())
	}
	_, writeErr := fmt.Fprintf(f, "\n[atto: %s]\n", text)
	return errors.Join(writeErr, f.Close())
}
