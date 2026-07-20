package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cdrrazan/roost/internal/config"
)

// runCLI executes the root command with args and returns combined output.
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

// fixturePath returns the absolute path of a detect testdata fixture.
func fixturePath(t *testing.T, name string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "..", "internal", "detect", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// writeTestConfig writes a config with two resolvable fixture apps and
// one app whose path is missing (so it gets skipped), returning the
// config path.
func writeTestConfig(t *testing.T) string {
	t.Helper()
	gone := filepath.Join(t.TempDir(), "gone-app")
	yaml := `domain: demo.example.com
apps:
  - ` + fixturePath(t, "rails-app") + `
  - path: ` + fixturePath(t, "django-app") + `
    domain: crm.other.org
  - ` + gone + `
`
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestListCommand(t *testing.T) {
	out, err := runCLI(t, "--config", writeTestConfig(t), "list")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	for _, want := range []string{
		"rails-app",
		"https://rails-app.demo.example.com",
		"rails",
		"https://crm.other.org",
		"django",
		"skipped",
		"path does not exist",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}
}

func TestDetectCommand(t *testing.T) {
	out, err := runCLI(t, "--config", writeTestConfig(t), "detect")
	if err != nil {
		t.Fatalf("detect: %v\n%s", err, out)
	}
	for _, want := range []string{
		"rails-app",
		"Gemfile + config/application.rb",
		"3000",
		"mysql",
		"django-app",
		"8000",
		"skipped",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("detect output missing %q:\n%s", want, out)
		}
	}
}

func TestGenerateCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	out, err := runCLI(t, "--config", writeTestConfig(t), "generate")
	if err != nil {
		t.Fatalf("generate: %v\n%s", err, out)
	}
	build := filepath.Join(home, ".roost", "build")
	for _, rel := range []string{
		"compose.yml",
		"Caddyfile",
		"mysql-init.sql",
		filepath.Join("dockerfiles", "rails-app.Dockerfile"),
		filepath.Join("dockerfiles", "django-app.Dockerfile"),
	} {
		if _, err := os.Stat(filepath.Join(build, rel)); err != nil {
			t.Errorf("expected artifact %s: %v", rel, err)
		}
	}
	if !strings.Contains(out, "skipped") {
		t.Errorf("generate output should report the skipped app:\n%s", out)
	}
	if !strings.Contains(out, "wrote") {
		t.Errorf("generate output should list written files:\n%s", out)
	}
}

func TestInitCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLOUDFLARE_API_TOKEN", "")

	// A scan folder containing one detectable app and one plain dir.
	scan := t.TempDir()
	for _, f := range []struct{ rel, content string }{
		{"shop/package.json", `{"dependencies":{"express":"^4"}}`},
		{"junk/readme.txt", "not an app"},
	} {
		path := filepath.Join(scan, f.rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(f.content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out, err := runCLI(t, "init", "--domain", "example.com", "--scan", scan)
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}

	cfgPath := filepath.Join(home, ".roost", "config.yml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	s := string(data)
	for _, want := range []string{
		"domain: example.com",
		"tunnel:",
		"name: roost",              // written explicitly, never generated
		"domain: shop.example.com", // explicit per-app hostname
	} {
		if !strings.Contains(s, want) {
			t.Errorf("config missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "junk") {
		t.Errorf("undetectable dir should not be added:\n%s", s)
	}
	// The two manual steps are explained with the token page.
	for _, want := range []string{"nameservers", "api-tokens", "Zone:DNS:Edit"} {
		if !strings.Contains(out, want) {
			t.Errorf("init output missing %q:\n%s", want, out)
		}
	}

	// The written config loads and resolves.
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	resolved, _, err := config.Resolve(cfg)
	if err != nil || len(resolved) != 1 || resolved[0].FQDN != "shop.example.com" {
		t.Errorf("resolved = %+v, err = %v", resolved, err)
	}

	// Refuses to clobber without --force.
	if _, err := runCLI(t, "init", "--domain", "example.com"); err == nil {
		t.Error("second init should refuse without --force")
	}
}

func TestVersionCommand(t *testing.T) {
	out, err := runCLI(t, "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.Contains(out, "roost") {
		t.Errorf("version output = %q", out)
	}
}
