// Package config loads and validates the roost configuration file,
// resolves app paths to absolute directories, and resolves each app
// to exactly one fully-qualified hostname.
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the parsed roost configuration.
type Config struct {
	// Domain is the optional global fallback: apps without their own
	// domain resolve to <name>.<Domain>.
	Domain string `yaml:"domain"`
	// Include is an optional list of glob patterns; each matched file
	// contributes its `apps:` to this config, letting a big app list be
	// split across one file per feature. Patterns resolve against this
	// config's directory.
	Include  Includes `yaml:"include"`
	Tunnel   Tunnel   `yaml:"tunnel"`
	Defaults Defaults `yaml:"defaults"`
	Apps     []App    `yaml:"apps"`

	// ControlHost, when set, exposes the `roost web` control panel at this
	// hostname through the tunnel: Caddy routes it to the panel running on
	// the host (outside the stack). Empty means no panel route is generated.
	ControlHost string `yaml:"control_host"`

	// Remote, when set, points roost at a remote Docker daemon (an ssh://,
	// tcp://, or unix:// endpoint) instead of the local one, so the same
	// config runs the stack on a VPS. Empty = local, the default. It only
	// changes WHERE containers run — roost still generates artifacts locally
	// under ~/.roost/build — and source is not bind-mounted in remote mode
	// (the remote host has no copy of it), so apps build into their image.
	Remote string `yaml:"remote"`

	// Server is optional display metadata for the web panel's Server card
	// (the box's public IP + SSH login). It has no effect on how roost runs.
	Server Server `yaml:"server"`

	// Notify is optional email-notification config for the web panel's
	// incident monitor. The SMTP password is never here — it comes from
	// $ROOST_SMTP_PASSWORD.
	Notify Notify `yaml:"notify"`

	// Dir is the directory containing the config file; relative app
	// paths are resolved against it.
	Dir string `yaml:"-"`
}

// Notify configures incident email alerts from the web panel. Empty Email or
// SMTPHost disables notifications. The password is read from
// $ROOST_SMTP_PASSWORD, never from this file.
type Notify struct {
	Email    Includes `yaml:"email"`     // recipient(s); scalar or list
	SMTPHost string   `yaml:"smtp_host"` // e.g. smtp.gmail.com
	SMTPPort int      `yaml:"smtp_port"` // e.g. 587
	SMTPUser string   `yaml:"smtp_user"` // SMTP login (often the from address)
	From     string   `yaml:"from"`      // sender; defaults to smtp_user
}

// Server is optional host metadata surfaced in the web panel (display only).
type Server struct {
	IP      string `yaml:"ip"`       // public IP shown + used in the ssh command
	SSHUser string `yaml:"ssh_user"` // ssh <user>@<ip>
	Label   string `yaml:"label"`    // free-text (provider / shape / region)
}

// Tunnel holds the cloudflared tunnel settings.
type Tunnel struct {
	Name   string  `yaml:"name"`
	Access *Access `yaml:"access"`
}

// Access is the optional Cloudflare Access policy configuration.
type Access struct {
	Emails []string `yaml:"emails"`
}

// Defaults are optional per-app defaults.
type Defaults struct {
	Memory  string `yaml:"memory"`
	Profile string `yaml:"profile"`
}

// App is one application entry. In YAML it may be either a bare string
// (a path, using the global domain) or a map.
type App struct {
	Path string `yaml:"path"`
	// Repo is the app's git source (e.g. https://github.com/user/app). When
	// set, `roost add --repo` clones it into ~/.roost/sources/<name> and
	// `roost update` pulls it before rebuilding. Purely informational to the
	// build pipeline — Path is what gets built.
	Repo      string            `yaml:"repo"`
	Domain    string            `yaml:"domain"`
	Name      string            `yaml:"name"`
	Framework string            `yaml:"framework"`
	Port      int               `yaml:"port"`
	Database  string            `yaml:"database"`
	Memory    string            `yaml:"memory"`
	Profile   string            `yaml:"profile"`
	Env       map[string]string `yaml:"env"`
	// BuildEnv is environment set at image-build time (Docker ENV in the
	// builder stage) rather than at runtime — needed by frameworks that
	// validate env during their build, e.g. a Next.js app using
	// @t3-oss/env, which needs SKIP_ENV_VALIDATION set for `next build`.
	// These values bake into image layers, so use runtime `env:` for
	// secrets; build_env is for non-secret build flags.
	BuildEnv map[string]string `yaml:"build_env"`
	// Seed is the per-app database-seed directive. `seed: true` runs the
	// framework's default seed command after the app starts; `seed: "<cmd>"`
	// runs that command in the app container. Absent or false disables it.
	Seed SeedSpec `yaml:"seed"`
	// Migrate is the per-app database-setup directive. Absent (the default)
	// runs the framework's idempotent setup on every up (Rails db:prepare,
	// Django migrate). `migrate: false` skips it — for images that migrate
	// themselves at boot, so roost never races their entrypoint.
	// `migrate: "<cmd>"` runs that command instead.
	Migrate MigrateSpec `yaml:"migrate"`
	// Redis requests the shared Redis service and REDIS_URL injection.
	// Detected automatically for apps whose Gemfile uses sidekiq/redis;
	// set `redis: true` to force it on or `redis: false` to opt out.
	Redis RedisSpec `yaml:"redis"`
	// Command overrides the container start command. Its main use is a
	// worker entry that runs a background process (e.g.
	// "bundle exec sidekiq") instead of the framework's web server.
	Command string `yaml:"command"`
	// Worker marks a non-HTTP background process — typically a second
	// entry sharing another app's path. A worker gets no domain and no
	// Caddy route, and roost runs no DB setup or seed for it; it must
	// carry a command:. roost still starts and supervises its container.
	Worker bool `yaml:"worker"`
	// Category is a display grouping for the web control panel only —
	// "main", "utility", or left empty (treated as main). It has no effect
	// on how roost builds or runs the app. Worker entries are grouped as
	// workers regardless of this value.
	Category string `yaml:"category"`
}

// SeedSpec is a per-app seed directive. In YAML it accepts a boolean
// (true = the framework's default seed command) or a string (a custom
// command run in the app container). Absent or false means no seeding.
type SeedSpec struct {
	Enabled bool
	// Command is an explicit seed command; empty means use the
	// framework default (only when Enabled).
	Command string
}

// UnmarshalYAML accepts a boolean or a command string for `seed:`.
func (s *SeedSpec) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.ScalarNode {
		return fmt.Errorf("seed: must be a boolean or a command string")
	}
	var b bool
	if err := value.Decode(&b); err == nil {
		s.Enabled = b
		return nil
	}
	var cmd string
	if err := value.Decode(&cmd); err != nil {
		return fmt.Errorf("seed: must be a boolean or a command string")
	}
	s.Enabled = true
	s.Command = cmd
	return nil
}

// MigrateSpec is a per-app database-setup directive. In YAML it accepts a
// boolean (true = the framework's default setup command; false = skip,
// because the image migrates itself) or a string (a custom setup command).
// Set distinguishes an explicit value from an absent key, so absent can
// fall back to roost's framework default.
type MigrateSpec struct {
	// Set is true when the migrate key was present in the config.
	Set bool
	// Enabled is the boolean value (or true for a command string).
	Enabled bool
	// Command is an explicit setup command; empty means the framework
	// default (only when Enabled).
	Command string
}

// UnmarshalYAML accepts a boolean or a command string for `migrate:`.
func (m *MigrateSpec) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.ScalarNode {
		return fmt.Errorf("migrate: must be a boolean or a command string")
	}
	m.Set = true
	var b bool
	if err := value.Decode(&b); err == nil {
		m.Enabled = b
		return nil
	}
	var cmd string
	if err := value.Decode(&cmd); err != nil {
		return fmt.Errorf("migrate: must be a boolean or a command string")
	}
	m.Enabled = true
	m.Command = cmd
	return nil
}

// RedisSpec is a per-app Redis directive. In YAML it accepts a boolean:
// true forces the shared Redis service and REDIS_URL injection, false
// opts out of auto-detected Redis. Set distinguishes an explicit value
// from an absent key, so absent falls back to detection.
type RedisSpec struct {
	// Set is true when the redis key was present in the config.
	Set bool
	// Enabled is the boolean value.
	Enabled bool
}

// UnmarshalYAML accepts a boolean for `redis:`.
func (rs *RedisSpec) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.ScalarNode {
		return fmt.Errorf("redis: must be a boolean")
	}
	var b bool
	if err := value.Decode(&b); err != nil {
		return fmt.Errorf("redis: must be a boolean")
	}
	rs.Set = true
	rs.Enabled = b
	return nil
}

// UnmarshalYAML accepts both the bare-string form (a path) and the
// map form for an app entry.
func (a *App) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		var path string
		if err := value.Decode(&path); err != nil {
			return err
		}
		*a = App{Path: path}
		return nil
	}
	// Alias type drops the custom unmarshaller to avoid recursion.
	type plain App
	var p plain
	if err := value.Decode(&p); err != nil {
		return err
	}
	*a = App(p)
	return nil
}

// Includes is a list of glob patterns for pulling in extra app files.
// In YAML it accepts either a single string or a list of strings.
type Includes []string

// UnmarshalYAML accepts both a single scalar pattern and a list.
func (in *Includes) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		var s string
		if err := value.Decode(&s); err != nil {
			return err
		}
		*in = Includes{s}
		return nil
	}
	var list []string
	if err := value.Decode(&list); err != nil {
		return err
	}
	*in = list
	return nil
}

// ResolvedApp is an app that resolved to exactly one hostname.
type ResolvedApp struct {
	App
	// Name is the resolved app name (explicit name, or the slugified
	// basename of the path).
	Name string
	// FQDN is the single fully-qualified hostname the app answers on.
	FQDN string
}

// SkippedApp is an app excluded from the run, with the reason why.
type SkippedApp struct {
	App
	Name   string
	Reason string
}

// Load reads and parses the config file at path, expanding ~ and
// resolving relative app paths against the config file's directory.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve config path %s: %w", path, err)
	}
	cfg.Dir = filepath.Dir(abs)

	if cfg.Remote != "" {
		switch {
		case strings.HasPrefix(cfg.Remote, "ssh://"),
			strings.HasPrefix(cfg.Remote, "tcp://"),
			strings.HasPrefix(cfg.Remote, "unix://"):
		default:
			return nil, fmt.Errorf("remote %q must be an ssh://, tcp://, or unix:// Docker endpoint", cfg.Remote)
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}
	expandApps(cfg.Apps, cfg.Dir, home)

	// Pull in apps from included files, appended after this file's own
	// apps in include-pattern order. Hostname collisions across files
	// are caught later by Resolve.
	for _, pattern := range cfg.Include {
		apps, err := loadIncluded(pattern, cfg.Dir, home)
		if err != nil {
			return nil, err
		}
		cfg.Apps = append(cfg.Apps, apps...)
	}
	return &cfg, nil
}

// expandApps makes every app path absolute, relative to dir.
func expandApps(apps []App, dir, home string) {
	for i := range apps {
		apps[i].Path = expandPath(apps[i].Path, dir, home)
	}
}

// loadIncluded expands pattern (relative to baseDir), globs it, and
// returns the apps from every matched file in sorted file order. A
// pattern that matches nothing is an error, not a silent no-op.
func loadIncluded(pattern, baseDir, home string) ([]App, error) {
	glob := expandPath(pattern, baseDir, home)
	matches, err := filepath.Glob(glob)
	if err != nil {
		return nil, fmt.Errorf("include %q: %w", pattern, err)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("include %q matched no files (looked under %s)", pattern, baseDir)
	}
	sort.Strings(matches)

	var apps []App
	for _, m := range matches {
		fileApps, err := loadIncludedFile(m, home)
		if err != nil {
			return nil, err
		}
		apps = append(apps, fileApps...)
	}
	return apps, nil
}

// loadIncludedFile reads one included file, which may contain only an
// `apps:` list. Any other top-level key (including a nested include:)
// is rejected. App paths resolve against the included file's own
// directory, so a per-feature file can use paths relative to itself.
func loadIncludedFile(path, home string) ([]App, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read included file %s: %w", path, err)
	}
	var inc struct {
		Apps []App `yaml:"apps"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&inc); err != nil {
		return nil, fmt.Errorf("included file %s must contain only `apps:`: %w", path, err)
	}
	if len(inc.Apps) == 0 {
		return nil, fmt.Errorf("included file %s has no apps", path)
	}
	expandApps(inc.Apps, filepath.Dir(path), home)
	return inc.Apps, nil
}

// expandPath turns an app path into an absolute path: ~ expands to
// home, relative paths resolve against the config file's directory.
func expandPath(p, cfgDir, home string) string {
	switch {
	case p == "~":
		p = home
	case strings.HasPrefix(p, "~/"):
		p = filepath.Join(home, p[2:])
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(cfgDir, p)
	}
	return filepath.Clean(p)
}

// FindConfig returns the config file path using the resolution order:
// the --config flag value, $ROOST_CONFIG, ./roost.yml, then
// ~/.roost/config.yml. First hit wins. An explicitly-requested file
// that does not exist is an error rather than a fallthrough.
func FindConfig(flagPath string) (string, error) {
	if flagPath != "" {
		if !fileExists(flagPath) {
			return "", fmt.Errorf("config file not found: %s", flagPath)
		}
		return flagPath, nil
	}
	if env := os.Getenv("ROOST_CONFIG"); env != "" {
		if !fileExists(env) {
			return "", fmt.Errorf("$ROOST_CONFIG points to a missing file: %s", env)
		}
		return env, nil
	}
	if fileExists("roost.yml") {
		abs, err := filepath.Abs("roost.yml")
		if err != nil {
			return "", err
		}
		return abs, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	def := filepath.Join(home, ".roost", "config.yml")
	if fileExists(def) {
		return def, nil
	}
	return "", fmt.Errorf("no config found: tried ./roost.yml and %s (run `roost init` to create one)", def)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// Resolve maps every app to exactly one hostname, per the rule:
// an explicit per-app domain is used verbatim; otherwise the global
// domain composes <name>.<domain>; otherwise the app is skipped.
// A missing or non-directory path also skips the app. Invalid hostname
// syntax and hostname collisions are hard errors.
func Resolve(cfg *Config) ([]ResolvedApp, []SkippedApp, error) {
	var resolved []ResolvedApp
	var skipped []SkippedApp
	owner := make(map[string]string) // FQDN -> app name that claimed it

	for _, app := range cfg.Apps {
		name := app.Name
		if name == "" {
			name = Slugify(filepath.Base(app.Path))
		}

		if info, err := os.Stat(app.Path); err != nil {
			skipped = append(skipped, SkippedApp{App: app, Name: name, Reason: "path does not exist: " + app.Path})
			continue
		} else if !info.IsDir() {
			skipped = append(skipped, SkippedApp{App: app, Name: name, Reason: "path is not a directory: " + app.Path})
			continue
		}

		// A worker is a non-HTTP background process: it needs a command to
		// run and gets no hostname or Caddy route.
		if app.Worker {
			if app.Command == "" {
				return nil, nil, fmt.Errorf("worker app %q needs a command: (the process to run, e.g. \"bundle exec sidekiq\")", name)
			}
			resolved = append(resolved, ResolvedApp{App: app, Name: name, FQDN: ""})
			continue
		}

		var fqdn string
		switch {
		case app.Domain != "":
			if err := ValidateHostname(app.Domain); err != nil {
				return nil, nil, fmt.Errorf("app %q: invalid domain: %w", name, err)
			}
			fqdn = app.Domain
		case cfg.Domain != "":
			fqdn = name + "." + cfg.Domain
			if err := ValidateHostname(fqdn); err != nil {
				return nil, nil, fmt.Errorf("app %q: invalid hostname %q derived from global domain %q: %w", name, fqdn, cfg.Domain, err)
			}
		default:
			skipped = append(skipped, SkippedApp{App: app, Name: name, Reason: "no domain configured (set domain: on the app or a global domain)"})
			continue
		}

		if other, taken := owner[fqdn]; taken {
			return nil, nil, fmt.Errorf("apps %q and %q both resolve to hostname %q; hostnames must be unique", other, name, fqdn)
		}
		owner[fqdn] = name
		resolved = append(resolved, ResolvedApp{App: app, Name: name, FQDN: fqdn})
	}
	return resolved, skipped, nil
}

// ValidateHostname checks that host is a valid fully-qualified
// hostname: labels of 1-63 alphanumerics-and-hyphens, no leading or
// trailing hyphen, at least two labels, total length <= 253. It never
// repairs the value; it accepts or rejects with a precise reason, and
// for URLs and ports it says what the correct value would be.
func ValidateHostname(host string) error {
	if host == "" {
		return fmt.Errorf("hostname is empty")
	}
	if idx := strings.Index(host, "://"); idx >= 0 {
		bare := host[idx+3:]
		if slash := strings.IndexAny(bare, "/:"); slash >= 0 {
			bare = bare[:slash]
		}
		return fmt.Errorf("%q is a URL, not a hostname; use %q instead", host, bare)
	}
	if idx := strings.Index(host, ":"); idx >= 0 {
		return fmt.Errorf("%q includes a port; use %q instead", host, host[:idx])
	}
	if idx := strings.Index(host, "/"); idx >= 0 {
		return fmt.Errorf("%q includes a path; use %q instead", host, host[:idx])
	}
	if strings.HasSuffix(host, ".") {
		return fmt.Errorf("%q has a trailing dot; remove it", host)
	}
	if len(host) > 253 {
		return fmt.Errorf("%q is %d characters; hostnames are limited to 253", host, len(host))
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return fmt.Errorf("%q is a bare label; a hostname needs at least two labels, like app.example.com", host)
	}
	for _, label := range labels {
		if label == "" {
			return fmt.Errorf("%q contains an empty label (consecutive dots)", host)
		}
		if len(label) > 63 {
			return fmt.Errorf("label %q is %d characters; labels are limited to 63", label, len(label))
		}
		if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return fmt.Errorf("label %q must not start or end with a hyphen", label)
		}
		for _, r := range label {
			if !isHostnameRune(r) {
				return fmt.Errorf("label %q contains invalid character %q; only letters, digits, and hyphens are allowed", label, r)
			}
		}
	}
	return nil
}

func isHostnameRune(r rune) bool {
	return r == '-' ||
		(r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9')
}

// Slugify derives an app name from a directory basename: lowercase,
// with every run of non-alphanumeric characters collapsed to a single
// hyphen and leading/trailing hyphens trimmed.
func Slugify(base string) string {
	var b strings.Builder
	lastHyphen := true // suppress a leading hyphen
	for _, r := range strings.ToLower(base) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastHyphen = false
		default:
			if !lastHyphen {
				b.WriteByte('-')
				lastHyphen = true
			}
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}
