package main

import (
	"path/filepath"
	"testing"

	"github.com/cdrrazan/roost/internal/web"
)

func TestPanelStoreMissingFileIsDefaults(t *testing.T) {
	p := &panelStore{path: filepath.Join(t.TempDir(), "panel.json")}
	got, err := p.Load()
	if err != nil {
		t.Fatalf("Load on missing file: %v", err)
	}
	if got.SMTPPort != 587 || got.DefaultView != "list" || got.MonitorMins != 2 {
		t.Errorf("missing file should yield defaults, got %+v", got)
	}
}

func TestPanelStoreRoundTripAnd0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.json")
	p := &panelStore{path: path}
	want := web.Settings{
		EmailTo: []string{"me@example.com"}, SMTPHost: "smtp.example.com",
		DefaultView: "grid", DefaultTheme: "dark", MaskSensitive: true,
		TechStacks: map[string]string{"rails": "Ruby on Rails"}, MonitorMins: 5,
	}
	if err := p.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// The password must never be persisted, whatever the caller passes: the
	// struct has no such field, so this just documents intent — the file is
	// owner-only regardless.
	fi, err := statFile(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("panel.json perm = %o, want 600", perm)
	}
	got, err := p.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.SMTPHost != "smtp.example.com" || got.DefaultView != "grid" || !got.MaskSensitive || got.TechStacks["rails"] != "Ruby on Rails" || got.MonitorMins != 5 {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}
