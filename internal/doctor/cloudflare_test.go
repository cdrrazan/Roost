package doctor

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cdrrazan/roost/internal/shell"
	"github.com/cdrrazan/roost/internal/state"
	"github.com/cdrrazan/roost/internal/tunnel"
)

// fakeAPI builds a Cloudflare client against a scriptable test server.
func fakeAPI(t *testing.T, mux *http.ServeMux) *tunnel.Client {
	t.Helper()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return &tunnel.Client{BaseURL: server.URL, Token: "tok", HTTP: server.Client()}
}

func okJSON(w http.ResponseWriter, result string) {
	fmt.Fprintf(w, `{"success":true,"errors":[],"result":%s}`, result)
}

// baseMux serves one account and one active zone.
func baseMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /accounts", func(w http.ResponseWriter, r *http.Request) {
		okJSON(w, `[{"id":"acc1","name":"Test"}]`)
	})
	mux.HandleFunc("GET /zones", func(w http.ResponseWriter, r *http.Request) {
		okJSON(w, `[{"id":"z1","name":"example.com","status":"active"}]`)
	})
	return mux
}

// findingsByCheck indexes findings, keeping the worst level per check
// prefix irrelevant — direct map by Check.
func findingsByCheck(fs []Finding) map[string]Finding {
	m := map[string]Finding{}
	for _, f := range fs {
		m[f.Check] = f
	}
	return m
}

func TestCheckCloudflareInvalidToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /accounts", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, `{"success":false,"errors":[{"code":9109,"message":"Invalid access token"}],"result":null}`)
	})
	fs := CheckCloudflare(fakeAPI(t, mux), &state.State{}, "roost", []string{"app.example.com"})
	if len(fs) != 1 || fs[0].Level != Fail || !strings.Contains(fs[0].Message, "Invalid access token") {
		t.Errorf("findings = %+v, want a single token failure carrying the API message", fs)
	}
	if !strings.Contains(fs[0].Remedy, "api-tokens") {
		t.Errorf("remedy should link token creation: %+v", fs[0])
	}
}

func TestCheckCloudflareHappyPath(t *testing.T) {
	mux := baseMux()
	mux.HandleFunc("GET /accounts/acc1/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		okJSON(w, `[{"id":"tun-1","name":"roost"}]`)
	})
	mux.HandleFunc("GET /zones/z1/dns_records", func(w http.ResponseWriter, r *http.Request) {
		okJSON(w, `[{"id":"r1","type":"CNAME","name":"*.example.com","content":"tun-1.cfargotunnel.com","proxied":true}]`)
	})
	st := &state.State{AccountID: "acc1", TunnelID: "tun-1"}
	fs := CheckCloudflare(fakeAPI(t, mux), st, "roost", []string{"app.example.com"})
	if HasFailures(fs) {
		t.Fatalf("unexpected failures:\n%s", Summary(fs))
	}
	by := findingsByCheck(fs)
	for _, check := range []string{"cloudflare-token", "zone:app.example.com", "ssl-depth", "tunnel", "dns:*.example.com"} {
		if f, found := by[check]; !found || f.Level != OK {
			t.Errorf("check %q = %+v, want OK", check, f)
		}
	}
}

func TestCheckCloudflareZoneOutcomes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /accounts", func(w http.ResponseWriter, r *http.Request) {
		okJSON(w, `[{"id":"acc1","name":"Test"}]`)
	})
	mux.HandleFunc("GET /zones", func(w http.ResponseWriter, r *http.Request) {
		okJSON(w, `[{"id":"z1","name":"example.com","status":"active"},{"id":"z2","name":"pend.dev","status":"pending"}]`)
	})
	mux.HandleFunc("GET /accounts/acc1/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		okJSON(w, `[]`)
	})
	mux.HandleFunc("GET /zones/{z}/dns_records", func(w http.ResponseWriter, r *http.Request) {
		okJSON(w, `[]`)
	})

	hosts := []string{"app.example.com", "app.pend.dev", "app.unknown.net"}
	fs := CheckCloudflare(fakeAPI(t, mux), &state.State{AccountID: "acc1"}, "roost", hosts)
	by := findingsByCheck(fs)

	if f := by["zone:app.example.com"]; f.Level != OK {
		t.Errorf("active zone = %+v, want OK", f)
	}
	if f := by["zone:app.pend.dev"]; f.Level != Warn || !strings.Contains(f.Message, "pending") {
		t.Errorf("pending zone = %+v, want distinct pending warning", f)
	}
	f := by["zone:app.unknown.net"]
	if f.Level != Fail {
		t.Fatalf("missing zone = %+v, want Fail", f)
	}
	// Both possible causes, and the visible zones for typo-spotting.
	for _, want := range []string{"isn't in this Cloudflare account", "zone-scoped", "example.com"} {
		if !strings.Contains(f.Message, want) {
			t.Errorf("missing-zone message lacks %q: %s", want, f.Message)
		}
	}
}

func TestCheckCloudflareTunnelDrift(t *testing.T) {
	serve := func(tunnels string) *tunnel.Client {
		mux := baseMux()
		mux.HandleFunc("GET /accounts/acc1/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
			okJSON(w, tunnels)
		})
		mux.HandleFunc("GET /zones/z1/dns_records", func(w http.ResponseWriter, r *http.Request) {
			okJSON(w, `[]`)
		})
		return fakeAPI(t, mux)
	}
	hosts := []string{"app.example.com"}

	t.Run("no remote tunnel suggests setup", func(t *testing.T) {
		fs := CheckCloudflare(serve(`[]`), &state.State{AccountID: "acc1"}, "roost", hosts)
		f := findingsByCheck(fs)["tunnel"]
		if f.Level != Warn || !strings.Contains(f.Remedy, "tunnel setup") {
			t.Errorf("finding = %+v", f)
		}
	})

	t.Run("foreign tunnel with empty state suggests adopt", func(t *testing.T) {
		fs := CheckCloudflare(serve(`[{"id":"tun-x","name":"roost"}]`), &state.State{AccountID: "acc1"}, "roost", hosts)
		f := findingsByCheck(fs)["tunnel"]
		if f.Level != Warn || !strings.Contains(f.Remedy, "--adopt") {
			t.Errorf("finding = %+v", f)
		}
	})

	t.Run("renamed config explains rather than recreating", func(t *testing.T) {
		st := &state.State{AccountID: "acc1", TunnelID: "tun-old"}
		fs := CheckCloudflare(serve(`[{"id":"tun-new","name":"roost"}]`), st, "roost", hosts)
		f := findingsByCheck(fs)["tunnel"]
		if f.Level != Warn || !strings.Contains(f.Message, "does not rename") {
			t.Errorf("finding = %+v, want the rename explanation from §9.0", f)
		}
	})
}

func TestCheckCloudflareDNSFindings(t *testing.T) {
	serve := func(records string) *tunnel.Client {
		mux := baseMux()
		mux.HandleFunc("GET /accounts/acc1/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
			okJSON(w, `[{"id":"tun-1","name":"roost"}]`)
		})
		mux.HandleFunc("GET /zones/z1/dns_records", func(w http.ResponseWriter, r *http.Request) {
			okJSON(w, records)
		})
		return fakeAPI(t, mux)
	}
	st := func() *state.State { return &state.State{AccountID: "acc1", TunnelID: "tun-1"} }
	hosts := []string{"app.example.com"}

	t.Run("missing record fails with the setup remedy", func(t *testing.T) {
		fs := CheckCloudflare(serve(`[]`), st(), "roost", hosts)
		f := findingsByCheck(fs)["dns:*.example.com"]
		if f.Level != Fail || !strings.Contains(f.Remedy, "tunnel setup") {
			t.Errorf("finding = %+v", f)
		}
	})

	t.Run("shadowing exact record is a hard finding", func(t *testing.T) {
		records := `[
			{"id":"r1","type":"CNAME","name":"*.example.com","content":"tun-1.cfargotunnel.com","proxied":true},
			{"id":"r2","type":"A","name":"app.example.com","content":"9.9.9.9","proxied":false}
		]`
		fs := CheckCloudflare(serve(records), st(), "roost", hosts)
		f := findingsByCheck(fs)["dns-shadow:app.example.com"]
		if f.Level != Fail {
			t.Fatalf("shadow finding = %+v, want Fail\n%s", f, Summary(fs))
		}
		for _, want := range []string{"9.9.9.9", "precedence"} {
			if !strings.Contains(f.Message, want) {
				t.Errorf("shadow message lacks %q: %s", want, f.Message)
			}
		}
	})
}

func TestCheckCloudflared(t *testing.T) {
	missing := &shell.Fake{RunFunc: func(string, ...string) (shell.Result, error) {
		return shell.Result{}, fmt.Errorf("not found")
	}}
	fs := CheckCloudflared(missing)
	if len(fs) != 1 || fs[0].Level != Warn {
		t.Errorf("findings = %+v, want a warning (compose runs cloudflared containerized)", fs)
	}
	if fs := CheckCloudflared(&shell.Fake{}); fs[0].Level != OK {
		t.Errorf("findings = %+v", fs)
	}
}

func TestSummaryAndHasFailures(t *testing.T) {
	fs := []Finding{
		ok("a", "fine"),
		warn("b", "meh", "consider x"),
		fail("c", "broken", "do y"),
	}
	s := Summary(fs)
	for _, want := range []string{"ok    a", "warn  b", "FAIL  c", "fix: do y", "fix: consider x"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary missing %q:\n%s", want, s)
		}
	}
	if !HasFailures(fs) {
		t.Error("HasFailures = false, want true")
	}
	if HasFailures(fs[:2]) {
		t.Error("HasFailures without Fail = true, want false")
	}
}
