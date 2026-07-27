package doctor

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/cdrrazan/roost/internal/state"
)

func TestCheckCredentials(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".roost"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".roost", "credentials")

	// Absent file: fine (token may come from the env).
	if f := CheckCredentials(home); f.Level != OK {
		t.Errorf("absent credentials = %+v, want OK", f)
	}

	// World-readable: fixable failure.
	if err := os.WriteFile(path, []byte("tok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := CheckCredentials(home)
	if f.Level != Fail {
		t.Errorf("0644 credentials = %+v, want Fail", f)
	}
	if f.Fix == nil || f.Fix.Kind != FixCredPerms || f.Fix.Path != path {
		t.Errorf("fix = %+v, want cred-perms on %s", f.Fix, path)
	}

	// 0600: clean.
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if f := CheckCredentials(home); f.Level != OK {
		t.Errorf("0600 credentials = %+v, want OK", f)
	}
}

func TestApplyFixesCredPerms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials")
	if err := os.WriteFile(path, []byte("tok\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	findings := []Finding{{Check: "credentials", Level: Fail, Fix: &Fix{Kind: FixCredPerms, Path: path}}}

	// A nil client is fine: cred-perms never needs the API.
	results := ApplyFixes(findings, nil)
	if len(results) != 1 || results[0].Level != OK {
		t.Fatalf("results = %+v, want one OK", results)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode after fix = %04o, want 0600", perm)
	}
}

func TestApplyFixesDNS(t *testing.T) {
	var patched, created bool
	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /zones/z1/dns_records/r1", func(w http.ResponseWriter, r *http.Request) {
		patched = true
		okJSON(w, `{"id":"r1"}`)
	})
	mux.HandleFunc("POST /zones/z1/dns_records", func(w http.ResponseWriter, r *http.Request) {
		created = true
		okJSON(w, `{"id":"r2","type":"CNAME","name":"*.example.com","content":"tun-1.cfargotunnel.com","proxied":true}`)
	})
	client := fakeAPI(t, mux)

	findings := []Finding{
		{Check: "dns:app.example.com", Level: Warn, Fix: &Fix{Kind: FixProxyDNS, ZoneID: "z1", RecordID: "r1", Name: "app.example.com"}},
		{Check: "dns:*.example.com", Level: Fail, Fix: &Fix{Kind: FixCreateDNS, ZoneID: "z1", Name: "*.example.com", Content: "tun-1.cfargotunnel.com"}},
		{Check: "zone:x", Level: OK}, // no Fix -> skipped
	}
	results := ApplyFixes(findings, client)
	if len(results) != 2 {
		t.Fatalf("results = %+v, want 2 (the fixable ones)", results)
	}
	for _, r := range results {
		if r.Level != OK {
			t.Errorf("fix result not OK: %+v", r)
		}
	}
	if !patched || !created {
		t.Errorf("patched=%v created=%v, want both true", patched, created)
	}
}

func TestApplyFixesDNSNeedsClient(t *testing.T) {
	findings := []Finding{{Check: "dns:x", Level: Fail, Fix: &Fix{Kind: FixCreateDNS, ZoneID: "z1", Name: "*.x.com"}}}
	results := ApplyFixes(findings, nil)
	if len(results) != 1 || results[0].Level != Fail {
		t.Fatalf("results = %+v, want one Fail (no client)", results)
	}
}

func TestCheckCloudflareUnproxiedGetsProxyFix(t *testing.T) {
	mux := baseMux()
	mux.HandleFunc("GET /accounts/acc1/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		okJSON(w, `[{"id":"tun-1","name":"roost"}]`)
	})
	// Correct target, but grey-cloud (proxied:false).
	mux.HandleFunc("GET /zones/z1/dns_records", func(w http.ResponseWriter, r *http.Request) {
		okJSON(w, `[{"id":"r1","type":"CNAME","name":"*.example.com","content":"tun-1.cfargotunnel.com","proxied":false}]`)
	})
	client := fakeAPI(t, mux)

	fs := CheckCloudflare(client, &state.State{AccountID: "acc1", TunnelID: "tun-1"}, "roost", []string{"app.example.com"})
	f := findingsByCheck(fs)["dns:*.example.com"]
	if f.Level != Warn {
		t.Fatalf("unproxied finding = %+v, want Warn\n%s", f, Summary(fs))
	}
	if f.Fix == nil || f.Fix.Kind != FixProxyDNS || f.Fix.RecordID != "r1" {
		t.Errorf("fix = %+v, want proxy-dns on r1", f.Fix)
	}
}
