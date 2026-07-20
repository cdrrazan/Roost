package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/cdrrazan/roost/internal/config"
	"github.com/cdrrazan/roost/internal/doctor"
	"github.com/cdrrazan/roost/internal/generate"
	"github.com/cdrrazan/roost/internal/shell"
	"github.com/cdrrazan/roost/internal/state"
	"github.com/cdrrazan/roost/internal/tunnel"
)

// newDoctorCmd runs every preflight check and exits non-zero when a
// hard failure would break `roost up`. Each failure prints a specific
// remedy — never a stack trace.
func newDoctorCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Preflight checks: every failure comes with a specific remedy",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			findings := runDoctor(flags, shell.Exec{})
			cmd.Print(doctor.Summary(findings))
			if doctor.HasFailures(findings) {
				return fmt.Errorf("doctor found problems that will break roost up")
			}
			cmd.Println("all checks passed")
			return nil
		},
	}
}

// runDoctor executes every check it has the prerequisites for,
// degrading gracefully: no token means the Cloudflare checks are
// skipped with a note, not failed.
func runDoctor(flags *rootFlags, sh shell.Runner) []doctor.Finding {
	var findings []doctor.Finding
	add := func(f doctor.Finding) { findings = append(findings, f) }

	// Host tooling.
	findings = append(findings, doctor.CheckDocker(sh)...)
	findings = append(findings, doctor.CheckCloudflared(sh)...)

	// Config.
	cfgPath, err := config.FindConfig(flags.configPath)
	if err != nil {
		add(doctor.Finding{Check: "config", Level: doctor.Fail, Message: err.Error(),
			Remedy: "run `roost init` or create ~/.roost/config.yml"})
		return findings
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		add(doctor.Finding{Check: "config", Level: doctor.Fail, Message: err.Error(),
			Remedy: "fix the YAML in " + cfgPath})
		return findings
	}
	resolved, skipped, err := config.Resolve(cfg)
	if err != nil {
		add(doctor.Finding{Check: "hostnames", Level: doctor.Fail, Message: err.Error(),
			Remedy: "fix the colliding or invalid domain in " + cfgPath})
		return findings
	}
	add(doctor.Finding{Check: "config", Level: doctor.OK,
		Message: fmt.Sprintf("%s parses; %d app(s) resolve to hostnames", cfgPath, len(resolved))})

	for _, app := range skipped {
		add(doctor.Finding{Check: "app:" + app.Name, Level: doctor.Fail,
			Message: "skipped: " + app.Reason,
			Remedy:  "set a domain (or fix the path) for " + app.Name + " in " + cfgPath})
	}

	// Detection + memory budget.
	apps, err := generate.Plan(cfg, resolved)
	if err != nil {
		add(doctor.Finding{Check: "detect", Level: doctor.Fail, Message: err.Error(),
			Remedy: "set framework: explicitly for the app in " + cfgPath})
	} else {
		findings = append(findings, doctor.CheckMemoryBudget(memoryCaps(apps))...)
	}

	// Cloudflare-side checks need a token.
	home, err := os.UserHomeDir()
	if err != nil {
		return findings
	}
	token, err := tunnel.LoadToken(home)
	if err != nil {
		add(doctor.Finding{Check: "cloudflare", Level: doctor.Warn,
			Message: "no API token available, skipping zone/DNS/SSL checks",
			Remedy:  err.Error()})
		return findings
	}
	client := tunnel.NewClient(token)
	if base := os.Getenv("ROOST_CF_API_BASE"); base != "" {
		client.BaseURL = base
	}
	st, err := state.Load(state.Path(home))
	if err != nil {
		st = &state.State{}
	}

	var hostnames []string
	for _, app := range resolved {
		hostnames = append(hostnames, app.FQDN)
	}
	findings = append(findings, doctor.CheckCloudflare(client, st, tunnelName(cfg), hostnames)...)
	return findings
}

func memoryCaps(apps []generate.App) map[string]string {
	caps := make(map[string]string, len(apps))
	for _, app := range apps {
		caps[app.Name] = app.Memory
	}
	return caps
}
