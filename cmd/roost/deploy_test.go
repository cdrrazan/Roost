package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/cdrrazan/roost/internal/shell"
)

type fakeStarter struct {
	started []string
	err     error
}

func (f *fakeStarter) Start(app string) error {
	f.started = append(f.started, app)
	return f.err
}

func calls(sh *shell.Fake) string {
	var b strings.Builder
	for _, c := range sh.Calls {
		b.WriteString(c.String())
		b.WriteString("\n")
	}
	return b.String()
}

func TestDeployAppPullsThenStarts(t *testing.T) {
	sh := &shell.Fake{}
	st := &fakeStarter{}

	if err := deployApp(sh, st, "/apps/keeparu", "keeparu"); err != nil {
		t.Fatalf("deployApp: %v", err)
	}

	got := calls(sh)
	for _, want := range []string{"git", "-C /apps/keeparu", "pull", "--ff-only"} {
		if !strings.Contains(got, want) {
			t.Errorf("git call missing %q; got:\n%s", want, got)
		}
	}
	if len(st.started) != 1 || st.started[0] != "keeparu" {
		t.Errorf("Start not called for keeparu: %v", st.started)
	}
}

func TestDeployAppSkipsStartWhenPullFails(t *testing.T) {
	sh := &shell.Fake{RunFunc: func(string, ...string) (shell.Result, error) {
		return shell.Result{}, errors.New("not a fast-forward")
	}}
	st := &fakeStarter{}

	if err := deployApp(sh, st, "/apps/x", "x"); err == nil {
		t.Fatal("expected an error when pull fails")
	}
	if len(st.started) != 0 {
		t.Errorf("Start must not run after a failed pull; started=%v", st.started)
	}
}
