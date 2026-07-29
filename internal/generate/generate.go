// Package generate turns resolved apps into the build artifacts under
// ~/.roost/build: compose.yml, per-app Dockerfiles, the Caddyfile, and
// database init scripts. roost never writes into the user's app
// directories; everything generated lands in the build directory.
package generate

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/cdrrazan/roost/internal/config"
	"github.com/cdrrazan/roost/internal/detect"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

var templates = template.Must(template.ParseFS(templateFS, "templates/*.tmpl"))

// App is one app fully planned for generation: detection results
// merged with config overrides and defaults applied.
type App struct {
	Name         string
	FQDN         string
	Path         string
	Framework    string
	Port         int
	StartCommand string
	Database     string // "", "mysql", "postgres"
	// Redis is true when the app needs the shared Redis service; roost
	// injects REDIS_URL and adds a depends_on.
	Redis bool
	// Command overrides the container start command (config command:),
	// e.g. a worker running "bundle exec sidekiq".
	Command string
	// Worker marks a non-HTTP background app: no Caddy route, and roost
	// runs no DB setup or seed for it. It still runs as a supervised
	// container.
	Worker bool
	// Category is a display-only grouping for the web panel ("main",
	// "utility", or "worker"); it never affects build or run.
	Category string
	Memory   string
	Profile  string
	Env      map[string]string
	// BuildEnv is environment injected at image-build time (see
	// config.App.BuildEnv), rendered as ENV in the Dockerfile builder
	// stage so it is present during the app's build step.
	BuildEnv       map[string]string
	RuntimeVersion string
	// HasOwnDockerfile is true when the app ships its own Dockerfile,
	// in which case roost uses theirs and generates none.
	HasOwnDockerfile bool
	// StaticBuild marks a static app that needs a build step (vite)
	// before Caddy can serve its dist/ output.
	StaticBuild bool
	// SetupCommand is the idempotent DB prepare/migrate command roost
	// runs in the container on every up for database-backed apps.
	// Empty means no setup step (e.g. static apps, or frameworks with
	// no known migration command).
	SetupCommand string
	// SeedCommand is the command roost runs — with SEED_DEMO=1 so gated
	// demo seeds execute — to seed demo data. It runs once per app
	// (tracked in state) unless reseeding is forced. Empty disables it.
	SeedCommand string
	// HealthCheck is the container healthcheck command (a TCP probe of the
	// app's own port using a runtime binary the image is guaranteed to
	// have). Empty for workers, own-Dockerfile apps, and frameworks with no
	// known probe — Docker then reports no health, as before.
	HealthCheck string
	// NoSourceMount forces the source bind-mount off even for interpreted
	// frameworks. Set in remote mode: the remote Docker host has no copy of
	// the local source, so the app must build everything into its image.
	NoSourceMount bool
}

// Plan merges each resolved app with its framework detection (or the
// framework defaults when framework: is set explicitly) and applies
// config defaults for memory and profile.
func Plan(cfg *config.Config, resolved []config.ResolvedApp) ([]App, error) {
	apps := make([]App, 0, len(resolved))
	for _, r := range resolved {
		var d detect.Detection
		if r.Framework != "" {
			var ok bool
			d, ok = detect.Defaults(r.Framework)
			if !ok {
				return nil, fmt.Errorf("app %q: unknown framework %q", r.Name, r.Framework)
			}
		} else {
			var err error
			d, err = detect.Detect(r.Path)
			if err != nil {
				return nil, fmt.Errorf("app %q: %w", r.Name, err)
			}
		}

		app := App{
			Name:           r.Name,
			FQDN:           r.FQDN,
			Path:           r.Path,
			Framework:      d.Framework,
			Port:           d.Port,
			StartCommand:   d.StartCommand,
			Database:       d.Database,
			RuntimeVersion: d.RuntimeVersion,
			Env:            r.Env,
			BuildEnv:       r.BuildEnv,
			Memory:         firstNonEmpty(r.Memory, cfg.Defaults.Memory, "512m"),
			Profile:        firstNonEmpty(r.Profile, cfg.Defaults.Profile),
			StaticBuild:    strings.Contains(d.Signal, "vite") || strings.Contains(d.Signal, "astro"),
			Redis:          d.Redis,
			Worker:         r.Worker,
			Category:       r.Category,
		}
		// Workers are always grouped as workers in the panel, whatever the
		// config says.
		if app.Worker {
			app.Category = "worker"
		}
		if r.Command != "" {
			// Command is the compose-level override (used when the app ships
			// its own Dockerfile); StartCommand is the CMD of a generated
			// Dockerfile. Set both so the override wins either way.
			app.Command = r.Command
			app.StartCommand = r.Command
		}
		if r.Port != 0 {
			app.Port = r.Port
		}
		if r.Database != "" {
			app.Database = r.Database
		}
		// Redis: detection is the default; an explicit config value wins.
		if r.Redis.Set {
			app.Redis = r.Redis.Enabled
		}
		if fileExists(filepath.Join(r.Path, "Dockerfile")) {
			app.HasOwnDockerfile = true
		}
		// A worker doesn't own the database lifecycle — the web entry runs
		// setup and seed — so it never gets its own setup/seed commands.
		if app.Database != "" && !app.Worker {
			// Setup (migrate) command. Default: the framework's idempotent
			// db:prepare/migrate on every up. `migrate: false` opts out (the
			// image migrates itself at boot, so roost must not race its
			// entrypoint with a second prepare); `migrate: "<cmd>"` overrides.
			switch {
			case r.Migrate.Set && !r.Migrate.Enabled:
				// opted out: leave SetupCommand empty
			case r.Migrate.Command != "":
				app.SetupCommand = r.Migrate.Command
			default:
				app.SetupCommand = dbSetupCommand(app.Framework)
			}
		}
		if r.Seed.Enabled && !app.Worker {
			cmd := r.Seed.Command
			if cmd == "" {
				cmd = defaultSeedCommand(app.Framework)
				if cmd == "" {
					return nil, fmt.Errorf("app %q: seed: true has no default command for framework %q — set seed to an explicit command string", r.Name, app.Framework)
				}
			}
			app.SeedCommand = cmd
		}
		// A worker has no HTTP port; an own-Dockerfile app's runtime binaries
		// are unknown to us — neither gets a generated healthcheck.
		if !app.Worker && !app.HasOwnDockerfile {
			app.HealthCheck = healthCommand(app.Framework, app.Port)
		}
		// Remote mode: the remote Docker host has no local source to mount,
		// so every app builds into its image instead.
		if cfg.Remote != "" {
			app.NoSourceMount = true
		}
		apps = append(apps, app)
	}
	return apps, nil
}

// dbSetupCommand is the idempotent database prepare/migrate command for a
// framework, run in the app container before seeding. Empty when roost
// knows no migration command for the framework.
// healthCommand returns a container healthcheck that TCP-connects to the
// app's own port using a runtime binary the image is guaranteed to have —
// curl/wget aren't present in slim images, but the language runtime is.
// Empty for frameworks with no known probe.
func healthCommand(framework string, port int) string {
	p := strconv.Itoa(port)
	switch framework {
	case "rails", "sinatra":
		return `ruby -rsocket -e 'TCPSocket.new("127.0.0.1",` + p + `).close'`
	case "next", "node":
		return `node -e 'require("net").connect(` + p + `,"127.0.0.1").on("connect",()=>process.exit(0)).on("error",()=>process.exit(1))'`
	case "django", "flask":
		return `python -c "import socket,sys; sys.exit(0 if socket.socket().connect_ex(('127.0.0.1',` + p + `))==0 else 1)"`
	case "laravel":
		return `php -r 'exit(@fsockopen("127.0.0.1",` + p + `)?0:1);'`
	case "static":
		return `wget -q --spider http://127.0.0.1:` + p + `/`
	}
	return ""
}

func dbSetupCommand(framework string) string {
	switch framework {
	case "rails":
		return "bin/rails db:prepare"
	case "django":
		return "python manage.py migrate --noinput"
	case "laravel":
		return "php artisan migrate --force"
	}
	return ""
}

// defaultSeedCommand is the seed command used when an app sets seed: true
// without an explicit command. Only frameworks with a conventional seed
// task have one; others must supply a command string.
func defaultSeedCommand(framework string) string {
	switch framework {
	case "rails":
		return "bin/rails db:seed"
	case "laravel":
		return "php artisan db:seed --force"
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// mountsSource reports whether an app's source is bind-mounted read-only at
// /app at runtime. Interpreted frameworks (Rails, Django, Sinatra) are mounted
// so `roost restart` picks up source edits without a rebuild. Compiled/bundled
// frameworks build their artifacts into the image — static (Vite → dist/),
// next (.next/), and node (build output + installed node_modules) — so mounting
// the host source over /app would shadow the build and break startup.
func mountsSource(framework string) bool {
	switch framework {
	case "static", "next", "node", "laravel":
		// laravel installs vendor/ into /app during build; a bind mount would
		// shadow it.
		return false
	}
	return true
}

// dockerfilePath returns the Dockerfile compose should build with:
// the app's own when it has one, the generated one otherwise.
func dockerfilePath(buildDir string, app App) string {
	if app.HasOwnDockerfile {
		return filepath.Join(app.Path, "Dockerfile")
	}
	return filepath.Join(buildDir, "dockerfiles", app.Name+".Dockerfile")
}

type envPair struct{ Key, Value string }

// sortedPairs turns an env map into key-sorted pairs for deterministic
// rendering.
func sortedPairs(env map[string]string) []envPair {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]envPair, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, envPair{k, env[k]})
	}
	return pairs
}

// loadSeedEnv reads ~/.roost/seed.env — the optional file holding shared
// demo-seed credentials (SEED_EMAIL, SEED_PASSWORD, …) that roost injects
// into every app so they seed the same super-admin. It is a simple
// KEY=VALUE file: blank lines and #-comments are ignored, and matching
// surrounding quotes are stripped. A missing file is not an error (the
// feature is opt-in); a malformed line is skipped rather than fatal.
func loadSeedEnv(roostDir string) map[string]string {
	data, err := os.ReadFile(filepath.Join(roostDir, "seed.env"))
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out
}

// appEnv assembles the container environment for an app: roost's
// injected values first, framework tuning, the shared demo-seed
// credentials, the database URL, then the user's env on top so explicit
// config always wins.
func appEnv(app App, seed map[string]string) []envPair {
	if app.Framework == "static" {
		return nil
	}
	env := map[string]string{
		"PORT": strconv.Itoa(app.Port),
		// The public hostname, so frameworks with host authorization
		// (e.g. Rails blocked-host protection) can allow it.
		"ROOST_HOST": app.FQDN,
	}
	if app.Framework == "rails" {
		// Cloudflare terminates TLS and forwards plain HTTP; without
		// assume_ssl a force_ssl Rails app redirects forever.
		env["RAILS_ASSUME_SSL"] = "true"
		// Single-user local workloads: one worker, few threads,
		// roughly half the memory.
		env["WEB_CONCURRENCY"] = "1"
		env["RAILS_MAX_THREADS"] = "3"
	}
	db := dbName(app.Name)
	switch app.Database {
	case "mysql":
		scheme := "mysql"
		if app.Framework == "rails" {
			scheme = "mysql2"
		}
		env["DATABASE_URL"] = scheme + "://root:roost@mysql:3306/" + db
	case "postgres":
		env["DATABASE_URL"] = fmt.Sprintf("postgres://%s:%s@postgres:5432/%s", dbUser(app.Name), dbPassword(app.Name), db)
	}
	if app.Redis {
		// Database 0 of the shared Redis; apps reach it by service name.
		env["REDIS_URL"] = "redis://redis:6379/0"
	}
	// Shared demo-seed credentials sit below the user's env so an explicit
	// per-app override still wins.
	for k, v := range seed {
		env[k] = v
	}
	for k, v := range app.Env {
		env[k] = v
	}
	return sortedPairs(env)
}

// dbName is the app's database name: hyphens become underscores so the
// name never needs quoting in connection URLs.
func dbName(app string) string {
	return strings.ReplaceAll(app, "-", "_")
}

// dbUser is the app's own Postgres role name (same slug as its database).
func dbUser(app string) string {
	return dbName(app)
}

// dbPassword is the app's Postgres password. It is derived deterministically
// from the app name so it stays stable across regenerations — the init
// script only runs once (on first volume boot), but appEnv rebuilds
// DATABASE_URL on every up, and the two must always agree. The database is
// never reachable off the Compose network, so the point of a per-app
// password is blast-radius isolation between co-tenant apps, not secrecy
// from the outside; a name-derived value gives each app a distinct
// credential without any state to persist.
func dbPassword(app string) string {
	sum := sha256.Sum256([]byte("roost-pg:" + app))
	return "rp_" + hex.EncodeToString(sum[:])[:24]
}

type composeApp struct {
	Name        string
	Path        string
	Dockerfile  string
	Memory      string
	Profile     string
	Database    string
	Redis       bool
	Command     string
	MountSource bool
	HealthCheck string
	Env         []envPair
}

// Opts carries the stack-wide (non-per-app) settings that shape the
// generated artifacts.
type Opts struct {
	// ControlHost, when set, exposes the `roost web` panel at this hostname
	// through the tunnel (Caddy route + host-gateway extra_host).
	ControlHost string
	// TunnelProtocol overrides the cloudflared transport ("" = QUIC default,
	// "http2" = TCP/443 for networks that throttle UDP).
	TunnelProtocol string
}

type composeData struct {
	BuildDir      string
	Apps          []composeApp
	NeedsMySQL    bool
	NeedsPostgres bool
	NeedsRedis    bool
	// ControlHost is non-empty when the `roost web` panel is exposed; it
	// makes Caddy add a host-gateway extra_host so it can reach the panel
	// running on the host.
	ControlHost string
	// TunnelProtocol, when set, forces the cloudflared --protocol flag.
	TunnelProtocol string
}

// RenderCompose renders compose.yml. buildDir is where generated
// artifacts live, used for Dockerfile and init-script mount paths.
// opts carries the control-host route and cloudflared transport override.
func RenderCompose(buildDir string, apps []App, opts Opts) ([]byte, error) {
	data := composeData{BuildDir: buildDir, ControlHost: opts.ControlHost, TunnelProtocol: opts.TunnelProtocol}
	// seed.env lives next to build/ under ~/.roost; its pairs are shared
	// across every app so demo seeds land the same super-admin login.
	seed := loadSeedEnv(filepath.Dir(buildDir))
	for _, app := range apps {
		data.NeedsMySQL = data.NeedsMySQL || app.Database == "mysql"
		data.NeedsPostgres = data.NeedsPostgres || app.Database == "postgres"
		data.NeedsRedis = data.NeedsRedis || app.Redis
		data.Apps = append(data.Apps, composeApp{
			Name:        app.Name,
			Path:        app.Path,
			Dockerfile:  dockerfilePath(buildDir, app),
			Memory:      app.Memory,
			Profile:     app.Profile,
			Database:    app.Database,
			Redis:       app.Redis,
			Command:     app.Command,
			MountSource: mountsSource(app.Framework) && !app.NoSourceMount,
			HealthCheck: app.HealthCheck,
			Env:         appEnv(app, seed),
		})
	}
	return render("compose.yml.tmpl", data)
}

// caddyData is the Caddyfile template input: the routed apps plus an
// optional control-panel host.
type caddyData struct {
	Apps        []App
	ControlHost string
}

// RenderCaddyfile renders host-based routing on plain :80 with
// automatic HTTPS off (Cloudflare terminates TLS at the edge). Worker
// apps have no HTTP server, so they get no route. controlHost, when set,
// adds a route to the `roost web` panel running on the host.
func RenderCaddyfile(apps []App, controlHost string) ([]byte, error) {
	routed := make([]App, 0, len(apps))
	for _, app := range apps {
		if app.Worker {
			continue
		}
		routed = append(routed, app)
	}
	return render("Caddyfile.tmpl", caddyData{Apps: routed, ControlHost: controlHost})
}

// RenderMySQLInit renders mysql-init.sql: one database per mysql app,
// all granted to the shared roost user.
func RenderMySQLInit(apps []App) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("-- Generated by roost — do not edit; regenerated on every run.\n")
	b.WriteString("CREATE USER IF NOT EXISTS 'roost'@'%' IDENTIFIED BY 'roost';\n")
	for _, app := range apps {
		if app.Database != "mysql" {
			continue
		}
		db := dbName(app.Name)
		fmt.Fprintf(&b, "CREATE DATABASE IF NOT EXISTS `%s`;\n", db)
		fmt.Fprintf(&b, "GRANT ALL PRIVILEGES ON `%s`.* TO 'roost'@'%%';\n", db)
		// A per-app user matching the app name, for apps whose database.yml
		// connects as their own username (the Rails convention) rather than as
		// roost — including multi-database setups (Solid Cache/Queue/Cable) that
		// create sibling databases named `<app>_*`. The grant spans that
		// database family so the app can create and migrate them.
		fmt.Fprintf(&b, "CREATE USER IF NOT EXISTS '%s'@'%%' IDENTIFIED BY 'roost';\n", app.Name)
		fmt.Fprintf(&b, "GRANT ALL PRIVILEGES ON `%s%%`.* TO '%s'@'%%';\n", db, app.Name)
	}
	b.WriteString("FLUSH PRIVILEGES;\n")
	return b.Bytes(), nil
}

// RenderPostgresInit renders postgres-init.sql: each postgres app gets its
// own login role (with a name-derived password) that owns its database, so
// one app's credentials can't reach another's data. CREATEDB lets an app
// create the sibling <app>_* databases Rails multi-db (Solid Queue/Cache/
// Cable) needs, while still being unable to read databases it doesn't own.
func RenderPostgresInit(apps []App) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("-- Generated by roost — do not edit; runs once on first database boot.\n")
	for _, app := range apps {
		if app.Database != "postgres" {
			continue
		}
		user := dbUser(app.Name)
		fmt.Fprintf(&b, "CREATE ROLE %s LOGIN CREATEDB PASSWORD '%s';\n", user, dbPassword(app.Name))
		fmt.Fprintf(&b, "CREATE DATABASE %q OWNER %s;\n", dbName(app.Name), user)
	}
	return b.Bytes(), nil
}

// dockerfileData is the template input for Dockerfile rendering.
type dockerfileData struct {
	App
	RubyTag     string
	NodeTag     string
	PythonTag   string
	PhpTag      string
	RailsAssets bool
	// BuildEnvPairs is app.BuildEnv sorted for a deterministic Dockerfile;
	// templates range over it to emit build-stage ENV lines.
	BuildEnvPairs []envPair
}

// RenderDockerfile renders the multi-stage Dockerfile for one app.
func RenderDockerfile(app App) ([]byte, error) {
	data := dockerfileData{
		App:           app,
		RubyTag:       versionTag(app.RuntimeVersion, "3.4"),
		NodeTag:       nodeTag(app.RuntimeVersion),
		PythonTag:     "3.12-slim",
		PhpTag:        "8.3-cli",
		RailsAssets:   app.Framework == "rails",
		BuildEnvPairs: sortedPairs(app.BuildEnv),
	}
	var name string
	switch app.Framework {
	case "rails", "sinatra":
		name = "ruby.Dockerfile.tmpl"
	case "next", "node":
		name = "node.Dockerfile.tmpl"
	case "django", "flask":
		name = "python.Dockerfile.tmpl"
	case "laravel":
		name = "php.Dockerfile.tmpl"
	case "static":
		name = "static.Dockerfile.tmpl"
	default:
		return nil, fmt.Errorf("app %q: no Dockerfile template for framework %q", app.Name, app.Framework)
	}
	return render(name, data)
}

// versionTag turns a declared runtime version into a slim image tag,
// falling back when nothing usable was declared.
func versionTag(version, fallback string) string {
	if v := exactVersion(version); v != "" {
		return v + "-slim"
	}
	return fallback + "-slim"
}

// nodeTag maps a Node version declaration to an image tag. Range
// declarations like ">=20" pin to the major version.
func nodeTag(version string) string {
	if v := exactVersion(version); v != "" {
		// Pin to the major: node publishes rolling patch tags, and an
		// engines range like ">=20" only tells us the major anyway.
		major, _, _ := strings.Cut(v, ".")
		return major + "-slim"
	}
	return "24-slim"
}

// exactVersion extracts the leading dotted-number version out of a
// declaration like "3.2.2", ">=20", or "~> 3.3", or "" if none.
func exactVersion(version string) string {
	start := strings.IndexFunc(version, func(r rune) bool { return r >= '0' && r <= '9' })
	if start < 0 {
		return ""
	}
	rest := version[start:]
	end := strings.IndexFunc(rest, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
	if end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSuffix(rest, ".")
}

func render(name string, data any) ([]byte, error) {
	var b bytes.Buffer
	if err := templates.ExecuteTemplate(&b, name, data); err != nil {
		return nil, fmt.Errorf("render %s: %w", name, err)
	}
	return b.Bytes(), nil
}

// Generate writes all artifacts into buildDir and returns the paths
// written. Apps with their own Dockerfile get none generated; init
// scripts are only written (and stale ones removed) for databases in
// use.
func Generate(buildDir string, apps []App, opts Opts) ([]string, error) {
	dockerfilesDir := filepath.Join(buildDir, "dockerfiles")
	// The dockerfiles dir is wholly roost-generated: clear it so
	// removed apps don't leave stale Dockerfiles behind.
	if err := os.RemoveAll(dockerfilesDir); err != nil {
		return nil, fmt.Errorf("clear %s: %w", dockerfilesDir, err)
	}
	if err := os.MkdirAll(dockerfilesDir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", dockerfilesDir, err)
	}

	var written []string
	write := func(rel string, content []byte) error {
		path := filepath.Join(buildDir, rel)
		if err := os.WriteFile(path, content, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		written = append(written, path)
		return nil
	}

	compose, err := RenderCompose(buildDir, apps, opts)
	if err != nil {
		return nil, err
	}
	if err := write("compose.yml", compose); err != nil {
		return nil, err
	}

	caddy, err := RenderCaddyfile(apps, opts.ControlHost)
	if err != nil {
		return nil, err
	}
	if err := write("Caddyfile", caddy); err != nil {
		return nil, err
	}

	needs := map[string]bool{}
	for _, app := range apps {
		needs[app.Database] = true
	}
	for db, renderInit := range map[string]func([]App) ([]byte, error){
		"mysql":    RenderMySQLInit,
		"postgres": RenderPostgresInit,
	} {
		script := db + "-init.sql"
		if !needs[db] {
			// Remove a stale script from a previous config.
			if err := os.Remove(filepath.Join(buildDir, script)); err != nil && !os.IsNotExist(err) {
				return nil, fmt.Errorf("remove stale %s: %w", script, err)
			}
			continue
		}
		content, err := renderInit(apps)
		if err != nil {
			return nil, err
		}
		if err := write(script, content); err != nil {
			return nil, err
		}
	}

	for _, app := range apps {
		if app.HasOwnDockerfile {
			continue
		}
		content, err := RenderDockerfile(app)
		if err != nil {
			return nil, err
		}
		if err := write(filepath.Join("dockerfiles", app.Name+".Dockerfile"), content); err != nil {
			return nil, err
		}
	}
	return written, nil
}

// ComposeArgs returns the docker arguments for compose invocations,
// always pinning the project name to roost: otherwise Compose derives
// it from the working directory, and running roost from two different
// folders would silently create two independent stacks.
func ComposeArgs(buildDir string) []string {
	return []string{"compose", "-p", "roost", "-f", filepath.Join(buildDir, "compose.yml")}
}
