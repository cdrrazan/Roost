package doctor

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/cdrrazan/roost/internal/shell"
	"github.com/cdrrazan/roost/internal/state"
	"github.com/cdrrazan/roost/internal/tunnel"
)

// CheckDocker verifies Docker is installed AND its daemon is running —
// two different failures with two different fixes.
func CheckDocker(sh shell.Runner) []Finding {
	if _, err := sh.Run("docker", "--version"); err != nil {
		return []Finding{fail("docker",
			"docker is not installed",
			"install Docker Desktop (macOS) or Docker Engine (Linux): https://docs.docker.com/get-docker/")}
	}
	if _, err := sh.Run("docker", "info"); err != nil {
		return []Finding{fail("docker",
			"docker is installed but the daemon is not running",
			"start Docker Desktop, or on Linux: sudo systemctl start docker")}
	}
	return []Finding{ok("docker", "installed and running")}
}

// CheckCloudflared reports whether the cloudflared binary is present.
// roost runs cloudflared as a container, so this is a warning, not a
// failure — the binary is only handy for local debugging.
func CheckCloudflared(sh shell.Runner) []Finding {
	if _, err := sh.Run("cloudflared", "--version"); err != nil {
		return []Finding{warn("cloudflared",
			"cloudflared binary not found on the host (roost runs it as a container, so this is not fatal)",
			"brew install cloudflared, or see https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/")}
	}
	return []Finding{ok("cloudflared", "binary present")}
}

// ParseMemory parses compose-style memory strings (512m, 1g, 768M)
// into bytes.
func ParseMemory(s string) (uint64, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, fmt.Errorf("empty memory value")
	}
	unit := uint64(1)
	switch s[len(s)-1] {
	case 'k':
		unit, s = 1<<10, s[:len(s)-1]
	case 'm':
		unit, s = 1<<20, s[:len(s)-1]
	case 'g':
		unit, s = 1<<30, s[:len(s)-1]
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("unparseable memory value %q", s)
	}
	return n * unit, nil
}

// CompareMemoryBudget checks the sum of app memory caps against
// available RAM.
func CompareMemoryBudget(caps map[string]string, availableBytes uint64) Finding {
	const check = "memory"
	var total uint64
	for app, cap := range caps {
		n, err := ParseMemory(cap)
		if err != nil {
			return warn(check, fmt.Sprintf("app %q has memory %q which roost cannot parse", app, cap),
				"use values like 512m or 1g")
		}
		total += n
	}
	gib := func(b uint64) string { return fmt.Sprintf("%.1fGiB", float64(b)/(1<<30)) }
	if total > availableBytes {
		return fail(check,
			fmt.Sprintf("app memory caps sum to %s but only %s RAM is available", gib(total), gib(availableBytes)),
			"lower per-app memory:, move apps to a profile you start on demand, or run fewer apps")
	}
	return ok(check, fmt.Sprintf("app memory caps sum to %s of %s available", gib(total), gib(availableBytes)))
}

// CheckMemoryBudget applies CompareMemoryBudget against the host's
// RAM, skipping quietly where the total isn't readable.
func CheckMemoryBudget(caps map[string]string) []Finding {
	avail, err := readTotalRAM()
	if err != nil {
		return nil
	}
	return []Finding{CompareMemoryBudget(caps, avail)}
}

// readTotalRAM reads total system memory. Linux only; other platforms
// return an error and the check is skipped.
func readTotalRAM() (uint64, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			break
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			break
		}
		return kb << 10, nil
	}
	return 0, fmt.Errorf("MemTotal not found")
}

// CheckCloudflare runs the API-backed checks: token validity, zone
// resolution per hostname (four distinct outcomes), tunnel name vs
// state, DNS record presence, wildcard shadowing, and SSL depth.
func CheckCloudflare(client *tunnel.Client, st *state.State, tunnelName string, hostnames []string) []Finding {
	var findings []Finding
	add := func(f Finding) { findings = append(findings, f) }

	accounts, err := client.Accounts()
	if err != nil {
		return []Finding{fail("cloudflare-token", err.Error(),
			"create a token at https://dash.cloudflare.com/profile/api-tokens with Zone:DNS:Edit and Account:Cloudflare Tunnel:Edit, then run `roost auth login`")}
	}
	add(ok("cloudflare-token", fmt.Sprintf("valid; grants access to %d account(s)", len(accounts))))

	zones, err := client.Zones()
	if err != nil {
		add(fail("zones", err.Error(), "the token needs Zone:DNS:Edit (which implies zone read)"))
		return findings
	}
	var visible []string
	for _, z := range zones {
		visible = append(visible, z.Name)
	}

	for _, host := range hostnames {
		zone, outcome := tunnel.ResolveZone(host, zones)
		switch outcome {
		case tunnel.ZoneActive:
			add(ok("zone:"+host, "zone "+zone.Name+" active"))
		case tunnel.ZoneMissing:
			add(fail("zone:"+host,
				fmt.Sprintf("no zone matches — either the domain isn't in this Cloudflare account, or the token is zone-scoped and can't see it; zones the token can see: %s", strings.Join(visible, ", ")),
				"add the domain to Cloudflare, or widen the token's zone scope"))
		case tunnel.ZonePending:
			add(warn("zone:"+host,
				fmt.Sprintf("zone %s is pending: nameservers haven't propagated yet", zone.Name),
				"wait for propagation (usually under 24h); records can be created meanwhile"))
		case tunnel.ZoneOther:
			add(warn("zone:"+host,
				fmt.Sprintf("zone %s has status %q", zone.Name, zone.Status),
				"check the zone's page at https://dash.cloudflare.com"))
		}

		if zone.ID != "" {
			add(CheckSSLDepth(host, zone, client.CertificateSANs))
		}
	}

	accountID := st.AccountID
	if accountID == "" && len(accounts) == 1 {
		accountID = accounts[0].ID
	}
	if accountID == "" {
		add(warn("tunnel", "cannot determine the Cloudflare account (token spans several, no state.json)",
			"run `roost tunnel setup --account <id>` once to persist the choice"))
		return findings
	}

	remote, err := client.FindTunnel(accountID, tunnelName)
	if err != nil {
		add(warn("tunnel", err.Error(), "the token needs Account:Cloudflare Tunnel:Edit"))
		return findings
	}
	switch {
	case remote == nil:
		add(warn("tunnel", fmt.Sprintf("no tunnel named %q exists yet", tunnelName),
			"run `roost tunnel setup` to create it"))
		return findings
	case st.TunnelID == "":
		add(warn("tunnel",
			fmt.Sprintf("tunnel %q exists (ID %s) but state.json doesn't record it — roost did not create it", tunnelName, remote.ID),
			"run `roost tunnel setup --adopt` to take it over, or set a different tunnel.name"))
	case st.TunnelID != remote.ID:
		add(warn("tunnel",
			fmt.Sprintf("config tunnel.name %q resolves to tunnel %s, but state.json records %s — renaming in config does not rename the remote tunnel, it points roost at a different one", tunnelName, remote.ID, st.TunnelID),
			"restore the original tunnel.name, or run `roost tunnel setup --adopt` to switch deliberately"))
	default:
		add(ok("tunnel", fmt.Sprintf("%q (%s) matches state.json", tunnelName, remote.ID)))
	}

	// DNS records + shadowing for the planned wildcards.
	tunnelID := st.TunnelID
	if tunnelID == "" && remote != nil {
		tunnelID = remote.ID
	}
	plan, _ := tunnel.PlanDNS(hostnames, zones)
	content := tunnel.TunnelCNAME(tunnelID)
	for _, rec := range plan {
		existing, err := client.ListDNS(rec.Zone.ID, strings.TrimPrefix(rec.Name, "*."))
		if err != nil {
			add(warn("dns:"+rec.Name, err.Error(), "the token needs Zone:DNS:Edit"))
			continue
		}
		found := false
		for _, e := range existing {
			if e.Name == rec.Name && e.Content == content {
				found = true
				break
			}
		}
		if found {
			add(ok("dns:"+rec.Name, "points at the tunnel"))
		} else {
			add(fail("dns:"+rec.Name, "record missing or not pointing at the tunnel",
				"run `roost tunnel setup`"))
		}
		if rec.Wildcard {
			for _, shadow := range tunnel.FindShadowing(existing, rec.Name, rec.Covers, content) {
				add(fail("dns-shadow:"+shadow.Hostname,
					fmt.Sprintf("exact record %s → %s takes precedence over %s; requests will show the old target with no error anywhere", shadow.Existing.Name, shadow.Existing.Content, rec.Name),
					"rename the app, or delete/repoint the existing record"))
			}
		}
	}
	return findings
}
