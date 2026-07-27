package main

import (
	"strings"
	"testing"

	"github.com/cdrrazan/roost/internal/generate"
)

func TestShareHost(t *testing.T) {
	got, err := shareHost("crm.example.com", "share-crm")
	if err != nil {
		t.Fatalf("shareHost: %v", err)
	}
	if got != "share-crm.example.com" {
		t.Errorf("shareHost = %q, want share-crm.example.com (same suffix, one level)", got)
	}
	// Deeper suffix is preserved so it stays under the app's own wildcard.
	if got, _ := shareHost("app.staging.example.com", "demo"); got != "demo.staging.example.com" {
		t.Errorf("shareHost deep = %q, want demo.staging.example.com", got)
	}
	if _, err := shareHost("localhost", "x"); err == nil {
		t.Error("a hostname with no suffix should error")
	}
}

func TestShareRouteRenders(t *testing.T) {
	apps := []generate.App{
		{Name: "crm", FQDN: "crm.example.com", Port: 8000, Framework: "django"},
	}
	host, _ := shareHost("crm.example.com", "share-crm")
	shareApp := generate.App{Name: "crm", FQDN: host, Port: 8000, Framework: "django"}

	out, err := generate.RenderCaddyfile(append(apps, shareApp), "")
	if err != nil {
		t.Fatalf("RenderCaddyfile: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "http://share-crm.example.com") {
		t.Errorf("share route missing from Caddyfile:\n%s", s)
	}
	// Both the original and the share host point at the same upstream.
	if strings.Count(s, "reverse_proxy crm:8000") < 2 {
		t.Errorf("expected the share host to proxy the same upstream:\n%s", s)
	}
}
