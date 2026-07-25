package main

import (
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/cdrrazan/roost/internal/generate"
)

// buildDir returns ~/.roost/build, creating it if needed. ALL
// generated artifacts live here — roost never writes into the user's
// app directories.
func buildDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".roost", "build")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// newGenerateCmd writes all build artifacts (compose.yml, Caddyfile,
// Dockerfiles, database init scripts) under ~/.roost/build without
// starting anything — useful for inspecting what `up` would run.
func newGenerateCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "generate",
		Short: "Write ~/.roost/build artifacts without starting anything",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, resolved, skipped, err := loadResolved(flags)
			if err != nil {
				return err
			}
			for _, app := range skipped {
				cmd.Printf("skipped: %s (%s)\n", app.Name, app.Reason)
			}
			if len(resolved) == 0 {
				cmd.Println("nothing to generate: no app resolves to a hostname")
				return nil
			}

			apps, err := generate.Plan(cfg, resolved)
			if err != nil {
				return err
			}
			dir, err := buildDir()
			if err != nil {
				return err
			}
			written, err := generate.Generate(dir, apps, cfg.ControlHost)
			if err != nil {
				return err
			}
			for _, path := range written {
				cmd.Println("wrote", path)
			}
			return nil
		},
	}
}
