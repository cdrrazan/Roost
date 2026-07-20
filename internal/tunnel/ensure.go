package tunnel

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/cdrrazan/roost/internal/state"
)

// EnsureTunnelResult reports what EnsureTunnel did.
type EnsureTunnelResult struct {
	Tunnel  Tunnel
	Token   string
	Created bool
	// ReplicaWarning is set when the tunnel already had active
	// connections and this machine has no state claiming it — running
	// here would make this machine a replica, load-balanced with the
	// other one.
	ReplicaWarning string
}

// EnsureTunnel finds or creates the named tunnel, idempotently.
// A same-named tunnel whose ID doesn't match state.json was not
// created by roost: refuse unless adopt is set, since silently
// adopting would overwrite a stranger's ingress configuration.
func EnsureTunnel(c *Client, st *state.State, accountID, name string, adopt bool) (*EnsureTunnelResult, error) {
	existing, err := c.FindTunnel(accountID, name)
	if err != nil {
		return nil, err
	}

	if existing == nil {
		created, err := c.CreateTunnel(accountID, name)
		if err != nil {
			return nil, err
		}
		st.TunnelID = created.ID
		st.TunnelName = name
		token := created.Token
		if token == "" {
			if token, err = c.TunnelToken(accountID, created.ID); err != nil {
				return nil, err
			}
		}
		return &EnsureTunnelResult{Tunnel: *created, Token: token, Created: true}, nil
	}

	if st.TunnelID != "" && st.TunnelID != existing.ID {
		return nil, fmt.Errorf(
			"configured tunnel name %q resolves to tunnel %s (created %s), but state.json records tunnel %s — the config was likely renamed after a tunnel existed; use a different tunnel.name or pass --adopt to switch to the existing tunnel",
			name, existing.ID, existing.CreatedAt.Format("2006-01-02"), st.TunnelID)
	}

	var replicaWarning string
	if st.TunnelID == "" {
		if !adopt {
			return nil, fmt.Errorf(
				"a tunnel named %q already exists (ID %s, created %s) but roost did not create it; pass --adopt to take it over (overwriting its ingress configuration) or set a different tunnel.name",
				name, existing.ID, existing.CreatedAt.Format("2006-01-02"))
		}
		if existing.ConnsActiveAt != nil {
			replicaWarning = fmt.Sprintf(
				"tunnel %q has active connections from another machine; running roost here makes this machine a replica and Cloudflare will load-balance traffic between the two unpredictably — consider a distinct tunnel.name for this machine",
				name)
		}
	}

	st.TunnelID = existing.ID
	st.TunnelName = name
	token, err := c.TunnelToken(accountID, existing.ID)
	if err != nil {
		return nil, err
	}
	return &EnsureTunnelResult{Tunnel: *existing, Token: token, ReplicaWarning: replicaWarning}, nil
}

// RecordAction is the resolved idempotency state of one planned record.
type RecordAction string

const (
	RecordCreated      RecordAction = "created"
	RecordPresent      RecordAction = "already present"
	RecordProxiedFixed RecordAction = "proxied enabled"
	RecordRefused      RecordAction = "refused"
	RecordOverwritten  RecordAction = "overwritten"
)

// EnsureRecord resolves one planned record into an action. It never
// blind-overwrites: a record with foreign content (another tunnel, an
// A record, a Pages target) is refused unless force is set, because it
// may be a live service and a silent overwrite is unrecoverable.
func EnsureRecord(c *Client, zoneID, name, content string, force bool) (RecordAction, *DNSRecord, error) {
	existing, err := c.ListDNS(zoneID, "")
	if err != nil {
		return "", nil, err
	}
	var match *DNSRecord
	for i := range existing {
		if existing[i].Name == name {
			match = &existing[i]
			break
		}
	}

	if match == nil {
		created, err := c.CreateDNS(zoneID, DNSRecord{Type: "CNAME", Name: name, Content: content, Proxied: true})
		if err != nil {
			return "", nil, err
		}
		return RecordCreated, &created, nil
	}

	if match.Type == "CNAME" && match.Content == content {
		if match.Proxied {
			return RecordPresent, match, nil
		}
		// Correct content but unproxied: the tunnel can't serve it.
		// Flipping proxied on is a safe, targeted change.
		if err := c.PatchDNSProxied(zoneID, match.ID); err != nil {
			return "", nil, err
		}
		match.Proxied = true
		return RecordProxiedFixed, match, nil
	}

	if !force {
		return RecordRefused, match, fmt.Errorf(
			"record %s already exists as %s → %s; it may be a live service, so roost will not overwrite it without --force",
			name, match.Type, match.Content)
	}
	if err := c.UpdateDNS(zoneID, match.ID, DNSRecord{Type: "CNAME", Name: name, Content: content, Proxied: true}); err != nil {
		return "", nil, err
	}
	return RecordOverwritten, match, nil
}

// TunnelCNAME is the DNS content that routes a name into a tunnel.
func TunnelCNAME(tunnelID string) string {
	return tunnelID + ".cfargotunnel.com"
}

// accessApp is a Cloudflare Access application.
type accessApp struct {
	ID     string `json:"id,omitempty"`
	Name   string `json:"name"`
	Domain string `json:"domain"`
	Type   string `json:"type"`
}

// EnsureAccess applies an Access application + allow-list policy for
// every routing pattern (wildcards per suffix, exact apexes), so apps
// get an edge auth wall before hostnames leak via CT logs.
func EnsureAccess(c *Client, accountID string, domains []string, emails []string) ([]string, error) {
	var existing []accessApp
	if err := c.do(http.MethodGet, "/accounts/"+accountID+"/access/apps?per_page=100", nil, &existing); err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, app := range existing {
		have[app.Domain] = true
	}

	var created []string
	for _, domain := range domains {
		if have[domain] {
			continue
		}
		var app accessApp
		body := accessApp{Name: "roost " + domain, Domain: domain, Type: "self_hosted"}
		if err := c.do(http.MethodPost, "/accounts/"+accountID+"/access/apps", body, &app); err != nil {
			return created, err
		}
		include := make([]map[string]any, 0, len(emails))
		for _, email := range emails {
			include = append(include, map[string]any{"email": map[string]string{"email": email}})
		}
		policy := map[string]any{
			"name":     "roost allow",
			"decision": "allow",
			"include":  include,
		}
		if err := c.do(http.MethodPost, "/accounts/"+accountID+"/access/apps/"+app.ID+"/policies", policy, nil); err != nil {
			return created, err
		}
		created = append(created, domain)
	}
	return created, nil
}

// LoadToken returns the Cloudflare API token: $CLOUDFLARE_API_TOKEN
// first, then ~/.roost/credentials, whose permissions must be 0600 —
// a group- or world-readable token file is refused.
func LoadToken(home string) (string, error) {
	if token := os.Getenv("CLOUDFLARE_API_TOKEN"); token != "" {
		return token, nil
	}
	path := filepath.Join(home, ".roost", "credentials")
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return "", fmt.Errorf("no Cloudflare token: set $CLOUDFLARE_API_TOKEN or run `roost auth login`")
	}
	if err != nil {
		return "", err
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return "", fmt.Errorf("%s is mode %o; it holds an API token and must be 0600 (fix: chmod 600 %s)", path, perm, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("%s is empty; run `roost auth login`", path)
	}
	return token, nil
}

// SaveToken stores the API token in ~/.roost/credentials with 0600.
// Tokens never go into config.yml.
func SaveToken(home, token string) (string, error) {
	dir := filepath.Join(home, ".roost")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "credentials")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(token)+"\n"), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
