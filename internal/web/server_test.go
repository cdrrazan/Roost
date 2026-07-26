package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cdrrazan/roost/internal/runner"
)

// fakeController records calls and never touches Docker. If release is
// non-nil, Up/Down/StartApp/StopApp block on it so a test can observe the
// "busy" window.
type fakeController struct {
	statuses       []runner.AppStatus
	statusErr      error
	up, down       int32
	upErr, downErr error
	release        chan struct{}

	mu               sync.Mutex
	started, stopped []string

	addPath, addDomain string
	addCalls           int
	removeName         string
	removeImage        bool
	removeCalls        int
	removed            []RemovedApp
	server             ServerInfo
	system             SystemInfo
	edge               EdgeInfo
	details            map[string]AppDetail
	emitLines          []string // lines AddApp/RemoveApp emit when called
}

func (f *fakeController) Status() ([]runner.AppStatus, error) { return f.statuses, f.statusErr }

func (f *fakeController) Up() error {
	atomic.AddInt32(&f.up, 1)
	if f.release != nil {
		<-f.release
	}
	return f.upErr
}

func (f *fakeController) Down() error {
	atomic.AddInt32(&f.down, 1)
	if f.release != nil {
		<-f.release
	}
	return f.downErr
}

func (f *fakeController) StartApp(name string) error {
	f.mu.Lock()
	f.started = append(f.started, name)
	f.mu.Unlock()
	if f.release != nil {
		<-f.release
	}
	return f.upErr
}

func (f *fakeController) StopApp(name string) error {
	f.mu.Lock()
	f.stopped = append(f.stopped, name)
	f.mu.Unlock()
	if f.release != nil {
		<-f.release
	}
	return f.downErr
}

func (f *fakeController) startedApps() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.started...)
}

func (f *fakeController) stoppedApps() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.stopped...)
}

func (f *fakeController) AddApp(path, domain string, emit func(string)) error {
	f.mu.Lock()
	f.addPath, f.addDomain, f.addCalls = path, domain, f.addCalls+1
	lines := append([]string(nil), f.emitLines...)
	f.mu.Unlock()
	for _, l := range lines {
		emit(l)
	}
	if f.release != nil {
		<-f.release
	}
	return f.upErr
}

func (f *fakeController) RemoveApp(name string, deleteImage bool, emit func(string)) error {
	f.mu.Lock()
	f.removeName, f.removeImage, f.removeCalls = name, deleteImage, f.removeCalls+1
	lines := append([]string(nil), f.emitLines...)
	f.mu.Unlock()
	for _, l := range lines {
		emit(l)
	}
	if f.release != nil {
		<-f.release
	}
	return f.downErr
}

func (f *fakeController) RemovedApps() ([]RemovedApp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]RemovedApp(nil), f.removed...), nil
}

func (f *fakeController) ServerInfo() ServerInfo { return f.server }
func (f *fakeController) SystemInfo() SystemInfo { return f.system }
func (f *fakeController) EdgeInfo() EdgeInfo     { return f.edge }

func (f *fakeController) AppDetail(name string) (AppDetail, error) {
	if d, ok := f.details[name]; ok {
		return d, nil
	}
	return AppDetail{}, errString("unknown app")
}

func (f *fakeController) snapshot() fakeController {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fakeController{
		addPath: f.addPath, addDomain: f.addDomain, addCalls: f.addCalls,
		removeName: f.removeName, removeImage: f.removeImage, removeCalls: f.removeCalls,
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within 1s")
}

func serve(srv *Server, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	return rr
}

func TestStatusRendersApps(t *testing.T) {
	f := &fakeController{statuses: []runner.AppStatus{
		{Name: "keeparu", State: "running", URL: "https://keeparu.byaru.com"},
		{Name: "sure", State: "exited"},
	}}
	rr := serve(NewServer(f, ""), "GET", "/", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"keeparu", "running", "sure", "exited", "https://keeparu.byaru.com"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}

func TestAppCardShowsBadgesAndMetrics(t *testing.T) {
	f := &fakeController{statuses: []runner.AppStatus{
		{Name: "keeparu", State: "running", URL: "https://keeparu.byaru.com",
			Framework: "rails", Database: "mysql", Redis: true, Runtime: "3.3.0",
			Memory: "160MiB / 512MiB", CPU: "2.75%", Net: "1.2MB / 800kB", Up: "Up 3 hours"},
	}}
	rr := serve(NewServer(f, ""), "GET", "/", nil)
	body := rr.Body.String()
	for _, want := range []string{"Rails", "MySQL", "Redis", "3.3.0", "2.75%", "1.2MB", "Up 3 hours"} {
		if !strings.Contains(body, want) {
			t.Errorf("card missing %q", want)
		}
	}
}

func TestDashboardCardsAndAlerts(t *testing.T) {
	f := &fakeController{
		statuses: []runner.AppStatus{
			{Name: "up-app", State: "running", URL: "https://a.example.com", HTTP: "200", Reachable: true},
			{Name: "down-app", State: "exited", URL: "https://b.example.com"},
			{Name: "broken", State: "running", URL: "https://c.example.com", HTTP: "502", Reachable: false},
		},
		system: SystemInfo{Images: 12, ImagesSize: "3.1GB", Containers: 14, Volumes: 5, VolumesSize: "1.2GB", Reclaimable: "800MB (25%)"},
		edge:   EdgeInfo{TunnelName: "roost", TunnelID: "abcdef012345", Account: "acc123", Hosts: []string{"byaru.com"}, Protected: true},
	}
	body := serve(NewServer(f, ""), "GET", "/", nil).Body.String()
	for _, want := range []string{
		`class="alerts"`, "Down App is exited", "Broken is up but returns 502", // alerts (names humanized)
		"System", "3.1GB", "800MB (25%)", // system card
		"Edge", "roost", "protected", // edge card
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
}

func TestAppDetailEndpoint(t *testing.T) {
	f := &fakeController{details: map[string]AppDetail{
		"keeparu": {Name: "keeparu", Image: "roost-keeparu", Status: "Up 2 hours", Restarts: 1, Logs: "listening on 0.0.0.0:3000"},
	}}
	s := NewServer(f, "")
	// Known app → JSON detail.
	rr := serve(s, "GET", "/api/app?name=keeparu", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{`"image":"roost-keeparu"`, `"status":"Up 2 hours"`, "0.0.0.0:3000"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail JSON missing %q; got %s", want, body)
		}
	}
	// Unknown app → 404, never a zero-value 200.
	if rr := serve(s, "GET", "/api/app?name=nope", nil); rr.Code != http.StatusNotFound {
		t.Errorf("unknown app code = %d, want 404", rr.Code)
	}
	// Missing name → 400.
	if rr := serve(s, "GET", "/api/app", nil); rr.Code != http.StatusBadRequest {
		t.Errorf("missing name code = %d, want 400", rr.Code)
	}
}

func TestSparklineAppearsAfterSamples(t *testing.T) {
	f := &fakeController{statuses: []runner.AppStatus{{Name: "x", State: "running", URL: "https://x", CPU: "2.5%"}}}
	s := NewServer(f, "")
	serve(s, "GET", "/", nil)                       // sample 1
	body := serve(s, "GET", "/", nil).Body.String() // sample 2 → sparkline has ≥2 points
	if !strings.Contains(body, "cpuspark") {
		t.Error("expected a cpu sparkline after two samples")
	}
}

func TestAppCardShowsReachabilityChip(t *testing.T) {
	f := &fakeController{statuses: []runner.AppStatus{
		{Name: "up-app", State: "running", URL: "https://a.example.com", HTTP: "200", Reachable: true},
		{Name: "bad-app", State: "running", URL: "https://b.example.com", HTTP: "502", Reachable: false},
	}}
	body := serve(NewServer(f, ""), "GET", "/", nil).Body.String()
	if !strings.Contains(body, `class="rchip up"`) || !strings.Contains(body, `class="rchip down"`) {
		t.Error("reachability chips (up/down) not both rendered")
	}
	if !strings.Contains(body, "502") {
		t.Error("down chip should show the failing HTTP code")
	}
}

func TestCommandPaletteRenders(t *testing.T) {
	body := serve(NewServer(&fakeController{}, ""), "GET", "/", nil).Body.String()
	for _, want := range []string{`id="palette"`, `id="palbtn"`, `id="pal-list"`} {
		if !strings.Contains(body, want) {
			t.Errorf("command palette missing %q", want)
		}
	}
}

func TestPublicStatusPage(t *testing.T) {
	f := &fakeController{statuses: []runner.AppStatus{
		{Name: "keeparu", State: "running", URL: "https://keeparu.byaru.com", HTTP: "200", Reachable: true, Up: "Up 3 hours"},
		{Name: "broken", State: "exited", URL: "https://broken.byaru.com"},
	}}
	rr := serve(NewServer(f, ""), "GET", "/status", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"Keeparu", "Operational", "Broken", "Down", "1/2 services operational"} {
		if !strings.Contains(body, want) {
			t.Errorf("status page missing %q", want)
		}
	}
	// Must not leak controls or the token into a page meant to be public.
	for _, bad := range []string{`action="/remove"`, `action="/up"`, "New app"} {
		if strings.Contains(body, bad) {
			t.Errorf("public status page leaks control %q", bad)
		}
	}
}

func TestActivityTimelineRecordsActions(t *testing.T) {
	f := &fakeController{}
	s := NewServer(f, "")
	serve(s, "POST", "/up", nil) // fires Up in a goroutine, then records an event
	waitFor(t, func() bool {
		return strings.Contains(serve(s, "GET", "/", nil).Body.String(), `class="timeline"`)
	})
	body := serve(s, "GET", "/", nil).Body.String()
	if !strings.Contains(body, "complete") {
		t.Error("timeline should record the completed action")
	}
}

func TestSidebarCollapseTogglesRender(t *testing.T) {
	rr := serve(NewServer(&fakeController{}, ""), "GET", "/", nil)
	body := rr.Body.String()
	for _, want := range []string{`id="sidetgl"`, `id="railtgl"`} {
		if !strings.Contains(body, want) {
			t.Errorf("collapse toggle %q missing from topbar", want)
		}
	}
}

func TestMemoryByAppBarsCarryHoverData(t *testing.T) {
	f := &fakeController{statuses: []runner.AppStatus{
		{Name: "keeparu", State: "running", Memory: "160MiB / 512MiB"},
	}}
	rr := serve(NewServer(f, ""), "GET", "/", nil)
	body := rr.Body.String()
	// Each spark bar must expose the app name + memory so the hover tooltip
	// can name which app owns which bar.
	for _, want := range []string{`data-app="Keeparu"`, `data-mem=`, `160MiB / 512MiB`} {
		if !strings.Contains(body, want) {
			t.Errorf("memory-by-app bar missing %q", want)
		}
	}
}

func TestStatusRendersRepoLink(t *testing.T) {
	f := &fakeController{statuses: []runner.AppStatus{
		{Name: "keeparu", State: "running", URL: "https://keeparu.byaru.com", Repo: "https://github.com/cdrrazan/keeparu"},
		{Name: "sure", State: "exited"}, // no repo → no link
	}}
	rr := serve(NewServer(f, ""), "GET", "/", nil)
	body := rr.Body.String()
	if !strings.Contains(body, `href="https://github.com/cdrrazan/keeparu"`) {
		t.Error("body missing repo link for keeparu")
	}
	// The repo link must open the code host in a new tab, not navigate the panel.
	if !strings.Contains(body, `href="https://github.com/cdrrazan/keeparu" target="_blank"`) {
		t.Error("repo link should open in a new tab")
	}
}

func TestStatusErrorShown(t *testing.T) {
	f := &fakeController{statusErr: errString("compose ps failed")}
	rr := serve(NewServer(f, ""), "GET", "/", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "compose ps failed") {
		t.Error("status error not rendered")
	}
}

func TestUpInvokesController(t *testing.T) {
	f := &fakeController{}
	rr := serve(NewServer(f, ""), "POST", "/up", nil)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("code = %d, want 303", rr.Code)
	}
	waitFor(t, func() bool { return atomic.LoadInt32(&f.up) == 1 })
	if atomic.LoadInt32(&f.down) != 0 {
		t.Error("down should not have been called")
	}
}

func TestDownInvokesController(t *testing.T) {
	f := &fakeController{}
	serve(NewServer(f, ""), "POST", "/down", nil)
	waitFor(t, func() bool { return atomic.LoadInt32(&f.down) == 1 })
}

func TestTokenGuard(t *testing.T) {
	f := &fakeController{}
	srv := NewServer(f, "s3cret")

	// Missing token: rejected, controller untouched.
	if rr := serve(srv, "POST", "/up", nil); rr.Code != http.StatusForbidden {
		t.Fatalf("no-token code = %d, want 403", rr.Code)
	}
	if atomic.LoadInt32(&f.up) != 0 {
		t.Fatal("up ran without a valid token")
	}

	// Correct token via header: accepted.
	rr := serve(srv, "POST", "/up", map[string]string{"X-Roost-Token": "s3cret"})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("token code = %d, want 303", rr.Code)
	}
	waitFor(t, func() bool { return atomic.LoadInt32(&f.up) == 1 })
}

func TestAppUpInvokesController(t *testing.T) {
	f := &fakeController{}
	rr := serve(NewServer(f, ""), "POST", "/app/up?app=blog", nil)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("code = %d, want 303", rr.Code)
	}
	waitFor(t, func() bool {
		s := f.startedApps()
		return len(s) == 1 && s[0] == "blog"
	})
	if len(f.stoppedApps()) != 0 {
		t.Error("StopApp should not have been called")
	}
}

func TestAppDownInvokesController(t *testing.T) {
	f := &fakeController{}
	serve(NewServer(f, ""), "POST", "/app/down?app=blog", nil)
	waitFor(t, func() bool {
		s := f.stoppedApps()
		return len(s) == 1 && s[0] == "blog"
	})
}

func TestAppActionRequiresApp(t *testing.T) {
	f := &fakeController{}
	rr := serve(NewServer(f, ""), "POST", "/app/up", nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("missing-app code = %d, want 400", rr.Code)
	}
	time.Sleep(20 * time.Millisecond)
	if len(f.startedApps()) != 0 {
		t.Error("StartApp ran without an app name")
	}
}

func TestAppActionHonorsTokenGuard(t *testing.T) {
	f := &fakeController{}
	srv := NewServer(f, "s3cret")
	if rr := serve(srv, "POST", "/app/down?app=blog", nil); rr.Code != http.StatusForbidden {
		t.Fatalf("no-token code = %d, want 403", rr.Code)
	}
	if len(f.stoppedApps()) != 0 {
		t.Fatal("StopApp ran without a valid token")
	}
}

func TestStatusRendersPerAppControls(t *testing.T) {
	f := &fakeController{statuses: []runner.AppStatus{{Name: "blog", State: "running"}}}
	body := serve(NewServer(f, ""), "GET", "/", nil).Body.String()
	for _, want := range []string{`action="/app/up"`, `action="/app/down"`, `value="blog"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing per-app control %q", want)
		}
	}
}

func TestAddInvokesController(t *testing.T) {
	f := &fakeController{}
	rr := serve(NewServer(f, ""), "POST", "/add?path=/apps/blog&domain=blog.example.com", nil)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("code = %d, want 303", rr.Code)
	}
	waitFor(t, func() bool { return f.snapshot().addCalls == 1 })
	s := f.snapshot()
	if s.addPath != "/apps/blog" || s.addDomain != "blog.example.com" {
		t.Errorf("AddApp got (%q,%q), want (/apps/blog, blog.example.com)", s.addPath, s.addDomain)
	}
}

func TestAddRequiresPath(t *testing.T) {
	f := &fakeController{}
	rr := serve(NewServer(f, ""), "POST", "/add?domain=x", nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("missing-path code = %d, want 400", rr.Code)
	}
	time.Sleep(20 * time.Millisecond)
	if f.snapshot().addCalls != 0 {
		t.Error("AddApp ran without a path")
	}
}

func TestRemoveInvokesControllerWithImageFlag(t *testing.T) {
	f := &fakeController{}
	serve(NewServer(f, ""), "POST", "/remove?app=blog&image=on", nil)
	waitFor(t, func() bool { return f.snapshot().removeCalls == 1 })
	if s := f.snapshot(); s.removeName != "blog" || !s.removeImage {
		t.Errorf("RemoveApp got (%q, image=%v), want (blog, true)", s.removeName, s.removeImage)
	}
}

func TestRemoveWithoutImageFlag(t *testing.T) {
	f := &fakeController{}
	serve(NewServer(f, ""), "POST", "/remove?app=blog", nil)
	waitFor(t, func() bool { return f.snapshot().removeCalls == 1 })
	if f.snapshot().removeImage {
		t.Error("image flag should default off when the checkbox is unchecked")
	}
}

func TestRemoveRequiresApp(t *testing.T) {
	f := &fakeController{}
	rr := serve(NewServer(f, ""), "POST", "/remove", nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("missing-app code = %d, want 400", rr.Code)
	}
}

func TestAddRemoveHonorTokenGuard(t *testing.T) {
	f := &fakeController{}
	srv := NewServer(f, "s3cret")
	if rr := serve(srv, "POST", "/add?path=/x", nil); rr.Code != http.StatusForbidden {
		t.Fatalf("/add no-token code = %d, want 403", rr.Code)
	}
	if rr := serve(srv, "POST", "/remove?app=blog", nil); rr.Code != http.StatusForbidden {
		t.Fatalf("/remove no-token code = %d, want 403", rr.Code)
	}
	if s := f.snapshot(); s.addCalls != 0 || s.removeCalls != 0 {
		t.Fatal("add/remove ran without a valid token")
	}
}

func TestRemovedAppsRendered(t *testing.T) {
	f := &fakeController{removed: []RemovedApp{{Name: "linkaru", Path: "/apps/linkaru", Domain: "linkaru.example.com"}}}
	body := serve(NewServer(f, ""), "GET", "/", nil).Body.String()
	// The removed app is listed with a one-click re-add that resubmits its path.
	for _, want := range []string{"linkaru", `action="/add"`, `value="/apps/linkaru"`} {
		if !strings.Contains(body, want) {
			t.Errorf("removed view missing %q", want)
		}
	}
}

func TestAddFormAndPerRowRemoveRendered(t *testing.T) {
	f := &fakeController{statuses: []runner.AppStatus{{Name: "blog", State: "running"}}}
	body := serve(NewServer(f, ""), "GET", "/", nil).Body.String()
	for _, want := range []string{`action="/add"`, `name="path"`, `action="/remove"`, `name="image"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
}

func TestProcessingStepsRendered(t *testing.T) {
	f := &fakeController{release: make(chan struct{}), emitLines: []string{"preflight: running roost doctor", "building blog"}}
	srv := NewServer(f, "")
	serve(srv, "POST", "/add?path=/apps/blog", nil) // emits, then blocks on release
	waitFor(t, func() bool {
		return strings.Contains(serve(srv, "GET", "/", nil).Body.String(), "building blog")
	})
	body := serve(srv, "GET", "/", nil).Body.String()
	if !strings.Contains(body, "preflight: running roost doctor") {
		t.Error("processing pane missing the first emitted step")
	}
	close(f.release)
}

func TestHumanize(t *testing.T) {
	cases := map[string]string{
		"trackaru":    "Trackaru",
		"sure-worker": "Sure Worker",
		"linkart":     "Linkart",
		"kamandar":    "Kamandar",
		"my_cool_app": "My Cool App",
		"":            "",
	}
	for in, want := range cases {
		if got := humanize(in); got != want {
			t.Errorf("humanize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGroupAppsOrderAndFallback(t *testing.T) {
	apps := []runner.AppStatus{
		{Name: "trackaru", Category: "main"},
		{Name: "sure", Category: "utility"},
		{Name: "sure-worker", Category: "worker"},
		{Name: "unlabeled", Category: ""}, // empty → Main apps
	}
	groups := groupApps(apps)
	if len(groups) != 3 {
		t.Fatalf("groups = %d, want 3 (empty groups omitted)", len(groups))
	}
	if groups[0].Title != "Main apps" || groups[1].Title != "Utilities" || groups[2].Title != "Workers" {
		t.Fatalf("titles = %q/%q/%q", groups[0].Title, groups[1].Title, groups[2].Title)
	}
	if len(groups[0].Apps) != 2 {
		t.Fatalf("Main apps = %d, want 2 (trackaru + unlabeled)", len(groups[0].Apps))
	}
}

func TestGroupAppsOmitsEmpty(t *testing.T) {
	groups := groupApps([]runner.AppStatus{{Name: "a", Category: "utility"}})
	if len(groups) != 1 || groups[0].Title != "Utilities" {
		t.Fatalf("groups = %+v, want only Utilities", groups)
	}
}

func TestMemPct(t *testing.T) {
	cases := map[string]int{
		"180MiB / 512MiB": 35,
		"512MiB / 512MiB": 100,
		"1.2GiB / 2GiB":   60,
		"9MiB / 512MiB":   2,
		"":                0, // unknown → 0, not a crash
		"garbage":         0,
		"10MiB / 0MiB":    0, // zero cap → 0, no divide panic
	}
	for in, want := range cases {
		if got := memPct(in); got != want {
			t.Errorf("memPct(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestMemColor(t *testing.T) {
	if memColor("500MiB / 512MiB") != "bad" { // ~98% → red
		t.Error("near-full memory should be bad")
	}
	if memColor("400MiB / 512MiB") != "warn" { // ~78% → amber
		t.Error("high memory should be warn")
	}
	if memColor("100MiB / 512MiB") != "ok" { // ~20% → green
		t.Error("low memory should be ok")
	}
	if memColor("") != "ok" { // unknown → neutral/ok, never red
		t.Error("unknown memory should not be red")
	}
}

func TestBusyBlocksConcurrentAction(t *testing.T) {
	f := &fakeController{release: make(chan struct{})}
	srv := NewServer(f, "")

	serve(srv, "POST", "/up", nil) // starts, blocks in Up
	waitFor(t, func() bool { return atomic.LoadInt32(&f.up) == 1 })

	serve(srv, "POST", "/up", nil)    // while busy → must not call again
	time.Sleep(30 * time.Millisecond) // give a stray goroutine a chance
	if got := atomic.LoadInt32(&f.up); got != 1 {
		t.Fatalf("up called %d times, want 1 (busy did not block)", got)
	}
	close(f.release)
}

type errString string

func (e errString) Error() string { return string(e) }
