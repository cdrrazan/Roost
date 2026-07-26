package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cdrrazan/roost/internal/doctor"
	"github.com/cdrrazan/roost/internal/generate"
)

type fakeStopper struct {
	stopped, resumed []string
	failOn           string
}

func (f *fakeStopper) Stop(app string) error {
	f.stopped = append(f.stopped, app)
	if app == f.failOn {
		return errors.New("boom")
	}
	return nil
}

func (f *fakeStopper) Resume(app string) error {
	f.resumed = append(f.resumed, app)
	if app == f.failOn {
		return errors.New("boom")
	}
	return nil
}

// stopApps must stop every app (leaving infra alone) even when one fails, and
// surface the failure.
func TestStopAppsStopsEveryAppAndJoinsErrors(t *testing.T) {
	apps := []generate.App{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	f := &fakeStopper{failOn: "b"}

	err := stopApps(f, apps)

	if len(f.stopped) != 3 {
		t.Fatalf("stopped %v, want all 3 attempted", f.stopped)
	}
	if err == nil {
		t.Fatal("expected an error from the failing app")
	}
}

func TestStopAppsAllSucceed(t *testing.T) {
	f := &fakeStopper{}
	if err := stopApps(f, []generate.App{{Name: "x"}, {Name: "y"}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.stopped) != 2 {
		t.Fatalf("stopped %v, want 2", f.stopped)
	}
}

func TestStartAppsResumesEveryApp(t *testing.T) {
	f := &fakeStopper{}
	if err := startApps(f, []generate.App{{Name: "a"}, {Name: "b"}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.resumed) != 2 {
		t.Fatalf("resumed %v, want 2", f.resumed)
	}
}

func TestStartAppsJoinsErrors(t *testing.T) {
	f := &fakeStopper{failOn: "b"}
	err := startApps(f, []generate.App{{Name: "a"}, {Name: "b"}, {Name: "c"}})
	if len(f.resumed) != 3 {
		t.Fatalf("resumed %v, want all 3 attempted", f.resumed)
	}
	if err == nil {
		t.Fatal("expected an error from the failing app")
	}
}

// resolveAppName gates the per-app panel actions: only a name that resolves to
// a configured app is accepted, so the panel can never stop/start an infra
// container (caddy, cloudflared) by posting an arbitrary service name.
func TestResolveAppNameAcceptsKnown(t *testing.T) {
	apps := []generate.App{{Name: "blog"}, {Name: "shop"}}
	if err := resolveAppName(apps, "shop"); err != nil {
		t.Fatalf("known app rejected: %v", err)
	}
}

func TestResolveAppNameRejectsUnknown(t *testing.T) {
	apps := []generate.App{{Name: "blog"}}
	if err := resolveAppName(apps, "caddy"); err == nil {
		t.Fatal("unknown app (infra service) must be rejected")
	}
}

// diffNewApps finds the app(s) an add introduced, so AddApp knows which
// container(s) to build + start without rebuilding the whole stack.
func TestDiffNewApps(t *testing.T) {
	before := appNameSet([]generate.App{{Name: "a"}, {Name: "b"}})
	got := diffNewApps(before, []generate.App{{Name: "a"}, {Name: "b"}, {Name: "c"}})
	if len(got) != 1 || got[0] != "c" {
		t.Fatalf("new apps = %v, want [c]", got)
	}
}

func TestDiffNewAppsNoneWhenUnchanged(t *testing.T) {
	before := appNameSet([]generate.App{{Name: "a"}})
	if got := diffNewApps(before, []generate.App{{Name: "a"}}); len(got) != 0 {
		t.Fatalf("new apps = %v, want none", got)
	}
}

// The add gate surfaces the first doctor failure — message and remedy — so the
// panel's processing pane tells the user how to fix it, never a bare error.
// repoURL reads an app's .git/config and normalizes the origin remote to a
// browsable https URL, whatever protocol the clone used. No git remote → "".
func TestRepoURL(t *testing.T) {
	cases := []struct {
		name   string
		remote string // origin url in .git/config; "" means no .git at all
		want   string
	}{
		{"scp-ssh", "git@github.com:cdrrazan/roost.git", "https://github.com/cdrrazan/roost"},
		{"ssh-url", "ssh://git@github.com/cdrrazan/roost.git", "https://github.com/cdrrazan/roost"},
		{"https", "https://github.com/cdrrazan/roost.git", "https://github.com/cdrrazan/roost"},
		{"https-no-suffix", "https://github.com/cdrrazan/roost", "https://github.com/cdrrazan/roost"},
		{"gitlab-ssh", "git@gitlab.com:group/thing.git", "https://gitlab.com/group/thing"},
		{"no-repo", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.remote != "" {
				gitdir := filepath.Join(dir, ".git")
				if err := os.MkdirAll(gitdir, 0o755); err != nil {
					t.Fatal(err)
				}
				cfg := "[core]\n\trepositoryformatversion = 0\n[remote \"origin\"]\n\turl = " + tc.remote + "\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n"
				if err := os.WriteFile(filepath.Join(gitdir, "config"), []byte(cfg), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := repoURL(dir); got != tc.want {
				t.Errorf("repoURL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFirstFailureMessageIncludesRemedy(t *testing.T) {
	findings := []doctor.Finding{
		{Level: doctor.OK, Message: "fine"},
		{Level: doctor.Fail, Message: "docker not running", Remedy: "start Docker Desktop"},
	}
	got := firstFailureMessage(findings)
	if !strings.Contains(got, "docker not running") || !strings.Contains(got, "start Docker Desktop") {
		t.Fatalf("message = %q, want it to carry the failure and its remedy", got)
	}
}
