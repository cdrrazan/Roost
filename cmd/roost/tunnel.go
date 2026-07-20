package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/cdrrazan/roost/internal/config"
	"github.com/cdrrazan/roost/internal/state"
	"github.com/cdrrazan/roost/internal/tunnel"
)

// newAuthCmd stores the Cloudflare API token in ~/.roost/credentials
// (0600), verifying it against the API before writing anything. Tokens
// never go into config.yml.
func newAuthCmd() *cobra.Command {
	auth := &cobra.Command{Use: "auth", Short: "Manage Cloudflare credentials"}
	var token string
	login := &cobra.Command{
		Use:   "login",
		Short: "Store a Cloudflare API token in ~/.roost/credentials (mode 0600)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if token == "" {
				cmd.Println("Paste your Cloudflare API token (create one at https://dash.cloudflare.com/profile/api-tokens")
				cmd.Println("with scopes Zone:DNS:Edit, Account:Cloudflare Tunnel:Edit, and, if you use Access,")
				cmd.Println("Account:Access: Apps and Policies:Edit):")
				scanner := bufio.NewScanner(cmd.InOrStdin())
				if !scanner.Scan() {
					return fmt.Errorf("no token provided")
				}
				token = strings.TrimSpace(scanner.Text())
			}
			if token == "" {
				return fmt.Errorf("no token provided")
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			// Verify before writing anything.
			if _, err := tunnel.NewClient(token).Accounts(); err != nil {
				return fmt.Errorf("token verification failed: %w", err)
			}
			path, err := tunnel.SaveToken(home, token)
			if err != nil {
				return err
			}
			cmd.Printf("token verified and stored in %s\n", path)
			return nil
		},
	}
	login.Flags().StringVar(&token, "token", "", "the API token (otherwise read from stdin)")
	auth.AddCommand(login)
	return auth
}

// tunnelContext gathers everything the tunnel commands need.
type tunnelContext struct {
	cfg       *config.Config
	hostnames []string
	client    *tunnel.Client
	st        *state.State
	statePath string
	accountID string
}

func loadTunnelContext(cmd *cobra.Command, flags *rootFlags, accountFlag string) (*tunnelContext, error) {
	cfg, resolved, skipped, err := loadResolved(flags)
	if err != nil {
		return nil, err
	}
	for _, app := range skipped {
		cmd.Printf("skipped: %s (%s)\n", app.Name, app.Reason)
	}
	var hostnames []string
	for _, app := range resolved {
		hostnames = append(hostnames, app.FQDN)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	token, err := tunnel.LoadToken(home)
	if err != nil {
		return nil, err
	}
	client := tunnel.NewClient(token)
	if base := os.Getenv("ROOST_CF_API_BASE"); base != "" {
		client.BaseURL = base
	}

	statePath := state.Path(home)
	st, err := state.Load(statePath)
	if err != nil {
		return nil, err
	}

	accountID := accountFlag
	if accountID == "" {
		accountID = st.AccountID
	}
	if accountID == "" {
		accounts, err := client.Accounts()
		if err != nil {
			return nil, err
		}
		switch len(accounts) {
		case 0:
			return nil, fmt.Errorf("the token grants access to no Cloudflare accounts")
		case 1:
			accountID = accounts[0].ID
		default:
			var lines []string
			for _, a := range accounts {
				lines = append(lines, fmt.Sprintf("  %s  %s", a.ID, a.Name))
			}
			return nil, fmt.Errorf("the token grants access to multiple accounts; rerun with --account <id>:\n%s", strings.Join(lines, "\n"))
		}
	}
	st.AccountID = accountID

	return &tunnelContext{
		cfg: cfg, hostnames: hostnames, client: client,
		st: st, statePath: statePath, accountID: accountID,
	}, nil
}

// refreshConnector restarts the cloudflared container so it re-reads a
// freshly pushed ingress. A remotely-managed connector doesn't always pick up
// new hostnames on its own, so a just-added zone can 404 until it refreshes.
// Overridable in tests; best-effort at the call site (nothing to restart if the
// stack isn't running).
var refreshConnector = func() error {
	r, err := newRunner()
	if err != nil {
		return err
	}
	return r.Restart("cloudflared")
}

// tunnelName is the configured tunnel name or the literal default
// "roost" — never generated, so the Cloudflare dashboard stays
// recognizable (§9.0 of the design).
func tunnelName(cfg *config.Config) string {
	if cfg.Tunnel.Name != "" {
		return cfg.Tunnel.Name
	}
	return "roost"
}

// accessPatterns are the routing patterns Access applications cover:
// one wildcard per suffix plus exact apexes.
func accessPatterns(plan []tunnel.PlannedRecord) []string {
	var patterns []string
	for _, rec := range plan {
		patterns = append(patterns, rec.Name)
	}
	return patterns
}

// newTunnelCmd groups `tunnel setup` (create the tunnel, plan and
// create every DNS record, push ingress, apply Access — the whole
// remote side, no dashboard visit) and `tunnel access` (policies only).
func newTunnelCmd(flags *rootFlags) *cobra.Command {
	root := &cobra.Command{Use: "tunnel", Short: "Manage the Cloudflare tunnel and DNS"}

	var adopt, force bool
	var account string
	setup := &cobra.Command{
		Use:   "setup",
		Short: "Create the tunnel and every DNS record via the API — no dashboard visit",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tc, err := loadTunnelContext(cmd, flags, account)
			if err != nil {
				return err
			}
			if len(tc.hostnames) == 0 {
				cmd.Println("nothing to set up: no app resolves to a hostname")
				return nil
			}

			zones, err := tc.client.Zones()
			if err != nil {
				return err
			}
			plan, unresolved := tunnel.PlanDNS(tc.hostnames, zones)

			for _, u := range unresolved {
				var visible []string
				for _, z := range zones {
					visible = append(visible, z.Name)
				}
				cmd.Printf("skipping %s: no matching zone — either this domain isn't in the Cloudflare account, or the token is zone-scoped and can't see it. Zones the token can see: %s\n",
					u.Hostname, strings.Join(visible, ", "))
			}
			if len(plan) == 0 {
				return fmt.Errorf("no app hostname maps to a zone this token can see — the token or account is wrong, refusing to create anything")
			}
			warnZoneStatus(cmd, tc.hostnames, zones)

			name := tunnelName(tc.cfg)
			res, err := tunnel.EnsureTunnel(tc.client, tc.st, tc.accountID, name, adopt)
			if err != nil {
				return err
			}
			if res.Created {
				cmd.Printf("created tunnel %q (%s)\n", name, res.Tunnel.ID)
			} else {
				cmd.Printf("using tunnel %q (%s)\n", name, res.Tunnel.ID)
			}
			if res.ReplicaWarning != "" {
				cmd.Println("warning:", res.ReplicaWarning)
			}
			if err := tc.st.Save(tc.statePath); err != nil {
				return err
			}

			content := tunnel.TunnelCNAME(res.Tunnel.ID)

			// Shadowing pre-flight: an exact record beats a wildcard in
			// DNS, so a matching exact record pointing anywhere but this
			// tunnel silently swallows an app.
			var shadowErrs []string
			for _, rec := range plan {
				if !rec.Wildcard {
					continue
				}
				existing, err := tc.client.ListDNS(rec.Zone.ID, strings.TrimPrefix(rec.Name, "*."))
				if err != nil {
					return err
				}
				for _, shadow := range tunnel.FindShadowing(existing, rec.Name, rec.Covers, content) {
					shadowErrs = append(shadowErrs, fmt.Sprintf(
						"%s is shadowed: the exact record %s → %s takes precedence over %s and requests will never reach the tunnel; rename the app or delete/repoint that record",
						shadow.Hostname, shadow.Existing.Name, shadow.Existing.Content, rec.Name))
				}
			}
			for _, msg := range shadowErrs {
				cmd.Println("finding:", msg)
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "RECORD\tZONE\tRESULT")
			var recordErrs []string
			newRoutes := false
			for _, rec := range plan {
				action, created, err := tunnel.EnsureRecord(tc.client, rec.Zone.ID, rec.Name, content, force)
				if err != nil {
					recordErrs = append(recordErrs, err.Error())
					fmt.Fprintf(w, "%s\t%s\t%s\n", rec.Name, rec.Zone.Name, action)
					continue
				}
				if action == tunnel.RecordCreated && created != nil {
					tc.st.AddRecord(state.Record{ID: created.ID, ZoneID: rec.Zone.ID, Name: rec.Name})
					newRoutes = true
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", rec.Name, rec.Zone.Name, action)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			if err := tc.st.Save(tc.statePath); err != nil {
				return err
			}

			if err := tc.client.PutIngress(tc.accountID, res.Tunnel.ID, tunnel.IngressRules(plan)); err != nil {
				return err
			}
			cmd.Println("ingress configuration pushed")

			// A newly-created record means a new hostname/zone entered the
			// ingress; nudge the running connector to re-read it so the new
			// route works without waiting for its periodic refresh.
			if newRoutes {
				if err := refreshConnector(); err != nil {
					cmd.Println("note: new routes added but cloudflared wasn't refreshed automatically; run `roost up` to apply them")
				} else {
					cmd.Println("refreshed cloudflared to apply the new routes")
				}
			}

			if err := writeTunnelEnv(res.Token); err != nil {
				return err
			}

			if tc.cfg.Tunnel.Access != nil {
				created, err := tunnel.EnsureAccess(tc.client, tc.accountID, accessPatterns(plan), tc.cfg.Tunnel.Access.Emails)
				if err != nil {
					return err
				}
				for _, domain := range created {
					cmd.Printf("access policy applied: %s\n", domain)
				}
			} else {
				cmd.Printf("warning: no tunnel.access configured — these hostnames are publicly reachable: %s\n", strings.Join(tc.hostnames, ", "))
			}

			var problems []string
			problems = append(problems, shadowErrs...)
			problems = append(problems, recordErrs...)
			if len(problems) > 0 {
				return fmt.Errorf("setup finished with findings:\n  %s", strings.Join(problems, "\n  "))
			}
			return nil
		},
	}
	setup.Flags().BoolVar(&adopt, "adopt", false, "take over a same-named tunnel roost did not create")
	setup.Flags().BoolVar(&force, "force", false, "overwrite existing DNS records that point elsewhere")
	setup.Flags().StringVar(&account, "account", "", "Cloudflare account ID (needed when the token spans several)")

	var accessAccount string
	access := &cobra.Command{
		Use:   "access",
		Short: "Apply the Access wildcard policy across every routing suffix",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tc, err := loadTunnelContext(cmd, flags, accessAccount)
			if err != nil {
				return err
			}
			if tc.cfg.Tunnel.Access == nil {
				return fmt.Errorf("no tunnel.access configured; add access.emails to config.yml first")
			}
			zones, err := tc.client.Zones()
			if err != nil {
				return err
			}
			plan, _ := tunnel.PlanDNS(tc.hostnames, zones)
			created, err := tunnel.EnsureAccess(tc.client, tc.accountID, accessPatterns(plan), tc.cfg.Tunnel.Access.Emails)
			if err != nil {
				return err
			}
			if len(created) == 0 {
				cmd.Println("access policies already in place")
			}
			for _, domain := range created {
				cmd.Printf("access policy applied: %s\n", domain)
			}
			return tc.st.Save(tc.statePath)
		},
	}
	access.Flags().StringVar(&accessAccount, "account", "", "Cloudflare account ID")

	root.AddCommand(setup, access)
	return root
}

// warnZoneStatus prints distinct warnings for pending and misconfigured
// zones; records still get created for pending zones.
func warnZoneStatus(cmd *cobra.Command, hostnames []string, zones []tunnel.Zone) {
	warned := map[string]bool{}
	for _, host := range hostnames {
		zone, outcome := tunnel.ResolveZone(host, zones)
		if warned[zone.Name] {
			continue
		}
		switch outcome {
		case tunnel.ZonePending:
			warned[zone.Name] = true
			cmd.Printf("warning: zone %s is pending — nameservers haven't propagated yet; records will start resolving once they do (usually within 24h)\n", zone.Name)
		case tunnel.ZoneOther:
			warned[zone.Name] = true
			cmd.Printf("warning: zone %s has status %q — check its dashboard page at https://dash.cloudflare.com\n", zone.Name, zone.Status)
		}
	}
}

// writeTunnelEnv stores the cloudflared connector token where compose
// picks it up, mode 0600 — never in config.yml.
func writeTunnelEnv(token string) error {
	dir, err := buildDir()
	if err != nil {
		return err
	}
	envPath := filepath.Join(dir, ".env")
	return os.WriteFile(envPath, []byte("ROOST_TUNNEL_TOKEN="+token+"\n"), 0o600)
}
