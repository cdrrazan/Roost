package main

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/cdrrazan/roost/internal/config"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

// rootFlags carries the persistent flags shared by every subcommand.
type rootFlags struct {
	// configPath is the --config override; empty means the standard
	// resolution order (see config.FindConfig).
	configPath string
}

// newRootCmd assembles the full roost command tree.
func newRootCmd() *cobra.Command {
	flags := &rootFlags{}

	root := &cobra.Command{
		Use:   "roost",
		Short: "Run every app in ~/.roost/config.yml, routed and live on HTTPS",
		Long: `roost turns a list of local application folders into live,
HTTPS-accessible apps on your own domain. You supply paths and hostnames;
roost infers everything else.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		// Point Docker at the configured remote daemon before any command
		// runs, so the whole stack lands on a VPS with the same config. An
		// explicit $DOCKER_HOST always wins; config problems are ignored here
		// (commands that need config surface their own errors).
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			applyRemote(flags)
			return nil
		},
	}

	root.PersistentFlags().StringVar(&flags.configPath, "config", "", "path to config file (default: $ROOST_CONFIG, ./roost.yml, then ~/.roost/config.yml)")

	root.AddCommand(
		newVersionCmd(),
		newInitCmd(flags),
		newListCmd(flags),
		newDetectCmd(flags),
		newGenerateCmd(flags),
		newUpCmd(flags),
		newDownCmd(flags),
		newUninstallCmd(flags),
		newShareCmd(flags),
		newStatusCmd(flags),
		newLogsCmd(flags),
		newStartCmd(flags),
		newStopCmd(flags),
		newRestartCmd(flags),
		newAddCmd(flags),
		newRemoveCmd(flags),
		newEnableCmd(),
		newDisableCmd(),
		newAuthCmd(),
		newTunnelCmd(flags),
		newDoctorCmd(flags),
		newWebCmd(flags),
		newDeployCmd(flags),
	)

	return root
}

// applyRemote sets DOCKER_HOST from the config's remote: endpoint so every
// docker subprocess targets the remote daemon. It's best-effort: an explicit
// $DOCKER_HOST wins, and an unreadable/local config is simply left alone.
func applyRemote(flags *rootFlags) {
	if os.Getenv("DOCKER_HOST") != "" {
		return
	}
	path, err := config.FindConfig(flags.configPath)
	if err != nil {
		return
	}
	cfg, err := config.Load(path)
	if err != nil || cfg.Remote == "" {
		return
	}
	_ = os.Setenv("DOCKER_HOST", cfg.Remote)
}

// newVersionCmd prints the build version (stamped by GoReleaser).
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the roost version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.Printf("roost %s\n", version)
			return nil
		},
	}
}
