package tunnel

import (
	"errors"
	"fmt"

	"github.com/cdrrazan/roost/internal/state"
)

// Teardown deletes every DNS record roost recorded in st and, when
// removeTunnel is set, the tunnel itself — the remote counterpart to
// `tunnel setup`. It only ever touches what state.json says roost made,
// never records or tunnels it didn't create.
//
// st is mutated to drop what was successfully removed; a record that
// fails to delete stays in st and its error is joined into the return,
// so a retry re-attempts only the leftovers. The tunnel is deleted last,
// after its DNS is gone.
func Teardown(client *Client, st *state.State, removeTunnel bool) error {
	var errs []error
	var kept []state.Record
	for _, rec := range st.Records {
		if err := client.DeleteDNS(rec.ZoneID, rec.ID); err != nil {
			errs = append(errs, fmt.Errorf("dns %s: %w", rec.Name, err))
			kept = append(kept, rec)
		}
	}
	st.Records = kept

	if removeTunnel && st.TunnelID != "" {
		if err := client.DeleteTunnel(st.AccountID, st.TunnelID); err != nil {
			errs = append(errs, fmt.Errorf("tunnel %s: %w", st.TunnelName, err))
		} else {
			st.TunnelID = ""
			st.TunnelName = ""
		}
	}
	return errors.Join(errs...)
}
