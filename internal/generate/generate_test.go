package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/cdrrazan/roost/internal/config"
)

// sampleApps is a spread that exercises every generator path: a Rails
// app on MySQL with overrides, a Django app on Postgres, a plain
// static site, and a Node app that ships its own Dockerfile.
func sampleApps() []App {
	return []App{
		{
			Name:           "blog",
			FQDN:           "blog.demo.example.com",
			Path:           "/apps/blog",
			Framework:      "rails",
			Port:           3000,
			StartCommand:   "bundle exec puma -b tcp://0.0.0.0:3000",
			Database:       "mysql",
			Memory:         "768m",
			Profile:        "extras",
			Env:            map[string]string{"SOME_KEY": "value"},
			RuntimeVersion: "3.2.2",
		},
		{
			Name:         "crm",
			FQDN:         "crm.other.org",
			Path:         "/apps/crm",
			Framework:    "django",
			Port:         8000,
			StartCommand: "gunicorn -b 0.0.0.0:8000",
			Database:     "postgres",
			Memory:       "512m",
		},
		{
			Name:      "site",
			FQDN:      "site.demo.example.com",
			Path:      "/apps/site",
			Framework: "static",
			Port:      80,
			Memory:    "512m",
		},
		{
			Name:             "api",
			FQDN:             "api.demo.example.com",
			Path:             "/apps/api",
			Framework:        "node",
			Port:             3000,
			StartCommand:     "npm run start",
			Memory:           "512m",
			HasOwnDockerfile: true,
		},
	}
}

func TestRenderCompose(t *testing.T) {
	out, err := RenderCompose("/home/u/.roost/build", sampleApps())
	if err != nil {
		t.Fatalf("RenderCompose: %v", err)
	}
	s := string(out)

	// Must be valid YAML.
	var doc map[string]any
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("compose.yml is not valid YAML: %v\n%s", err, s)
	}

	t.Run("project name pinned to roost", func(t *testing.T) {
		if doc["name"] != "roost" {
			t.Errorf("name = %v, want roost", doc["name"])
		}
	})

	services, _ := doc["services"].(map[string]any)
	if services == nil {
		t.Fatalf("no services map in compose.yml:\n%s", s)
	}

	t.Run("one service per app plus shared services", func(t *testing.T) {
		for _, name := range []string{"blog", "crm", "site", "api", "caddy", "cloudflared", "mysql", "postgres"} {
			if _, ok := services[name]; !ok {
				t.Errorf("service %q missing", name)
			}
		}
	})

	t.Run("no service ever publishes ports", func(t *testing.T) {
		for name, raw := range services {
			svc, _ := raw.(map[string]any)
			if _, has := svc["ports"]; has {
				t.Errorf("service %q publishes ports; nothing may", name)
			}
		}
	})

	t.Run("restart unless-stopped everywhere", func(t *testing.T) {
		for name, raw := range services {
			svc, _ := raw.(map[string]any)
			if svc["restart"] != "unless-stopped" {
				t.Errorf("service %q restart = %v, want unless-stopped", name, svc["restart"])
			}
		}
	})

	t.Run("mem_limit on every app", func(t *testing.T) {
		if !strings.Contains(s, "mem_limit: 768m") {
			t.Error("missing mem_limit: 768m for blog")
		}
		if !strings.Contains(s, "mem_limit: 512m") {
			t.Error("missing mem_limit: 512m")
		}
	})

	t.Run("profiles preserved", func(t *testing.T) {
		blog, _ := services["blog"].(map[string]any)
		profiles, _ := blog["profiles"].([]any)
		if len(profiles) != 1 || profiles[0] != "extras" {
			t.Errorf("blog profiles = %v, want [extras]", profiles)
		}
		crm, _ := services["crm"].(map[string]any)
		if _, has := crm["profiles"]; has {
			t.Errorf("crm should have no profiles key")
		}
	})

	t.Run("rails tuning env", func(t *testing.T) {
		for _, want := range []string{"RAILS_ASSUME_SSL", "WEB_CONCURRENCY", "RAILS_MAX_THREADS"} {
			if !strings.Contains(s, want) {
				t.Errorf("compose missing %s for the rails app", want)
			}
		}
	})

	t.Run("user env preserved", func(t *testing.T) {
		if !strings.Contains(s, "SOME_KEY") {
			t.Error("compose missing user env SOME_KEY")
		}
	})

	t.Run("source mounted read-only", func(t *testing.T) {
		if !strings.Contains(s, "/apps/blog:/app:ro") {
			t.Error("blog source not bind-mounted read-only")
		}
	})

	t.Run("db init scripts mounted", func(t *testing.T) {
		if !strings.Contains(s, "mysql-init.sql") {
			t.Error("mysql init script not mounted")
		}
		if !strings.Contains(s, "postgres-init.sql") {
			t.Error("postgres init script not mounted")
		}
	})

	t.Run("explains why db ports stay private", func(t *testing.T) {
		if !strings.Contains(s, "never published") {
			t.Error("compose should carry the comment explaining unpublished database ports")
		}
	})

	t.Run("own dockerfile respected", func(t *testing.T) {
		api, _ := services["api"].(map[string]any)
		build, _ := api["build"].(map[string]any)
		df, _ := build["dockerfile"].(string)
		if strings.Contains(df, ".roost/build") {
			t.Errorf("api dockerfile = %q, want the app's own Dockerfile, not a generated one", df)
		}
	})
}

func TestPlanSeedAndSetupCommands(t *testing.T) {
	cfg := &config.Config{}
	resolved := []config.ResolvedApp{
		// Rails app, seed: true → framework-default seed + db setup.
		{App: config.App{Framework: "rails", Database: "mysql", Seed: config.SeedSpec{Enabled: true}}, Name: "blog", FQDN: "blog.example.com"},
		// Rails app, explicit command wins over the default.
		{App: config.App{Framework: "rails", Database: "mysql", Seed: config.SeedSpec{Enabled: true, Command: "bin/rails custom:seed"}}, Name: "shop", FQDN: "shop.example.com"},
		// Rails app with no seed directive: setup only, no seed.
		{App: config.App{Framework: "rails", Database: "mysql"}, Name: "wiki", FQDN: "wiki.example.com"},
		// Static app: no db setup, no seed.
		{App: config.App{Framework: "static"}, Name: "site", FQDN: "site.example.com"},
	}
	apps, err := Plan(cfg, resolved)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	by := map[string]App{}
	for _, a := range apps {
		by[a.Name] = a
	}

	if by["blog"].SeedCommand != "bin/rails db:seed" {
		t.Errorf("blog SeedCommand = %q, want the rails default", by["blog"].SeedCommand)
	}
	if by["blog"].SetupCommand == "" {
		t.Error("blog should have a db setup command")
	}
	if by["shop"].SeedCommand != "bin/rails custom:seed" {
		t.Errorf("shop SeedCommand = %q, want the explicit command", by["shop"].SeedCommand)
	}
	if by["wiki"].SeedCommand != "" {
		t.Errorf("wiki SeedCommand = %q, want empty (no seed directive)", by["wiki"].SeedCommand)
	}
	if by["wiki"].SetupCommand == "" {
		t.Error("wiki (rails+db) should still get a setup command")
	}
	if by["site"].SetupCommand != "" || by["site"].SeedCommand != "" {
		t.Errorf("static site should have no setup/seed commands, got setup=%q seed=%q", by["site"].SetupCommand, by["site"].SeedCommand)
	}
}

func TestPlanMigrateOptOutAndOverride(t *testing.T) {
	cfg := &config.Config{}
	resolved := []config.ResolvedApp{
		// migrate: false → app self-migrates at boot; roost runs no setup.
		{App: config.App{Framework: "rails", Database: "mysql", Migrate: config.MigrateSpec{Set: true, Enabled: false}}, Name: "self", FQDN: "self.example.com"},
		// migrate: "<cmd>" → explicit setup command wins over db:prepare.
		{App: config.App{Framework: "rails", Database: "mysql", Migrate: config.MigrateSpec{Set: true, Enabled: true, Command: "bin/rails db:migrate"}}, Name: "custom", FQDN: "custom.example.com"},
		// migrate: true → framework default (db:prepare), same as absent.
		{App: config.App{Framework: "rails", Database: "mysql", Migrate: config.MigrateSpec{Set: true, Enabled: true}}, Name: "explicit", FQDN: "explicit.example.com"},
		// absent → framework default.
		{App: config.App{Framework: "rails", Database: "mysql"}, Name: "default", FQDN: "default.example.com"},
		// Opting out must not disable seeding — the app still gets its seed
		// command; only the redundant setup step is skipped.
		{App: config.App{Framework: "rails", Database: "mysql", Migrate: config.MigrateSpec{Set: true, Enabled: false}, Seed: config.SeedSpec{Enabled: true}}, Name: "seedonly", FQDN: "seedonly.example.com"},
	}
	apps, err := Plan(cfg, resolved)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	by := map[string]App{}
	for _, a := range apps {
		by[a.Name] = a
	}

	if by["self"].SetupCommand != "" {
		t.Errorf("migrate: false must skip the setup command, got %q", by["self"].SetupCommand)
	}
	if by["custom"].SetupCommand != "bin/rails db:migrate" {
		t.Errorf("custom migrate command = %q, want the override", by["custom"].SetupCommand)
	}
	if by["explicit"].SetupCommand != "bin/rails db:prepare" {
		t.Errorf("migrate: true = %q, want the rails default db:prepare", by["explicit"].SetupCommand)
	}
	if by["default"].SetupCommand != "bin/rails db:prepare" {
		t.Errorf("absent migrate = %q, want the rails default db:prepare", by["default"].SetupCommand)
	}
	if by["seedonly"].SetupCommand != "" {
		t.Errorf("seedonly setup = %q, want empty (opted out)", by["seedonly"].SetupCommand)
	}
	if by["seedonly"].SeedCommand != "bin/rails db:seed" {
		t.Errorf("seedonly seed = %q, want the seed still to run", by["seedonly"].SeedCommand)
	}
}

func TestPlanSeedTrueNeedsDefaultOrError(t *testing.T) {
	cfg := &config.Config{}
	// A static app cannot infer a seed command; seed: true must error.
	resolved := []config.ResolvedApp{
		{App: config.App{Framework: "static", Seed: config.SeedSpec{Enabled: true}}, Name: "site", FQDN: "site.example.com"},
	}
	if _, err := Plan(cfg, resolved); err == nil {
		t.Fatal("expected an error: seed: true with no framework default and no command")
	}
}

func TestRenderComposeInjectsSeedEnv(t *testing.T) {
	// ~/.roost/seed.env holds the shared demo super-admin credentials so
	// every app seeds the same login. RenderCompose reads it relative to
	// the build dir's parent and injects the pairs into each app's env.
	home := t.TempDir()
	buildDir := filepath.Join(home, "build")
	seed := "# demo super-admin, shared across apps\n" +
		"SEED_EMAIL=rajan@rsynk.com\n" +
		"SEED_PASSWORD=s3cr3t-shared\n"
	if err := os.WriteFile(filepath.Join(home, "seed.env"), []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	apps := []App{
		{Name: "blog", FQDN: "blog.example.com", Path: "/apps/blog", Framework: "rails", Port: 3000, StartCommand: "puma", Database: "mysql", Memory: "512m"},
		// A per-app override must still win over the shared default.
		{Name: "crm", FQDN: "crm.example.com", Path: "/apps/crm", Framework: "django", Port: 8000, StartCommand: "gunicorn", Memory: "512m", Env: map[string]string{"SEED_PASSWORD": "crm-only"}},
		// Static apps carry no env at all; nothing to seed.
		{Name: "site", FQDN: "site.example.com", Path: "/apps/site", Framework: "static", Port: 80, Memory: "512m"},
	}
	out, err := RenderCompose(buildDir, apps)
	if err != nil {
		t.Fatalf("RenderCompose: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("compose.yml is not valid YAML: %v\n%s", err, out)
	}
	services, _ := doc["services"].(map[string]any)
	env := func(name string) map[string]any {
		svc, _ := services[name].(map[string]any)
		e, _ := svc["environment"].(map[string]any)
		return e
	}

	if got := env("blog")["SEED_EMAIL"]; got != "rajan@rsynk.com" {
		t.Errorf("blog SEED_EMAIL = %v, want rajan@rsynk.com", got)
	}
	if got := env("blog")["SEED_PASSWORD"]; got != "s3cr3t-shared" {
		t.Errorf("blog SEED_PASSWORD = %v, want s3cr3t-shared", got)
	}
	if got := env("crm")["SEED_EMAIL"]; got != "rajan@rsynk.com" {
		t.Errorf("crm SEED_EMAIL = %v, want the shared value", got)
	}
	if got := env("crm")["SEED_PASSWORD"]; got != "crm-only" {
		t.Errorf("crm SEED_PASSWORD = %v, want the per-app override crm-only", got)
	}
	if e := env("site"); len(e) != 0 {
		t.Errorf("static app should carry no environment, got %v", e)
	}
}

func TestRenderComposeNoSeedFileIsClean(t *testing.T) {
	// Missing ~/.roost/seed.env is not an error and injects nothing.
	home := t.TempDir()
	apps := []App{{Name: "blog", FQDN: "blog.example.com", Path: "/apps/blog", Framework: "rails", Port: 3000, StartCommand: "puma", Memory: "512m"}}
	out, err := RenderCompose(filepath.Join(home, "build"), apps)
	if err != nil {
		t.Fatalf("RenderCompose: %v", err)
	}
	if strings.Contains(string(out), "SEED_") {
		t.Errorf("no seed.env present, yet SEED_ vars appeared:\n%s", out)
	}
}

func TestRenderComposeNoMountForCompiledApps(t *testing.T) {
	apps := []App{
		{Name: "web", FQDN: "web.example.com", Path: "/apps/web", Framework: "next", Port: 3000, StartCommand: "npm run start", Memory: "512m"},
		{Name: "svc", FQDN: "svc.example.com", Path: "/apps/svc", Framework: "node", Port: 3000, StartCommand: "npm run start", Memory: "512m"},
		{Name: "blog", FQDN: "blog.example.com", Path: "/apps/blog", Framework: "rails", Port: 3000, StartCommand: "puma", Memory: "512m"},
	}
	out, err := RenderCompose("/b", apps)
	if err != nil {
		t.Fatalf("RenderCompose: %v", err)
	}
	s := string(out)
	// Compiled/bundled apps build into the image; mounting host source over
	// /app would shadow the built .next / node_modules and break startup.
	if strings.Contains(s, "/apps/web:/app:ro") {
		t.Errorf("next app source must not be bind-mounted:\n%s", s)
	}
	if strings.Contains(s, "/apps/svc:/app:ro") {
		t.Errorf("node app source must not be bind-mounted:\n%s", s)
	}
	// Interpreted frameworks stay mounted so restart picks up edits.
	if !strings.Contains(s, "/apps/blog:/app:ro") {
		t.Errorf("rails app source should be bind-mounted:\n%s", s)
	}
}

func TestRenderComposeOmitsUnusedDatabases(t *testing.T) {
	apps := []App{{
		Name: "site", FQDN: "site.example.com", Path: "/apps/site",
		Framework: "static", Port: 80, Memory: "512m",
	}}
	out, err := RenderCompose("/b", apps)
	if err != nil {
		t.Fatalf("RenderCompose: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	services, _ := doc["services"].(map[string]any)
	if _, has := services["mysql"]; has {
		t.Error("mysql service present with no mysql apps")
	}
	if _, has := services["postgres"]; has {
		t.Error("postgres service present with no postgres apps")
	}
}

func TestRenderCaddyfile(t *testing.T) {
	out, err := RenderCaddyfile(sampleApps())
	if err != nil {
		t.Fatalf("RenderCaddyfile: %v", err)
	}
	s := string(out)

	if !strings.Contains(s, "auto_https off") {
		t.Error("Caddyfile must disable automatic HTTPS; Cloudflare terminates TLS")
	}
	for _, want := range []string{
		"http://blog.demo.example.com",
		"reverse_proxy blog:3000",
		"http://crm.other.org",
		"reverse_proxy crm:8000",
		"reverse_proxy site:80",
		"reverse_proxy api:3000",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("Caddyfile missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "tls ") {
		t.Error("Caddyfile must not configure TLS")
	}
	// Cloudflare terminates TLS at the edge, so the request reaches the app
	// over http internally. Without telling the app the public scheme is
	// https, Rails computes an http base_url and rejects the browser's https
	// Origin (CSRF InvalidAuthenticityToken → 422). Force it upstream.
	if !strings.Contains(s, "header_up X-Forwarded-Proto https") {
		t.Errorf("Caddyfile must set X-Forwarded-Proto https upstream:\n%s", s)
	}
}

func TestRenderMySQLInit(t *testing.T) {
	out, err := RenderMySQLInit(sampleApps())
	if err != nil {
		t.Fatalf("RenderMySQLInit: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "CREATE DATABASE IF NOT EXISTS `blog`") {
		t.Errorf("mysql-init.sql missing blog database:\n%s", s)
	}
	if strings.Contains(s, "crm") {
		t.Errorf("mysql-init.sql must not include the postgres app crm:\n%s", s)
	}
	// A per-app mysql user (matching the app name) so apps whose database.yml
	// connects as their own username — including Rails multi-database setups —
	// work, with a grant spanning the app's database family.
	if !strings.Contains(s, "CREATE USER IF NOT EXISTS 'blog'@'%'") {
		t.Errorf("mysql-init.sql missing per-app user for blog:\n%s", s)
	}
	if !strings.Contains(s, "GRANT ALL PRIVILEGES ON `blog%`.* TO 'blog'@'%'") {
		t.Errorf("mysql-init.sql missing per-app grant for blog:\n%s", s)
	}
}

func TestRenderPostgresInit(t *testing.T) {
	out, err := RenderPostgresInit(sampleApps())
	if err != nil {
		t.Fatalf("RenderPostgresInit: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, `CREATE DATABASE "crm"`) {
		t.Errorf("postgres-init.sql missing crm database:\n%s", s)
	}
	if strings.Contains(s, "blog") {
		t.Errorf("postgres-init.sql must not include the mysql app blog:\n%s", s)
	}
}

func TestRenderDockerfile(t *testing.T) {
	apps := map[string]App{}
	for _, a := range sampleApps() {
		apps[a.Name] = a
	}

	t.Run("rails is multi-stage, versioned, non-root", func(t *testing.T) {
		out, err := RenderDockerfile(apps["blog"])
		if err != nil {
			t.Fatalf("RenderDockerfile: %v", err)
		}
		s := string(out)
		if got := strings.Count(s, "FROM "); got < 2 {
			t.Errorf("rails Dockerfile has %d FROM lines, want multi-stage (>= 2)", got)
		}
		for _, want := range []string{"ruby:3.2.2", "USER", "assets:precompile", "bundle exec puma -b tcp://0.0.0.0:3000"} {
			if !strings.Contains(s, want) {
				t.Errorf("rails Dockerfile missing %q:\n%s", want, s)
			}
		}
	})

	t.Run("static builds into a caddy image", func(t *testing.T) {
		out, err := RenderDockerfile(apps["site"])
		if err != nil {
			t.Fatalf("RenderDockerfile: %v", err)
		}
		if !strings.Contains(string(out), "FROM caddy") {
			t.Errorf("static Dockerfile should serve via caddy:\n%s", out)
		}
	})

	t.Run("node is multi-stage and non-root", func(t *testing.T) {
		out, err := RenderDockerfile(apps["api"])
		if err != nil {
			t.Fatalf("RenderDockerfile: %v", err)
		}
		s := string(out)
		if got := strings.Count(s, "FROM "); got < 2 {
			t.Errorf("node Dockerfile has %d FROM lines, want >= 2", got)
		}
		for _, want := range []string{"FROM node:", "USER", "npm run start"} {
			if !strings.Contains(s, want) {
				t.Errorf("node Dockerfile missing %q:\n%s", want, s)
			}
		}
	})

	t.Run("build_env is injected into the builder stage, sorted", func(t *testing.T) {
		app := apps["api"]
		app.BuildEnv = map[string]string{
			"SKIP_ENV_VALIDATION":     "1",
			"NEXT_TELEMETRY_DISABLED": "1",
		}
		out, err := RenderDockerfile(app)
		if err != nil {
			t.Fatalf("RenderDockerfile: %v", err)
		}
		s := string(out)
		if !strings.Contains(s, `ENV SKIP_ENV_VALIDATION="1"`) {
			t.Errorf("node Dockerfile missing build_env ENV line:\n%s", s)
		}
		// Deterministic (sorted) order: NEXT_* before SKIP_*.
		if strings.Index(s, "NEXT_TELEMETRY_DISABLED") > strings.Index(s, "SKIP_ENV_VALIDATION") {
			t.Errorf("build_env keys not sorted:\n%s", s)
		}
		// Must land in the builder stage, before the runtime FROM, so it
		// is present during `npm run build`.
		env := strings.Index(s, `ENV SKIP_ENV_VALIDATION`)
		build := strings.Index(s, "npm run build")
		if env < 0 || build < 0 || env > build {
			t.Errorf("build_env ENV must precede the build step:\n%s", s)
		}
	})

	t.Run("no build_env leaves the Dockerfile unchanged", func(t *testing.T) {
		out, err := RenderDockerfile(apps["api"])
		if err != nil {
			t.Fatalf("RenderDockerfile: %v", err)
		}
		if strings.Contains(string(out), "ENV SKIP_ENV_VALIDATION") {
			t.Errorf("unexpected build_env output when none set:\n%s", out)
		}
	})
}

func TestGenerateWritesArtifacts(t *testing.T) {
	buildDir := filepath.Join(t.TempDir(), "build")
	written, err := Generate(buildDir, sampleApps())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	for _, rel := range []string{
		"compose.yml",
		"Caddyfile",
		"mysql-init.sql",
		"postgres-init.sql",
		filepath.Join("dockerfiles", "blog.Dockerfile"),
		filepath.Join("dockerfiles", "crm.Dockerfile"),
		filepath.Join("dockerfiles", "site.Dockerfile"),
	} {
		path := filepath.Join(buildDir, rel)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected artifact %s: %v", rel, err)
		}
	}

	// api ships its own Dockerfile: roost must not generate one.
	if _, err := os.Stat(filepath.Join(buildDir, "dockerfiles", "api.Dockerfile")); err == nil {
		t.Error("api.Dockerfile generated despite the app having its own Dockerfile")
	}

	if len(written) == 0 {
		t.Error("Generate should report the paths it wrote")
	}
}

func TestGenerateOmitsUnusedInitScripts(t *testing.T) {
	buildDir := t.TempDir()
	apps := []App{{
		Name: "site", FQDN: "site.example.com", Path: "/apps/site",
		Framework: "static", Port: 80, Memory: "512m",
	}}
	if _, err := Generate(buildDir, apps); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(buildDir, "mysql-init.sql")); err == nil {
		t.Error("mysql-init.sql written with no mysql apps")
	}
	if _, err := os.Stat(filepath.Join(buildDir, "postgres-init.sql")); err == nil {
		t.Error("postgres-init.sql written with no postgres apps")
	}
}

func TestComposeArgsPinsProjectName(t *testing.T) {
	args := strings.Join(ComposeArgs("/home/u/.roost/build"), " ")
	if !strings.Contains(args, "-p roost") {
		t.Errorf("compose args %q must pin the project name with -p roost", args)
	}
	if !strings.Contains(args, filepath.Join("/home/u/.roost/build", "compose.yml")) {
		t.Errorf("compose args %q must reference the generated compose.yml", args)
	}
}

func TestPlan(t *testing.T) {
	fixtures, err := filepath.Abs(filepath.Join("..", "detect", "testdata"))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("detection fills the gaps", func(t *testing.T) {
		cfg := &config.Config{Defaults: config.Defaults{Memory: "640m", Profile: "core"}}
		resolved := []config.ResolvedApp{{
			App:  config.App{Path: filepath.Join(fixtures, "rails-app")},
			Name: "rails-app", FQDN: "rails-app.example.com",
		}}
		apps, err := Plan(cfg, resolved)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		a := apps[0]
		if a.Framework != "rails" || a.Port != 3000 || a.Database != "mysql" {
			t.Errorf("planned = %+v, want rails/3000/mysql", a)
		}
		if a.Memory != "640m" || a.Profile != "core" {
			t.Errorf("defaults not applied: memory=%q profile=%q", a.Memory, a.Profile)
		}
	})

	t.Run("explicit framework skips detection", func(t *testing.T) {
		empty := t.TempDir() // nothing detectable inside
		cfg := &config.Config{}
		resolved := []config.ResolvedApp{{
			App:  config.App{Path: empty, Framework: "node", Port: 4000},
			Name: "custom", FQDN: "custom.example.com",
		}}
		apps, err := Plan(cfg, resolved)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		a := apps[0]
		if a.Framework != "node" || a.Port != 4000 {
			t.Errorf("planned = %+v, want node with overridden port 4000", a)
		}
		if a.StartCommand == "" {
			t.Error("explicit framework should still get the framework default start command")
		}
	})

	t.Run("memory falls back to 512m", func(t *testing.T) {
		cfg := &config.Config{}
		resolved := []config.ResolvedApp{{
			App:  config.App{Path: filepath.Join(fixtures, "static-app")},
			Name: "static-app", FQDN: "static-app.example.com",
		}}
		apps, err := Plan(cfg, resolved)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if apps[0].Memory != "512m" {
			t.Errorf("Memory = %q, want 512m", apps[0].Memory)
		}
	})

	t.Run("own dockerfile detected", func(t *testing.T) {
		dir := t.TempDir()
		for name, content := range map[string]string{
			"package.json": `{"dependencies":{"express":"^4"}}`,
			"Dockerfile":   "FROM node:22-slim\n",
		} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		apps, err := Plan(&config.Config{}, []config.ResolvedApp{{
			App: config.App{Path: dir}, Name: "own", FQDN: "own.example.com",
		}})
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if !apps[0].HasOwnDockerfile {
			t.Error("HasOwnDockerfile = false, want true")
		}
	})

	t.Run("undetectable app is an error naming the app", func(t *testing.T) {
		apps, err := Plan(&config.Config{}, []config.ResolvedApp{{
			App: config.App{Path: filepath.Join(fixtures, "unknown-app")}, Name: "mystery", FQDN: "mystery.example.com",
		}})
		if err == nil {
			t.Fatalf("Plan = %+v, want error", apps)
		}
		if !strings.Contains(err.Error(), "mystery") {
			t.Errorf("error %q should name the app", err)
		}
	})
}
