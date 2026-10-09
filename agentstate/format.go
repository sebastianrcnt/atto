package agentstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/fsutil"
)

// FormatVersion is the layout of the agent data this atto understands:
// 1 is the layout of parent directories (agent-state/<parent>/<name>.json,
// _up, _closed, the subagents symlink) and has no marker; 2 is the layout of
// this package, whose marker, agent-state/.format, `atto agent migrate`
// writes last. A marker newer than FormatVersion makes every agent command
// refuse, so an older atto never changes data it does not understand.
const FormatVersion = 2

// Marker is the content of the format marker.
type Marker struct {
	Version  int       `json:"version"`
	Migrated time.Time `json:"migrated"`
	By       string    `json:"by,omitempty"` // atto version that wrote it
	Backup   string    `json:"backup,omitempty"`
}

func markerPath() string { return filepath.Join(dir(), ".format") }

// ReadMarker reads the format marker; ok is false when there is none.
func ReadMarker() (m Marker, ok bool, err error) {
	data, err := os.ReadFile(markerPath())
	if errors.Is(err, os.ErrNotExist) {
		return m, false, nil
	}
	if err != nil {
		return m, false, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, false, fmt.Errorf("%s: %w", markerPath(), err)
	}
	return m, true, nil
}

// WriteMarker writes the marker for this layout: the last step of a migration.
func WriteMarker(by, backup string) error {
	if err := os.MkdirAll(dir(), 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(Marker{Version: FormatVersion, Migrated: time.Now().UTC(), By: by, Backup: backup}, "", "  ")
	return fsutil.WriteAtomic(markerPath(), data, 0o644)
}

// Layout is what is on disk.
type Layout int

const (
	// LayoutEmpty: no agent data at all.
	LayoutEmpty Layout = iota
	// LayoutCurrent: the marker is there.
	LayoutCurrent
	// LayoutLegacy: the old layout, or a migration that stopped before its marker.
	LayoutLegacy
	// LayoutNewer: the marker is from a newer atto.
	LayoutNewer
)

// ErrNeedsMigration is wrapped by Ready when the data has the old layout.
var ErrNeedsMigration = errors.New("agent data has the old layout")

// ErrNewerFormat is wrapped by Ready when the data is newer than this atto.
var ErrNewerFormat = errors.New("agent data is newer than this atto understands")

// legacyRoot is where the layout of version 1 sometimes lived instead.
func legacyDir() string { return filepath.Join(config.Dir(), "subagents") }

// Detect says which layout is on disk and, for the legacy one, why.
func Detect() (Layout, string, error) {
	m, ok, err := ReadMarker()
	if err != nil {
		return 0, "", err
	}
	if ok {
		if m.Version > FormatVersion {
			return LayoutNewer, fmt.Sprintf("format %d (this atto understands %d)", m.Version, FormatVersion), nil
		}
		return LayoutCurrent, "", nil
	}
	if why := legacyEvidence(dir()); why != "" {
		return LayoutLegacy, why, nil
	}
	if st, err := os.Lstat(legacyDir()); err == nil {
		if st.Mode()&os.ModeSymlink != 0 {
			if _, err := os.Stat(legacyDir()); err == nil {
				// A link to agent-state is no data of its own.
				if why := legacyEvidence(legacyDir()); why != "" {
					return LayoutLegacy, why, nil
				}
			}
		} else if why := legacyEvidence(legacyDir()); why != "" {
			return LayoutLegacy, why, nil
		}
	}
	return LayoutEmpty, "", nil
}

// legacyEvidence says what in root shows the old layout, "" for none.
func legacyEvidence(root string) string {
	ents, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	for _, e := range ents {
		name := e.Name()
		switch {
		case name == ".coord" || name == ".format":
		case e.IsDir() && !strings.HasPrefix(name, "."):
			return fmt.Sprintf("%s/%s/ is a parent directory", filepath.Base(root), name)
		case !e.IsDir() && strings.HasSuffix(name, ".json") && !strings.HasSuffix(name, ".turn.json"):
			return fmt.Sprintf("%s/%s is a record without the format marker", filepath.Base(root), name)
		}
	}
	return ""
}

// Ready checks, for an agent command, that the data has the layout this atto
// reads. A layout nobody has used yet is stamped with the marker.
func Ready() error {
	layout, why, err := Detect()
	if err != nil {
		return err
	}
	switch layout {
	case LayoutLegacy:
		return fmt.Errorf("%w (%s): run atto agent migrate", ErrNeedsMigration, why)
	case LayoutNewer:
		return fmt.Errorf("%w: %s; upgrade atto on this machine (every machine that shares ~/.atto must be upgraded)", ErrNewerFormat, why)
	case LayoutEmpty:
		return WriteMarker("", "")
	}
	return nil
}
