package main

import (
	"github.com/spf13/cobra"

	"github.com/cdrrazan/roost/internal/tunnel"
)

// newUninstallCmd tears the stack down and removes the remote side roost
// created: the DNS records and the tunnel recorded in state.json. It only
// deletes what roost made — foreign records and tunnels are never touched.
// Local build artifacts and config are left in place, so a later `roost up`
// + `roost tunnel setup` recreates everything.
func newUninstallCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Stop the stack and delete the DNS records and tunnel roost created",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Best-effort stack stop: it may already be down, which is fine.
			if r, err := newRunner(); err == nil {
				_ = r.Down()
			}
			client, st, statePath, err := loadRemoteState()
			if err != nil {
				return err
			}
			dnsBefore := len(st.Records)
			hadTunnel := st.TunnelID != ""
			tErr := tunnel.Teardown(client, st, true)
			if err := st.Save(statePath); err != nil {
				return err
			}
			cmd.Printf("removed %d DNS record(s)", dnsBefore-len(st.Records))
			if hadTunnel && st.TunnelID == "" {
				cmd.Print(" and the tunnel")
			}
			cmd.Println()
			return tErr
		},
	}
}
