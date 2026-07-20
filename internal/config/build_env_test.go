package config

import (
	"path/filepath"
	"testing"
)

func TestLoadBuildEnv(t *testing.T) {
	dir := t.TempDir()
	mkApps(t, dir, "web")

	yaml := "apps:\n" +
		"  - path: " + filepath.Join(dir, "web") + "\n" +
		"    domain: web.example.com\n" +
		"    build_env:\n" +
		"      SKIP_ENV_VALIDATION: \"1\"\n" +
		"      NEXT_TELEMETRY_DISABLED: \"1\"\n"

	cfgPath := writeConfig(t, t.TempDir(), yaml)
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Apps) != 1 {
		t.Fatalf("got %d apps, want 1", len(cfg.Apps))
	}
	be := cfg.Apps[0].BuildEnv
	if got := be["SKIP_ENV_VALIDATION"]; got != "1" {
		t.Errorf("SKIP_ENV_VALIDATION = %q, want 1", got)
	}
	if got := be["NEXT_TELEMETRY_DISABLED"]; got != "1" {
		t.Errorf("NEXT_TELEMETRY_DISABLED = %q, want 1", got)
	}
}
