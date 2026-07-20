package tunnel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cdrrazan/roost/internal/state"
)

// fakeCF is a scriptable Cloudflare API for tests. No test in this
// repo ever calls the real network.
type fakeCF struct {
	t        *testing.T
	mux      *http.ServeMux
	server   *httptest.Server
	requests []string // "METHOD /path"
}

func newFakeCF(t *testing.T) *fakeCF {
	t.Helper()
	f := &fakeCF{t: t, mux: http.NewServeMux()}
	logger := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-token" {
			t.Errorf("missing bearer token, got %q", auth)
		}
		f.mux.ServeHTTP(w, r)
	})
	f.server = httptest.NewServer(logger)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeCF) client() *Client {
	return &Client{BaseURL: f.server.URL, Token: "test-token", HTTP: f.server.Client()}
}

// reply writes a successful Cloudflare envelope around result.
func reply(w http.ResponseWriter, result any) {
	data, _ := json.Marshal(result)
	fmt.Fprintf(w, `{"success":true,"errors":[],"result":%s}`, data)
}

func replyError(w http.ResponseWriter, status int, code int, message string) {
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"success":false,"errors":[{"code":%d,"message":%q}],"result":null}`, code, message)
}

func TestClientAccountsAndZones(t *testing.T) {
	f := newFakeCF(t)
	f.mux.HandleFunc("GET /accounts", func(w http.ResponseWriter, r *http.Request) {
		reply(w, []Account{{ID: "acc1", Name: "My Account"}})
	})
	f.mux.HandleFunc("GET /zones", func(w http.ResponseWriter, r *http.Request) {
		reply(w, []Zone{{ID: "z1", Name: "example.com", Status: "active"}})
	})

	c := f.client()
	accounts, err := c.Accounts()
	if err != nil {
		t.Fatalf("Accounts: %v", err)
	}
	if len(accounts) != 1 || accounts[0].ID != "acc1" {
		t.Errorf("accounts = %+v", accounts)
	}
	zones, err := c.Zones()
	if err != nil {
		t.Fatalf("Zones: %v", err)
	}
	if len(zones) != 1 || zones[0].Name != "example.com" {
		t.Errorf("zones = %+v", zones)
	}
}

func TestClientErrorSurfacesAPIMessage(t *testing.T) {
	f := newFakeCF(t)
	f.mux.HandleFunc("GET /accounts", func(w http.ResponseWriter, r *http.Request) {
		replyError(w, http.StatusForbidden, 9109, "Invalid access token")
	})
	_, err := f.client().Accounts()
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "Invalid access token") {
		t.Errorf("error %q should carry the API message", err)
	}
}

func TestEnsureTunnelCreates(t *testing.T) {
	f := newFakeCF(t)
	f.mux.HandleFunc("GET /accounts/acc1/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		reply(w, []Tunnel{})
	})
	f.mux.HandleFunc("POST /accounts/acc1/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["config_src"] != "cloudflare" {
			t.Errorf("create body %v missing config_src cloudflare; the tunnel would be locally-managed", body)
		}
		if body["name"] != "roost" {
			t.Errorf("tunnel name = %q, want roost verbatim (never generated)", body["name"])
		}
		reply(w, Tunnel{ID: "tun-1", Name: "roost", Token: "conn-token"})
	})

	st := &state.State{}
	res, err := EnsureTunnel(f.client(), st, "acc1", "roost", false)
	if err != nil {
		t.Fatalf("EnsureTunnel: %v", err)
	}
	if !res.Created || res.Token != "conn-token" || st.TunnelID != "tun-1" {
		t.Errorf("result = %+v, state = %+v", res, st)
	}
}

func TestEnsureTunnelExistingMatchingState(t *testing.T) {
	f := newFakeCF(t)
	f.mux.HandleFunc("GET /accounts/acc1/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		reply(w, []Tunnel{{ID: "tun-1", Name: "roost"}})
	})
	f.mux.HandleFunc("GET /accounts/acc1/cfd_tunnel/tun-1/token", func(w http.ResponseWriter, r *http.Request) {
		reply(w, "fetched-token")
	})

	st := &state.State{TunnelID: "tun-1"}
	res, err := EnsureTunnel(f.client(), st, "acc1", "roost", false)
	if err != nil {
		t.Fatalf("EnsureTunnel: %v", err)
	}
	if res.Created || res.Token != "fetched-token" {
		t.Errorf("result = %+v", res)
	}
}

func TestEnsureTunnelForeignRefusesWithoutAdopt(t *testing.T) {
	f := newFakeCF(t)
	f.mux.HandleFunc("GET /accounts/acc1/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		reply(w, []Tunnel{{ID: "tun-foreign", Name: "roost"}})
	})
	f.mux.HandleFunc("GET /accounts/acc1/cfd_tunnel/tun-foreign/token", func(w http.ResponseWriter, r *http.Request) {
		reply(w, "tok")
	})

	t.Run("no state refuses and names the tunnel", func(t *testing.T) {
		_, err := EnsureTunnel(f.client(), &state.State{}, "acc1", "roost", false)
		if err == nil {
			t.Fatal("want refusal for a tunnel roost did not create")
		}
		for _, want := range []string{"tun-foreign", "--adopt"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q missing %q", err, want)
			}
		}
	})

	t.Run("adopt takes it over", func(t *testing.T) {
		st := &state.State{}
		res, err := EnsureTunnel(f.client(), st, "acc1", "roost", true)
		if err != nil {
			t.Fatalf("EnsureTunnel --adopt: %v", err)
		}
		if st.TunnelID != "tun-foreign" || res.Token != "tok" {
			t.Errorf("adopt result = %+v state = %+v", res, st)
		}
	})

	t.Run("state pointing at a different tunnel refuses", func(t *testing.T) {
		_, err := EnsureTunnel(f.client(), &state.State{TunnelID: "tun-old"}, "acc1", "roost", false)
		if err == nil {
			t.Fatal("want error when config name resolves to a different tunnel than state records")
		}
		if !strings.Contains(err.Error(), "tun-old") {
			t.Errorf("error %q should mention the state tunnel", err)
		}
	})
}

func TestEnsureRecordStates(t *testing.T) {
	content := "tun-1.cfargotunnel.com"

	serve := func(t *testing.T, existing []DNSRecord) (*fakeCF, *[]string) {
		f := newFakeCF(t)
		var writes []string
		f.mux.HandleFunc("GET /zones/z1/dns_records", func(w http.ResponseWriter, r *http.Request) {
			reply(w, existing)
		})
		f.mux.HandleFunc("POST /zones/z1/dns_records", func(w http.ResponseWriter, r *http.Request) {
			var rec DNSRecord
			_ = json.NewDecoder(r.Body).Decode(&rec)
			if !rec.Proxied {
				t.Error("created record must be proxied; the tunnel cannot serve unproxied records")
			}
			writes = append(writes, "POST")
			rec.ID = "new-id"
			reply(w, rec)
		})
		f.mux.HandleFunc("PATCH /zones/z1/dns_records/rec-1", func(w http.ResponseWriter, r *http.Request) {
			writes = append(writes, "PATCH")
			reply(w, DNSRecord{})
		})
		f.mux.HandleFunc("PUT /zones/z1/dns_records/rec-1", func(w http.ResponseWriter, r *http.Request) {
			writes = append(writes, "PUT")
			reply(w, DNSRecord{})
		})
		return f, &writes
	}

	t.Run("absent record is created", func(t *testing.T) {
		f, writes := serve(t, nil)
		action, rec, err := EnsureRecord(f.client(), "z1", "*.example.com", content, false)
		if err != nil {
			t.Fatal(err)
		}
		if action != RecordCreated || rec.ID != "new-id" || len(*writes) != 1 {
			t.Errorf("action=%v rec=%+v writes=%v", action, rec, *writes)
		}
	})

	t.Run("matching record is a no-op", func(t *testing.T) {
		f, writes := serve(t, []DNSRecord{{ID: "rec-1", Type: "CNAME", Name: "*.example.com", Content: content, Proxied: true}})
		action, _, err := EnsureRecord(f.client(), "z1", "*.example.com", content, false)
		if err != nil {
			t.Fatal(err)
		}
		if action != RecordPresent || len(*writes) != 0 {
			t.Errorf("action=%v writes=%v, want no-op", action, *writes)
		}
	})

	t.Run("foreign record is refused without force", func(t *testing.T) {
		f, writes := serve(t, []DNSRecord{{ID: "rec-1", Type: "A", Name: "*.example.com", Content: "9.9.9.9"}})
		action, _, err := EnsureRecord(f.client(), "z1", "*.example.com", content, false)
		if err == nil {
			t.Fatal("want refusal for a foreign record")
		}
		for _, want := range []string{"*.example.com", "A", "9.9.9.9", "--force"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal %q missing %q", err, want)
			}
		}
		if action != RecordRefused || len(*writes) != 0 {
			t.Errorf("action=%v writes=%v, want refusal with no writes", action, *writes)
		}
	})

	t.Run("force overwrites the foreign record", func(t *testing.T) {
		f, writes := serve(t, []DNSRecord{{ID: "rec-1", Type: "A", Name: "*.example.com", Content: "9.9.9.9"}})
		action, _, err := EnsureRecord(f.client(), "z1", "*.example.com", content, true)
		if err != nil {
			t.Fatal(err)
		}
		if action != RecordOverwritten || strings.Join(*writes, ",") != "PUT" {
			t.Errorf("action=%v writes=%v", action, *writes)
		}
	})

	t.Run("correct but unproxied record gets patched", func(t *testing.T) {
		f, writes := serve(t, []DNSRecord{{ID: "rec-1", Type: "CNAME", Name: "*.example.com", Content: content, Proxied: false}})
		action, _, err := EnsureRecord(f.client(), "z1", "*.example.com", content, false)
		if err != nil {
			t.Fatal(err)
		}
		if action != RecordProxiedFixed || strings.Join(*writes, ",") != "PATCH" {
			t.Errorf("action=%v writes=%v", action, *writes)
		}
	})
}

func TestPutIngress(t *testing.T) {
	f := newFakeCF(t)
	var got map[string]any
	f.mux.HandleFunc("PUT /accounts/acc1/cfd_tunnel/tun-1/configurations", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		reply(w, map[string]any{})
	})

	rules := IngressRules([]PlannedRecord{{Name: "*.demo.example.com", Wildcard: true}})
	if err := f.client().PutIngress("acc1", "tun-1", rules); err != nil {
		t.Fatalf("PutIngress: %v", err)
	}
	cfg, _ := got["config"].(map[string]any)
	ingress, _ := cfg["ingress"].([]any)
	if len(ingress) != 2 {
		t.Fatalf("ingress = %+v", ingress)
	}
	last, _ := ingress[len(ingress)-1].(map[string]any)
	if last["service"] != "http_status:404" {
		t.Errorf("last rule = %v, want the mandatory http_status:404 catch-all", last)
	}
}

func TestEnsureAccess(t *testing.T) {
	f := newFakeCF(t)
	var createdApps, createdPolicies []string
	f.mux.HandleFunc("GET /accounts/acc1/access/apps", func(w http.ResponseWriter, r *http.Request) {
		reply(w, []accessApp{{ID: "app-old", Domain: "*.already.example.com"}})
	})
	f.mux.HandleFunc("POST /accounts/acc1/access/apps", func(w http.ResponseWriter, r *http.Request) {
		var app accessApp
		_ = json.NewDecoder(r.Body).Decode(&app)
		createdApps = append(createdApps, app.Domain)
		app.ID = "app-" + app.Domain
		reply(w, app)
	})
	f.mux.HandleFunc("POST /accounts/acc1/access/apps/{id}/policies", func(w http.ResponseWriter, r *http.Request) {
		var policy map[string]any
		_ = json.NewDecoder(r.Body).Decode(&policy)
		if policy["decision"] != "allow" {
			t.Errorf("policy decision = %v", policy["decision"])
		}
		createdPolicies = append(createdPolicies, r.PathValue("id"))
		reply(w, policy)
	})

	domains := []string{"*.demo.example.com", "trackaru.com", "*.already.example.com"}
	created, err := EnsureAccess(f.client(), "acc1", domains, []string{"me@example.com"})
	if err != nil {
		t.Fatalf("EnsureAccess: %v", err)
	}
	if len(created) != 2 {
		t.Errorf("created = %v, want the two new domains only", created)
	}
	if len(createdApps) != 2 || len(createdPolicies) != 2 {
		t.Errorf("apps=%v policies=%v", createdApps, createdPolicies)
	}
}

func TestLoadToken(t *testing.T) {
	t.Run("env var wins", func(t *testing.T) {
		t.Setenv("CLOUDFLARE_API_TOKEN", "env-token")
		token, err := LoadToken(t.TempDir())
		if err != nil || token != "env-token" {
			t.Errorf("token=%q err=%v", token, err)
		}
	})

	t.Run("credentials file with 0600", func(t *testing.T) {
		t.Setenv("CLOUDFLARE_API_TOKEN", "")
		home := t.TempDir()
		if _, err := SaveToken(home, "file-token"); err != nil {
			t.Fatal(err)
		}
		token, err := LoadToken(home)
		if err != nil || token != "file-token" {
			t.Errorf("token=%q err=%v", token, err)
		}
	})

	t.Run("loose permissions are refused", func(t *testing.T) {
		t.Setenv("CLOUDFLARE_API_TOKEN", "")
		home := t.TempDir()
		path := filepath.Join(home, ".roost", "credentials")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("leaky"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := LoadToken(home)
		if err == nil || !strings.Contains(err.Error(), "0600") {
			t.Errorf("err = %v, want a mode complaint", err)
		}
	})

	t.Run("missing everything points at auth login", func(t *testing.T) {
		t.Setenv("CLOUDFLARE_API_TOKEN", "")
		_, err := LoadToken(t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "auth login") {
			t.Errorf("err = %v", err)
		}
	})
}
