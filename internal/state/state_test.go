package state

import (
	"path/filepath"
	"testing"
)

func TestLoadMissingIsEmpty(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.TunnelID != "" || len(s.Records) != 0 {
		t.Errorf("state = %+v, want empty", s)
	}
}

func TestSeededTracking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := &State{}
	if s.HasSeeded("blog") {
		t.Error("blog should not be seeded on an empty state")
	}
	s.MarkSeeded("blog")
	s.MarkSeeded("blog") // dedupe
	if !s.HasSeeded("blog") {
		t.Error("blog should be seeded after MarkSeeded")
	}
	if err := s.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.HasSeeded("blog") {
		t.Error("seeded set did not survive a save/load round trip")
	}
	if len(got.Seeded) != 1 {
		t.Errorf("seeded = %v, want a single deduplicated entry", got.Seeded)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	s := &State{AccountID: "acc1", TunnelID: "tun-1", TunnelName: "roost"}
	s.AddRecord(Record{ID: "r1", ZoneID: "z1", Name: "*.example.com"})
	s.AddRecord(Record{ID: "r1", ZoneID: "z1", Name: "*.example.com"}) // dedupe
	if err := s.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.TunnelID != "tun-1" || got.AccountID != "acc1" {
		t.Errorf("state = %+v", got)
	}
	if len(got.Records) != 1 {
		t.Errorf("records = %+v, want deduplicated single record", got.Records)
	}
}
