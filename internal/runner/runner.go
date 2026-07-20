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

// Down stops and removes the whole stack.
func (r *Runner) Down() error {
	if _, err := r.run(r.compose("down", "--remove-orphans")...); err != nil {
		return fmt.Errorf("stop stack: %w", err)
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
func (r *Runner) Logs(app string, follow bool) error {
	args := []string{"logs"}
	if follow {
		args = append(args, "--follow")
	}
	args = append(args, app)
	return r.Shell.Stream("docker", r.compose(args...)...)
}

// AppStatus is one app's runtime state.
type AppStatus struct {
	Name   string
	State  string // running, exited, restarting, not created, skipped...
	Health string
	Memory string // "used / cap" from docker stats, "" when unknown
	URL    string
}

// psLine is the subset of `docker compose ps --format json` output we
// read.
type psLine struct {
	Service string `json:"Service"`
	State   string `json:"State"`
	Health  string `json:"Health"`
	Name    string `json:"Name"`
}

// statsLine is the subset of `docker stats --format json` output we
// read.
type statsLine struct {
	Name     string `json:"Name"`
	MemUsage string `json:"MemUsage"`
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

	memory := map[string]string{}
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
			memory[svc] = s.MemUsage
		}
	}

	statuses := make([]AppStatus, 0, len(apps))
	for _, app := range apps {
		st := AppStatus{
			Name:  app.Name,
			State: "not created",
			URL:   "https://" + app.FQDN,
		}
		if p, ok := states[app.Name]; ok {
			st.State = p.State
			st.Health = p.Health
		}
		if mem, ok := memory[app.Name]; ok {
			st.Memory = mem
		}
		statuses = append(statuses, st)
	}
	return statuses, nil
}
