package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cdrrazan/roost/internal/doctor"
	"github.com/cdrrazan/roost/internal/shell"
)

// TestRunDoctor drives the full doctor orchestration with a healthy
// fake shell and a fake Cloudflare API.
func TestRunDoctor(t *testing.T) {
	mux := http.NewServeMux()
	okJSON := func(w http.ResponseWriter, result string) {
		fmt.Fprintf(w, `{"success":true,"errors":[],"result":%s}`, result)
	}
	mux.HandleFunc("GET /accounts", func(w http.ResponseWriter, r *http.Request) {
		okJSON(w, `[{"id":"acc1","name":"Test"}]`)
	})
	mux.HandleFunc("GET /zones", func(w http.ResponseWriter, r *http.Request) {
		okJSON(w, `[{"id":"z1","name":"demo.example.com","status":"active"},{"id":"z2","name":"other.org","status":"active"}]`)
	})
	mux.HandleFunc("GET /accounts/acc1/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		okJSON(w, `[]`)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLOUDFLARE_API_TOKEN", "tok")
	t.Setenv("ROOST_CF_API_BASE", server.URL)

	flags := &rootFlags{configPath: writeTestConfig(t)}
	findings := runDoctor(flags, &shell.Fake{})

	byCheck := map[string]doctor.Finding{}
	for _, f := range findings {
		byCheck[f.Check] = f
	}

	if f := byCheck["docker"]; f.Level != doctor.OK {
		t.Errorf("docker = %+v, want OK with a healthy shell", f)
	}
	if f := byCheck["config"]; f.Level != doctor.OK {
		t.Errorf("config = %+v", f)
	}
	// The missing-path app from writeTestConfig is a finding with a fix.
	if f := byCheck["app:gone-app"]; f.Level != doctor.Fail || f.Remedy == "" {
		t.Errorf("skipped app = %+v, want Fail with a remedy", f)
	}
	if f := byCheck["zone:rails-app.demo.example.com"]; f.Level != doctor.OK {
		t.Errorf("zone check = %+v", f)
	}
	if f := byCheck["tunnel"]; f.Level != doctor.Warn || !strings.Contains(f.Remedy, "tunnel setup") {
		t.Errorf("tunnel = %+v, want the setup suggestion before any tunnel exists", f)
	}
	if !doctor.HasFailures(findings) {
		t.Error("the skipped app should make doctor fail overall")
	}
}

// TestRunDoctorWithoutToken degrades to a warning instead of failing.
func TestRunDoctorWithoutToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLOUDFLARE_API_TOKEN", "")

	cfgPath := writeTestConfig(t)
	findings := runDoctor(&rootFlags{configPath: cfgPath}, &shell.Fake{})
	var sawSkipNote bool
	for _, f := range findings {
		if f.Check == "cloudflare" && f.Level == doctor.Warn {
			sawSkipNote = true
		}
		if strings.HasPrefix(f.Check, "zone:") {
			t.Errorf("zone checks should be skipped without a token, got %+v", f)
		}
	}
	if !sawSkipNote {
		t.Errorf("expected a warning that Cloudflare checks were skipped:\n%s", doctor.Summary(findings))
	}
}
