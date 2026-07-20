// Package config loads and validates the roost configuration file,
// resolves app paths to absolute directories, and resolves each app
// to exactly one fully-qualified hostname.
package config

import "errors"

// Config is the parsed roost configuration.
type Config struct {
	// Domain is the optional global fallback: apps without their own
	// domain resolve to <name>.<Domain>.
	Domain   string   `yaml:"domain"`
	Tunnel   Tunnel   `yaml:"tunnel"`
	Defaults Defaults `yaml:"defaults"`
	Apps     []App    `yaml:"apps"`

	// Dir is the directory containing the config file; relative app
	// paths are resolved against it.
	Dir string `yaml:"-"`
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
	Path      string            `yaml:"path"`
	Domain    string            `yaml:"domain"`
	Name      string            `yaml:"name"`
	Framework string            `yaml:"framework"`
	Port      int               `yaml:"port"`
	Database  string            `yaml:"database"`
	Memory    string            `yaml:"memory"`
	Profile   string            `yaml:"profile"`
	Env       map[string]string `yaml:"env"`
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

var errNotImplemented = errors.New("not implemented")

// Load reads and parses the config file at path, expanding ~ and
// resolving relative app paths against the config file's directory.
func Load(path string) (*Config, error) {
	return nil, errNotImplemented
}

// FindConfig returns the config file path using the resolution order:
// the --config flag value, $ROOST_CONFIG, ./roost.yml, then
// ~/.roost/config.yml. First hit wins.
func FindConfig(flagPath string) (string, error) {
	return "", errNotImplemented
}

// Resolve maps every app to exactly one hostname. Apps with no usable
// domain or a missing path are skipped with a reason; hostname syntax
// errors and collisions are hard errors.
func Resolve(cfg *Config) ([]ResolvedApp, []SkippedApp, error) {
	return nil, nil, errNotImplemented
}

// ValidateHostname checks that host is a valid fully-qualified hostname.
// It never repairs the value; it accepts or rejects with a precise reason.
func ValidateHostname(host string) error {
	return errNotImplemented
}

// Slugify derives an app name from a directory basename: lowercase,
// alphanumerics and hyphens only.
func Slugify(base string) string {
	return ""
}
