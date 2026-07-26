package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig writes content to dir/config.yml and returns the path.
func writeConfig(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// mkApps creates named subdirectories under dir and returns dir.
func mkApps(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := os.MkdirAll(filepath.Join(dir, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadAppForms(t *testing.T) {
	dir := t.TempDir()
	mkApps(t, dir, "app1", "app2", "app3")

	tests := []struct {
		name string
		yaml string
		want []App
	}{
		{
			name: "bare string form",
			yaml: "domain: example.com\napps:\n  - " + filepath.Join(dir, "app1") + "\n",
			want: []App{{Path: filepath.Join(dir, "app1")}},
		},
		{
			name: "map form",
			yaml: "apps:\n  - path: " + filepath.Join(dir, "app1") + "\n    domain: app1.example.com\n    name: custom\n    port: 3000\n    memory: 768m\n    env:\n      SOME_KEY: value\n",
			want: []App{{
				Path:   filepath.Join(dir, "app1"),
				Domain: "app1.example.com",
				Name:   "custom",
				Port:   3000,
				Memory: "768m",
				Env:    map[string]string{"SOME_KEY": "value"},
			}},
		},
		{
			name: "mixed list",
			yaml: "domain: example.com\napps:\n" +
				"  - " + filepath.Join(dir, "app1") + "\n" +
				"  - path: " + filepath.Join(dir, "app2") + "\n    domain: two.other.com\n" +
				"  - " + filepath.Join(dir, "app3") + "\n",
			want: []App{
				{Path: filepath.Join(dir, "app1")},
				{Path: filepath.Join(dir, "app2"), Domain: "two.other.com"},
				{Path: filepath.Join(dir, "app3")},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfgPath := writeConfig(t, t.TempDir(), tt.yaml)
			cfg, err := Load(cfgPath)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if len(cfg.Apps) != len(tt.want) {
				t.Fatalf("got %d apps, want %d", len(cfg.Apps), len(tt.want))
			}
			for i, want := range tt.want {
				got := cfg.Apps[i]
				if got.Path != want.Path {
					t.Errorf("app %d: Path = %q, want %q", i, got.Path, want.Path)
				}
				if got.Domain != want.Domain {
					t.Errorf("app %d: Domain = %q, want %q", i, got.Domain, want.Domain)
				}
				if got.Name != want.Name {
					t.Errorf("app %d: Name = %q, want %q", i, got.Name, want.Name)
				}
				if got.Port != want.Port {
					t.Errorf("app %d: Port = %d, want %d", i, got.Port, want.Port)
				}
				if want.Env != nil && got.Env["SOME_KEY"] != want.Env["SOME_KEY"] {
					t.Errorf("app %d: Env = %v, want %v", i, got.Env, want.Env)
				}
			}
		})
	}
}

func TestLoadTopLevelFields(t *testing.T) {
	dir := t.TempDir()
	mkApps(t, dir, "app1")
	yaml := `domain: demo.example.com
tunnel:
  name: roost
  access:
    emails:
      - me@example.com
defaults:
  memory: 512m
  profile: core
apps:
  - ` + filepath.Join(dir, "app1") + `
`
	cfg, err := Load(writeConfig(t, t.TempDir(), yaml))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Domain != "demo.example.com" {
		t.Errorf("Domain = %q, want demo.example.com", cfg.Domain)
	}
	if cfg.Tunnel.Name != "roost" {
		t.Errorf("Tunnel.Name = %q, want roost", cfg.Tunnel.Name)
	}
	if cfg.Tunnel.Access == nil || len(cfg.Tunnel.Access.Emails) != 1 || cfg.Tunnel.Access.Emails[0] != "me@example.com" {
		t.Errorf("Tunnel.Access = %+v, want one email me@example.com", cfg.Tunnel.Access)
	}
	if cfg.Defaults.Memory != "512m" || cfg.Defaults.Profile != "core" {
		t.Errorf("Defaults = %+v, want 512m/core", cfg.Defaults)
	}
}

func TestLoadNotifyBlock(t *testing.T) {
	dir := t.TempDir()
	mkApps(t, dir, "app1")
	// email as a scalar must parse into the list (convenience).
	yaml := `domain: demo.example.com
notify:
  email: me@example.com
  smtp_host: smtp.gmail.com
  smtp_port: 587
  smtp_user: bot@example.com
apps:
  - ` + filepath.Join(dir, "app1") + `
`
	cfg, err := Load(writeConfig(t, t.TempDir(), yaml))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Notify.Email) != 1 || cfg.Notify.Email[0] != "me@example.com" {
		t.Errorf("Notify.Email = %v, want [me@example.com]", cfg.Notify.Email)
	}
	if cfg.Notify.SMTPHost != "smtp.gmail.com" || cfg.Notify.SMTPPort != 587 || cfg.Notify.SMTPUser != "bot@example.com" {
		t.Errorf("Notify smtp = %+v", cfg.Notify)
	}
}

func TestLoadPathResolution(t *testing.T) {
	t.Run("tilde expands to home", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		mkApps(t, home, "projects/app1")
		cfgPath := writeConfig(t, t.TempDir(), "domain: example.com\napps:\n  - ~/projects/app1\n")
		cfg, err := Load(cfgPath)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		want := filepath.Join(home, "projects", "app1")
		if cfg.Apps[0].Path != want {
			t.Errorf("Path = %q, want %q", cfg.Apps[0].Path, want)
		}
	})

	t.Run("relative resolves against config dir", func(t *testing.T) {
		cfgDir := t.TempDir()
		mkApps(t, cfgDir, "apps/app1")
		cfgPath := writeConfig(t, cfgDir, "domain: example.com\napps:\n  - ./apps/app1\n")
		cfg, err := Load(cfgPath)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		want := filepath.Join(cfgDir, "apps", "app1")
		if cfg.Apps[0].Path != want {
			t.Errorf("Path = %q, want %q", cfg.Apps[0].Path, want)
		}
	})

	t.Run("all paths absolute after load", func(t *testing.T) {
		cfgDir := t.TempDir()
		mkApps(t, cfgDir, "app1")
		cfgPath := writeConfig(t, cfgDir, "domain: example.com\napps:\n  - app1\n")
		cfg, err := Load(cfgPath)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !filepath.IsAbs(cfg.Apps[0].Path) {
			t.Errorf("Path = %q, want absolute", cfg.Apps[0].Path)
		}
	})
}

func TestFindConfig(t *testing.T) {
	// Each subtest gets an isolated home/cwd so the real machine's
	// ~/.roost never leaks in.
	setup := func(t *testing.T) (home, cwd string) {
		home = t.TempDir()
		cwd = t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("ROOST_CONFIG", "")
		t.Chdir(cwd)
		return home, cwd
	}
	touch := func(t *testing.T, path string) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("apps: []\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("flag wins over everything", func(t *testing.T) {
		home, cwd := setup(t)
		flagFile := touch(t, filepath.Join(t.TempDir(), "flag.yml"))
		envFile := touch(t, filepath.Join(t.TempDir(), "env.yml"))
		t.Setenv("ROOST_CONFIG", envFile)
		touch(t, filepath.Join(cwd, "roost.yml"))
		touch(t, filepath.Join(home, ".roost", "config.yml"))
		got, err := FindConfig(flagFile)
		if err != nil {
			t.Fatalf("FindConfig: %v", err)
		}
		if got != flagFile {
			t.Errorf("got %q, want %q", got, flagFile)
		}
	})

	t.Run("env var beats local and home", func(t *testing.T) {
		home, cwd := setup(t)
		envFile := touch(t, filepath.Join(t.TempDir(), "env.yml"))
		t.Setenv("ROOST_CONFIG", envFile)
		touch(t, filepath.Join(cwd, "roost.yml"))
		touch(t, filepath.Join(home, ".roost", "config.yml"))
		got, err := FindConfig("")
		if err != nil {
			t.Fatalf("FindConfig: %v", err)
		}
		if got != envFile {
			t.Errorf("got %q, want %q", got, envFile)
		}
	})

	t.Run("local roost.yml beats home", func(t *testing.T) {
		home, cwd := setup(t)
		local := touch(t, filepath.Join(cwd, "roost.yml"))
		touch(t, filepath.Join(home, ".roost", "config.yml"))
		got, err := FindConfig("")
		if err != nil {
			t.Fatalf("FindConfig: %v", err)
		}
		// The local file may come back relative or absolute; compare resolved.
		gotAbs, _ := filepath.Abs(got)
		if gotAbs != local {
			t.Errorf("got %q, want %q", got, local)
		}
	})

	t.Run("home config is the default", func(t *testing.T) {
		home, _ := setup(t)
		homeCfg := touch(t, filepath.Join(home, ".roost", "config.yml"))
		got, err := FindConfig("")
		if err != nil {
			t.Fatalf("FindConfig: %v", err)
		}
		if got != homeCfg {
			t.Errorf("got %q, want %q", got, homeCfg)
		}
	})

	t.Run("nothing found is an error", func(t *testing.T) {
		setup(t)
		if _, err := FindConfig(""); err == nil {
			t.Fatal("want error when no config exists anywhere")
		}
	})

	t.Run("explicit flag path missing is an error", func(t *testing.T) {
		setup(t)
		if _, err := FindConfig(filepath.Join(t.TempDir(), "nope.yml")); err == nil {
			t.Fatal("want error for missing --config path")
		}
	})
}

func TestResolveHostnames(t *testing.T) {
	t.Run("per-app domain wins over global", func(t *testing.T) {
		dir := t.TempDir()
		mkApps(t, dir, "app1")
		cfg := &Config{
			Domain: "demo.example.com",
			Apps:   []App{{Path: filepath.Join(dir, "app1"), Domain: "tweetx.app.example.com"}},
		}
		resolved, skipped, err := Resolve(cfg)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if len(skipped) != 0 {
			t.Fatalf("skipped = %+v, want none", skipped)
		}
		if len(resolved) != 1 || resolved[0].FQDN != "tweetx.app.example.com" {
			t.Fatalf("resolved = %+v, want FQDN tweetx.app.example.com", resolved)
		}
	})

	t.Run("global domain composes name.domain", func(t *testing.T) {
		dir := t.TempDir()
		mkApps(t, dir, "app1")
		cfg := &Config{
			Domain: "demo.example.com",
			Apps:   []App{{Path: filepath.Join(dir, "app1")}},
		}
		resolved, _, err := Resolve(cfg)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if len(resolved) != 1 || resolved[0].FQDN != "app1.demo.example.com" {
			t.Fatalf("resolved = %+v, want FQDN app1.demo.example.com", resolved)
		}
	})

	t.Run("basename is slugified for the name", func(t *testing.T) {
		dir := t.TempDir()
		mkApps(t, dir, "My_Cool App")
		cfg := &Config{
			Domain: "example.com",
			Apps:   []App{{Path: filepath.Join(dir, "My_Cool App")}},
		}
		resolved, _, err := Resolve(cfg)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if len(resolved) != 1 || resolved[0].FQDN != "my-cool-app.example.com" {
			t.Fatalf("resolved = %+v, want FQDN my-cool-app.example.com", resolved)
		}
	})

	t.Run("no domain anywhere skips with reason", func(t *testing.T) {
		dir := t.TempDir()
		mkApps(t, dir, "legacy-app", "app1")
		cfg := &Config{
			Apps: []App{
				{Path: filepath.Join(dir, "legacy-app")},
				{Path: filepath.Join(dir, "app1"), Domain: "app1.example.com"},
			},
		}
		resolved, skipped, err := Resolve(cfg)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if len(resolved) != 1 || resolved[0].FQDN != "app1.example.com" {
			t.Fatalf("resolved = %+v, want only app1.example.com", resolved)
		}
		if len(skipped) != 1 {
			t.Fatalf("skipped = %+v, want exactly one", skipped)
		}
		if skipped[0].Name != "legacy-app" || !strings.Contains(skipped[0].Reason, "no domain configured") {
			t.Errorf("skipped = %+v, want legacy-app with reason containing %q", skipped[0], "no domain configured")
		}
	})

	t.Run("missing path skips with reason, others still resolve", func(t *testing.T) {
		dir := t.TempDir()
		mkApps(t, dir, "app1")
		gone := filepath.Join(dir, "gone-app")
		cfg := &Config{
			Domain: "example.com",
			Apps: []App{
				{Path: filepath.Join(dir, "app1")},
				{Path: gone},
			},
		}
		resolved, skipped, err := Resolve(cfg)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if len(resolved) != 1 {
			t.Fatalf("resolved = %+v, want one", resolved)
		}
		if len(skipped) != 1 {
			t.Fatalf("skipped = %+v, want one", skipped)
		}
		if !strings.Contains(skipped[0].Reason, "path does not exist") || !strings.Contains(skipped[0].Reason, gone) {
			t.Errorf("Reason = %q, want it to name the missing path %q", skipped[0].Reason, gone)
		}
	})

	t.Run("path that is a file skips with reason", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "not-a-dir")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg := &Config{
			Domain: "example.com",
			Apps:   []App{{Path: file}},
		}
		_, skipped, err := Resolve(cfg)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if len(skipped) != 1 || !strings.Contains(skipped[0].Reason, "not a directory") {
			t.Errorf("skipped = %+v, want reason containing %q", skipped, "not a directory")
		}
	})

	t.Run("explicit name overrides basename", func(t *testing.T) {
		dir := t.TempDir()
		mkApps(t, dir, "app1")
		cfg := &Config{
			Domain: "example.com",
			Apps:   []App{{Path: filepath.Join(dir, "app1"), Name: "custom-name"}},
		}
		resolved, _, err := Resolve(cfg)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if len(resolved) != 1 || resolved[0].FQDN != "custom-name.example.com" {
			t.Fatalf("resolved = %+v, want custom-name.example.com", resolved)
		}
	})

	// The M1 acceptance case: three different domains plus one skipped app.
	t.Run("three domains plus one skipped app", func(t *testing.T) {
		dir := t.TempDir()
		mkApps(t, dir, "app1", "tweetx", "trackaru", "legacy")
		cfg := &Config{
			Domain: "demo.example.com",
			Apps: []App{
				{Path: filepath.Join(dir, "app1")},                                     // app1.demo.example.com
				{Path: filepath.Join(dir, "tweetx"), Domain: "tweetx.app.example.com"}, // explicit, different suffix
				{Path: filepath.Join(dir, "trackaru"), Domain: "trackaru.com"},         // bare apex
				{Path: filepath.Join(dir, "legacy"), Name: "legacy-no-home"},           // resolves via global
			},
		}
		resolved, skipped, err := Resolve(cfg)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if len(resolved) != 4 || len(skipped) != 0 {
			t.Fatalf("resolved=%d skipped=%d, want 4/0", len(resolved), len(skipped))
		}
		want := []string{"app1.demo.example.com", "tweetx.app.example.com", "trackaru.com", "legacy-no-home.demo.example.com"}
		for i, w := range want {
			if resolved[i].FQDN != w {
				t.Errorf("resolved[%d].FQDN = %q, want %q", i, resolved[i].FQDN, w)
			}
		}

		// Now the same list with no global domain: only explicit-domain
		// apps survive, the rest are skipped with reasons.
		cfg.Domain = ""
		resolved, skipped, err = Resolve(cfg)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if len(resolved) != 2 || len(skipped) != 2 {
			t.Fatalf("resolved=%d skipped=%d, want 2/2", len(resolved), len(skipped))
		}
	})

	t.Run("nothing to run is valid, not an error", func(t *testing.T) {
		dir := t.TempDir()
		mkApps(t, dir, "app1")
		cfg := &Config{Apps: []App{{Path: filepath.Join(dir, "app1")}}}
		resolved, skipped, err := Resolve(cfg)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if len(resolved) != 0 || len(skipped) != 1 {
			t.Fatalf("resolved=%d skipped=%d, want 0/1", len(resolved), len(skipped))
		}
	})
}

func TestResolveCollisions(t *testing.T) {
	t.Run("two explicit domains collide", func(t *testing.T) {
		dir := t.TempDir()
		mkApps(t, dir, "one", "two")
		cfg := &Config{Apps: []App{
			{Path: filepath.Join(dir, "one"), Domain: "same.example.com"},
			{Path: filepath.Join(dir, "two"), Domain: "same.example.com"},
		}}
		_, _, err := Resolve(cfg)
		if err == nil {
			t.Fatal("want collision error")
		}
		for _, want := range []string{"one", "two", "same.example.com"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q missing %q", err, want)
			}
		}
	})

	t.Run("two basenames slugify to the same name", func(t *testing.T) {
		dir1 := t.TempDir()
		dir2 := t.TempDir()
		mkApps(t, dir1, "my-app")
		mkApps(t, dir2, "My_App")
		cfg := &Config{
			Domain: "example.com",
			Apps: []App{
				{Path: filepath.Join(dir1, "my-app")},
				{Path: filepath.Join(dir2, "My_App")},
			},
		}
		_, _, err := Resolve(cfg)
		if err == nil {
			t.Fatal("want collision error for identical slugs")
		}
		if !strings.Contains(err.Error(), "my-app.example.com") {
			t.Errorf("error %q missing colliding hostname my-app.example.com", err)
		}
	})
}

func TestResolveInvalidHostnameIsHardError(t *testing.T) {
	dir := t.TempDir()
	mkApps(t, dir, "app1")
	cfg := &Config{Apps: []App{
		{Path: filepath.Join(dir, "app1"), Domain: "https://app1.example.com"},
	}}
	_, _, err := Resolve(cfg)
	if err == nil {
		t.Fatal("want hard error for URL as domain")
	}
	if !strings.Contains(err.Error(), "app1") {
		t.Errorf("error %q should name the app", err)
	}
}

func TestValidateHostname(t *testing.T) {
	long63 := strings.Repeat("a", 63)
	long64 := strings.Repeat("a", 64)

	valid := []string{
		"app.example.com",
		"a.co",
		"tweetx.app.example.com",
		"trackaru.com",
		"my-app.demo.example.com",
		long63 + ".example.com",
	}
	for _, h := range valid {
		t.Run("valid "+h, func(t *testing.T) {
			if err := ValidateHostname(h); err != nil {
				t.Errorf("ValidateHostname(%q) = %v, want nil", h, err)
			}
		})
	}

	invalid := []struct {
		host     string
		wantSubs []string // every substring must appear in the error
	}{
		// URL: the error must say what the correct value would be.
		{"https://app.demo.com", []string{"URL", "app.demo.com"}},
		{"http://app.demo.com", []string{"URL", "app.demo.com"}},
		// Port: same.
		{"app.demo.com:3000", []string{"port", "app.demo.com"}},
		// Path.
		{"app.demo.com/x", []string{"path"}},
		// Bare label: needs at least two labels.
		{"app", []string{"label"}},
		// Trailing dot.
		{"app.demo.com.", []string{"trailing dot"}},
		// Hyphen placement.
		{"-app.example.com", []string{"hyphen"}},
		{"app-.example.com", []string{"hyphen"}},
		// Label too long.
		{long64 + ".example.com", []string{"63"}},
		// Total too long: four 63-char labels + dots = 255.
		{long63 + "." + long63 + "." + long63 + "." + long63, []string{"253"}},
		// Empty label.
		{"app..example.com", []string{"empty label"}},
		// Bad character.
		{"app_1.example.com", []string{"_"}},
		// Empty string.
		{"", []string{"empty"}},
	}
	for _, tt := range invalid {
		t.Run("invalid "+tt.host, func(t *testing.T) {
			err := ValidateHostname(tt.host)
			if err == nil {
				t.Fatalf("ValidateHostname(%q) = nil, want error", tt.host)
			}
			for _, sub := range tt.wantSubs {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("error %q missing %q", err, sub)
				}
			}
		})
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct{ in, want string }{
		{"app1", "app1"},
		{"My App", "my-app"},
		{"my_app", "my-app"},
		{"My_Cool App", "my-cool-app"},
		{"some.rails.app", "some-rails-app"},
		{"--edgy--", "edgy"},
		{"CamelCase", "camelcase"},
	}
	for _, tt := range tests {
		if got := Slugify(tt.in); got != tt.want {
			t.Errorf("Slugify(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
