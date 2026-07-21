// Package state persists roost's remote-side knowledge in
// ~/.roost/state.json: the tunnel it created, the account in use, and
// every DNS record it made, so down/uninstall can clean up only what
// roost owns.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Record is one DNS record roost created.
type Record struct {
	ID     string `json:"id"`
	ZoneID string `json:"zone_id"`
	Name   string `json:"name"`
}

// State is the persisted contents of state.json.
type State struct {
	AccountID  string   `json:"account_id,omitempty"`
	TunnelID   string   `json:"tunnel_id,omitempty"`
	TunnelName string   `json:"tunnel_name,omitempty"`
	Records    []Record `json:"records,omitempty"`
	// Seeded is the set of app names roost has already run demo seeds
	// for, so `roost up` seeds each app once instead of on every run.
	Seeded []string `json:"seeded,omitempty"`
}

// Path returns the state file location under the given home dir.
func Path(home string) string {
	return filepath.Join(home, ".roost", "state.json")
}

// Load reads state from path. A missing file is an empty state, not an
// error.
func Load(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &State{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse state %s: %w", path, err)
	}
	return &s, nil
}

// Save writes state to path, creating parent directories as needed.
func (s *State) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write state %s: %w", path, err)
	}
	return nil
}

// AddRecord records a DNS record roost created, deduplicating by ID.
func (s *State) AddRecord(r Record) {
	for _, have := range s.Records {
		if have.ID == r.ID {
			return
		}
	}
	s.Records = append(s.Records, r)
}

// HasSeeded reports whether roost has already seeded the named app.
func (s *State) HasSeeded(app string) bool {
	for _, have := range s.Seeded {
		if have == app {
			return true
		}
	}
	return false
}

// MarkSeeded records that the named app has been seeded, deduplicating.
func (s *State) MarkSeeded(app string) {
	if s.HasSeeded(app) {
		return
	}
	s.Seeded = append(s.Seeded, app)
}
