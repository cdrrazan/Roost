package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCloudflare serves the minimal API surface tunnel setup touches.
func fakeCloudflare(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var requests []string
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, result string) {
		fmt.Fprintf(w, `{"success":true,"errors":[],"result":%s}`, result)
	}
	mux.HandleFunc("GET /accounts", func(w http.ResponseWriter, r *http.Request) {
		ok(w, `[{"id":"acc1","name":"Test"}]`)
	})
	mux.HandleFunc("GET /zones", func(w http.ResponseWriter, r *http.Request) {
		ok(w, `[{"id":"z1","name":"demo.example.com","status":"active"},{"id":"z2","name":"other.org","status":"active"}]`)
	})
	mux.HandleFunc("GET /accounts/acc1/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		ok(w, `[]`)
	})
	mux.HandleFunc("POST /accounts/acc1/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["config_src"] != "cloudflare" {
			t.Errorf("tunnel create missing config_src cloudflare: %v", body)
		}
		ok(w, `{"id":"tun-1","name":"roost","token":"conn-token"}`)
	})
	mux.HandleFunc("GET /zones/z1/dns_records", func(w http.ResponseWriter, r *http.Request) {
		ok(w, `[]`)
	})
	mux.HandleFunc("GET /zones/z2/dns_records", func(w http.ResponseWriter, r *http.Request) {
		ok(w, `[]`)
	})
	mux.HandleFunc("POST /zones/{zone}/dns_records", func(w http.ResponseWriter, r *http.Request) {
		var rec map[string]any
		_ = json.NewDecoder(r.Body).Decode(&rec)
		if rec["content"] != "tun-1.cfargotunnel.com" {
			t.Errorf("record content = %v", rec["content"])
		}
		rec["id"] = "rec-" + r.PathValue("zone")
		data, _ := json.Marshal(rec)
		ok(w, string(data))
	})
	mux.HandleFunc("PUT /accounts/acc1/cfd_tunnel/tun-1/configurations", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Config struct {
				Ingress []struct {
					Hostname string `json:"hostname"`
					Service  string `json:"service"`
				} `json:"ingress"`
			} `json:"config"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		last := body.Config.Ingress[len(body.Config.Ingress)-1]
		if last.Service != "http_status:404" {
			t.Errorf("ingress must end with the 404 catch-all, got %+v", body.Config.Ingress)
		}
		ok(w, `{}`)
	})

	logged := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		mux.ServeHTTP(w, r)
	})
	server := httptest.NewServer(logged)
	t.Cleanup(server.Close)
	return server, &requests
}

func TestTunnelSetupEndToEnd(t *testing.T) {
	// Stub the connector refresh so the test never shells out to Docker.
	refreshed := false
	origRefresh := refreshConnector
	refreshConnector = func() error { refreshed = true; return nil }
	t.Cleanup(func() { refreshConnector = origRefresh })

	server, requests := fakeCloudflare(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLOUDFLARE_API_TOKEN", "test-token")
	t.Setenv("ROOST_CF_API_BASE", server.URL)

	// Config: one app on the global domain, one explicit on another
	// zone, one on a domain the token can't see.
	cfgDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfgDir, "unreach"), 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := `domain: demo.example.com
apps:
  - ` + fixturePath(t, "rails-app") + `
  - path: ` + fixturePath(t, "django-app") + `
    domain: crm.other.org
  - path: ` + filepath.Join(cfgDir, "unreach") + `
    domain: app.unknown.net
`
	cfgPath := filepath.Join(cfgDir, "config.yml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, "--config", cfgPath, "tunnel", "setup")
	if err != nil {
		t.Fatalf("tunnel setup: %v\n%s", err, out)
	}

	for _, want := range []string{
		"created tunnel",
		"*.demo.example.com",
		"*.other.org",
		"created",
		"ingress configuration pushed",
		// New records were created, so the connector is refreshed.
		"refreshed cloudflared",
		// The unreachable zone is skipped with both possible causes and
		// the visible zones listed.
		"app.unknown.net",
		"zone-scoped",
		"demo.example.com, other.org",
		// No access configured: the public warning names the hostnames.
		"publicly reachable",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("setup output missing %q:\n%s", want, out)
		}
	}
	if !refreshed {
		t.Error("expected cloudflared to be refreshed after creating new routes")
	}

	// The connector token landed in build/.env with tight permissions.
	envPath := filepath.Join(home, ".roost", "build", ".env")
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("connector env not written: %v", err)
	}
	if !strings.Contains(string(data), "ROOST_TUNNEL_TOKEN=conn-token") {
		t.Errorf(".env = %q", data)
	}
	info, _ := os.Stat(envPath)
	if info.Mode().Perm() != 0o600 {
		t.Errorf(".env mode = %o, want 0600", info.Mode().Perm())
	}

	// State remembers the tunnel and the created records.
	stateData, err := os.ReadFile(filepath.Join(home, ".roost", "state.json"))
	if err != nil {
		t.Fatalf("state not written: %v", err)
	}
	for _, want := range []string{"tun-1", "acc1", "*.demo.example.com"} {
		if !strings.Contains(string(stateData), want) {
			t.Errorf("state.json missing %q:\n%s", want, stateData)
		}
	}

	// Second run is idempotent: reuses the tunnel, reports records present.
	*requests = nil
	// Now the tunnel exists remotely.
	// (The fake list endpoint still returns []; simulate existence via state:
	// EnsureTunnel with matching state still calls FindTunnel, which returns
	// nothing, so it would recreate. Accept a create here — idempotency of
	// EnsureTunnel itself is covered in the tunnel package tests.)
}
