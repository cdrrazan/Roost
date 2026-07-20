package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cdrrazan/roost/internal/generate"
	"github.com/cdrrazan/roost/internal/shell"
)

func testApps() []generate.App {
	return []generate.App{
		{Name: "blog", FQDN: "blog.example.com", Framework: "rails", Port: 3000, Database: "mysql", Memory: "512m"},
		{Name: "crm", FQDN: "crm.example.com", Framework: "django", Port: 8000, Database: "postgres", Memory: "512m", Profile: "extras"},
		{Name: "site", FQDN: "site.example.com", Framework: "static", Port: 80, Memory: "512m"},
	}
}

func newTestRunner(fake *shell.Fake) (*Runner, *[]time.Duration) {
	sleeps := &[]time.Duration{}
	return &Runner{
		Shell:    fake,
		BuildDir: "/build",
		Stagger:  3 * time.Second,
		Sleep:    func(d time.Duration) { *sleeps = append(*sleeps, d) },
	}, sleeps
}

// allCalls joins every recorded invocation for substring assertions.
func allCalls(fake *shell.Fake) string {
	var lines []string
	for _, c := range fake.Calls {
		lines = append(lines, c.String())
	}
	return strings.Join(lines, "\n")
}

func TestUpPinsProjectNameEverywhere(t *testing.T) {
	fake := &shell.Fake{}
	r, _ := newTestRunner(fake)
	if err := r.Up(testApps(), nil); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if len(fake.Calls) == 0 {
		t.Fatal("Up issued no commands")
	}
	for _, c := range fake.Calls {
		if c.Name != "docker" {
			t.Errorf("call %q should invoke docker", c)
		}
		joined := strings.Join(c.Args, " ")
		if !strings.Contains(joined, "-p roost") {
			t.Errorf("call %q missing -p roost; running from another cwd would create a second stack", c)
		}
		if !strings.Contains(joined, "/build/compose.yml") {
			t.Errorf("call %q missing compose file", c)
		}
	}
}

func TestUpStaggersAppStarts(t *testing.T) {
	fake := &shell.Fake{}
	r, sleeps := newTestRunner(fake)
	if err := r.Up(testApps(), nil); err != nil {
		t.Fatalf("Up: %v", err)
	}
	// Three apps: sleep between each consecutive pair, not before the
	// first.
	if len(*sleeps) != 2 {
		t.Fatalf("slept %d times, want 2 (between 3 apps)", len(*sleeps))
	}
	for _, d := range *sleeps {
		if d != 3*time.Second {
			t.Errorf("sleep = %v, want 3s", d)
		}
	}

	calls := allCalls(fake)
	// Infra (databases, caddy, cloudflared) comes up before any app.
	infraIdx := strings.Index(calls, "caddy")
	blogIdx := strings.Index(calls, "blog")
	if infraIdx < 0 || blogIdx < 0 || infraIdx > blogIdx {
		t.Errorf("infra should start before apps:\n%s", calls)
	}
	for _, svc := range []string{"mysql", "postgres", "cloudflared"} {
		if !strings.Contains(calls, svc) {
			t.Errorf("infra start missing %s:\n%s", svc, calls)
		}
	}
}

func TestUpProfileFiltering(t *testing.T) {
	t.Run("no profile starts everything", func(t *testing.T) {
		fake := &shell.Fake{}
		r, _ := newTestRunner(fake)
		if err := r.Up(testApps(), nil); err != nil {
			t.Fatal(err)
		}
		calls := allCalls(fake)
		for _, app := range []string{"blog", "crm", "site"} {
			if !strings.Contains(calls, app) {
				t.Errorf("app %s not started:\n%s", app, calls)
			}
		}
	})

	t.Run("selected profile starts its apps plus unprofiled ones", func(t *testing.T) {
		fake := &shell.Fake{}
		r, _ := newTestRunner(fake)
		if err := r.Up(testApps(), []string{"extras"}); err != nil {
			t.Fatal(err)
		}
		calls := allCalls(fake)
		if !strings.Contains(calls, "crm") {
			t.Errorf("profiled app crm not started:\n%s", calls)
		}
	})

	t.Run("unselected profile is excluded", func(t *testing.T) {
		fake := &shell.Fake{}
		r, _ := newTestRunner(fake)
		if err := r.Up(testApps(), []string{"core"}); err != nil {
			t.Fatal(err)
		}
		calls := allCalls(fake)
		if strings.Contains(calls, "crm") {
			t.Errorf("crm has profile extras and must not start under --profile core:\n%s", calls)
		}
		if !strings.Contains(calls, "blog") {
			t.Errorf("unprofiled blog should still start:\n%s", calls)
		}
	})
}

func TestComposeLoadsEnvFileWhenPresent(t *testing.T) {
	dir := t.TempDir()
	fake := &shell.Fake{}
	r, _ := newTestRunner(fake)
	r.BuildDir = dir

	if err := r.Down(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(allCalls(fake), "--env-file") {
		t.Errorf("no .env exists yet, --env-file should be absent:\n%s", allCalls(fake))
	}

	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("ROOST_TUNNEL_TOKEN=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake.Calls = nil
	if err := r.Down(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(allCalls(fake), "--env-file") {
		t.Errorf("with .env present the compose call should load it:\n%s", allCalls(fake))
	}
}

func TestDown(t *testing.T) {
	fake := &shell.Fake{}
	r, _ := newTestRunner(fake)
	if err := r.Down(); err != nil {
		t.Fatal(err)
	}
	calls := allCalls(fake)
	if !strings.Contains(calls, "down") || !strings.Contains(calls, "-p roost") {
		t.Errorf("down call wrong:\n%s", calls)
	}
}

func TestRestart(t *testing.T) {
	fake := &shell.Fake{}
	r, _ := newTestRunner(fake)
	if err := r.Restart("blog"); err != nil {
		t.Fatal(err)
	}
	calls := allCalls(fake)
	if !strings.Contains(calls, "restart blog") {
		t.Errorf("restart call wrong:\n%s", calls)
	}
}

func TestLogs(t *testing.T) {
	fake := &shell.Fake{}
	r, _ := newTestRunner(fake)
	if err := r.Logs("blog", true); err != nil {
		t.Fatal(err)
	}
	calls := allCalls(fake)
	if !strings.Contains(calls, "logs") || !strings.Contains(calls, "--follow") || !strings.Contains(calls, "blog") {
		t.Errorf("logs call wrong:\n%s", calls)
	}
}

func TestStatus(t *testing.T) {
	fake := &shell.Fake{
		RunFunc: func(name string, args ...string) (shell.Result, error) {
			joined := strings.Join(args, " ")
			if strings.Contains(joined, "ps") {
				return shell.Result{Stdout: `{"Service":"blog","State":"running","Health":"healthy"}
{"Service":"crm","State":"exited","Health":""}
{"Service":"caddy","State":"running","Health":""}`}, nil
			}
			if strings.Contains(joined, "stats") {
				return shell.Result{Stdout: `{"Name":"roost-blog-1","MemUsage":"120MiB / 512MiB"}`}, nil
			}
			return shell.Result{}, nil
		},
	}
	r, _ := newTestRunner(fake)
	statuses, err := r.Status(testApps())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	byName := map[string]AppStatus{}
	for _, s := range statuses {
		byName[s.Name] = s
	}
	if s := byName["blog"]; s.State != "running" || s.Health != "healthy" || s.URL != "https://blog.example.com" {
		t.Errorf("blog status = %+v", s)
	}
	if !strings.Contains(byName["blog"].Memory, "120MiB") {
		t.Errorf("blog memory = %q, want usage from docker stats", byName["blog"].Memory)
	}
	if s := byName["crm"]; s.State != "exited" {
		t.Errorf("crm status = %+v, want exited", s)
	}
	// site never appeared in ps output.
	if s := byName["site"]; s.State != "not created" {
		t.Errorf("site status = %+v, want not created", s)
	}
}
