package main

import (
	"errors"
	"testing"

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
