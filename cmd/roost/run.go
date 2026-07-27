package main

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/cdrrazan/roost/internal/generate"
	"github.com/cdrrazan/roost/internal/runner"
	"github.com/cdrrazan/roost/internal/state"
)

// loadPlanned loads config, resolves hostnames, and plans generation,
// printing skip notices as it goes. It also returns the configured control
// host (empty when unset) so callers that regenerate artifacts can route the
// web panel.
func loadPlanned(cmd *cobra.Command, flags *rootFlags) ([]generate.App, string, error) {
	cfg, resolved, skipped, err := loadResolved(flags)
	if err != nil {
		return nil, "", err
	}
	for _, app := range skipped {
		cmd.Printf("skipped: %s (%s)\n", app.Name, app.Reason)
	}
	if len(resolved) == 0 {
		return nil, cfg.ControlHost, nil
	}
	apps, err := generate.Plan(cfg, resolved)
	return apps, cfg.ControlHost, err
}

// newRunner builds the real-shell compose runner against the standard
// build directory.
func newRunner() (*runner.Runner, error) {
	dir, err := buildDir()
	if err != nil {
		return nil, err
	}
	return runner.New(dir), nil
}

// newUpCmd is the one-liner the tool exists for: regenerate artifacts,
// start shared infrastructure, then each app with a staggered pause.
// Boot-on-login and crash recovery come from Docker's restart policy,
// not a roost daemon.
func newUpCmd(flags *rootFlags) *cobra.Command {
	var profiles []string
	var reseed bool
	var noSeed bool
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Generate artifacts and start every app, routed and live",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			apps, controlHost, err := loadPlanned(cmd, flags)
			if err != nil {
				return err
			}
			if len(apps) == 0 {
				cmd.Println("nothing to run: no app resolves to a hostname")
				return nil
			}
			dir, err := buildDir()
			if err != nil {
				return err
			}
			if _, err := generate.Generate(dir, apps, controlHost); err != nil {
				return err
			}
			r := runner.New(dir)
			cmd.Println("starting stack (first runs build images and can take several minutes)...")
			if err := r.Up(apps, profiles); err != nil {
				return err
			}
			for _, app := range apps {
				if !runner.AppSelected(app, profiles) {
					continue
				}
				if app.FQDN == "" {
					cmd.Printf("up: %s → worker\n", app.Name)
				} else {
					cmd.Printf("up: %s → https://%s\n", app.Name, app.FQDN)
				}
			}
			if err := prepareApps(cmd, r, apps, profiles, reseed, noSeed); err != nil {
				return err
			}
			if _, err := os.Stat(filepath.Join(dir, ".env")); err != nil {
				cmd.Println("note: no tunnel connector token found — run `roost tunnel setup` to create the tunnel and DNS records")
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&profiles, "profile", nil, "only start apps in these profiles (unprofiled apps always start)")
	cmd.Flags().BoolVar(&reseed, "reseed", false, "re-run seeds for apps already seeded (default: seed each app once)")
	cmd.Flags().BoolVar(&noSeed, "no-seed", false, "skip all seeding this run (migrations still run); useful for a clean start without demo data")
	cmd.MarkFlagsMutuallyExclusive("reseed", "no-seed")
	return cmd
}

// seedDecision returns the predicate prepareApps uses to decide whether an app
// should be seeded. --no-seed suppresses seeding entirely (it wins over
// --reseed, which the CLI also rejects as mutually exclusive); --reseed forces
// a re-seed; otherwise an app is seeded only if state has no record of it.
func seedDecision(noSeed, reseed bool, st *state.State) func(string) bool {
	if noSeed {
		return func(string) bool { return false }
	}
	return func(name string) bool { return reseed || !st.HasSeeded(name) }
}

// mysqlVolumeRecreatedNotice is the line printed when roost detects the MySQL
// data volume was recreated (Clean/Purge, volume rm, fresh machine) and clears
// its seeded set. Under --no-seed no re-seed follows, so the message must not
// promise one.
func mysqlVolumeRecreatedNotice(noSeed bool) string {
	if noSeed {
		return "mysql data volume was recreated — seeding skipped (--no-seed)"
	}
	return "mysql data volume was recreated — re-seeding all apps"
}

// prepareApps runs each app's idempotent DB setup and, for seed-enabled
// apps, its seed command — once per app unless --reseed is passed. Seeded
// apps are recorded in state.json so later ups skip them. A missing state
// file is fine (first run); seeding failures are surfaced but do not tear
// down the already-running stack.
func prepareApps(cmd *cobra.Command, r *runner.Runner, apps []generate.App, profiles []string, reseed, noSeed bool) error {
	needsPrepare := false
	for _, app := range apps {
		if app.SetupCommand != "" || app.SeedCommand != "" {
			needsPrepare = true
			break
		}
	}
	if !needsPrepare {
		return nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	statePath := state.Path(home)
	st, err := state.Load(statePath)
	if err != nil {
		return err
	}

	// If the MySQL data volume was recreated since we last seeded (a Docker
	// Desktop Clean/Purge, `docker volume rm`, or a fresh machine), the
	// recorded "seeded" set points at a database that no longer exists.
	// Detect the drift and clear the set so every app re-seeds against the
	// now-empty volume. An unknown identity (docker could not report it) is
	// left alone rather than risking a spurious wipe.
	if vid, verr := r.MysqlVolumeID(); verr == nil && vid != "" {
		if st.SyncMysqlVolume(vid) {
			cmd.Println(mysqlVolumeRecreatedNotice(noSeed))
		}
		if err := st.Save(statePath); err != nil {
			return err
		}
	}

	shouldSeed := seedDecision(noSeed, reseed, st)
	onSeeded := func(name string) error {
		cmd.Printf("seeded: %s\n", name)
		st.MarkSeeded(name)
		return st.Save(statePath)
	}
	cmd.Println("preparing databases (migrate + seed)...")
	return r.Prepare(apps, profiles, shouldSeed, onSeeded)
}

// newDownCmd stops and removes the whole stack (containers only; DNS
// and the tunnel stay for the next up).
func newDownCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "down",
		Short: "Stop and remove the whole roost stack",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r, err := newRunner()
			if err != nil {
				return err
			}
			return r.Down()
		},
	}
}

// newStatusCmd reports per-app container state, health, memory used
// against the cap, and the public URL.
func newStatusCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Per-app state, health, memory, and public URL",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			apps, _, err := loadPlanned(cmd, flags)
			if err != nil {
				return err
			}
			if len(apps) == 0 {
				cmd.Println("no apps to report")
				return nil
			}
			r, err := newRunner()
			if err != nil {
				return err
			}
			statuses, err := r.Status(apps)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "APP\tSTATE\tHEALTH\tMEMORY\tURL")
			for _, s := range statuses {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", s.Name, s.State, s.Health, s.Memory, s.URL)
			}
			return w.Flush()
		},
	}
}

// newLogsCmd streams container logs, optionally following. With an app it
// tails that one; with no app it multiplexes every app's logs.
func newLogsCmd(flags *rootFlags) *cobra.Command {
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs [app]",
		Short: "Show container logs (all apps if none named)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := newRunner()
			if err != nil {
				return err
			}
			app := ""
			if len(args) == 1 {
				app = args[0]
			}
			return r.Logs(app, follow)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow log output")
	return cmd
}

// newStartCmd builds (if needed) and starts a single app's container,
// for bringing one app up without touching the rest of the stack.
func newStartCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "start <app>",
		Short: "Build and start one app's container",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := newRunner()
			if err != nil {
				return err
			}
			if err := r.Start(args[0]); err != nil {
				return err
			}
			cmd.Printf("started %s\n", args[0])
			return nil
		},
	}
}

// newStopCmd stops a single app's container, leaving it in place to
// start again quickly.
func newStopCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "stop <app>",
		Short: "Stop one app's container (without removing it)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := newRunner()
			if err != nil {
				return err
			}
			if err := r.Stop(args[0]); err != nil {
				return err
			}
			cmd.Printf("stopped %s\n", args[0])
			return nil
		},
	}
}

// newRestartCmd restarts a single app's container.
func newRestartCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "restart <app>",
		Short: "Restart one app's container",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := newRunner()
			if err != nil {
				return err
			}
			return r.Restart(args[0])
		},
	}
}
