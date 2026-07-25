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
