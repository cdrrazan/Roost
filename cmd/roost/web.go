package main

import (
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

// Up mirrors `roost up`: regenerate artifacts, then bring the stack up. It does
// not run migrations/seed — the panel is on/off for an already-configured
// stack; DB setup is owned by `roost up` on the CLI.
func (c *stackController) Up() error {
	apps, controlHost, err := loadPlanned(c.cmd, c.flags)
	if err != nil {
		return err
	}
	if len(apps) == 0 {
		return fmt.Errorf("nothing to run: no app resolves to a hostname")
	}
	dir, err := buildDir()
	if err != nil {
		return err
	}
	if _, err := generate.Generate(dir, apps, controlHost); err != nil {
		return err
	}
	return runner.New(dir).Up(apps, nil)
}

func (c *stackController) Down() error {
	r, err := newRunner()
	if err != nil {
		return err
	}
	return r.Down()
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
