package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cdrrazan/roost/internal/config"
	"github.com/cdrrazan/roost/internal/detect"
	"github.com/cdrrazan/roost/internal/tunnel"
)

// newInitCmd bootstraps ~/.roost/config.yml: picks the domain (from
// the account's live zone list when a token is stored), scans a folder
// for detectable apps, writes explicit per-app hostnames plus
// tunnel.name, and walks the user through the only two manual steps —
// nameservers and token creation.
func newInitCmd(flags *rootFlags) *cobra.Command {
	var domain, scanDir, tunnelName string
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create ~/.roost/config.yml: pick a domain, scan a folder for apps",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			target := flags.configPath
			if target == "" {
				target = filepath.Join(home, ".roost", "config.yml")
			}
			if _, err := os.Stat(target); err == nil && !force {
				return fmt.Errorf("%s already exists; pass --force to overwrite it", target)
			}

			if domain == "" {
				domain, err = pickDomain(cmd, home)
				if err != nil {
					return err
				}
			}
			if err := config.ValidateHostname(domain); err != nil {
				return fmt.Errorf("domain: %w", err)
			}

			// Steer toward one-level hostnames: a nested domain means
			// every app lands two+ levels below the apex, which free
			// Universal SSL does not cover (see roost doctor).
			if strings.Count(domain, ".") >= 2 {
				cmd.Printf("note: %s looks nested — app hostnames like app1.%s may need Advanced Certificate Manager for TLS; a zone apex like %s avoids that\n",
					domain, domain, domain[strings.Index(domain, ".")+1:])
			}

			var lines []string
			lines = append(lines,
				"# roost configuration — see https://github.com/cdrrazan/roost",
				"domain: "+domain,
				"",
				"tunnel:",
				"  name: "+tunnelName,
				"",
				"apps:",
			)

			added := 0
			if scanDir != "" {
				entries, err := os.ReadDir(scanDir)
				if err != nil {
					return fmt.Errorf("scan %s: %w", scanDir, err)
				}
				for _, entry := range entries {
					if !entry.IsDir() {
						continue
					}
					appPath := filepath.Join(scanDir, entry.Name())
					det, err := detect.Detect(appPath)
					if err != nil {
						continue // not an app; skip quietly during scanning
					}
					name := config.Slugify(entry.Name())
					// Explicit hostnames so the file is self-describing.
					lines = append(lines,
						"  - path: "+appPath,
						"    domain: "+name+"."+domain,
					)
					cmd.Printf("found %s (%s) → https://%s.%s\n", entry.Name(), det.Framework, name, domain)
					added++
				}
			}
			if added == 0 {
				lines = append(lines,
					"  # - path: ~/projects/app1",
					"  #   domain: app1."+domain,
				)
			}

			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(target, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
				return err
			}
			cmd.Printf("wrote %s (%d app(s))\n", target, added)

			cmd.Println()
			cmd.Println("Two one-time manual steps remain (roost automates everything else):")
			cmd.Println("  1. Point your domain's nameservers at Cloudflare (registrar dashboard, ~24h).")
			cmd.Println("  2. Create an API token: https://dash.cloudflare.com/profile/api-tokens")
			cmd.Println("     Scopes: Zone:DNS:Edit + Account:Cloudflare Tunnel:Edit")
			cmd.Println("     (+ Account:Access: Apps and Policies:Edit if you configure access)")
			cmd.Println("     Then: roost auth login")
			cmd.Println()
			cmd.Println("Next: roost doctor && roost tunnel setup && roost up")
			return nil
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "the domain for app hostnames (with a token stored, roost offers your zones as a picker)")
	cmd.Flags().StringVar(&tunnelName, "tunnel-name", "roost", "name for the Cloudflare tunnel — used verbatim, never generated")
	cmd.Flags().StringVar(&scanDir, "scan", "", "folder whose subdirectories are scanned for apps")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing config")
	return cmd
}

// pickDomain offers the account's actual zones so typos and
// wrong-account mistakes can't happen; falls back to free-text entry
// when no token is available.
func pickDomain(cmd *cobra.Command, home string) (string, error) {
	scanner := bufio.NewScanner(cmd.InOrStdin())
	token, err := tunnel.LoadToken(home)
	if err == nil {
		client := tunnel.NewClient(token)
		if base := os.Getenv("ROOST_CF_API_BASE"); base != "" {
			client.BaseURL = base
		}
		zones, err := client.Zones()
		if err == nil && len(zones) > 0 {
			sort.Slice(zones, func(i, j int) bool { return zones[i].Name < zones[j].Name })
			cmd.Println("Your Cloudflare zones:")
			for i, z := range zones {
				cmd.Printf("  %d) %s (%s)\n", i+1, z.Name, z.Status)
			}
			cmd.Print("Pick a number (or type a different domain): ")
			if !scanner.Scan() {
				return "", fmt.Errorf("no domain chosen")
			}
			answer := strings.TrimSpace(scanner.Text())
			if n, err := strconv.Atoi(answer); err == nil && n >= 1 && n <= len(zones) {
				return zones[n-1].Name, nil
			}
			if answer != "" {
				return answer, nil
			}
			return "", fmt.Errorf("no domain chosen")
		}
	}
	cmd.Print("Domain for your apps (e.g. example.com): ")
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) == "" {
		return "", fmt.Errorf("no domain provided")
	}
	return strings.TrimSpace(scanner.Text()), nil
}
