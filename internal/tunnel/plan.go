// Package tunnel integrates with Cloudflare: one remotely-managed
// tunnel, wildcard DNS per routing suffix, ingress pointing at Caddy,
// and optional Access policies. roost orchestrates cloudflared — it
// never implements tunnelling itself.
package tunnel

import (
	"sort"
	"strings"
)

// ZoneOutcome classifies how a hostname maps onto the account's zones.
type ZoneOutcome string

const (
	// ZoneActive: zone found and active — proceed.
	ZoneActive ZoneOutcome = "active"
	// ZoneMissing: no zone matches. Either the domain isn't in this
	// Cloudflare account, or the token is zone-scoped and can't see it
	// — the API cannot distinguish the two.
	ZoneMissing ZoneOutcome = "missing"
	// ZonePending: zone added but nameservers haven't propagated.
	// Records can be created and will resolve once propagation
	// completes.
	ZonePending ZoneOutcome = "pending"
	// ZoneOther: zone in a state like moved or initializing.
	ZoneOther ZoneOutcome = "other"
)

// ResolveZone matches a hostname to its zone by longest suffix:
// tweetx.app.rsynk.com matches zone app.rsynk.com over rsynk.com when
// both exist in the account.
func ResolveZone(host string, zones []Zone) (Zone, ZoneOutcome) {
	var best Zone
	found := false
	for _, z := range zones {
		if host != z.Name && !strings.HasSuffix(host, "."+z.Name) {
			continue
		}
		if !found || len(z.Name) > len(best.Name) {
			best = z
			found = true
		}
	}
	if !found {
		return Zone{}, ZoneMissing
	}
	switch best.Status {
	case "active":
		return best, ZoneActive
	case "pending":
		return best, ZonePending
	default:
		return best, ZoneOther
	}
}

// PlannedRecord is one DNS record the setup will ensure exists.
type PlannedRecord struct {
	// Name is the record name: "*.<parent>" for wildcards, the bare
	// hostname for apex records.
	Name     string
	Zone     Zone
	Wildcard bool
	// Covers lists the app hostnames this record serves.
	Covers []string
}

// UnresolvedHost is an app hostname whose zone couldn't be used.
type UnresolvedHost struct {
	Hostname string
	Zone     Zone // populated for pending/other outcomes
	Outcome  ZoneOutcome
}

// PlanDNS computes the minimal record set for the given app hostnames:
// hostnames group by their parent suffix and each distinct parent gets
// one wildcard (wildcards match exactly one label, so deeper-nested
// hostnames form their own group); a hostname that IS a zone apex gets
// an exact record instead, relying on CNAME flattening. Hostnames in
// missing zones come back as unresolved rather than failing the plan;
// pending/other zones stay in the plan — the caller warns.
func PlanDNS(hostnames []string, zones []Zone) ([]PlannedRecord, []UnresolvedHost) {
	byName := map[string]*PlannedRecord{}
	var unresolved []UnresolvedHost
	var order []string

	for _, host := range hostnames {
		zone, outcome := ResolveZone(host, zones)
		if outcome == ZoneMissing {
			unresolved = append(unresolved, UnresolvedHost{Hostname: host, Outcome: outcome})
			continue
		}

		var name string
		wildcard := false
		if host == zone.Name {
			// Zone apex: a wildcard can't cover it.
			name = host
		} else {
			_, parent, _ := strings.Cut(host, ".")
			name = "*." + parent
			wildcard = true
		}

		if rec, ok := byName[name]; ok {
			rec.Covers = append(rec.Covers, host)
			continue
		}
		byName[name] = &PlannedRecord{Name: name, Zone: zone, Wildcard: wildcard, Covers: []string{host}}
		order = append(order, name)
	}

	sort.Strings(order)
	plan := make([]PlannedRecord, 0, len(order))
	for _, name := range order {
		plan = append(plan, *byName[name])
	}
	return plan, unresolved
}

// IngressRule is one entry in the tunnel's ingress configuration.
type IngressRule struct {
	Hostname string `json:"hostname,omitempty"`
	Service  string `json:"service"`
}

// IngressRules maps the DNS plan to tunnel ingress: every record name
// (wildcard or apex) routes to Caddy, and the mandatory
// http_status:404 catch-all closes the list — Cloudflare rejects an
// ingress without it.
func IngressRules(plan []PlannedRecord) []IngressRule {
	rules := make([]IngressRule, 0, len(plan)+1)
	for _, rec := range plan {
		rules = append(rules, IngressRule{Hostname: rec.Name, Service: "http://caddy:80"})
	}
	rules = append(rules, IngressRule{Service: "http_status:404"})
	return rules
}

// Shadow is an exact DNS record that takes precedence over a planned
// wildcard for one of the app hostnames: requests will hit the old
// record and never reach the tunnel, with no error anywhere.
type Shadow struct {
	Hostname string
	Existing DNSRecord
}

// FindShadowing reports app hostnames under the wildcard's suffix that
// have their own exact record pointing somewhere else. An exact record
// whose content is ourContent (this tunnel) takes precedence over the
// wildcard but routes identically — the migration case — so it is not
// a shadow; a record pointing at a different tunnel or any other
// target is.
func FindShadowing(existing []DNSRecord, wildcard string, appHosts []string, ourContent string) []Shadow {
	suffix := strings.TrimPrefix(wildcard, "*")
	hosts := map[string]bool{}
	for _, h := range appHosts {
		if strings.HasSuffix(h, suffix) {
			hosts[h] = true
		}
	}
	var shadows []Shadow
	for _, rec := range existing {
		if rec.Name == wildcard || !hosts[rec.Name] || rec.Content == ourContent {
			continue
		}
		shadows = append(shadows, Shadow{Hostname: rec.Name, Existing: rec})
	}
	return shadows
}
