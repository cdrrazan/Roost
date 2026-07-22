package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRedisSpec(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(path, []byte(`
domain: example.com
apps:
  - path: ./a
    redis: true
  - path: ./b
    redis: false
  - path: ./c
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Apps[0].Redis.Set || !cfg.Apps[0].Redis.Enabled {
		t.Errorf("app a redis = %+v, want set + enabled", cfg.Apps[0].Redis)
	}
	if !cfg.Apps[1].Redis.Set || cfg.Apps[1].Redis.Enabled {
		t.Errorf("app b redis = %+v, want set + disabled", cfg.Apps[1].Redis)
	}
	if cfg.Apps[2].Redis.Set {
		t.Errorf("app c redis = %+v, want unset (absent falls back to detection)", cfg.Apps[2].Redis)
	}
}

func TestResolveWorker(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Domain: "example.com",
		Apps: []App{
			{Path: dir, Name: "web"},
			{Path: dir, Name: "worker", Worker: true, Command: "bundle exec sidekiq"},
		},
	}
	resolved, skipped, err := Resolve(cfg)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(skipped) != 0 {
		t.Fatalf("unexpected skips: %+v", skipped)
	}
	by := map[string]ResolvedApp{}
	for _, r := range resolved {
		by[r.Name] = r
	}
	if by["web"].FQDN != "web.example.com" {
		t.Errorf("web FQDN = %q, want web.example.com", by["web"].FQDN)
	}
	if by["worker"].FQDN != "" {
		t.Errorf("worker FQDN = %q, want empty (no route)", by["worker"].FQDN)
	}
}

func TestResolveWorkerNeedsCommand(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Domain: "example.com",
		Apps:   []App{{Path: dir, Name: "w", Worker: true}},
	}
	if _, _, err := Resolve(cfg); err == nil {
		t.Fatal("want an error for a worker without a command")
	} else if !strings.Contains(err.Error(), "command") {
		t.Errorf("error = %v, want it to mention command", err)
	}
}

func TestLoadRejectsNonBooleanRedis(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(path, []byte(`
apps:
  - path: ./a
    redis: "yes please"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("want an error for a non-boolean redis value")
	}
}
