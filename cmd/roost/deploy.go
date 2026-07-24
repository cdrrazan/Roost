package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cdrrazan/roost/internal/shell"
)

// appStarter rebuilds and restarts one app — *runner.Runner satisfies it.
type appStarter interface {
	Start(app string) error
}

// deployApp pulls the app's repo (fast-forward only) then rebuilds and
// restarts just that app. Fast-forward-only is deliberate: if the box clone
// has diverged from the remote, the deploy fails loudly instead of silently
// merging.
func deployApp(sh shell.Runner, r appStarter, repoPath, name string) error {
	if _, err := sh.Run("git", "-C", repoPath, "pull", "--ff-only"); err != nil {
		return fmt.Errorf("git pull %s: %w", name, err)
	}
	return r.Start(name)
}

// newDeployCmd pulls one app's repo and rebuilds + restarts only that app,
// leaving the rest of the stack running. It is the command CI invokes over SSH
// on a push to the app's production branch. The box clone must already be on
// that branch (roost has no branch field; it pulls whatever is checked out).
func newDeployCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "deploy <app>",
		Short: "Pull one app's repo and rebuild + restart just that app",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			apps, _, err := loadPlanned(cmd, flags)
			if err != nil {
				return err
			}
			var path string
			for _, a := range apps {
				if a.Name == name {
					path = a.Path
					break
				}
			}
			if path == "" {
				return fmt.Errorf("app %q not found in config", name)
			}
			r, err := newRunner()
			if err != nil {
				return err
			}
			cmd.Printf("deploying %s: git pull + rebuild + restart...\n", name)
			if err := deployApp(shell.Exec{}, r, path, name); err != nil {
				return err
			}
			cmd.Printf("deployed %s\n", name)
			return nil
		},
	}
}
