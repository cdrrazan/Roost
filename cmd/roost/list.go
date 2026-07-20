package main

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/cdrrazan/roost/internal/config"
	"github.com/cdrrazan/roost/internal/detect"
)

// loadResolved loads the config and resolves every app to a hostname.
func loadResolved(flags *rootFlags) (*config.Config, []config.ResolvedApp, []config.SkippedApp, error) {
	path, err := config.FindConfig(flags.configPath)
	if err != nil {
		return nil, nil, nil, err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, nil, nil, err
	}
	resolved, skipped, err := config.Resolve(cfg)
	return cfg, resolved, skipped, err
}

// newListCmd shows every configured app with its resolved name,
// framework, and public URL — skipped apps appear with their reason,
// never silently dropped.
func newListCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured apps with resolved name, framework, and URL",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, resolved, skipped, err := loadResolved(flags)
			if err != nil {
				return err
			}
			if len(resolved) == 0 && len(skipped) == 0 {
				cmd.Println("no apps configured; run `roost add <path>` to add one")
				return nil
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tFRAMEWORK\tURL\tSTATE")
			for _, app := range resolved {
				framework := app.Framework
				if framework == "" {
					if d, err := detect.Detect(app.Path); err == nil {
						framework = d.Framework
					} else {
						framework = "unknown"
					}
				}
				fmt.Fprintf(w, "%s\t%s\thttps://%s\t\n", app.Name, framework, app.FQDN)
			}
			for _, app := range skipped {
				fmt.Fprintf(w, "%s\t\t\tskipped: %s\n", app.Name, app.Reason)
			}
			return w.Flush()
		},
	}
}
