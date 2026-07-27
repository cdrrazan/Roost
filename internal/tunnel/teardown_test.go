package tunnel

import (
	"net/http"
	"strings"
	"testing"

	"github.com/cdrrazan/roost/internal/state"
)

func TestTeardownDeletesRecordsAndTunnel(t *testing.T) {
	f := newFakeCF(t)
	f.mux.HandleFunc("DELETE /zones/z1/dns_records/rec-a", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]string{"id": "rec-a"})
	})
	f.mux.HandleFunc("DELETE /zones/z1/dns_records/rec-b", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]string{"id": "rec-b"})
	})
	f.mux.HandleFunc("DELETE /accounts/acc1/cfd_tunnel/tun-1", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]string{"id": "tun-1"})
	})

	st := &state.State{
		AccountID:  "acc1",
		TunnelID:   "tun-1",
		TunnelName: "roost",
		Records: []state.Record{
			{ID: "rec-a", ZoneID: "z1", Name: "*.a.example.com"},
			{ID: "rec-b", ZoneID: "z1", Name: "*.b.example.com"},
		},
	}
	if err := Teardown(f.client(), st, true); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if len(st.Records) != 0 {
		t.Errorf("records = %+v, want all removed", st.Records)
	}
	if st.TunnelID != "" || st.TunnelName != "" {
		t.Errorf("tunnel not cleared: %+v", st)
	}
	joined := strings.Join(f.requests, "\n")
	for _, want := range []string{
		"DELETE /zones/z1/dns_records/rec-a",
		"DELETE /zones/z1/dns_records/rec-b",
		"DELETE /accounts/acc1/cfd_tunnel/tun-1",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing request %q in:\n%s", want, joined)
		}
	}
}

func TestTeardownKeepsFailedRecordsAndSkipsTunnel(t *testing.T) {
	f := newFakeCF(t)
	f.mux.HandleFunc("DELETE /zones/z1/dns_records/ok", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]string{"id": "ok"})
	})
	f.mux.HandleFunc("DELETE /zones/z1/dns_records/boom", func(w http.ResponseWriter, r *http.Request) {
		replyError(w, http.StatusInternalServerError, 1000, "kaboom")
	})

	st := &state.State{
		AccountID:  "acc1",
		TunnelID:   "tun-1",
		TunnelName: "roost",
		Records: []state.Record{
			{ID: "ok", ZoneID: "z1", Name: "*.ok.example.com"},
			{ID: "boom", ZoneID: "z1", Name: "*.boom.example.com"},
		},
	}
	// removeTunnel=false: the tunnel must be left intact.
	err := Teardown(f.client(), st, false)
	if err == nil {
		t.Fatal("want error naming the failed record")
	}
	if !strings.Contains(err.Error(), "boom.example.com") {
		t.Errorf("error %q should name the failed record", err)
	}
	if len(st.Records) != 1 || st.Records[0].ID != "boom" {
		t.Errorf("records = %+v, want only the failed one kept", st.Records)
	}
	if st.TunnelID != "tun-1" {
		t.Errorf("tunnel must survive removeTunnel=false, got %+v", st)
	}
}
