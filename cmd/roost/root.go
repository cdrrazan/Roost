package main

import (
	"github.com/spf13/cobra"
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
