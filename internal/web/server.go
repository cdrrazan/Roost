// Package web serves a small control panel for the roost stack: a live status
// view plus whole-stack on/off. It is meant to run as a host process *outside*
// the stack (so turning the stack "off" cannot take down the control plane that
// turns it back on) and drives the stack through a Controller. The real
// controller shells to docker compose via internal/runner; tests inject a fake
// and never touch Docker.
//
// Auth is expected at the edge (Cloudflare Access). The optional token is
// defense-in-depth on the mutating actions, not the primary gate.
package web

import (
	"fmt"
	"html/template"
	"net/http"
	"sync"

	"github.com/cdrrazan/roost/internal/runner"
)

// Controller is the stack behind the panel: whole-stack on/off, per-app
// on/off, and status.
type Controller interface {
	Status() ([]runner.AppStatus, error)
	Up() error
	Down() error
	// StartApp resumes one app's container; StopApp stops it. The
	// implementation must reject any name that is not a configured app so the
	// panel cannot toggle infra containers (caddy, cloudflared).
	StartApp(app string) error
	StopApp(app string) error
}

// Server renders the panel and serialises on/off actions. A single in-flight
// action is tracked by busy so a double-click cannot launch two concurrent
// docker compose runs.
type Server struct {
	ctrl  Controller
	token string

	mu   sync.Mutex
	busy string // "", "starting", or "stopping"
	last string // result of the most recent action
}

// NewServer returns a panel over ctrl. An empty token disables the action
// guard (rely on edge auth); a non-empty token is required on /up and /down.
func NewServer(ctrl Controller, token string) *Server {
	return &Server{ctrl: ctrl, token: token}
}

// Handler returns the panel's routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleStatus)
	mux.HandleFunc("POST /up", s.guard(s.handleAction("starting", s.ctrl.Up)))
	mux.HandleFunc("POST /down", s.guard(s.handleAction("stopping", s.ctrl.Down)))
	mux.HandleFunc("POST /app/up", s.guard(s.handleAppAction("starting", s.ctrl.StartApp)))
	mux.HandleFunc("POST /app/down", s.guard(s.handleAppAction("stopping", s.ctrl.StopApp)))
	return mux
}

// guard enforces the shared-secret token on mutating actions when one is set.
// The token may arrive as an X-Roost-Token header (curl/API) or a token form
// field (the browser form embeds it, since the page is only served to
// edge-authenticated users).
func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" {
			got := r.Header.Get("X-Roost-Token")
			if got == "" {
				got = r.FormValue("token")
			}
			if got != s.token {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		next(w, r)
	}
}

// handleAction runs a whole-stack action.
func (s *Server) handleAction(verb string, fn func() error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.runAction(w, r, verb, fn)
	}
}

// handleAppAction runs a per-app action. The target app arrives in the "app"
// form field; a missing name is a 400 (the panel always sends it). The busy
// label names the app so the status page shows which one is in flight.
func (s *Server) handleAppAction(verb string, fn func(string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		app := r.FormValue("app")
		if app == "" {
			http.Error(w, "missing app", http.StatusBadRequest)
			return
		}
		s.runAction(w, r, verb+" "+app, func() error { return fn(app) })
	}
}

// runAction starts fn in the background (docker compose can take minutes) under
// the busy guard, then redirects back to the status page. A second action while
// one is in flight is a no-op redirect — the single busy flag serialises every
// action (whole-stack and per-app) so two docker compose runs never overlap.
func (s *Server) runAction(w http.ResponseWriter, r *http.Request, verb string, fn func() error) {
	s.mu.Lock()
	if s.busy != "" {
		s.mu.Unlock()
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.busy = verb
	s.mu.Unlock()

	go func() {
		err := fn()
		s.mu.Lock()
		if err != nil {
			s.last = fmt.Sprintf("%s failed: %v", verb, err)
		} else {
			s.last = verb + " complete"
		}
		s.busy = ""
		s.mu.Unlock()
	}()

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

type statusView struct {
	Apps  []runner.AppStatus
	Busy  string
	Last  string
	Error string
	Token string // embedded in the form when non-empty
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	view := statusView{Busy: s.busy, Last: s.last, Token: s.token}
	s.mu.Unlock()

	if apps, err := s.ctrl.Status(); err != nil {
		view.Error = err.Error()
	} else {
		view.Apps = apps
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := statusTmpl.Execute(w, view); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

var statusTmpl = template.Must(template.New("status").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>roost</title>
<style>
 body{font:15px/1.5 system-ui,sans-serif;max-width:760px;margin:2rem auto;padding:0 1rem;color:#1a1a1a}
 h1{font-size:1.4rem} table{width:100%;border-collapse:collapse;margin:1rem 0}
 th,td{text-align:left;padding:.45rem .6rem;border-bottom:1px solid #eee}
 .running{color:#0a7d33;font-weight:600}.down{color:#b00020;font-weight:600}
 form{display:inline} button{font:inherit;padding:.5rem 1.1rem;border:0;border-radius:6px;cursor:pointer;color:#fff}
 .on{background:#0a7d33}.off{background:#b00020} button[disabled]{opacity:.5;cursor:not-allowed}
 td.act form{margin-right:.3rem} td.act button{padding:.3rem .7rem;font-size:.85rem}
 .msg{background:#f3f4f6;padding:.6rem .8rem;border-radius:6px} a{color:#2563eb}
 @media(prefers-color-scheme:dark){body{background:#111;color:#eee}th,td{border-color:#333}.msg{background:#1c1c1c}}
</style></head><body>
<h1>roost control</h1>
{{if .Busy}}<p class="msg">⏳ {{.Busy}}… refresh in a moment.</p>
{{else if .Last}}<p class="msg">{{.Last}}</p>{{end}}
<p>
 <form method="post" action="/up">{{if .Token}}<input type="hidden" name="token" value="{{.Token}}">{{end}}<button class="on" {{if .Busy}}disabled{{end}}>Start apps</button></form>
 <form method="post" action="/down">{{if .Token}}<input type="hidden" name="token" value="{{.Token}}">{{end}}<button class="off" {{if .Busy}}disabled{{end}}>Stop apps</button></form>
</p>
<p style="font-size:.85rem;color:#666">Stop leaves the proxy &amp; tunnel running, so this panel stays reachable.</p>
{{if .Error}}<p class="down">status error: {{.Error}}</p>{{else}}
<table><thead><tr><th>App</th><th>State</th><th>Health</th><th>Memory</th><th>URL</th><th>Actions</th></tr></thead><tbody>
{{range .Apps}}<tr>
 <td>{{.Name}}</td>
 <td class="{{if eq .State "running"}}running{{else}}down{{end}}">{{.State}}</td>
 <td>{{.Health}}</td><td>{{.Memory}}</td>
 <td>{{if .URL}}<a href="{{.URL}}">{{.URL}}</a>{{end}}</td>
 <td class="act">
  <form method="post" action="/app/up"><input type="hidden" name="app" value="{{.Name}}">{{if $.Token}}<input type="hidden" name="token" value="{{$.Token}}">{{end}}<button class="on" {{if or $.Busy (eq .State "running")}}disabled{{end}}>Start</button></form>
  <form method="post" action="/app/down"><input type="hidden" name="app" value="{{.Name}}">{{if $.Token}}<input type="hidden" name="token" value="{{$.Token}}">{{end}}<button class="off" {{if or $.Busy (ne .State "running")}}disabled{{end}}>Stop</button></form>
 </td>
</tr>{{end}}
</tbody></table>{{end}}
</body></html>`))
