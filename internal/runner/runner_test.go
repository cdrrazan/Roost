package runner

import (
	"errors"
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

func TestRemoveStopsAndRemovesContainer(t *testing.T) {
	fake := &shell.Fake{}
	r, _ := newTestRunner(fake)
	if err := r.Remove("blog", false); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	calls := allCalls(fake)
	if !strings.Contains(calls, "rm -sf blog") {
		t.Errorf("Remove should stop+remove the container:\n%s", calls)
	}
	// Without deleteImage, the image is left in place for a fast re-add.
	if strings.Contains(calls, "image rm") {
		t.Errorf("Remove(false) must not delete the image:\n%s", calls)
	}
}

func TestRemoveWithImageDeletesImage(t *testing.T) {
	fake := &shell.Fake{}
	r, _ := newTestRunner(fake)
	if err := r.Remove("blog", true); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	calls := allCalls(fake)
	if !strings.Contains(calls, "image rm -f roost-blog") {
		t.Errorf("Remove(true) should delete the built image roost-blog:\n%s", calls)
	}
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

func TestUpReloadsCaddyAfterApps(t *testing.T) {
	fake := &shell.Fake{}
	r, _ := newTestRunner(fake)
	if err := r.Up(testApps(), nil); err != nil {
		t.Fatalf("Up: %v", err)
	}
	calls := allCalls(fake)
	reloadIdx := strings.Index(calls, "caddy reload")
	if reloadIdx < 0 {
		t.Fatalf("Up should reload caddy to drop stale upstream connections:\n%s", calls)
	}
	// The reload must run after apps (re)start, otherwise it can't clear
	// Caddy's stale keep-alives to freshly-recreated app containers.
	appUpIdx := strings.LastIndex(calls, "up -d blog")
	if appUpIdx < 0 || reloadIdx < appUpIdx {
		t.Errorf("caddy reload must run after app starts:\n%s", calls)
	}
}

func TestStartBuildsUpsAndReloadsCaddy(t *testing.T) {
	fake := &shell.Fake{}
	r, _ := newTestRunner(fake)
	if err := r.Start("blog"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	calls := allCalls(fake)
	for _, want := range []string{"build blog", "up -d blog", "caddy reload"} {
		if !strings.Contains(calls, want) {
			t.Errorf("Start missing %q:\n%s", want, calls)
		}
	}
}

func TestStopStopsOnlyThatApp(t *testing.T) {
	fake := &shell.Fake{}
	r, _ := newTestRunner(fake)
	if err := r.Stop("blog"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	calls := allCalls(fake)
	if !strings.Contains(calls, "stop blog") {
		t.Errorf("Stop should stop blog:\n%s", calls)
	}
	// Must not tear the whole stack down.
	if strings.Contains(calls, " down") {
		t.Errorf("Stop must not down the stack:\n%s", calls)
	}
}

func TestResumeStartsWithoutRebuilding(t *testing.T) {
	fake := &shell.Fake{}
	r, _ := newTestRunner(fake)
	if err := r.Resume("blog"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	calls := allCalls(fake)
	if !strings.Contains(calls, "start blog") {
		t.Errorf("Resume should start blog:\n%s", calls)
	}
	// The fast path: no rebuild, no up (the build-dir path also contains the
	// word "build", so match the subcommands, not the substring).
	if strings.Contains(calls, "build blog") || strings.Contains(calls, "up -d") {
		t.Errorf("Resume must not build or up:\n%s", calls)
	}
}

func TestUpStreamsBuilds(t *testing.T) {
	// First builds take minutes (base image pulls, dependency installs);
	// their output must stream to the terminal, not be captured — a
	// captured build looks like a frozen roost.
	fake := &shell.Fake{}
	r, _ := newTestRunner(fake)
	if err := r.Up(testApps(), nil); err != nil {
		t.Fatal(err)
	}
	var sawBuild bool
	for _, c := range fake.Calls {
		joined := strings.Join(c.Args, " ")
		if strings.Contains(joined, " build ") || strings.HasSuffix(joined, " build blog") {
			sawBuild = true
		}
		if strings.Contains(joined, "--build") {
			t.Errorf("call %q hides the build behind a captured `up --build`; build must be a separate streamed step", c)
		}
	}
	if !sawBuild {
		t.Errorf("no explicit build step issued:\n%s", allCalls(fake))
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

func TestPrepareRunsSetupThenSeed(t *testing.T) {
	fake := &shell.Fake{}
	r, _ := newTestRunner(fake)
	apps := []generate.App{
		{Name: "blog", FQDN: "blog.example.com", Framework: "rails", Database: "mysql", SetupCommand: "bin/rails db:prepare", SeedCommand: "bin/rails db:seed"},
		// A profiled app not selected: must be skipped entirely.
		{Name: "crm", FQDN: "crm.example.com", Framework: "django", Database: "postgres", Profile: "extras", SetupCommand: "python manage.py migrate --noinput", SeedCommand: "python manage.py seed"},
		// No seed command: setup only.
		{Name: "wiki", FQDN: "wiki.example.com", Framework: "rails", Database: "mysql", SetupCommand: "bin/rails db:prepare"},
	}
	var seeded []string
	shouldSeed := func(string) bool { return true }
	onSeeded := func(name string) error { seeded = append(seeded, name); return nil }

	// Select the default profile only, so the "extras"-profiled crm is excluded.
	if err := r.Prepare(apps, []string{"default"}, shouldSeed, onSeeded); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	out := allCalls(fake)

	// blog: setup then seed, seed carries SEED_DEMO=1.
	if !strings.Contains(out, "exec -T blog sh -lc bin/rails db:prepare") {
		t.Errorf("missing blog setup exec:\n%s", out)
	}
	if !strings.Contains(out, "-e SEED_DEMO=1 blog sh -lc bin/rails db:seed") {
		t.Errorf("blog seed must run with SEED_DEMO=1:\n%s", out)
	}
	// crm is profiled-out: no calls for it at all.
	if strings.Contains(out, " crm ") {
		t.Errorf("profiled-out crm should be skipped:\n%s", out)
	}
	// wiki: setup only, never seeded.
	if !strings.Contains(out, "exec -T wiki sh -lc bin/rails db:prepare") {
		t.Errorf("missing wiki setup exec:\n%s", out)
	}
	if strings.Contains(out, "SEED_DEMO=1 wiki") {
		t.Errorf("wiki has no seed command; must not be seeded:\n%s", out)
	}
	if len(seeded) != 1 || seeded[0] != "blog" {
		t.Errorf("onSeeded called for %v, want [blog]", seeded)
	}
}

func TestMysqlVolumeID(t *testing.T) {
	fake := &shell.Fake{RunFunc: func(name string, args ...string) (shell.Result, error) {
		if name == "docker" && len(args) >= 2 && args[0] == "volume" && args[1] == "inspect" {
			return shell.Result{Stdout: "2026-07-21T04:31:40Z\n"}, nil
		}
		return shell.Result{}, nil
	}}
	r, _ := newTestRunner(fake)
	id, err := r.MysqlVolumeID()
	if err != nil {
		t.Fatalf("MysqlVolumeID: %v", err)
	}
	if id != "2026-07-21T04:31:40Z" {
		t.Errorf("id = %q, want the trimmed CreatedAt", id)
	}
	if !strings.Contains(allCalls(fake), "volume inspect roost_roost-mysql-data --format {{.CreatedAt}}") {
		t.Errorf("unexpected inspect call:\n%s", allCalls(fake))
	}
}

func TestMysqlVolumeIDAbsentVolumeIsUnknown(t *testing.T) {
	// An absent volume (nothing created yet, or removed) is not an error —
	// the identity is simply unknown so drift detection is skipped.
	fake := &shell.Fake{RunFunc: func(name string, args ...string) (shell.Result, error) {
		return shell.Result{Stderr: "Error: No such volume"}, errors.New("exit status 1")
	}}
	r, _ := newTestRunner(fake)
	id, err := r.MysqlVolumeID()
	if err != nil {
		t.Fatalf("absent volume must not error: %v", err)
	}
	if id != "" {
		t.Errorf("absent volume id = %q, want empty", id)
	}
}

func TestPrepareDoesNotMarkFailedSeed(t *testing.T) {
	// The seed exec fails; setup succeeds. The app must NOT be recorded as
	// seeded, and Prepare must surface the failure.
	fake := &shell.Fake{RunFunc: func(name string, args ...string) (shell.Result, error) {
		for _, a := range args {
			if a == "SEED_DEMO=1" {
				return shell.Result{Stderr: "seed boom"}, errors.New("exit status 1")
			}
		}
		return shell.Result{}, nil
	}}
	r, _ := newTestRunner(fake)
	apps := []generate.App{
		{Name: "blog", FQDN: "blog.example.com", Framework: "rails", Database: "mysql", SetupCommand: "bin/rails db:prepare", SeedCommand: "bin/rails db:seed"},
	}
	var seeded []string
	err := r.Prepare(apps, nil, func(string) bool { return true }, func(n string) error {
		seeded = append(seeded, n)
		return nil
	})
	if err == nil {
		t.Fatal("Prepare must return an error when a seed fails")
	}
	if len(seeded) != 0 {
		t.Errorf("a failed seed must not be marked; onSeeded fired for %v", seeded)
	}
	if !strings.Contains(allCalls(fake), "db:prepare") {
		t.Error("setup should still have run before the failing seed")
	}
}

func TestPrepareSkipsSeedWhenShouldSeedFalse(t *testing.T) {
	fake := &shell.Fake{}
	r, _ := newTestRunner(fake)
	apps := []generate.App{
		{Name: "blog", FQDN: "blog.example.com", Framework: "rails", Database: "mysql", SetupCommand: "bin/rails db:prepare", SeedCommand: "bin/rails db:seed"},
	}
	called := false
	if err := r.Prepare(apps, nil, func(string) bool { return false }, func(string) error { called = true; return nil }); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	out := allCalls(fake)
	// Setup still runs; seed does not.
	if !strings.Contains(out, "db:prepare") {
		t.Errorf("setup should still run:\n%s", out)
	}
	if strings.Contains(out, "SEED_DEMO=1") {
		t.Errorf("seed must be skipped when shouldSeed is false:\n%s", out)
	}
	if called {
		t.Error("onSeeded must not fire when seeding is skipped")
	}
}
