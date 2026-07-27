package detect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectFixtures(t *testing.T) {
	tests := []struct {
		fixture string
		want    Detection
	}{
		{
			fixture: "rails-app",
			want: Detection{
				Framework:      "rails",
				Signal:         "Gemfile + config/application.rb",
				Port:           3000,
				StartCommand:   "bundle exec puma -b tcp://0.0.0.0:3000",
				Database:       "mysql",
				RuntimeVersion: "3.2.2",
			},
		},
		{
			fixture: "sinatra-app",
			want: Detection{
				Framework:    "sinatra",
				Signal:       "Gemfile + config.ru + sinatra in Gemfile",
				Port:         4567,
				StartCommand: "bundle exec rackup -o 0.0.0.0 -p 4567",
				Database:     "postgres",
			},
		},
		{
			fixture: "next-app",
			want: Detection{
				Framework:      "next",
				Signal:         "package.json with next dependency",
				Port:           3000,
				StartCommand:   "npm run start",
				RuntimeVersion: ">=20",
			},
		},
		{
			fixture: "vite-app",
			want: Detection{
				Framework: "static",
				Signal:    "package.json with vite dependency",
				Port:      80,
			},
		},
		{
			fixture: "express-app",
			want: Detection{
				Framework:      "node",
				Signal:         "package.json with express dependency",
				Port:           3000,
				StartCommand:   "npm run start",
				RuntimeVersion: "20.12.2",
			},
		},
		{
			fixture: "django-app",
			want: Detection{
				Framework:    "django",
				Signal:       "manage.py + requirements.txt",
				Port:         8000,
				StartCommand: "gunicorn -b 0.0.0.0:8000",
				Database:     "postgres",
			},
		},
		{
			fixture: "static-app",
			want: Detection{
				Framework: "static",
				Signal:    "index.html at root",
				Port:      80,
			},
		},
		{
			// Has vite too; the @sveltejs/kit rule must win over vite.
			fixture: "sveltekit-app",
			want: Detection{
				Framework:    "node",
				Signal:       "package.json with @sveltejs/kit dependency",
				Port:         3000,
				StartCommand: "node build",
			},
		},
		{
			fixture: "astro-app",
			want: Detection{
				Framework: "static",
				Signal:    "package.json with astro dependency",
				Port:      80,
			},
		},
		{
			fixture: "flask-app",
			want: Detection{
				Framework:    "flask",
				Signal:       "requirements.txt with Flask",
				Port:         8000,
				StartCommand: "gunicorn -b 0.0.0.0:8000 app:app",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			got, err := Detect(filepath.Join("testdata", tt.fixture))
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if got.Framework != tt.want.Framework {
				t.Errorf("Framework = %q, want %q", got.Framework, tt.want.Framework)
			}
			if got.Signal != tt.want.Signal {
				t.Errorf("Signal = %q, want %q", got.Signal, tt.want.Signal)
			}
			if got.Port != tt.want.Port {
				t.Errorf("Port = %d, want %d", got.Port, tt.want.Port)
			}
			if got.StartCommand != tt.want.StartCommand {
				t.Errorf("StartCommand = %q, want %q", got.StartCommand, tt.want.StartCommand)
			}
			if got.Database != tt.want.Database {
				t.Errorf("Database = %q, want %q", got.Database, tt.want.Database)
			}
			if got.RuntimeVersion != tt.want.RuntimeVersion {
				t.Errorf("RuntimeVersion = %q, want %q", got.RuntimeVersion, tt.want.RuntimeVersion)
			}
		})
	}
}

// writeFiles creates a temp app dir from a map of relative path -> content.
func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDetectPriority(t *testing.T) {
	t.Run("next beats vite and express in one package.json", func(t *testing.T) {
		dir := writeFiles(t, map[string]string{
			"package.json": `{"dependencies":{"next":"14.0.0","express":"^4"},"devDependencies":{"vite":"^5"}}`,
		})
		got, err := Detect(dir)
		if err != nil {
			t.Fatalf("Detect: %v", err)
		}
		if got.Framework != "next" {
			t.Errorf("Framework = %q, want next", got.Framework)
		}
	})

	t.Run("vite beats express", func(t *testing.T) {
		dir := writeFiles(t, map[string]string{
			"package.json": `{"dependencies":{"express":"^4"},"devDependencies":{"vite":"^5"}}`,
		})
		got, err := Detect(dir)
		if err != nil {
			t.Fatalf("Detect: %v", err)
		}
		if got.Framework != "static" || !strings.Contains(got.Signal, "vite") {
			t.Errorf("got %q via %q, want static via vite", got.Framework, got.Signal)
		}
	})

	t.Run("rails beats stray index.html", func(t *testing.T) {
		dir := writeFiles(t, map[string]string{
			"Gemfile":               `gem "rails"`,
			"config/application.rb": "module X; end",
			"public/index.html":     "<html></html>",
			"index.html":            "<html></html>",
		})
		got, err := Detect(dir)
		if err != nil {
			t.Fatalf("Detect: %v", err)
		}
		if got.Framework != "rails" {
			t.Errorf("Framework = %q, want rails", got.Framework)
		}
	})

	t.Run("django via pyproject.toml", func(t *testing.T) {
		dir := writeFiles(t, map[string]string{
			"manage.py":      "#!/usr/bin/env python",
			"pyproject.toml": "[project]\nname = \"x\"\n",
		})
		got, err := Detect(dir)
		if err != nil {
			t.Fatalf("Detect: %v", err)
		}
		if got.Framework != "django" {
			t.Errorf("Framework = %q, want django", got.Framework)
		}
	})
}

func TestDetectDatabase(t *testing.T) {
	t.Run("rails postgres adapter", func(t *testing.T) {
		dir := writeFiles(t, map[string]string{
			"Gemfile":               `gem "rails"` + "\n" + `gem "pg"`,
			"config/application.rb": "module X; end",
			"config/database.yml":   "default:\n  adapter: postgresql\n",
		})
		got, err := Detect(dir)
		if err != nil {
			t.Fatalf("Detect: %v", err)
		}
		if got.Database != "postgres" {
			t.Errorf("Database = %q, want postgres", got.Database)
		}
	})

	t.Run("mysql DATABASE_URL in .env.example", func(t *testing.T) {
		dir := writeFiles(t, map[string]string{
			"package.json": `{"dependencies":{"express":"^4"}}`,
			".env.example": "DATABASE_URL=mysql://u:p@localhost/db\n",
		})
		got, err := Detect(dir)
		if err != nil {
			t.Fatalf("Detect: %v", err)
		}
		if got.Database != "mysql" {
			t.Errorf("Database = %q, want mysql", got.Database)
		}
	})

	t.Run("no database detected", func(t *testing.T) {
		dir := writeFiles(t, map[string]string{
			"package.json": `{"dependencies":{"express":"^4"}}`,
		})
		got, err := Detect(dir)
		if err != nil {
			t.Fatalf("Detect: %v", err)
		}
		if got.Database != "" {
			t.Errorf("Database = %q, want empty", got.Database)
		}
	})
}

func TestDetectRuntimeVersionFromGemfile(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"Gemfile":               "source \"https://rubygems.org\"\n\nruby \"3.3.0\"\n\ngem \"rails\"\n",
		"config/application.rb": "module X; end",
	})
	got, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if got.RuntimeVersion != "3.3.0" {
		t.Errorf("RuntimeVersion = %q, want 3.3.0", got.RuntimeVersion)
	}
}

func TestDetectUnknown(t *testing.T) {
	_, err := Detect(filepath.Join("testdata", "unknown-app"))
	if err == nil {
		t.Fatal("want error for undetectable app")
	}
	if !strings.Contains(err.Error(), "unknown-app") {
		t.Errorf("error %q should name the folder", err)
	}
	if !strings.Contains(err.Error(), "framework") {
		t.Errorf("error %q should tell the user to set framework: explicitly", err)
	}
}

func TestDefaults(t *testing.T) {
	for _, name := range []string{"rails", "sinatra", "next", "node", "django", "static"} {
		d, ok := Defaults(name)
		if !ok {
			t.Errorf("Defaults(%q) unknown, want known", name)
			continue
		}
		if d.Framework != name || d.Port == 0 {
			t.Errorf("Defaults(%q) = %+v, want matching framework and a port", name, d)
		}
	}
	if _, ok := Defaults("fortran-on-rails"); ok {
		t.Error("Defaults should not know made-up frameworks")
	}
}

func TestDetectMissingDir(t *testing.T) {
	if _, err := Detect(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("want error for missing directory")
	}
}
