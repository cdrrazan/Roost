package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/cdrrazan/roost/internal/config"
	"github.com/cdrrazan/roost/internal/doctor"
	"github.com/cdrrazan/roost/internal/generate"
	"github.com/cdrrazan/roost/internal/runner"
	"github.com/cdrrazan/roost/internal/shell"
	"github.com/cdrrazan/roost/internal/state"
	"github.com/cdrrazan/roost/internal/web"
)

// stackController is the real web.Controller: it drives the stack the same way
// the up/down/status commands do, so the panel and the CLI stay in lockstep.
type stackController struct {
	cmd   *cobra.Command
	flags *rootFlags
}

var _ web.Controller = (*stackController)(nil)

func (c *stackController) Status() ([]runner.AppStatus, error) {
	apps, _, err := loadPlanned(c.cmd, c.flags)
	if err != nil {
		return nil, err
	}
	r, err := newRunner()
	if err != nil {
		return nil, err
	}
	return r.Status(apps)
}

// appResumer is the runner subset Up needs: resume one stopped app container.
type appResumer interface {
	Resume(app string) error
}

// startApps resumes every stopped app container (fast — no rebuild), the
// counterpart to stopApps. Errors are joined so one failure doesn't skip the
// rest.
func startApps(r appResumer, apps []generate.App) error {
	var errs []error
	for _, app := range apps {
		if err := r.Resume(app.Name); err != nil {
			errs = append(errs, fmt.Errorf("start %s: %w", app.Name, err))
		}
	}
	return errors.Join(errs...)
}

// Up resumes the app containers the panel's Stop paused. It is a fast runtime
// toggle, not a provisioner: it does NOT rebuild images or regenerate
// artifacts, and it leaves shared infrastructure (which Stop never touched)
// alone. Initial provisioning and config changes are `roost up` on the CLI.
func (c *stackController) Up() error {
	apps, _, err := loadPlanned(c.cmd, c.flags)
	if err != nil {
		return err
	}
	if len(apps) == 0 {
		return fmt.Errorf("nothing to run: no app resolves to a hostname")
	}
	r, err := newRunner()
	if err != nil {
		return err
	}
	return startApps(r, apps)
}

// appStopper is the runner subset Down needs: stop one app container.
type appStopper interface {
	Stop(app string) error
}

// stopApps stops every app container while leaving shared infrastructure
// (Caddy, cloudflared, databases) running. Errors are joined so one failure
// doesn't skip the rest.
func stopApps(r appStopper, apps []generate.App) error {
	var errs []error
	for _, app := range apps {
		if err := r.Stop(app.Name); err != nil {
			errs = append(errs, fmt.Errorf("stop %s: %w", app.Name, err))
		}
	}
	return errors.Join(errs...)
}

// Down stops the app containers only. It deliberately does NOT run the full
// `roost down` (which also stops Caddy + cloudflared): that would kill the
// tunnel route to this very panel, leaving no way to start again from the web.
// The CLI `roost down` is still the way to take everything down.
func (c *stackController) Down() error {
	apps, _, err := loadPlanned(c.cmd, c.flags)
	if err != nil {
		return err
	}
	r, err := newRunner()
	if err != nil {
		return err
	}
	return stopApps(r, apps)
}

// resolveAppName reports nil only if name matches a configured app. It is the
// gate on the per-app panel actions: without it a crafted POST could name an
// infra service (caddy, cloudflared) and the panel would stop the tunnel to
// itself.
func resolveAppName(apps []generate.App, name string) error {
	for _, a := range apps {
		if a.Name == name {
			return nil
		}
	}
	return fmt.Errorf("unknown app %q", name)
}

// StartApp resumes one stopped app container (fast — no rebuild), after
// checking the name resolves to a configured app.
func (c *stackController) StartApp(name string) error {
	apps, _, err := loadPlanned(c.cmd, c.flags)
	if err != nil {
		return err
	}
	if err := resolveAppName(apps, name); err != nil {
		return err
	}
	r, err := newRunner()
	if err != nil {
		return err
	}
	return r.Resume(name)
}

// StopApp stops one app container, leaving shared infrastructure running, after
// checking the name resolves to a configured app.
func (c *stackController) StopApp(name string) error {
	apps, _, err := loadPlanned(c.cmd, c.flags)
	if err != nil {
		return err
	}
	if err := resolveAppName(apps, name); err != nil {
		return err
	}
	r, err := newRunner()
	if err != nil {
		return err
	}
	return r.Stop(name)
}

// AddApp adds an app to the running stack from the panel: a doctor preflight
// gate, then config edit, artifact regen, and build + start of just the new
// container(s). Every phase streams a line to emit so the processing pane shows
// progress. It regenerates and builds only — it does not touch other apps.
//
// Security note: path is an arbitrary host path the panel user typed, so this
// builds and runs whatever Dockerfile lives there. That is by design (the panel
// is a host-side admin tool) and is why the panel must sit behind Cloudflare
// Access and the on/off token — an unauthenticated caller here is code
// execution on the box.
func (c *stackController) AddApp(path, domain string, emit func(string)) error {
	emit("preflight: running roost doctor")
	findings := runDoctor(c.flags, shell.Exec{})
	if doctor.HasFailures(findings) {
		return fmt.Errorf("preflight failed — %s", firstFailureMessage(findings))
	}
	emit("preflight passed")

	before, _, err := loadPlanned(c.cmd, c.flags)
	if err != nil {
		return err
	}
	beforeNames := appNameSet(before)

	cfgPath, err := config.FindConfig(c.flags.configPath)
	if err != nil {
		return err
	}
	emit("adding to config: " + path)
	if err := config.AddApp(cfgPath, path, domain); err != nil {
		return err
	}

	apps, controlHost, err := loadPlanned(c.cmd, c.flags)
	if err != nil {
		return err
	}
	dir, err := buildDir()
	if err != nil {
		return err
	}
	emit("generating artifacts")
	if _, err := generate.Generate(dir, apps, controlHost); err != nil {
		return err
	}

	newNames := diffNewApps(beforeNames, apps)
	if len(newNames) == 0 {
		return fmt.Errorf("added %q to config but no new app resolved to a hostname — set a domain for it", path)
	}
	r := runner.New(dir)
	for _, n := range newNames {
		emit("building + starting " + n)
		if err := r.Start(n); err != nil {
			return err
		}
	}
	c.clearRemoved(newNames)
	emit("done — " + newNames[0] + " is live")
	return nil
}

// RemoveApp tears one app out of the running stack: stop + remove its container
// (optionally its image), drop it from the config, record it for one-click
// re-add, and regenerate artifacts so compose no longer references it. Shared
// database volumes are never touched — an app's data outlives its removal.
func (c *stackController) RemoveApp(name string, deleteImage bool, emit func(string)) error {
	apps, _, err := loadPlanned(c.cmd, c.flags)
	if err != nil {
		return err
	}
	var target *generate.App
	for i := range apps {
		if apps[i].Name == name {
			target = &apps[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("unknown app %q", name)
	}

	r, err := newRunner()
	if err != nil {
		return err
	}
	emit("stopping + removing container: " + name)
	if err := r.Remove(name, deleteImage); err != nil {
		return err
	}
	if deleteImage {
		emit("deleted image roost-" + name)
	}

	cfgPath, err := config.FindConfig(c.flags.configPath)
	if err != nil {
		return err
	}
	emit("removing from config")
	if err := config.RemoveApp(cfgPath, name); err != nil {
		return err
	}

	if st, sp, err := c.loadState(); err == nil {
		st.MarkRemoved(state.RemovedApp{Name: name, Path: target.Path, Domain: target.FQDN})
		if err := st.Save(sp); err != nil {
			emit("warning: could not record for re-add: " + err.Error())
		}
	}

	// Regenerate so the removed app leaves the compose file. Skipped when the
	// config is now empty (nothing to generate) — best-effort either way.
	if apps2, controlHost, lerr := loadPlanned(c.cmd, c.flags); lerr == nil && len(apps2) > 0 {
		if dir, derr := buildDir(); derr == nil {
			emit("regenerating artifacts")
			_, _ = generate.Generate(dir, apps2, controlHost)
		}
	}
	emit("done — moved to the Removed list")
	return nil
}

// RemovedApps returns the apps the panel has removed, for one-click re-add.
func (c *stackController) RemovedApps() ([]web.RemovedApp, error) {
	st, _, err := c.loadState()
	if err != nil {
		return nil, err
	}
	out := make([]web.RemovedApp, 0, len(st.Removed))
	for _, r := range st.Removed {
		out = append(out, web.RemovedApp{Name: r.Name, Path: r.Path, Domain: r.Domain})
	}
	return out, nil
}

// appNameSet indexes app names for membership tests.
func appNameSet(apps []generate.App) map[string]bool {
	m := make(map[string]bool, len(apps))
	for _, a := range apps {
		m[a.Name] = true
	}
	return m
}

// diffNewApps returns the names present in after but not before — the app(s) an
// add introduced.
func diffNewApps(before map[string]bool, after []generate.App) []string {
	var out []string
	for _, a := range after {
		if !before[a.Name] {
			out = append(out, a.Name)
		}
	}
	return out
}

// firstFailureMessage renders the first doctor failure as an actionable line
// (message + remedy) for the panel's processing pane.
func firstFailureMessage(findings []doctor.Finding) string {
	for _, f := range findings {
		if f.Level == doctor.Fail {
			if f.Remedy != "" {
				return f.Message + " (fix: " + f.Remedy + ")"
			}
			return f.Message
		}
	}
	return "see `roost doctor`"
}

// loadState opens roost's state.json under the real home directory.
func (c *stackController) loadState() (*state.State, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, "", err
	}
	p := state.Path(home)
	st, err := state.Load(p)
	return st, p, err
}

// clearRemoved drops re-added apps from the removed list, persisting only when
// something changed. Best-effort: a state write failure must not fail an
// otherwise-successful add.
func (c *stackController) clearRemoved(names []string) {
	st, sp, err := c.loadState()
	if err != nil {
		return
	}
	changed := false
	for _, n := range names {
		before := len(st.Removed)
		st.ClearRemoved(n)
		if len(st.Removed) != before {
			changed = true
		}
	}
	if changed {
		_ = st.Save(sp)
	}
}

// newWebCmd serves the control panel. It is a long-running process, meant to be
// supervised (systemd/launchd) *outside* the stack it controls and fronted by
// Cloudflare Access. Default bind is loopback; expose it only through the
// tunnel, never 0.0.0.0 on an untrusted network.
func newWebCmd(flags *rootFlags) *cobra.Command {
	var addr, token string
	cmd := &cobra.Command{
		Use:   "web",
		Short: "Serve a control panel (stack on/off + status) over HTTP",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if token == "" {
				token = os.Getenv("ROOST_WEB_TOKEN")
			}
			ctrl := &stackController{cmd: cmd, flags: flags}
			srv := &http.Server{
				Addr:              addr,
				Handler:           web.NewServer(ctrl, token).Handler(),
				ReadHeaderTimeout: 10 * time.Second,
			}
			cmd.Printf("roost web listening on %s\n", addr)
			return srv.ListenAndServe()
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:4600", "address to listen on")
	cmd.Flags().StringVar(&token, "token", "", "shared secret required for on/off actions (or $ROOST_WEB_TOKEN)")
	return cmd
}
