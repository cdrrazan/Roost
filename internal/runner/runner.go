// Package runner orchestrates docker compose for the generated stack.
// It never talks to Docker directly — every invocation goes through a
// shell.Runner so tests inject a fake. There is no roost daemon:
// Docker's restart policy is the supervisor.
package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cdrrazan/roost/internal/generate"
	"github.com/cdrrazan/roost/internal/shell"
)

// DefaultStagger spaces app container starts so 6+ apps don't spike a
// laptop's CPU all at once.
const DefaultStagger = 3 * time.Second

// Runner drives docker compose against the generated build directory.
type Runner struct {
	Shell    shell.Runner
	BuildDir string
	// Stagger is the pause between consecutive app starts.
	Stagger time.Duration
	// Sleep is time.Sleep, injectable for tests.
	Sleep func(time.Duration)
}

// New returns a Runner with real shell execution and default stagger.
func New(buildDir string) *Runner {
	return &Runner{
		Shell:    shell.Exec{},
		BuildDir: buildDir,
		Stagger:  DefaultStagger,
		Sleep:    time.Sleep,
	}
}

// compose builds a docker invocation with the project name pinned to
// roost and the generated compose file. When tunnel setup has written
// a .env (the cloudflared connector token), compose loads it too.
func (r *Runner) compose(args ...string) []string {
	base := generate.ComposeArgs(r.BuildDir)
	envFile := filepath.Join(r.BuildDir, ".env")
	if _, err := os.Stat(envFile); err == nil {
		base = append(base, "--env-file", envFile)
	}
	return append(base, args...)
}

func (r *Runner) run(args ...string) (shell.Result, error) {
	return r.Shell.Run("docker", args...)
}

// mysqlVolume is the docker volume backing MySQL data. Compose prefixes the
// pinned project name ("roost", see ComposeArgs) onto the compose-file
// volume key ("roost-mysql-data"), giving "roost_roost-mysql-data".
const mysqlVolume = "roost_roost-mysql-data"

// MysqlVolumeID returns an identity for the MySQL data volume that changes
// whenever the volume is recreated (a Docker Desktop Clean/Purge, a
// `docker volume rm`, or a fresh machine): the volume's CreatedAt stamp.
// An absent volume yields an empty id and no error, so callers treat the
// identity as merely unknown rather than as a failure.
func (r *Runner) MysqlVolumeID() (string, error) {
	res, err := r.run("volume", "inspect", mysqlVolume, "--format", "{{.CreatedAt}}")
	if err != nil {
		// No such volume yet: unknown identity, not a hard error.
		return "", nil
	}
	return strings.TrimSpace(res.Stdout), nil
}

// AppSelected reports whether an app runs under the selected profiles:
// unprofiled apps always run; profiled apps only when selected. With
// no profiles selected, everything runs.
func AppSelected(app generate.App, profiles []string) bool {
	if app.Profile == "" || len(profiles) == 0 {
		return true
	}
	for _, p := range profiles {
		if app.Profile == p {
			return true
		}
	}
	return false
}

// Up starts the stack: shared infrastructure first (databases, caddy,
// cloudflared), then each app with a staggered pause between starts.
func (r *Runner) Up(apps []generate.App, profiles []string) error {
	infra := []string{"caddy", "cloudflared"}
	needs := map[string]bool{}
	var selected []generate.App
	for _, app := range apps {
		if !AppSelected(app, profiles) {
			continue
		}
		selected = append(selected, app)
		needs[app.Database] = true
	}
	if needs["mysql"] {
		infra = append(infra, "mysql")
	}
	if needs["postgres"] {
		infra = append(infra, "postgres")
	}

	// Stream infra startup: first runs pull images (mysql alone is
	// hundreds of MB) and captured output would look like a hang.
	if err := r.Shell.Stream("docker", r.compose(append([]string{"up", "-d"}, infra...)...)...); err != nil {
		return fmt.Errorf("start shared services: %w", err)
	}

	for i, app := range selected {
		if i > 0 {
			r.Sleep(r.Stagger)
		}
		// Build as an explicit streamed step: a first build installs
		// dependencies and can take minutes, and the user must see it
		// happening rather than a silent prompt.
		if err := r.Shell.Stream("docker", r.compose("build", app.Name)...); err != nil {
			return fmt.Errorf("build app %q: %w", app.Name, err)
		}
		if _, err := r.run(r.compose("up", "-d", app.Name)...); err != nil {
			return fmt.Errorf("start app %q: %w", app.Name, err)
		}
	}

	if len(selected) > 0 {
		r.reloadProxy()
	}
	return nil
}

// Prepare runs post-start database setup and seeding for the selected
// apps. Every app with a SetupCommand (a DB-backed app) gets its
// idempotent migrate/prepare command on each call. An app with a
// SeedCommand is seeded — executed with SEED_DEMO=1 so gated demo seeds
// run — only when shouldSeed approves it (first time, or a forced
// reseed); onSeeded records each success so later runs skip it.
//
// Setup and seed both run through `sh -lc` so a command string may chain
// steps or set inline env. Failures are collected per app and returned
// together: one app's seed failing must not stop the others from being
// prepared.
func (r *Runner) Prepare(apps []generate.App, profiles []string, shouldSeed func(name string) bool, onSeeded func(name string) error) error {
	var errs []string
	for _, app := range apps {
		if !AppSelected(app, profiles) {
			continue
		}
		if app.SetupCommand != "" {
			if _, err := r.run(r.compose("exec", "-T", app.Name, "sh", "-lc", app.SetupCommand)...); err != nil {
				errs = append(errs, fmt.Sprintf("db setup for %q: %v", app.Name, err))
				// Skip seeding an app whose migrations failed.
				continue
			}
		}
		if app.SeedCommand == "" || (shouldSeed != nil && !shouldSeed(app.Name)) {
			continue
		}
		if _, err := r.run(r.compose("exec", "-T", "-e", "SEED_DEMO=1", app.Name, "sh", "-lc", app.SeedCommand)...); err != nil {
			errs = append(errs, fmt.Sprintf("seed %q: %v", app.Name, err))
			continue
		}
		if onSeeded != nil {
			if err := onSeeded(app.Name); err != nil {
				errs = append(errs, fmt.Sprintf("record seed for %q: %v", app.Name, err))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("prepare: %s", strings.Join(errs, "; "))
	}
	return nil
}

// ReloadProxy reloads Caddy against the generated Caddyfile, surfacing any
// error. It's the exported path used by `roost share` after writing a
// temporary route; internal callers use reloadProxy (best-effort).
func (r *Runner) ReloadProxy() error {
	_, err := r.run(r.compose("exec", "-T", "caddy", "caddy", "reload", "--config", "/etc/caddy/Caddyfile")...)
	return err
}

// reloadProxy reloads Caddy so it rebuilds its reverse-proxy connection
// pools. Apps may have been recreated with new container IPs, and Caddy —
// started earlier as infra — can otherwise hold stale upstream keep-alives to
// the old containers and serve empty 200s. Best-effort: the stack is already
// up, and a reload hiccup must not fail the caller (a stale connection
// self-heals once it errors on the next request).
func (r *Runner) reloadProxy() {
	_, _ = r.run(r.compose("exec", "-T", "caddy", "caddy", "reload", "--config", "/etc/caddy/Caddyfile")...)
}

// Start builds (if needed) and starts one app's container, then reloads Caddy
// so the proxy routes to the fresh container. Assumes shared infrastructure is
// already up (use `roost up` for a full bring-up).
func (r *Runner) Start(app string) error {
	if err := r.Shell.Stream("docker", r.compose("build", app)...); err != nil {
		return fmt.Errorf("build %q: %w", app, err)
	}
	if _, err := r.run(r.compose("up", "-d", app)...); err != nil {
		return fmt.Errorf("start %q: %w", app, err)
	}
	r.reloadProxy()
	return nil
}

// Stop stops one app's container without removing it, leaving the rest of the
// stack (and this app's image) in place so `roost start` brings it back fast.
func (r *Runner) Stop(app string) error {
	if _, err := r.run(r.compose("stop", app)...); err != nil {
		return fmt.Errorf("stop %q: %w", app, err)
	}
	return nil
}

// Resume starts an already-created but stopped app container without
// rebuilding — the fast counterpart to Stop.
func (r *Runner) Resume(app string) error {
	if _, err := r.run(r.compose("start", app)...); err != nil {
		return fmt.Errorf("resume %q: %w", app, err)
	}
	return nil
}

// Down stops and removes the whole stack.
func (r *Runner) Down() error {
	if _, err := r.run(r.compose("down", "--remove-orphans")...); err != nil {
		return fmt.Errorf("stop stack: %w", err)
	}
	return nil
}

// Remove stops and removes one app's container, leaving the rest of the stack
// running. With deleteImage it also deletes the app's built image
// (roost-<app>) to reclaim disk — a best-effort step (a missing or shared
// image must not fail the removal). It never touches the shared database
// volumes: an app's data lives in the mysql/postgres volume, not per-app.
func (r *Runner) Remove(app string, deleteImage bool) error {
	if _, err := r.run(r.compose("rm", "-sf", app)...); err != nil {
		return fmt.Errorf("remove %q: %w", app, err)
	}
	if deleteImage {
		// Compose names build-service images <project>-<service>; the pinned
		// project is "roost" (see ComposeArgs). Best-effort — an image still
		// referenced elsewhere, or already gone, is not an error here.
		_, _ = r.run("image", "rm", "-f", "roost-"+app)
	}
	return nil
}

// Restart restarts one app's container.
func (r *Runner) Restart(app string) error {
	if _, err := r.run(r.compose("restart", app)...); err != nil {
		return fmt.Errorf("restart %q: %w", app, err)
	}
	return nil
}

// Logs streams an app's logs to the terminal.
// Logs streams container logs. A non-empty app tails just that service;
// an empty app omits the service argument so compose multiplexes every
// service's logs — `roost logs` with no app = all apps.
func (r *Runner) Logs(app string, follow bool) error {
	args := []string{"logs"}
	if follow {
		args = append(args, "--follow")
	}
	if app != "" {
		args = append(args, app)
	}
	return r.Shell.Stream("docker", r.compose(args...)...)
}

// TunnelHealth is an advisory classification of the cloudflared connector.
type TunnelHealth string

const (
	TunnelConnected    TunnelHealth = "connected"
	TunnelReconnecting TunnelHealth = "reconnecting"
	TunnelDown         TunnelHealth = "down"
	TunnelUnknown      TunnelHealth = "unknown"
)

// TunnelStatus classifies the cloudflared connector so `roost status` can
// tell "the edge is reconnecting after a wake (~5-10s, apps may 502 briefly)"
// apart from "an app is actually down". It is advisory: the only in-band
// signal is cloudflared's own log tail — the container stays "running" while
// it retries — so the result is heuristic, not authoritative.
func (r *Runner) TunnelStatus() TunnelHealth {
	psOut, err := r.run(r.compose("ps", "--format", "json", "cloudflared")...)
	if err != nil {
		return TunnelUnknown
	}
	running := false
	for _, line := range strings.Split(psOut.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var p psLine
		if json.Unmarshal([]byte(line), &p) == nil && p.State == "running" {
			running = true
		}
	}
	if !running {
		return TunnelDown
	}
	logs, err := r.LogTail("cloudflared", 50)
	if err != nil {
		return TunnelUnknown
	}
	return classifyTunnelLog(logs)
}

// classifyTunnelLog reads cloudflared's recent log tail and reports whether
// its last connection event was a (re)connect or a loss. The marker strings
// are cloudflared's own; matching is deliberately conservative — only a
// clear loss occurring after the last connect reads as reconnecting,
// otherwise a connector with any connect marker reads as connected.
func classifyTunnelLog(logs string) TunnelHealth {
	connect := []string{"Registered tunnel connection", "Connection registered", "registered connIndex"}
	lost := []string{"Unregistered tunnel connection", "Lost connection with the edge", "Retrying connection", "Serve tunnel error", "Connection terminated"}
	lastConnect, lastLost := -1, -1
	for i, line := range strings.Split(logs, "\n") {
		for _, m := range connect {
			if strings.Contains(line, m) {
				lastConnect = i
			}
		}
		for _, m := range lost {
			if strings.Contains(line, m) {
				lastLost = i
			}
		}
	}
	switch {
	case lastConnect == -1 && lastLost == -1:
		return TunnelUnknown
	case lastLost > lastConnect:
		return TunnelReconnecting
	default:
		return TunnelConnected
	}
}

// LogTail captures the last n lines of an app's logs (for the panel drawer).
func (r *Runner) LogTail(app string, n int) (string, error) {
	out, err := r.run(r.compose("logs", "--no-color", "--tail", fmt.Sprintf("%d", n), app)...)
	if err != nil {
		return "", fmt.Errorf("logs %q: %w", app, err)
	}
	return out.Stdout, nil
}

// AppInfo is one app's container detail for the panel drawer.
type AppInfo struct {
	Image    string
	Status   string // "Up 2 hours"
	Health   string
	Restarts int
}

// AppInfo returns image, status/health, and restart count for one app,
// combining `compose ps` (image/status) with `docker inspect` (restart count).
func (r *Runner) AppInfo(app string) (AppInfo, error) {
	psOut, err := r.run(r.compose("ps", "--all", "--format", "json", app)...)
	if err != nil {
		return AppInfo{}, fmt.Errorf("ps %q: %w", app, err)
	}
	var info AppInfo
	var container string
	for _, line := range strings.Split(psOut.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var p psLine
		if err := json.Unmarshal([]byte(line), &p); err != nil {
			continue
		}
		if p.Service == app || container == "" {
			info.Image, info.Status, info.Health, container = p.Image, p.Status, p.Health, p.Name
		}
	}
	// Restart count is best-effort — a missing container just leaves it 0.
	if container != "" {
		if out, err := r.run("inspect", "-f", "{{.RestartCount}}", container); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(out.Stdout)); err == nil {
				info.Restarts = n
			}
		}
	}
	return info, nil
}

// AppStatus is one app's runtime state.
type AppStatus struct {
	Name     string
	State    string // running, exited, restarting, not created, skipped...
	Health   string
	Memory   string // "used / cap" from docker stats, "" when unknown
	URL      string
	Category string // display grouping for the panel: main, utility, worker
	Repo     string // browsable code-host URL for the app's git origin, "" if none
	// Static per-app metadata (from detection/config) shown as badges.
	Framework string
	Database  string // "", "mysql", "postgres"
	Redis     bool
	Runtime   string // language runtime version, "" if unknown
	Worker    bool
	// Live runtime metrics from docker stats / ps.
	CPU string // "2.75%" from docker stats, "" when unknown
	Net string // "1.2MB / 800kB" network I/O, "" when unknown
	Up  string // human uptime string from compose ps ("Up 3 hours"), "" when down
	// Reachability from an actual HTTP probe of the public URL (set by the
	// web controller, not the runner). HTTP is the status code or an error
	// word ("timeout"); Reachable is true when the app answered without a
	// gateway error. Both empty/false when not probed (down, worker).
	HTTP      string
	Reachable bool
}

// psLine is the subset of `docker compose ps --format json` output we
// read.
type psLine struct {
	Service string `json:"Service"`
	State   string `json:"State"`
	Health  string `json:"Health"`
	Name    string `json:"Name"`
	Status  string `json:"Status"` // "Up 3 hours (healthy)" — used for uptime
	Image   string `json:"Image"`
}

// statsLine is the subset of `docker stats --format json` output we
// read.
type statsLine struct {
	Name     string `json:"Name"`
	MemUsage string `json:"MemUsage"`
	CPUPerc  string `json:"CPUPerc"`
	NetIO    string `json:"NetIO"`
}

// Status reports state, health, and memory per app. Apps absent from
// compose ps are reported as "not created".
func (r *Runner) Status(apps []generate.App) ([]AppStatus, error) {
	psOut, err := r.run(r.compose("ps", "--all", "--format", "json")...)
	if err != nil {
		return nil, fmt.Errorf("compose ps: %w", err)
	}
	states := map[string]psLine{}
	for _, line := range strings.Split(psOut.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var p psLine
		if err := json.Unmarshal([]byte(line), &p); err != nil {
			continue
		}
		states[p.Service] = p
	}

	stats := map[string]statsLine{}
	if statsOut, err := r.run("stats", "--no-stream", "--format", "json"); err == nil {
		for _, line := range strings.Split(statsOut.Stdout, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var s statsLine
			if err := json.Unmarshal([]byte(line), &s); err != nil {
				continue
			}
			// Container names look like roost-<service>-1.
			svc := strings.TrimPrefix(s.Name, "roost-")
			if i := strings.LastIndex(svc, "-"); i > 0 {
				svc = svc[:i]
			}
			stats[svc] = s
		}
	}

	statuses := make([]AppStatus, 0, len(apps))
	for _, app := range apps {
		url := "https://" + app.FQDN
		if app.FQDN == "" {
			url = "(worker)"
		}
		st := AppStatus{
			Name:      app.Name,
			State:     "not created",
			URL:       url,
			Category:  app.Category,
			Framework: app.Framework,
			Database:  app.Database,
			Redis:     app.Redis,
			Runtime:   app.RuntimeVersion,
			Worker:    app.Worker,
		}
		if p, ok := states[app.Name]; ok {
			st.State = p.State
			st.Health = p.Health
			st.Up = p.Status
		}
		if s, ok := stats[app.Name]; ok {
			st.Memory = s.MemUsage
			st.CPU = s.CPUPerc
			st.Net = s.NetIO
		}
		statuses = append(statuses, st)
	}
	return statuses, nil
}
