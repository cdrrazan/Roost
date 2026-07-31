package tunnel

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/cdrrazan/roost/internal/state"
)

func TestBuildWorkerScriptEmbedsPageSafely(t *testing.T) {
	// A page with characters that would break a naive JS literal.
	page := []byte(`<h1>down "now" ` + "`backtick`" + ` </script></h1>`)
	script := BuildWorkerScript(page)
	if !strings.Contains(script, "addEventListener") {
		t.Fatalf("script missing worker body:\n%s", script)
	}
	// The embedded literal must be valid JSON (hence a valid JS string),
	// so the raw backtick/quote/closing-script don't terminate it early.
	start := strings.Index(script, "const PAGE = ")
	if start < 0 {
		t.Fatal("no PAGE literal")
	}
	rest := script[start+len("const PAGE = "):]
	end := strings.Index(rest, ";\n")
	if end < 0 {
		t.Fatal("PAGE literal not terminated")
	}
	var decoded string
	if err := json.Unmarshal([]byte(rest[:end]), &decoded); err != nil {
		t.Fatalf("PAGE is not a valid string literal: %v", err)
	}
	if decoded != string(page) {
		t.Errorf("round-trip mismatch:\n got %q\nwant %q", decoded, page)
	}
}

func TestEnsureWorkerUploadsAndRoutes(t *testing.T) {
	f := newFakeCF(t)
	var uploaded string
	f.mux.HandleFunc("PUT /accounts/acc1/workers/scripts/roost-maintenance", func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/javascript" {
			t.Errorf("script upload content-type = %q, want application/javascript", ct)
		}
		b, _ := io.ReadAll(r.Body)
		uploaded = string(b)
		reply(w, map[string]any{"id": "roost-maintenance"})
	})
	// No existing routes → a create happens.
	f.mux.HandleFunc("GET /zones/z1/workers/routes", func(w http.ResponseWriter, r *http.Request) {
		reply(w, []WorkerRoute{})
	})
	f.mux.HandleFunc("POST /zones/z1/workers/routes", func(w http.ResponseWriter, r *http.Request) {
		var got map[string]string
		_ = json.NewDecoder(r.Body).Decode(&got)
		if got["pattern"] != "*.example.com/*" || got["script"] != "roost-maintenance" {
			t.Errorf("route create body = %+v", got)
		}
		reply(w, map[string]string{"id": "route-1"})
	})

	worker, err := EnsureWorker(f.client(), "acc1",
		[]byte("<html>offline</html>"),
		[]WorkerRouteSpec{{ZoneID: "z1", Pattern: "*.example.com/*"}})
	if err != nil {
		t.Fatalf("EnsureWorker: %v", err)
	}
	if !strings.Contains(uploaded, "addEventListener") {
		t.Errorf("uploaded script missing worker body")
	}
	if worker.ScriptName != "roost-maintenance" {
		t.Errorf("script name = %q", worker.ScriptName)
	}
	if len(worker.Routes) != 1 || worker.Routes[0].ID != "route-1" || worker.Routes[0].ZoneID != "z1" {
		t.Errorf("routes = %+v", worker.Routes)
	}
}

func TestEnsureWorkerRepointsExistingRoute(t *testing.T) {
	f := newFakeCF(t)
	f.mux.HandleFunc("PUT /accounts/acc1/workers/scripts/roost-maintenance", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]any{"id": "roost-maintenance"})
	})
	// A route with our pattern already exists but points at another script:
	// EnsureWorker must UPDATE it, never POST a duplicate (CF rejects dupes).
	f.mux.HandleFunc("GET /zones/z1/workers/routes", func(w http.ResponseWriter, r *http.Request) {
		reply(w, []WorkerRoute{{ID: "old", Pattern: "*.example.com/*", Script: "someone-else"}})
	})
	updated := false
	f.mux.HandleFunc("PUT /zones/z1/workers/routes/old", func(w http.ResponseWriter, r *http.Request) {
		updated = true
		reply(w, map[string]string{"id": "old"})
	})
	f.mux.HandleFunc("POST /zones/z1/workers/routes", func(w http.ResponseWriter, r *http.Request) {
		t.Error("must not POST a duplicate route")
		replyError(w, http.StatusBadRequest, 10020, "duplicate route")
	})

	worker, err := EnsureWorker(f.client(), "acc1", []byte("x"),
		[]WorkerRouteSpec{{ZoneID: "z1", Pattern: "*.example.com/*"}})
	if err != nil {
		t.Fatalf("EnsureWorker: %v", err)
	}
	if !updated {
		t.Error("existing route was not repointed")
	}
	if len(worker.Routes) != 1 || worker.Routes[0].ID != "old" {
		t.Errorf("routes = %+v", worker.Routes)
	}
}

func TestTeardownWorkerRemovesRoutesThenScript(t *testing.T) {
	f := newFakeCF(t)
	f.mux.HandleFunc("DELETE /zones/z1/workers/routes/route-1", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]string{"id": "route-1"})
	})
	f.mux.HandleFunc("DELETE /accounts/acc1/workers/scripts/roost-maintenance", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]string{"id": "roost-maintenance"})
	})

	st := &state.State{
		AccountID: "acc1",
		Worker: &state.Worker{
			ScriptName: "roost-maintenance",
			Routes:     []state.WorkerRoute{{ID: "route-1", ZoneID: "z1", Pattern: "*.example.com/*"}},
		},
	}
	if err := TeardownWorker(f.client(), st); err != nil {
		t.Fatalf("TeardownWorker: %v", err)
	}
	if st.Worker != nil {
		t.Errorf("worker not cleared: %+v", st.Worker)
	}
}

func TestTeardownWorkerKeepsScriptWhenRouteFails(t *testing.T) {
	f := newFakeCF(t)
	f.mux.HandleFunc("DELETE /zones/z1/workers/routes/route-1", func(w http.ResponseWriter, r *http.Request) {
		replyError(w, http.StatusInternalServerError, 1000, "boom")
	})
	f.mux.HandleFunc("DELETE /accounts/acc1/workers/scripts/roost-maintenance", func(w http.ResponseWriter, r *http.Request) {
		t.Error("must not delete the script while a route survives")
		reply(w, map[string]string{})
	})

	st := &state.State{
		AccountID: "acc1",
		Worker: &state.Worker{
			ScriptName: "roost-maintenance",
			Routes:     []state.WorkerRoute{{ID: "route-1", ZoneID: "z1", Pattern: "*.example.com/*"}},
		},
	}
	err := TeardownWorker(f.client(), st)
	if err == nil {
		t.Fatal("want error naming the failed route")
	}
	if st.Worker == nil || len(st.Worker.Routes) != 1 {
		t.Errorf("failed route must be kept for retry: %+v", st.Worker)
	}
}
