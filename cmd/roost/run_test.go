package main

import (
	"strings"
	"testing"

	"github.com/cdrrazan/roost/internal/state"
)

// seedDecision is the single source of truth for whether an app gets seeded on
// `roost up`: --no-seed suppresses all seeding, --reseed forces it, and the
// default seeds only apps not yet recorded in state.
func TestSeedDecision(t *testing.T) {
	seeded := &state.State{}
	seeded.MarkSeeded("blog")
	// fresh has no seeded apps.
	fresh := &state.State{}

	cases := []struct {
		name    string
		noSeed  bool
		reseed  bool
		st      *state.State
		app     string
		want    bool
		comment string
	}{
		{"no-seed wins over unseeded", true, false, fresh, "blog", false, "--no-seed must skip even a never-seeded app"},
		{"no-seed wins over reseed", true, true, fresh, "blog", false, "--no-seed must override --reseed"},
		{"reseed forces already-seeded", false, true, seeded, "blog", true, "--reseed re-runs an already-seeded app"},
		{"default seeds unseeded", false, false, fresh, "blog", true, "default seeds an app absent from state"},
		{"default skips already-seeded", false, false, seeded, "blog", false, "default seeds each app once"},
	}
	for _, c := range cases {
		got := seedDecision(c.noSeed, c.reseed, c.st)(c.app)
		if got != c.want {
			t.Errorf("%s: seedDecision(noSeed=%v, reseed=%v)(%q) = %v, want %v — %s",
				c.name, c.noSeed, c.reseed, c.app, got, c.want, c.comment)
		}
	}
}

// The volume-recreated notice must not promise a re-seed under --no-seed,
// since seeding is suppressed for that run.
func TestMysqlVolumeRecreatedNotice(t *testing.T) {
	if got := mysqlVolumeRecreatedNotice(false); !strings.Contains(got, "re-seeding") {
		t.Errorf("default notice should mention re-seeding, got %q", got)
	}
	got := mysqlVolumeRecreatedNotice(true)
	if strings.Contains(got, "re-seeding") {
		t.Errorf("--no-seed notice must not promise re-seeding, got %q", got)
	}
	if !strings.Contains(got, "skip") {
		t.Errorf("--no-seed notice should say seeding is skipped, got %q", got)
	}
}
