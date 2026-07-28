package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/cdrrazan/roost/internal/config"
	"github.com/cdrrazan/roost/internal/lifecycle"
	"github.com/cdrrazan/roost/internal/shell"
	"github.com/cdrrazan/roost/internal/source"
)

// newAddCmd is the everyday command: append an app to the config
// (comments preserved). Two forms:
//
//	roost add <path>            reference a folder already on disk
//	roost add --repo <url>      clone a git repo into ~/.roost/sources/<name>
//
// With --domain the entry gets an explicit FQDN; without, the global
// domain rule applies. --name overrides the name derived from the repo.
func newAddCmd(flags *rootFlags) *cobra.Command {
	var domain, repo, name string
	cmd := &cobra.Command{
		Use:   "add [path]",
		Short: "Append an app to the config (a local path or a git repo to clone)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgPath, err := config.FindConfig(flags.configPath)
			if err != nil {
				return err
			}

			// A local path and a repo are mutually exclusive.
			if repo != "" && len(args) > 0 {
				return fmt.Errorf("give either a <path> or --repo, not both")
			}
			if repo == "" && len(args) == 0 {
				return fmt.Errorf("give a <path> to an existing folder, or --repo <url> to clone")
			}

			if repo == "" {
				if err := config.AddApp(cfgPath, args[0], domain, ""); err != nil {
					return err
				}
				cmd.Printf("added %s to %s\n", args[0], cfgPath)
				return nil
			}

			if name == "" {
				name = source.NameFromRepo(repo)
			}
			dest, err := source.PathFor(name)
			if err != nil {
				return err
			}
			cmd.Printf("cloning %s → %s\n", repo, dest)
			if err := source.Clone(shell.Exec{}, repo, dest); err != nil {
				return err
			}
			if err := config.AddApp(cfgPath, dest, domain, repo); err != nil {
				return err
			}
			cmd.Printf("added %s (%s) to %s\n", name, repo, cfgPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "explicit FQDN for the app (otherwise the global domain applies)")
	cmd.Flags().StringVar(&repo, "repo", "", "git URL to clone into ~/.roost/sources/<name>")
	cmd.Flags().StringVar(&name, "name", "", "app name (default: derived from the repo URL)")
	return cmd
}

// newRemoveCmd deletes an app from the config by resolved name,
// erroring with the list of known apps on a miss.
func newRemoveCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove an app from the config",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgPath, err := config.FindConfig(flags.configPath)
			if err != nil {
				return err
			}
			if err := config.RemoveApp(cfgPath, args[0]); err != nil {
				return err
			}
			cmd.Printf("removed %s from %s\n", args[0], cfgPath)
			return nil
		},
	}
}

// newLifecycleManager wires the platform unit installer to the real
// home directory, resolved roost binary path, and real shell.
func newLifecycleManager() (*lifecycle.Manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	execPath, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(execPath); err == nil {
		execPath = resolved
	}
	return &lifecycle.Manager{
		GOOS:  runtime.GOOS,
		Home:  home,
		Exec:  execPath,
		Shell: shell.Exec{},
	}, nil
}

// newEnableCmd installs the boot-on-login unit (launchd on macOS,
// systemd --user on Linux; manual instructions elsewhere).
func newEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enable",
		Short: "Run roost up at login (launchd on macOS, systemd --user on Linux, Task Scheduler on Windows)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := newLifecycleManager()
			if err != nil {
				return err
			}
			path, notes, err := m.Enable()
			if err != nil {
				return err
			}
			if path != "" {
				cmd.Printf("installed %s\n", path)
			}
			for _, n := range notes {
				cmd.Println(n)
			}
			return nil
		},
	}
}

// newDisableCmd removes the boot-on-login unit with no leftovers.
func newDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable",
		Short: "Remove the boot-on-login unit",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := newLifecycleManager()
			if err != nil {
				return err
			}
			return m.Disable()
		},
	}
}
