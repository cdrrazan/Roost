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

	// AddApp adds an app by host path (domain optional): resource/preflight
	// check, config edit, artifact regen, then build + start. RemoveApp tears
	// one app down (optionally deleting its image) and records it for re-add.
	// Both stream human-readable progress lines through emit so the panel's
	// processing pane can show what's happening.
	AddApp(path, domain string, emit func(string)) error
	RemoveApp(name string, deleteImage bool, emit func(string)) error

	// RemovedApps lists apps removed via the panel, so it can offer a
	// one-click re-add without the user retyping the path.
	RemovedApps() ([]RemovedApp, error)
}

// RemovedApp is a previously-removed app the panel offers to re-add.
type RemovedApp struct {
	Name   string
	Path   string
	Domain string
}

// Server renders the panel and serialises on/off actions. A single in-flight
// action is tracked by busy so a double-click cannot launch two concurrent
// docker compose runs.
type Server struct {
	ctrl  Controller
	token string

	mu    sync.Mutex
	busy  string   // "" when idle, else a human label for the in-flight action
	last  string   // result of the most recent action
	steps []string // progress lines for the current/last action (processing pane)
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
	mux.HandleFunc("POST /add", s.guard(s.handleAdd))
	mux.HandleFunc("POST /remove", s.guard(s.handleRemove))
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

// handleAction runs a whole-stack action. It ignores the progress emitter —
// up/down report only start/finish, not per-step.
func (s *Server) handleAction(verb string, fn func() error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.runAction(w, r, verb, func(func(string)) error { return fn() })
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
		s.runAction(w, r, verb+" "+app, func(func(string)) error { return fn(app) })
	}
}

// handleAdd adds an app by host path (domain optional). The work — preflight,
// config edit, build, start — streams into the processing pane via emit.
func (s *Server) handleAdd(w http.ResponseWriter, r *http.Request) {
	path := r.FormValue("path")
	if path == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
	domain := r.FormValue("domain")
	s.runAction(w, r, "adding "+path, func(emit func(string)) error {
		return s.ctrl.AddApp(path, domain, emit)
	})
}

// handleRemove tears one app down. The "image" checkbox (value "on") also
// deletes its built image to reclaim disk.
func (s *Server) handleRemove(w http.ResponseWriter, r *http.Request) {
	app := r.FormValue("app")
	if app == "" {
		http.Error(w, "missing app", http.StatusBadRequest)
		return
	}
	deleteImage := r.FormValue("image") == "on"
	s.runAction(w, r, "removing "+app, func(emit func(string)) error {
		return s.ctrl.RemoveApp(app, deleteImage, emit)
	})
}

// runAction starts fn in the background (docker compose can take minutes) under
// the busy guard, then redirects back to the status page. A second action while
// one is in flight is a no-op redirect — the single busy flag serialises every
// action (whole-stack, per-app, add, remove) so two docker compose runs never
// overlap. fn receives an emit callback that appends a progress line to the
// processing pane, safe to call from the action goroutine.
func (s *Server) runAction(w http.ResponseWriter, r *http.Request, verb string, fn func(emit func(string)) error) {
	s.mu.Lock()
	if s.busy != "" {
		s.mu.Unlock()
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.busy = verb
	s.steps = nil
	s.mu.Unlock()

	emit := func(line string) {
		s.mu.Lock()
		s.steps = append(s.steps, line)
		s.mu.Unlock()
	}

	go func() {
		err := fn(emit)
		s.mu.Lock()
		if err != nil {
			s.last = fmt.Sprintf("%s failed: %v", verb, err)
			s.steps = append(s.steps, "✗ "+err.Error())
		} else {
			s.last = verb + " complete"
		}
		s.busy = ""
		s.mu.Unlock()
	}()

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

type statusView struct {
	Apps    []runner.AppStatus
	Busy    string
	Last    string
	Steps   []string     // processing-pane progress lines
	Removed []RemovedApp // apps removed via the panel, offered for re-add
	Error   string
	Token   string // embedded in the form when non-empty
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	view := statusView{Busy: s.busy, Last: s.last, Token: s.token,
		Steps: append([]string(nil), s.steps...)}
	s.mu.Unlock()

	if apps, err := s.ctrl.Status(); err != nil {
		view.Error = err.Error()
	} else {
		view.Apps = apps
	}
	if removed, err := s.ctrl.RemovedApps(); err == nil {
		view.Removed = removed
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
{{if .Busy}}<meta http-equiv="refresh" content="2">{{end}}
<style>
 body{font:15px/1.5 system-ui,sans-serif;max-width:1040px;margin:2rem auto;padding:0 1rem;color:#1a1a1a}
 h1{font-size:1.4rem} h2{font-size:1rem;margin:0 0 .5rem}
 .layout{display:flex;gap:1.5rem;align-items:flex-start}
 main{flex:1;min-width:0} aside{width:300px;flex:none}
 table{width:100%;border-collapse:collapse;margin:1rem 0}
 th,td{text-align:left;padding:.45rem .6rem;border-bottom:1px solid #eee}
 .running{color:#0a7d33;font-weight:600}.down{color:#b00020;font-weight:600}
 form{display:inline} button{font:inherit;padding:.5rem 1.1rem;border:0;border-radius:6px;cursor:pointer;color:#fff}
 .on{background:#0a7d33}.off{background:#b00020} button[disabled]{opacity:.5;cursor:not-allowed}
 td.act{white-space:nowrap} td.act form{margin-right:.3rem} td.act button{padding:.3rem .6rem;font-size:.82rem}
 td.act .del{font-size:.75rem;color:#666;margin-right:.2rem}
 .msg{background:#f3f4f6;padding:.6rem .8rem;border-radius:6px} a{color:#2563eb}
 .card{background:#f7f7f8;border:1px solid #eee;border-radius:8px;padding:.9rem 1rem;margin-bottom:1rem}
 .steps{margin:.4rem 0 0;padding-left:1.2rem;font-family:ui-monospace,monospace;font-size:.82rem}
 .steps li{margin:.15rem 0} .idle{color:#888;font-size:.85rem;margin:.2rem 0 0}
 .rrow{display:flex;justify-content:space-between;align-items:center;padding:.25rem 0;border-bottom:1px solid #eee}
 .rrow:last-child{border:0} .rrow button{padding:.25rem .7rem;font-size:.82rem}
 .addbox input[type=text]{font:inherit;padding:.45rem .6rem;border:1px solid #ccc;border-radius:6px;margin:.2rem .3rem .2rem 0;min-width:15rem}
 .hint{font-size:.8rem;color:#666;margin:.3rem 0 0} .note{font-size:.85rem;color:#666}
 @media(prefers-color-scheme:dark){body{background:#111;color:#eee}th,td,.rrow{border-color:#333}.msg,.card{background:#1c1c1c;border-color:#2a2a2a}.addbox input[type=text]{background:#161616;color:#eee;border-color:#333}}
 @media(max-width:820px){.layout{flex-direction:column}aside{width:100%}}
</style></head><body>
<h1>roost control</h1>
<p>
 <form method="post" action="/up">{{if .Token}}<input type="hidden" name="token" value="{{.Token}}">{{end}}<button class="on" {{if .Busy}}disabled{{end}}>Start apps</button></form>
 <form method="post" action="/down">{{if .Token}}<input type="hidden" name="token" value="{{.Token}}">{{end}}<button class="off" {{if .Busy}}disabled{{end}}>Stop apps</button></form>
</p>
<p class="note">Stop leaves the proxy &amp; tunnel running, so this panel stays reachable.</p>
<div class="layout">
<main>
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
  <form method="post" action="/remove" onsubmit="return confirm('Remove {{.Name}} from the config?')"><input type="hidden" name="app" value="{{.Name}}">{{if $.Token}}<input type="hidden" name="token" value="{{$.Token}}">{{end}}<label class="del"><input type="checkbox" name="image" value="on"> free disk</label><button class="off" {{if $.Busy}}disabled{{end}}>Remove</button></form>
 </td>
</tr>{{end}}
</tbody></table>{{end}}
<section class="addbox card">
 <h2>Add an app</h2>
 <form method="post" action="/add">
  <input type="text" name="path" placeholder="/path/to/app (host folder)" required>
  <input type="text" name="domain" placeholder="host.example.com (optional)">
  {{if .Token}}<input type="hidden" name="token" value="{{.Token}}">{{end}}
  <button class="on" {{if .Busy}}disabled{{end}}>Check &amp; add</button>
 </form>
 <p class="hint">Runs <code>roost doctor</code> first; the app is added, built, and started only if preflight passes.</p>
</section>
</main>
<aside>
 <section class="card">
  <h2>Processing</h2>
  {{if .Busy}}<p class="msg">⏳ {{.Busy}}…</p>{{end}}
  {{if .Steps}}<ol class="steps">{{range .Steps}}<li>{{.}}</li>{{end}}</ol>{{else if not .Busy}}<p class="idle">idle — no action running</p>{{end}}
  {{if and (not .Busy) .Last}}<p class="msg">{{.Last}}</p>{{end}}
 </section>
 <section class="card">
  <h2>Removed apps</h2>
  {{if .Removed}}{{range .Removed}}<div class="rrow">
   <span>{{.Name}}</span>
   <form method="post" action="/add"><input type="hidden" name="path" value="{{.Path}}"><input type="hidden" name="domain" value="{{.Domain}}">{{if $.Token}}<input type="hidden" name="token" value="{{$.Token}}">{{end}}<button class="on" {{if $.Busy}}disabled{{end}}>Add</button></form>
  </div>{{end}}{{else}}<p class="idle">none — removing an app lists it here for one-click re-add.</p>{{end}}
 </section>
</aside>
</div>
</body></html>`))
