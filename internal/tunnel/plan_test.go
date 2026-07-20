package tunnel

import (
	"strings"
	"testing"
)

func zones(names ...string) []Zone {
	var zs []Zone
	for _, n := range names {
		zs = append(zs, Zone{ID: "zone-" + n, Name: n, Status: "active"})
	}
	return zs
}

func TestResolveZone(t *testing.T) {
	zs := []Zone{
		{ID: "z1", Name: "rsynk.com", Status: "active"},
		{ID: "z2", Name: "app.rsynk.com", Status: "active"},
		{ID: "z3", Name: "pending.dev", Status: "pending"},
		{ID: "z4", Name: "moved.dev", Status: "moved"},
	}

	tests := []struct {
		host     string
		wantZone string
		wantOut  ZoneOutcome
	}{
		// Longest suffix wins: app.rsynk.com is its own zone.
		{"tweetx.app.rsynk.com", "app.rsynk.com", ZoneActive},
		{"x.rsynk.com", "rsynk.com", ZoneActive},
		{"rsynk.com", "rsynk.com", ZoneActive},
		{"a.pending.dev", "pending.dev", ZonePending},
		{"a.moved.dev", "moved.dev", ZoneOther},
		{"nothing.example.net", "", ZoneMissing},
		// Suffix must align on a label boundary.
		{"badrsynk.com", "", ZoneMissing},
	}
	for _, tt := range tests {
		zone, outcome := ResolveZone(tt.host, zs)
		if outcome != tt.wantOut {
			t.Errorf("ResolveZone(%q) outcome = %v, want %v", tt.host, outcome, tt.wantOut)
		}
		if zone.Name != tt.wantZone {
			t.Errorf("ResolveZone(%q) zone = %q, want %q", tt.host, zone.Name, tt.wantZone)
		}
	}
}

func TestPlanDNS(t *testing.T) {
	t.Run("three suffixes plus an apex", func(t *testing.T) {
		hosts := []string{
			"app1.demo.example.com",
			"app2.demo.example.com",
			"tweetx.app.example.com",
			"crm.other.org",
			"trackaru.com", // apex
		}
		zs := zones("example.com", "other.org", "trackaru.com")
		plan, unresolved := PlanDNS(hosts, zs)
		if len(unresolved) != 0 {
			t.Fatalf("unresolved = %+v, want none", unresolved)
		}
		byName := map[string]PlannedRecord{}
		for _, r := range plan {
			byName[r.Name] = r
		}
		if len(plan) != 4 {
			t.Fatalf("plan = %+v, want 4 records", plan)
		}
		if r := byName["*.demo.example.com"]; !r.Wildcard || len(r.Covers) != 2 {
			t.Errorf("*.demo.example.com = %+v, want wildcard covering both demo apps", r)
		}
		if r := byName["*.app.example.com"]; !r.Wildcard || r.Zone.Name != "example.com" {
			t.Errorf("*.app.example.com = %+v", r)
		}
		if r := byName["*.other.org"]; !r.Wildcard || r.Zone.Name != "other.org" {
			t.Errorf("*.other.org = %+v", r)
		}
		// The apex can't be covered by a wildcard: exact CNAME.
		if r := byName["trackaru.com"]; r.Wildcard || r.Zone.Name != "trackaru.com" {
			t.Errorf("trackaru.com = %+v, want exact apex record", r)
		}
	})

	t.Run("wildcards match one label only", func(t *testing.T) {
		hosts := []string{"www.example.com", "tweetx.app.example.com"}
		plan, _ := PlanDNS(hosts, zones("example.com"))
		byName := map[string]PlannedRecord{}
		for _, r := range plan {
			byName[r.Name] = r
		}
		if len(plan) != 2 {
			t.Fatalf("plan = %+v, want separate wildcards per depth", plan)
		}
		if r, ok := byName["*.app.example.com"]; !ok || len(r.Covers) != 1 || r.Covers[0] != "tweetx.app.example.com" {
			t.Errorf("deeply nested host must get its own record, got %+v", plan)
		}
		if r := byName["*.example.com"]; len(r.Covers) != 1 || r.Covers[0] != "www.example.com" {
			t.Errorf("*.example.com must not claim the deeper host: %+v", r)
		}
	})

	t.Run("missing zone is unresolved, not fatal", func(t *testing.T) {
		hosts := []string{"app.known.com", "app.unknown.net"}
		plan, unresolved := PlanDNS(hosts, zones("known.com"))
		if len(plan) != 1 {
			t.Errorf("plan = %+v, want just the known zone", plan)
		}
		if len(unresolved) != 1 || unresolved[0].Hostname != "app.unknown.net" || unresolved[0].Outcome != ZoneMissing {
			t.Errorf("unresolved = %+v", unresolved)
		}
	})
}

func TestIngressRules(t *testing.T) {
	plan := []PlannedRecord{
		{Name: "*.demo.example.com", Wildcard: true},
		{Name: "trackaru.com"},
	}
	rules := IngressRules(plan)
	if len(rules) != 3 {
		t.Fatalf("rules = %+v, want 2 host rules + catch-all", rules)
	}
	for _, r := range rules[:2] {
		if r.Service != "http://caddy:80" {
			t.Errorf("rule %+v should point at caddy", r)
		}
	}
	last := rules[len(rules)-1]
	if last.Service != "http_status:404" || last.Hostname != "" {
		t.Errorf("final rule = %+v, want the mandatory http_status:404 catch-all", last)
	}
}

func TestFindShadowing(t *testing.T) {
	appHosts := []string{"tweetx.example.com", "blog.example.com"}
	wildcard := "*.example.com"

	t.Run("wildcard only passes", func(t *testing.T) {
		existing := []DNSRecord{{Name: "*.example.com", Type: "CNAME", Content: "t.cfargotunnel.com"}}
		if got := FindShadowing(existing, wildcard, appHosts); len(got) != 0 {
			t.Errorf("shadows = %+v, want none", got)
		}
	})

	t.Run("unrelated exact record passes", func(t *testing.T) {
		existing := []DNSRecord{
			{Name: "*.example.com", Type: "CNAME", Content: "t.cfargotunnel.com"},
			{Name: "mail.example.com", Type: "A", Content: "1.2.3.4"},
		}
		if got := FindShadowing(existing, wildcard, appHosts); len(got) != 0 {
			t.Errorf("shadows = %+v, want none", got)
		}
	})

	t.Run("exact record matching an app hostname is a hard finding", func(t *testing.T) {
		existing := []DNSRecord{
			{Name: "*.example.com", Type: "CNAME", Content: "t.cfargotunnel.com"},
			{Name: "tweetx.example.com", Type: "A", Content: "9.9.9.9"},
		}
		got := FindShadowing(existing, wildcard, appHosts)
		if len(got) != 1 {
			t.Fatalf("shadows = %+v, want one", got)
		}
		if got[0].Hostname != "tweetx.example.com" || !strings.Contains(got[0].Existing.Content, "9.9.9.9") {
			t.Errorf("shadow = %+v", got[0])
		}
	})
}
