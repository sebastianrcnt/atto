// Package approval records decisions about executable project content by hash.
package approval

import (
	"path/filepath"
	"strings"
)

type Status int

const (
	Approved Status = iota
	Pending
	Denied
)

// Decisions keeps the existing project approval files' JSON shape.
type Decisions struct {
	Approved map[string]string `json:"approved"`
	Denied   map[string]string `json:"denied,omitempty"`
}

// Init makes an empty or legacy decision file ready to edit.
func (d *Decisions) Init() {
	if d.Approved == nil {
		d.Approved = map[string]string{}
	}
	if d.Denied == nil {
		d.Denied = map[string]string{}
	}
}

func (d Decisions) Of(key, hash string) Status {
	if hash == "" {
		return Pending
	}
	switch {
	case d.Approved[key] == hash:
		return Approved
	case d.Denied[key] == hash:
		return Denied
	default:
		return Pending
	}
}

func (d *Decisions) Set(key, hash string, allow bool) {
	d.Init()
	if allow {
		d.Approved[key] = hash
		delete(d.Denied, key)
	} else {
		d.Denied[key] = hash
		delete(d.Approved, key)
	}
}

func (d *Decisions) Forget(key string) {
	d.Init()
	delete(d.Approved, key)
	delete(d.Denied, key)
}

func (d *Decisions) ForgetPrefix(prefix string) {
	d.Init()
	for key := range d.Approved {
		if strings.HasPrefix(key, prefix) {
			delete(d.Approved, key)
		}
	}
	for key := range d.Denied {
		if strings.HasPrefix(key, prefix) {
			delete(d.Denied, key)
		}
	}
}

// Path preserves the absolute, cleaned path spelling used in existing keys.
// It deliberately does not resolve symlinks or change case.
func Path(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return filepath.Clean(path)
}
