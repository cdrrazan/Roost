package main

import (
	"errors"
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
