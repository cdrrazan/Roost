package main

import (
	"errors"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/cdrrazan/roost/internal/detect"
)

// newDetectCmd is the dry-run explainability table: for each app, the
// detected framework, the exact signal that triggered the detection,
// the port, and the database. Detection failures surface as an error
// telling the user to set framework: explicitly — never a silent guess.
func newDetectCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "detect",
		Short: "Dry-run framework detection: app → framework → signal → port → database",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, resolved, skipped, err := loadResolved(flags)
			if err != nil {
				return err
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "APP\tFRAMEWORK\tSIGNAL\tPORT\tDATABASE")
			var failures []error
			for _, app := range resolved {
				if app.Framework != "" {
					fmt.Fprintf(w, "%s\t%s\tset explicitly in config\t%d\t%s\n", app.Name, app.Framework, app.Port, app.Database)
					continue
				}
				d, err := detect.Detect(app.Path)
				if err != nil {
					failures = append(failures, err)
					fmt.Fprintf(w, "%s\tunknown\tdetection failed\t\t\n", app.Name)
					continue
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n", app.Name, d.Framework, d.Signal, d.Port, d.Database)
			}
			for _, app := range skipped {
				fmt.Fprintf(w, "%s\tskipped: %s\t\t\t\n", app.Name, app.Reason)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			return errors.Join(failures...)
		},
	}
}
