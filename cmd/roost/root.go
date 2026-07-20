package main

import (
	"github.com/spf13/cobra"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

type rootFlags struct {
	configPath string
}

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
		newStatusCmd(flags),
		newLogsCmd(flags),
		newRestartCmd(flags),
		newAddCmd(flags),
		newRemoveCmd(flags),
		newEnableCmd(),
		newDisableCmd(),
		newAuthCmd(),
		newTunnelCmd(flags),
		newDoctorCmd(flags),
	)

	return root
}

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
