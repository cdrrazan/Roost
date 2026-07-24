package main

import (
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
