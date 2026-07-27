package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/cdrrazan/roost/internal/web"
)

// panelStore persists the control panel's settings to ~/.roost/panel.json.
//
// It holds only recipient/host/template + UI preferences — never the SMTP
// password (that stays in $ROOST_SMTP_PASSWORD). The file is written 0600 so a
// backup or screen-share doesn't leak the recipient list.
type panelStore struct{ path string }

// newPanelStore points the store at ~/.roost/panel.json.
func newPanelStore() (*panelStore, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &panelStore{path: filepath.Join(home, ".roost", "panel.json")}, nil
}

// Load reads settings, returning normalized defaults when the file is absent
// (a fresh install has no panel.json yet) so the panel always starts cleanly.
func (p *panelStore) Load() (web.Settings, error) {
	b, err := os.ReadFile(p.path)
	if errors.Is(err, fs.ErrNotExist) {
		return web.DefaultSettings(), nil
	}
	if err != nil {
		return web.DefaultSettings(), err
	}
	var s web.Settings
	if err := json.Unmarshal(b, &s); err != nil {
		return web.DefaultSettings(), fmt.Errorf("panel.json: %w", err)
	}
	return s.Normalize(), nil
}

// Save writes normalized settings 0600, creating ~/.roost if needed.
func (p *panelStore) Save(s web.Settings) error {
	if err := os.MkdirAll(filepath.Dir(p.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.Normalize(), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p.path, b, 0o600)
}

// statFile is a tiny indirection so tests can assert file permissions without
// importing os at the call site.
func statFile(path string) (fs.FileInfo, error) { return os.Stat(path) }
