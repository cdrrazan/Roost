package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile writes content at path, creating parent directories.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadIncludeMergesApps(t *testing.T) {
	base := t.TempDir()
	mkApps(t, filepath.Join(base, "projects"), "blog", "shop")
	mkApps(t, base, "main-app")

	// Included files use paths relative to their OWN directory (apps/).
	writeFile(t, filepath.Join(base, "apps", "blog.yml"),
		"apps:\n  - path: ../projects/blog\n    domain: blog.example.com\n    framework: rails\n")
	writeFile(t, filepath.Join(base, "apps", "shop.yml"),
		"apps:\n  - ../projects/shop\n")

	main := "domain: example.com\ninclude: apps/*.yml\napps:\n  - " +
		filepath.Join(base, "main-app") + "\n"
	cfgPath := writeConfig(t, base, main)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Main's own apps come first, then includes in lexical file order
	// (blog before shop).
	wantPaths := []string{
		filepath.Join(base, "main-app"),
		filepath.Join(base, "projects", "blog"),
		filepath.Join(base, "projects", "shop"),
	}
	if len(cfg.Apps) != len(wantPaths) {
		t.Fatalf("got %d apps, want %d: %+v", len(cfg.Apps), len(wantPaths), cfg.Apps)
	}
	for i, want := range wantPaths {
		if cfg.Apps[i].Path != want {
			t.Errorf("app %d path = %q, want %q", i, cfg.Apps[i].Path, want)
		}
	}
	if got := cfg.Apps[1].Domain; got != "blog.example.com" {
		t.Errorf("blog domain = %q, want blog.example.com", got)
	}
	if got := cfg.Apps[1].Framework; got != "rails" {
		t.Errorf("blog framework = %q, want rails", got)
	}
}

func TestLoadIncludeListForm(t *testing.T) {
	base := t.TempDir()
	mkApps(t, filepath.Join(base, "projects"), "a", "b")
	writeFile(t, filepath.Join(base, "one", "a.yml"), "apps:\n  - ../projects/a\n")
	writeFile(t, filepath.Join(base, "two", "b.yml"), "apps:\n  - ../projects/b\n")

	main := "domain: example.com\ninclude:\n  - one/*.yml\n  - two/*.yml\n"
	cfgPath := writeConfig(t, base, main)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Apps) != 2 {
		t.Fatalf("got %d apps, want 2: %+v", len(cfg.Apps), cfg.Apps)
	}
	// Patterns apply in listed order: one/ before two/.
	if cfg.Apps[0].Path != filepath.Join(base, "projects", "a") {
		t.Errorf("app 0 = %q", cfg.Apps[0].Path)
	}
	if cfg.Apps[1].Path != filepath.Join(base, "projects", "b") {
		t.Errorf("app 1 = %q", cfg.Apps[1].Path)
	}
}

func TestLoadIncludeNoMatch(t *testing.T) {
	base := t.TempDir()
	cfgPath := writeConfig(t, base, "domain: example.com\ninclude: apps/*.yml\n")
	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected error for include pattern matching no files")
	}
	if !strings.Contains(err.Error(), "matched no files") {
		t.Errorf("error = %v, want 'matched no files'", err)
	}
}

func TestLoadIncludeRejectsExtraKeys(t *testing.T) {
	base := t.TempDir()
	mkApps(t, filepath.Join(base, "projects"), "blog")
	// A stray top-level key (domain) in an included file is an error:
	// included files carry apps only.
	writeFile(t, filepath.Join(base, "apps", "blog.yml"),
		"domain: nope.example.com\napps:\n  - ../projects/blog\n")
	cfgPath := writeConfig(t, base, "include: apps/*.yml\n")
	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected error for extra key in included file")
	}
	if !strings.Contains(err.Error(), "apps:") {
		t.Errorf("error = %v, want it to mention apps:", err)
	}
}

func TestLoadIncludeRejectsNesting(t *testing.T) {
	base := t.TempDir()
	mkApps(t, filepath.Join(base, "projects"), "blog")
	writeFile(t, filepath.Join(base, "apps", "blog.yml"),
		"include: more/*.yml\napps:\n  - ../projects/blog\n")
	cfgPath := writeConfig(t, base, "include: apps/*.yml\n")
	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected error for nested include in included file")
	}
}

func TestLoadIncludeEmptyFile(t *testing.T) {
	base := t.TempDir()
	writeFile(t, filepath.Join(base, "apps", "empty.yml"), "apps: []\n")
	cfgPath := writeConfig(t, base, "include: apps/*.yml\n")
	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected error for included file with no apps")
	}
	if !strings.Contains(err.Error(), "no apps") {
		t.Errorf("error = %v, want 'no apps'", err)
	}
}
