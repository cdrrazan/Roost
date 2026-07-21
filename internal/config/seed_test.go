package config

import (
	"path/filepath"
	"testing"
)

func TestLoadSeedSpec(t *testing.T) {
	dir := t.TempDir()
	mkApps(t, dir, "a", "b", "c")

	yaml := "apps:\n" +
		"  - path: " + filepath.Join(dir, "a") + "\n" +
		"    domain: a.example.com\n" +
		"    seed: true\n" +
		"  - path: " + filepath.Join(dir, "b") + "\n" +
		"    domain: b.example.com\n" +
		"    seed: \"bin/rails db:prepare && SEED_DEMO=1 bin/rails db:seed\"\n" +
		"  - path: " + filepath.Join(dir, "c") + "\n" +
		"    domain: c.example.com\n"

	cfgPath := writeConfig(t, t.TempDir(), yaml)
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Apps) != 3 {
		t.Fatalf("got %d apps, want 3", len(cfg.Apps))
	}

	// seed: true → enabled, no explicit command (framework default).
	if !cfg.Apps[0].Seed.Enabled {
		t.Error("app a: seed: true should be enabled")
	}
	if cfg.Apps[0].Seed.Command != "" {
		t.Errorf("app a: command = %q, want empty (framework default)", cfg.Apps[0].Seed.Command)
	}

	// seed: "<cmd>" → enabled with that command.
	if !cfg.Apps[1].Seed.Enabled {
		t.Error("app b: string seed should be enabled")
	}
	if cfg.Apps[1].Seed.Command == "" {
		t.Error("app b: command should carry the custom string")
	}

	// absent → disabled.
	if cfg.Apps[2].Seed.Enabled {
		t.Error("app c: no seed key should be disabled")
	}
}

func TestSeedSpecFalseDisables(t *testing.T) {
	dir := t.TempDir()
	mkApps(t, dir, "a")
	yaml := "apps:\n" +
		"  - path: " + filepath.Join(dir, "a") + "\n" +
		"    domain: a.example.com\n" +
		"    seed: false\n"
	cfg, err := Load(writeConfig(t, t.TempDir(), yaml))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Apps[0].Seed.Enabled {
		t.Error("seed: false should disable seeding")
	}
}
