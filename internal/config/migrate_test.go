package config

import (
	"path/filepath"
	"testing"
)

func TestLoadMigrateSpec(t *testing.T) {
	dir := t.TempDir()
	mkApps(t, dir, "a", "b", "c", "d")

	yaml := "apps:\n" +
		"  - path: " + filepath.Join(dir, "a") + "\n" +
		"    domain: a.example.com\n" +
		"    migrate: false\n" +
		"  - path: " + filepath.Join(dir, "b") + "\n" +
		"    domain: b.example.com\n" +
		"    migrate: \"bin/rails db:migrate\"\n" +
		"  - path: " + filepath.Join(dir, "c") + "\n" +
		"    domain: c.example.com\n" +
		"    migrate: true\n" +
		"  - path: " + filepath.Join(dir, "d") + "\n" +
		"    domain: d.example.com\n"

	cfg, err := Load(writeConfig(t, t.TempDir(), yaml))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Apps) != 4 {
		t.Fatalf("got %d apps, want 4", len(cfg.Apps))
	}

	// migrate: false → present and disabled (app self-migrates).
	if !cfg.Apps[0].Migrate.Set || cfg.Apps[0].Migrate.Enabled {
		t.Errorf("app a: migrate: false should be Set && !Enabled, got %+v", cfg.Apps[0].Migrate)
	}

	// migrate: "<cmd>" → present, enabled, custom command.
	if !cfg.Apps[1].Migrate.Set || !cfg.Apps[1].Migrate.Enabled {
		t.Errorf("app b: string migrate should be Set && Enabled, got %+v", cfg.Apps[1].Migrate)
	}
	if cfg.Apps[1].Migrate.Command != "bin/rails db:migrate" {
		t.Errorf("app b: command = %q, want the custom string", cfg.Apps[1].Migrate.Command)
	}

	// migrate: true → present, enabled, no explicit command (framework default).
	if !cfg.Apps[2].Migrate.Set || !cfg.Apps[2].Migrate.Enabled {
		t.Errorf("app c: migrate: true should be Set && Enabled, got %+v", cfg.Apps[2].Migrate)
	}
	if cfg.Apps[2].Migrate.Command != "" {
		t.Errorf("app c: command = %q, want empty (framework default)", cfg.Apps[2].Migrate.Command)
	}

	// absent → not Set (roost applies its framework default).
	if cfg.Apps[3].Migrate.Set {
		t.Errorf("app d: no migrate key should leave Migrate unset, got %+v", cfg.Apps[3].Migrate)
	}
}
