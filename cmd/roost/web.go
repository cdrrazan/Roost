package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/cdrrazan/roost/internal/generate"
	"github.com/cdrrazan/roost/internal/runner"
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
