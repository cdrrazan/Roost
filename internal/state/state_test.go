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
