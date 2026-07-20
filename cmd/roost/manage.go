package main

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/cdrrazan/roost/internal/config"
	"github.com/cdrrazan/roost/internal/lifecycle"
	"github.com/cdrrazan/roost/internal/shell"
)

func newAddCmd(flags *rootFlags) *cobra.Command {
	var domain string
	cmd := &cobra.Command{
		Use:   "add <path>",
		Short: "Append an app path to the config",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgPath, err := config.FindConfig(flags.configPath)
			if err != nil {
				return err
			}
			if err := config.AddApp(cfgPath, args[0], domain); err != nil {
				return err
			}
			cmd.Printf("added %s to %s\n", args[0], cfgPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "explicit FQDN for the app (otherwise the global domain applies)")
	return cmd
}

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

func newEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enable",
		Short: "Run roost up at login (launchd on macOS, systemd --user on Linux)",
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
