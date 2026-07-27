package tunnel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is Cloudflare's v4 API endpoint.
const DefaultBaseURL = "https://api.cloudflare.com/client/v4"

// Client is a minimal Cloudflare API client (stdlib only).
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// NewClient returns a Client against the real API.
func NewClient(token string) *Client {
	return &Client{BaseURL: DefaultBaseURL, Token: token, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// Account is a Cloudflare account the token can act on.
type Account struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Zone is a DNS zone in the account.
type Zone struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// Tunnel is a cfd_tunnel object.
type Tunnel struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	CreatedAt     time.Time  `json:"created_at"`
	ConnsActiveAt *time.Time `json:"conns_active_at"`
	Token         string     `json:"token,omitempty"`
}

// DNSRecord is a DNS record in a zone.
type DNSRecord struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type envelope struct {
	Success bool            `json:"success"`
	Errors  []apiError      `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

// do performs one API call, unwrapping Cloudflare's response envelope.
func (c *Client) do(method, path string, body, out any) error {
	reqBody := bytes.NewBuffer(nil)
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		reqBody = bytes.NewBuffer(data)
	}
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	req, err := http.NewRequest(method, base+path, reqBody)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")

	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("cloudflare API %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	var env envelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("cloudflare API %s %s: HTTP %d, unreadable body: %w", method, path, resp.StatusCode, err)
	}
	if !env.Success {
		var msgs []string
		for _, e := range env.Errors {
			msgs = append(msgs, fmt.Sprintf("%s (code %d)", e.Message, e.Code))
		}
		if len(msgs) == 0 {
			msgs = append(msgs, fmt.Sprintf("HTTP %d", resp.StatusCode))
		}
		return fmt.Errorf("cloudflare API %s %s: %s", method, path, strings.Join(msgs, "; "))
	}
	if out != nil {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return fmt.Errorf("cloudflare API %s %s: decode result: %w", method, path, err)
		}
	}
	return nil
}

// Accounts lists the accounts the token grants access to.
func (c *Client) Accounts() ([]Account, error) {
	var accounts []Account
	if err := c.do(http.MethodGet, "/accounts", nil, &accounts); err != nil {
		return nil, err
	}
	return accounts, nil
}

// Zones lists every zone the token can see.
func (c *Client) Zones() ([]Zone, error) {
	var zones []Zone
	if err := c.do(http.MethodGet, "/zones?per_page=50", nil, &zones); err != nil {
		return nil, err
	}
	return zones, nil
}

// FindTunnel looks a tunnel up by name; nil when absent.
func (c *Client) FindTunnel(accountID, name string) (*Tunnel, error) {
	var tunnels []Tunnel
	path := fmt.Sprintf("/accounts/%s/cfd_tunnel?name=%s&is_deleted=false", accountID, url.QueryEscape(name))
	if err := c.do(http.MethodGet, path, nil, &tunnels); err != nil {
		return nil, err
	}
	for i := range tunnels {
		if tunnels[i].Name == name {
			return &tunnels[i], nil
		}
	}
	return nil, nil
}

// CreateTunnel creates a remotely-managed tunnel. config_src cloudflare
// is what makes PUT /configurations meaningful and avoids cert.pem
// handling plus the one-zone limit of locally-managed tunnels; the
// create response already includes the connector token.
func (c *Client) CreateTunnel(accountID, name string) (*Tunnel, error) {
	var tun Tunnel
	body := map[string]string{"name": name, "config_src": "cloudflare"}
	if err := c.do(http.MethodPost, "/accounts/"+accountID+"/cfd_tunnel", body, &tun); err != nil {
		return nil, err
	}
	return &tun, nil
}

// TunnelToken fetches the connector token for an existing tunnel.
func (c *Client) TunnelToken(accountID, tunnelID string) (string, error) {
	var token string
	if err := c.do(http.MethodGet, "/accounts/"+accountID+"/cfd_tunnel/"+tunnelID+"/token", nil, &token); err != nil {
		return "", err
	}
	return token, nil
}

// PutIngress replaces the tunnel's ingress configuration.
func (c *Client) PutIngress(accountID, tunnelID string, rules []IngressRule) error {
	body := map[string]any{"config": map[string]any{"ingress": rules}}
	return c.do(http.MethodPut, "/accounts/"+accountID+"/cfd_tunnel/"+tunnelID+"/configurations", body, nil)
}

// ListDNS lists records in a zone, optionally filtered to names ending
// with suffix.
func (c *Client) ListDNS(zoneID, endsWith string) ([]DNSRecord, error) {
	path := "/zones/" + zoneID + "/dns_records?per_page=100"
	if endsWith != "" {
		path += "&name.endswith=" + url.QueryEscape(endsWith)
	}
	var records []DNSRecord
	if err := c.do(http.MethodGet, path, nil, &records); err != nil {
		return nil, err
	}
	return records, nil
}

// CreateDNS creates a record in a zone.
func (c *Client) CreateDNS(zoneID string, rec DNSRecord) (DNSRecord, error) {
	var created DNSRecord
	if err := c.do(http.MethodPost, "/zones/"+zoneID+"/dns_records", rec, &created); err != nil {
		return DNSRecord{}, err
	}
	return created, nil
}

// UpdateDNS overwrites a record.
func (c *Client) UpdateDNS(zoneID, recordID string, rec DNSRecord) error {
	return c.do(http.MethodPut, "/zones/"+zoneID+"/dns_records/"+recordID, rec, nil)
}

// certificatePack is one edge certificate pack on a zone.
type certificatePack struct {
	Hosts []string `json:"hosts"`
}

// CertificateSANs lists every hostname covered by the zone's edge
// certificates (Universal SSL and any ACM packs).
func (c *Client) CertificateSANs(zoneID string) ([]string, error) {
	var packs []certificatePack
	if err := c.do(http.MethodGet, "/zones/"+zoneID+"/ssl/certificate_packs?status=all", nil, &packs); err != nil {
		return nil, err
	}
	var sans []string
	for _, p := range packs {
		sans = append(sans, p.Hosts...)
	}
	return sans, nil
}

// PatchDNSProxied flips a record to proxied, the safe targeted fix for
// a correct-but-unproxied tunnel record.
func (c *Client) PatchDNSProxied(zoneID, recordID string) error {
	return c.do(http.MethodPatch, "/zones/"+zoneID+"/dns_records/"+recordID, map[string]bool{"proxied": true}, nil)
}

// DeleteDNS removes a record from a zone.
func (c *Client) DeleteDNS(zoneID, recordID string) error {
	return c.do(http.MethodDelete, "/zones/"+zoneID+"/dns_records/"+recordID, nil, nil)
}

// DeleteTunnel removes a tunnel from an account.
func (c *Client) DeleteTunnel(accountID, tunnelID string) error {
	return c.do(http.MethodDelete, "/accounts/"+accountID+"/cfd_tunnel/"+tunnelID, nil, nil)
}
