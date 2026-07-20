package doctor

import (
	"errors"
	"strings"
	"testing"

	"github.com/cdrrazan/roost/internal/tunnel"
)

func TestSubdomainDepth(t *testing.T) {
	tests := []struct {
		host, zone string
		want       int
	}{
		{"example.com", "example.com", 0},
		{"app1.example.com", "example.com", 1},
		{"app1.demo.example.com", "example.com", 2},
		{"a.b.c.example.com", "example.com", 3},
		{"tweetx.app.rsynk.com", "app.rsynk.com", 1},
	}
	for _, tt := range tests {
		if got := SubdomainDepth(tt.host, tt.zone); got != tt.want {
			t.Errorf("SubdomainDepth(%q, %q) = %d, want %d", tt.host, tt.zone, got, tt.want)
		}
	}
}

func TestCheckSSLDepth(t *testing.T) {
	zone := tunnel.Zone{ID: "z1", Name: "example.com", Status: "active"}
	noCerts := func(string) ([]string, error) { return []string{"example.com", "*.example.com"}, nil }

	t.Run("one level passes", func(t *testing.T) {
		f := CheckSSLDepth("app1.example.com", zone, noCerts)
		if f.Level != OK {
			t.Errorf("finding = %+v, want OK", f)
		}
	})

	t.Run("apex passes", func(t *testing.T) {
		f := CheckSSLDepth("example.com", zone, noCerts)
		if f.Level != OK {
			t.Errorf("finding = %+v, want OK", f)
		}
	})

	t.Run("two levels without ACM is a hard finding with three fixes", func(t *testing.T) {
		f := CheckSSLDepth("app1.demo.example.com", zone, noCerts)
		if f.Level != Fail {
			t.Fatalf("finding = %+v, want Fail", f)
		}
		text := f.Message + " " + f.Remedy
		for _, want := range []string{
			"Universal SSL",        // the cause
			"app1.example.com",     // fix 1: flatten
			"dedicated one-level",  // fix 2: dedicated domain
			"Advanced Certificate", // fix 3: ACM
		} {
			if !strings.Contains(text, want) {
				t.Errorf("finding text missing %q:\n%s", want, text)
			}
		}
	})

	t.Run("two levels with a matching ACM SAN passes", func(t *testing.T) {
		withACM := func(string) ([]string, error) {
			return []string{"example.com", "*.example.com", "*.demo.example.com"}, nil
		}
		f := CheckSSLDepth("app1.demo.example.com", zone, withACM)
		if f.Level != OK {
			t.Errorf("finding = %+v, want OK with matching multi-level SAN", f)
		}
	})

	t.Run("cert API denied degrades to a warning, not a false negative", func(t *testing.T) {
		denied := func(string) ([]string, error) { return nil, errors.New("Authentication error (code 9109)") }
		f := CheckSSLDepth("app1.demo.example.com", zone, denied)
		if f.Level != Warn {
			t.Errorf("finding = %+v, want Warn when scopes prevent verification", f)
		}
		if !strings.Contains(f.Message+f.Remedy, "verify") {
			t.Errorf("warning should say it could not verify: %+v", f)
		}
	})
}

func TestWildcardCovers(t *testing.T) {
	tests := []struct {
		san, host string
		want      bool
	}{
		{"*.demo.example.com", "app1.demo.example.com", true},
		{"*.example.com", "app1.example.com", true},
		{"*.example.com", "app1.demo.example.com", false}, // one label only
		{"example.com", "example.com", true},
		{"example.com", "app1.example.com", false},
	}
	for _, tt := range tests {
		if got := wildcardCovers(tt.san, tt.host); got != tt.want {
			t.Errorf("wildcardCovers(%q, %q) = %v, want %v", tt.san, tt.host, got, tt.want)
		}
	}
}
