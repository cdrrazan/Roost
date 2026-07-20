package main

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/cdrrazan/roost/internal/generate"
	"github.com/cdrrazan/roost/internal/runner"
)

// loadPlanned loads config, resolves hostnames, and plans generation,
// printing skip notices as it goes.
func loadPlanned(cmd *cobra.Command, flags *rootFlags) ([]generate.App, error) {
	cfg, resolved, skipped, err := loadResolved(flags)
	if err != nil {
		return nil, err
	}
	for _, app := range skipped {
		cmd.Printf("skipped: %s (%s)\n", app.Name, app.Reason)
	}
	if len(resolved) == 0 {
		return nil, nil
	}
	return generate.Plan(cfg, resolved)
}

func newRunner() (*runner.Runner, error) {
	dir, err := buildDir()
	if err != nil {
		return nil, err
	}
	return runner.New(dir), nil
}

func newUpCmd(flags *rootFlags) *cobra.Command {
	var profiles []string
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Generate artifacts and start every app, routed and live",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			apps, err := loadPlanned(cmd, flags)
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
			if _, err := generate.Generate(dir, apps); err != nil {
				return err
			}
			r := runner.New(dir)
			if err := r.Up(apps, profiles); err != nil {
				return err
			}
			for _, app := range apps {
				if runner.AppSelected(app, profiles) {
					cmd.Printf("up: %s → https://%s\n", app.Name, app.FQDN)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&profiles, "profile", nil, "only start apps in these profiles (unprofiled apps always start)")
	return cmd
}

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

func newStatusCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Per-app state, health, memory, and public URL",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			apps, err := loadPlanned(cmd, flags)
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

func newLogsCmd(flags *rootFlags) *cobra.Command {
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs <app>",
		Short: "Show an app's container logs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := newRunner()
			if err != nil {
				return err
			}
			return r.Logs(args[0], follow)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow log output")
	return cmd
}

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
