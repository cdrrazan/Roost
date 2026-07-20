// Package generate turns resolved apps into the build artifacts under
// ~/.roost/build: compose.yml, per-app Dockerfiles, the Caddyfile, and
// database init scripts. roost never writes into the user's app
// directories; everything generated lands in the build directory.
package generate

import (
	"bytes"
	"embed"
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
	Name           string
	FQDN           string
	Path           string
	Framework      string
	Port           int
	StartCommand   string
	Database       string // "", "mysql", "postgres"
	Memory         string
	Profile        string
	Env            map[string]string
	RuntimeVersion string
	// HasOwnDockerfile is true when the app ships its own Dockerfile,
	// in which case roost uses theirs and generates none.
	HasOwnDockerfile bool
	// StaticBuild marks a static app that needs a build step (vite)
	// before Caddy can serve its dist/ output.
	StaticBuild bool
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
			Memory:         firstNonEmpty(r.Memory, cfg.Defaults.Memory, "512m"),
			Profile:        firstNonEmpty(r.Profile, cfg.Defaults.Profile),
			StaticBuild:    strings.Contains(d.Signal, "vite"),
		}
		if r.Port != 0 {
			app.Port = r.Port
		}
		if r.Database != "" {
			app.Database = r.Database
		}
		if fileExists(filepath.Join(r.Path, "Dockerfile")) {
			app.HasOwnDockerfile = true
		}
		apps = append(apps, app)
	}
	return apps, nil
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

// dockerfilePath returns the Dockerfile compose should build with:
// the app's own when it has one, the generated one otherwise.
func dockerfilePath(buildDir string, app App) string {
	if app.HasOwnDockerfile {
		return filepath.Join(app.Path, "Dockerfile")
	}
	return filepath.Join(buildDir, "dockerfiles", app.Name+".Dockerfile")
}

type envPair struct{ Key, Value string }

// appEnv assembles the container environment for an app: roost's
// injected values first, framework tuning, the database URL, then the
// user's env on top so explicit config always wins.
func appEnv(app App) []envPair {
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
		env["DATABASE_URL"] = "postgres://roost:roost@postgres:5432/" + db
	}
	for k, v := range app.Env {
		env[k] = v
	}

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

// dbName is the app's database name: hyphens become underscores so the
// name never needs quoting in connection URLs.
func dbName(app string) string {
	return strings.ReplaceAll(app, "-", "_")
}

type composeApp struct {
	Name        string
	Path        string
	Dockerfile  string
	Memory      string
	Profile     string
	Database    string
	MountSource bool
	Env         []envPair
}

type composeData struct {
	BuildDir      string
	Apps          []composeApp
	NeedsMySQL    bool
	NeedsPostgres bool
}

// RenderCompose renders compose.yml. buildDir is where generated
// artifacts live, used for Dockerfile and init-script mount paths.
func RenderCompose(buildDir string, apps []App) ([]byte, error) {
	data := composeData{BuildDir: buildDir}
	for _, app := range apps {
		data.NeedsMySQL = data.NeedsMySQL || app.Database == "mysql"
		data.NeedsPostgres = data.NeedsPostgres || app.Database == "postgres"
		data.Apps = append(data.Apps, composeApp{
			Name:       app.Name,
			Path:       app.Path,
			Dockerfile: dockerfilePath(buildDir, app),
			Memory:     app.Memory,
			Profile:    app.Profile,
			Database:   app.Database,
			// Static app content is copied at image build time; every
			// other app gets its source bind-mounted read-only.
			MountSource: app.Framework != "static",
			Env:         appEnv(app),
		})
	}
	return render("compose.yml.tmpl", data)
}

// RenderCaddyfile renders host-based routing on plain :80 with
// automatic HTTPS off (Cloudflare terminates TLS at the edge).
func RenderCaddyfile(apps []App) ([]byte, error) {
	return render("Caddyfile.tmpl", apps)
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
	}
	b.WriteString("FLUSH PRIVILEGES;\n")
	return b.Bytes(), nil
}

// RenderPostgresInit renders postgres-init.sql: one database per
// postgres app owned by the shared roost user.
func RenderPostgresInit(apps []App) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("-- Generated by roost — do not edit; runs once on first database boot.\n")
	for _, app := range apps {
		if app.Database != "postgres" {
			continue
		}
		fmt.Fprintf(&b, "CREATE DATABASE %q OWNER roost;\n", dbName(app.Name))
	}
	return b.Bytes(), nil
}

// dockerfileData is the template input for Dockerfile rendering.
type dockerfileData struct {
	App
	RubyTag     string
	NodeTag     string
	PythonTag   string
	RailsAssets bool
}

// RenderDockerfile renders the multi-stage Dockerfile for one app.
func RenderDockerfile(app App) ([]byte, error) {
	data := dockerfileData{
		App:         app,
		RubyTag:     versionTag(app.RuntimeVersion, "3.3"),
		NodeTag:     nodeTag(app.RuntimeVersion),
		PythonTag:   "3.12-slim",
		RailsAssets: app.Framework == "rails",
	}
	var name string
	switch app.Framework {
	case "rails", "sinatra":
		name = "ruby.Dockerfile.tmpl"
	case "next", "node":
		name = "node.Dockerfile.tmpl"
	case "django":
		name = "django.Dockerfile.tmpl"
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
	return "22-slim"
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
func Generate(buildDir string, apps []App) ([]string, error) {
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

	compose, err := RenderCompose(buildDir, apps)
	if err != nil {
		return nil, err
	}
	if err := write("compose.yml", compose); err != nil {
		return nil, err
	}

	caddy, err := RenderCaddyfile(apps)
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
