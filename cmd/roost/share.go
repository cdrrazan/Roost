package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/cdrrazan/roost/internal/generate"
)

// shareHost builds the temporary hostname for `roost share`. It keeps the
// app's routing suffix — so the existing wildcard DNS record, the tunnel's
// wildcard ingress, and one-level Cloudflare Universal SSL all already cover
// it — and swaps the leftmost label for sub. That's why share is a purely
// local Caddy change: nothing new is needed at the edge.
func shareHost(appFQDN, sub string) (string, error) {
	i := strings.IndexByte(appFQDN, '.')
	if i < 0 {
		return "", fmt.Errorf("app hostname %q has no domain suffix to share under", appFQDN)
	}
	return sub + "." + appFQDN[i+1:], nil
}

// newShareCmd exposes one running app at a temporary hostname on your own
// domain — spiritually `cloudflared tunnel --url`, but the URL is yours. It
// adds a Caddy route, reloads, prints the URL, and blocks until Ctrl-C, then
// removes the route. Nothing is created at the edge (the wildcard already
// covers it) so there's nothing to leak if the process is killed.
func newShareCmd(flags *rootFlags) *cobra.Command {
	var sub string
	cmd := &cobra.Command{
		Use:   "share <app>",
		Short: "Expose one app at a temporary hostname on your domain until Ctrl-C",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			apps, opts, err := loadPlanned(cmd, flags)
			if err != nil {
				return err
			}
			var target *generate.App
			for i := range apps {
				if apps[i].Name == name {
					target = &apps[i]
					break
				}
			}
			if target == nil {
				return fmt.Errorf("app %q not found in the config", name)
			}
			if target.FQDN == "" {
				return fmt.Errorf("app %q is a worker with no hostname — nothing to share", name)
			}
			if sub == "" {
				sub = "share-" + name
			}
			host, err := shareHost(target.FQDN, sub)
			if err != nil {
				return err
			}

			dir, err := buildDir()
			if err != nil {
				return err
			}
			r, err := newRunner()
			if err != nil {
				return err
			}

			// A synthetic app produces the extra Caddy route host -> app:port.
			shareApp := generate.App{Name: target.Name, FQDN: host, Port: target.Port, Framework: target.Framework}
			writeCaddy := func(extra ...generate.App) error {
				data, err := generate.RenderCaddyfile(append(append([]generate.App{}, apps...), extra...), opts.ControlHost)
				if err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(dir, "Caddyfile"), data, 0o644)
			}

			if err := writeCaddy(shareApp); err != nil {
				return err
			}
			if err := r.ReloadProxy(); err != nil {
				return fmt.Errorf("reload proxy with the share route: %w", err)
			}
			cmd.Printf("sharing %s at https://%s\n", name, host)
			cmd.Println("press Ctrl-C to stop sharing")

			sig := make(chan os.Signal, 1)
			signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
			<-sig

			// Restore the Caddyfile without the share route and reload.
			cmd.Println("\nremoving the share route…")
			if err := writeCaddy(); err != nil {
				return err
			}
			if err := r.ReloadProxy(); err != nil {
				return fmt.Errorf("restore proxy after sharing: %w", err)
			}
			cmd.Printf("stopped sharing %s\n", name)
			return nil
		},
	}
	cmd.Flags().StringVar(&sub, "as", "", "subdomain label for the share (default share-<app>)")
	return cmd
}
