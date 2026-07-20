// Package generate turns resolved apps into the build artifacts under
// ~/.roost/build: compose.yml, per-app Dockerfiles, the Caddyfile, and
// database init scripts. roost never writes into the user's app
// directories; everything generated lands in the build directory.
package generate

import (
	"errors"

	"github.com/cdrrazan/roost/internal/config"
)

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
}

var errNotImplemented = errors.New("not implemented")

// Plan merges each resolved app with its framework detection (or the
// framework defaults when framework: is set explicitly) and applies
// config defaults for memory and profile.
func Plan(cfg *config.Config, resolved []config.ResolvedApp) ([]App, error) {
	return nil, errNotImplemented
}

// RenderCompose renders compose.yml. buildDir is where generated
// artifacts live, used for Dockerfile and init-script mount paths.
func RenderCompose(buildDir string, apps []App) ([]byte, error) {
	return nil, errNotImplemented
}

// RenderCaddyfile renders host-based routing on plain :80 with
// automatic HTTPS off (Cloudflare terminates TLS at the edge).
func RenderCaddyfile(apps []App) ([]byte, error) {
	return nil, errNotImplemented
}

// RenderMySQLInit renders mysql-init.sql: one database per mysql app,
// all granted to the shared roost user.
func RenderMySQLInit(apps []App) ([]byte, error) {
	return nil, errNotImplemented
}

// RenderPostgresInit renders postgres-init.sql: one database per
// postgres app owned by the shared roost user.
func RenderPostgresInit(apps []App) ([]byte, error) {
	return nil, errNotImplemented
}

// RenderDockerfile renders the multi-stage Dockerfile for one app.
func RenderDockerfile(app App) ([]byte, error) {
	return nil, errNotImplemented
}

// Generate writes all artifacts into buildDir and returns the paths
// written. Apps with their own Dockerfile get none generated.
func Generate(buildDir string, apps []App) ([]string, error) {
	return nil, errNotImplemented
}

// ComposeArgs returns the docker arguments for compose invocations,
// always pinning the project name to roost so runs from different
// working directories address the same stack.
func ComposeArgs(buildDir string) []string {
	return nil
}
