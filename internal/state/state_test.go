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

func TestSyncMysqlVolume(t *testing.T) {
	s := &State{}
	s.MarkSeeded("blog")

	// First observation records the identity without reporting drift or
	// clearing what is already seeded.
	if s.SyncMysqlVolume("2026-07-21T04:31:40Z") {
		t.Error("first observation must not report drift")
	}
	if !s.HasSeeded("blog") {
		t.Error("first observation must not clear the seeded set")
	}
	if s.MysqlVolumeID != "2026-07-21T04:31:40Z" {
		t.Errorf("volume id = %q, want the observed identity", s.MysqlVolumeID)
	}

	// The same identity: no drift, seeded set intact.
	if s.SyncMysqlVolume("2026-07-21T04:31:40Z") {
		t.Error("unchanged volume must not report drift")
	}
	if !s.HasSeeded("blog") {
		t.Error("unchanged volume must keep the seeded set")
	}

	// An empty identity (docker could not report it) is ignored — never a
	// reason to wipe the seeded set.
	if s.SyncMysqlVolume("") {
		t.Error("empty identity must not report drift")
	}
	if !s.HasSeeded("blog") || s.MysqlVolumeID != "2026-07-21T04:31:40Z" {
		t.Error("empty identity must not mutate state")
	}

	// A new identity: the volume was recreated, so the recorded seeds no
	// longer match reality — report drift and clear them.
	if !s.SyncMysqlVolume("2026-07-21T09:00:00Z") {
		t.Error("recreated volume must report drift")
	}
	if s.HasSeeded("blog") {
		t.Error("drift must clear the seeded set")
	}
	if s.MysqlVolumeID != "2026-07-21T09:00:00Z" {
		t.Errorf("volume id = %q, want the updated identity", s.MysqlVolumeID)
	}
}

func TestSyncMysqlVolumeNoSeedsIsNotDrift(t *testing.T) {
	// A volume change with nothing seeded yet has nothing to reset, so it is
	// not reported as a drift — but the new identity is still recorded.
	s := &State{MysqlVolumeID: "old"}
	if s.SyncMysqlVolume("new") {
		t.Error("no seeded apps => not a meaningful drift")
	}
	if s.MysqlVolumeID != "new" {
		t.Errorf("volume id = %q, want the new identity recorded", s.MysqlVolumeID)
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
