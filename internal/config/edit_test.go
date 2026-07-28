package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const commentedConfig = `# my roost config — precious comment
domain: demo.example.com

apps:
  # the first app
  - ~/projects/app1
`

func writeEditConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(commentedConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAddApp(t *testing.T) {
	t.Run("bare path when no domain given", func(t *testing.T) {
		path := writeEditConfig(t)
		if err := AddApp(path, "~/projects/app2", "", ""); err != nil {
			t.Fatalf("AddApp: %v", err)
		}
		data, _ := os.ReadFile(path)
		s := string(data)
		if !strings.Contains(s, "precious comment") || !strings.Contains(s, "the first app") {
			t.Errorf("comments clobbered:\n%s", s)
		}
		if !strings.Contains(s, "~/projects/app2") {
			t.Errorf("app2 not added:\n%s", s)
		}
	})

	t.Run("map entry when domain given", func(t *testing.T) {
		path := writeEditConfig(t)
		if err := AddApp(path, "~/projects/app3", "app3.other.org", ""); err != nil {
			t.Fatalf("AddApp: %v", err)
		}
		data, _ := os.ReadFile(path)
		s := string(data)
		if !strings.Contains(s, "app3.other.org") {
			t.Errorf("domain not written:\n%s", s)
		}
		if !strings.Contains(s, "precious comment") {
			t.Errorf("comments clobbered:\n%s", s)
		}
	})

	t.Run("repo written as a map entry with path and repo", func(t *testing.T) {
		path := writeEditConfig(t)
		if err := AddApp(path, "~/.roost/sources/app4", "", "https://github.com/u/app4"); err != nil {
			t.Fatalf("AddApp: %v", err)
		}
		home := t.TempDir()
		t.Setenv("HOME", home)
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("Load after AddApp: %v", err)
		}
		var got *App
		for i := range cfg.Apps {
			if cfg.Apps[i].Repo == "https://github.com/u/app4" {
				got = &cfg.Apps[i]
			}
		}
		if got == nil {
			t.Fatalf("app with repo not found; apps: %+v", cfg.Apps)
		}
		if !strings.HasSuffix(got.Path, "sources/app4") {
			t.Errorf("path = %q, want it to end in sources/app4", got.Path)
		}
	})

	t.Run("result still parses", func(t *testing.T) {
		path := writeEditConfig(t)
		if err := AddApp(path, "~/projects/app2", "app2.example.com", ""); err != nil {
			t.Fatal(err)
		}
		home := t.TempDir()
		t.Setenv("HOME", home)
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("Load after AddApp: %v", err)
		}
		if len(cfg.Apps) != 2 {
			t.Errorf("apps = %d, want 2", len(cfg.Apps))
		}
	})

	t.Run("apps key that is null gets a real list", func(t *testing.T) {
		// What `roost init` writes with no apps found: an apps: key
		// followed only by commented-out examples — YAML-null.
		path := filepath.Join(t.TempDir(), "config.yml")
		content := "domain: example.com\n\ntunnel:\n  name: roost\n\napps:\n  # - path: ~/projects/app1\n  #   domain: app1.example.com\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := AddApp(path, "/apps/one", "one.example.com", ""); err != nil {
			t.Fatalf("AddApp: %v", err)
		}
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("Load after AddApp: %v", err)
		}
		if len(cfg.Apps) != 1 || cfg.Apps[0].Domain != "one.example.com" {
			t.Fatalf("apps = %+v, want the added app to actually persist", cfg.Apps)
		}
	})

	t.Run("apps key created when missing", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yml")
		if err := os.WriteFile(path, []byte("domain: example.com\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := AddApp(path, "/apps/one", "", ""); err != nil {
			t.Fatalf("AddApp: %v", err)
		}
		data, _ := os.ReadFile(path)
		if !strings.Contains(string(data), "/apps/one") {
			t.Errorf("app not added:\n%s", data)
		}
	})
}

func TestRemoveApp(t *testing.T) {
	t.Run("removes by name and keeps comments", func(t *testing.T) {
		path := writeEditConfig(t)
		if err := AddApp(path, "~/projects/app2", "", ""); err != nil {
			t.Fatal(err)
		}
		if err := RemoveApp(path, "app2"); err != nil {
			t.Fatalf("RemoveApp: %v", err)
		}
		data, _ := os.ReadFile(path)
		s := string(data)
		if strings.Contains(s, "app2") {
			t.Errorf("app2 still present:\n%s", s)
		}
		if !strings.Contains(s, "app1") {
			t.Errorf("app1 removed too:\n%s", s)
		}
		if !strings.Contains(s, "precious comment") {
			t.Errorf("comments clobbered:\n%s", s)
		}
	})

	t.Run("removes map-form app by explicit name", func(t *testing.T) {
		path := writeEditConfig(t)
		if err := AddApp(path, "~/projects/whatever", "x.example.com", ""); err != nil {
			t.Fatal(err)
		}
		if err := RemoveApp(path, "whatever"); err != nil {
			t.Fatalf("RemoveApp: %v", err)
		}
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), "whatever") {
			t.Errorf("app not removed:\n%s", data)
		}
	})

	t.Run("unknown name errors and names known apps", func(t *testing.T) {
		path := writeEditConfig(t)
		err := RemoveApp(path, "ghost")
		if err == nil {
			t.Fatal("want error for unknown app")
		}
		if !strings.Contains(err.Error(), "ghost") || !strings.Contains(err.Error(), "app1") {
			t.Errorf("error %q should name the missing app and the known ones", err)
		}
	})
}
