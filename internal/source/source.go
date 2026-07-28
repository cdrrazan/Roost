// Package source manages the app checkouts roost owns under
// ~/.roost/sources. `roost add --repo` clones into it; `roost update`
// pulls it. All git runs through a shell.Runner so tests never touch a
// real repository or the network.
package source

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cdrrazan/roost/internal/config"
	"github.com/cdrrazan/roost/internal/shell"
)

// Dir returns ~/.roost/sources, creating it. This is roost's own
// directory — cloning here never writes into the user's existing repos.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".roost", "sources")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// PathFor returns the managed checkout path for an app name.
func PathFor(name string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// Clone clones repo into dest. dest must not already exist; its parent
// is created. git runs through the Runner (Stream, so progress shows).
func Clone(r shell.Runner, repo, dest string) error {
	if strings.TrimSpace(repo) == "" {
		return fmt.Errorf("clone: empty repository URL")
	}
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("clone: destination already exists: %s (remove it or pick another name)", dest)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("clone: create sources dir: %w", err)
	}
	if err := r.Stream("git", "clone", repo, dest); err != nil {
		return fmt.Errorf("git clone %s: %w", repo, err)
	}
	return nil
}

// NameFromRepo derives an app name from a git URL: the basename minus a
// trailing slash and ".git", slugified the same way config resolves an
// app's name from its path — so `add --repo` and detection agree.
func NameFromRepo(repo string) string {
	s := strings.TrimRight(strings.TrimSpace(repo), "/")
	s = strings.TrimSuffix(s, ".git")
	if i := strings.LastIndexAny(s, "/:"); i >= 0 {
		s = s[i+1:]
	}
	return config.Slugify(s)
}
