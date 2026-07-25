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
	Apps         []runner.AppStatus
	RunningCount int
	Busy         string
	Last         string
	Steps        []string     // processing-pane progress lines
	Removed      []RemovedApp // apps removed via the panel, offered for re-add
	Error        string
	Token        string // embedded in the form when non-empty
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
		for _, a := range apps {
			if a.State == "running" {
				view.RunningCount++
			}
		}
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
<title>roost control</title>
{{if .Busy}}<meta http-equiv="refresh" content="2">{{end}}
<style>
 :root{
  --bg:#f4f5f7; --panel:#ffffff; --panel2:#fafbfc; --line:#e7e9ee; --line2:#eef0f4;
  --ink:#101322; --muted:#6b7280; --faint:#9aa2b1;
  --brand:#4f46e5; --brand-ink:#ffffff;
  --ok:#16a34a; --ok-bg:#e9f7ef; --ok-ink:#0f7a37;
  --danger:#dc2626; --danger-bg:#fdecec;
  --shadow:0 1px 2px rgba(16,19,34,.04),0 8px 24px -12px rgba(16,19,34,.12);
  --radius:16px;
 }
 @media(prefers-color-scheme:dark){:root{
  --bg:#0b0e14; --panel:#12161f; --panel2:#151a24; --line:#222836; --line2:#1c2230;
  --ink:#e6ebf4; --muted:#9aa4b5; --faint:#6b7688;
  --brand:#6d67f0; --ok:#34d17f; --ok-bg:#0f2a1c; --ok-ink:#57d98d;
  --danger:#f0616a; --danger-bg:#2a1618;
  --shadow:0 1px 2px rgba(0,0,0,.3),0 12px 30px -14px rgba(0,0,0,.6);
 }}
 *{box-sizing:border-box}
 body{margin:0;background:var(--bg);color:var(--ink);
  font:14.5px/1.55 -apple-system,BlinkMacSystemFont,"Segoe UI",Inter,Roboto,sans-serif;
  -webkit-font-smoothing:antialiased}
 .wrap{max-width:1120px;margin:0 auto;padding:24px 20px 56px}
 a{color:var(--brand);text-decoration:none} a:hover{text-decoration:underline}
 /* top bar */
 .top{display:flex;align-items:center;gap:16px;flex-wrap:wrap;margin-bottom:22px}
 .brand{display:flex;align-items:center;gap:11px}
 .logo{width:34px;height:34px;border-radius:10px;background:linear-gradient(135deg,var(--brand),#8b5cf6);
  display:grid;place-items:center;color:#fff;font-weight:800;box-shadow:var(--shadow)}
 .brand h1{font-size:18px;margin:0;letter-spacing:-.2px}
 .brand p{margin:0;font-size:12.5px;color:var(--muted)}
 .top .spacer{flex:1}
 .summary{display:flex;align-items:center;gap:8px;font-size:13px;color:var(--muted);
  background:var(--panel);border:1px solid var(--line);border-radius:999px;padding:7px 14px;box-shadow:var(--shadow)}
 .summary b{color:var(--ink)} .summary .dot{width:8px;height:8px;border-radius:50%;background:var(--ok)}
 /* buttons */
 .btn{font:inherit;font-weight:600;font-size:13px;border:1px solid transparent;border-radius:10px;
  padding:8px 14px;cursor:pointer;color:var(--ink);background:var(--panel2);
  border-color:var(--line);transition:.12s;white-space:nowrap;display:inline-flex;align-items:center;gap:6px}
 .btn:hover{transform:translateY(-1px)} .btn:active{transform:none}
 .btn[disabled]{opacity:.45;cursor:not-allowed;transform:none}
 .btn-sm{padding:6px 11px;font-size:12.5px;border-radius:9px}
 .btn-primary{background:var(--brand);border-color:var(--brand);color:var(--brand-ink)}
 .btn-ok{background:var(--ok-bg);border-color:transparent;color:var(--ok-ink)}
 .btn-ok:hover{background:var(--ok);color:#fff}
 .btn-danger{background:transparent;border-color:var(--line);color:var(--danger)}
 .btn-danger:hover{background:var(--danger-bg);border-color:var(--danger)}
 form.inline{display:inline}
 /* layout */
 .grid{display:grid;grid-template-columns:minmax(0,1fr) 344px;gap:20px;align-items:start}
 @media(max-width:900px){.grid{grid-template-columns:1fr}}
 .card{background:var(--panel);border:1px solid var(--line);border-radius:var(--radius);box-shadow:var(--shadow)}
 .card-h{display:flex;align-items:center;justify-content:space-between;padding:15px 18px;border-bottom:1px solid var(--line2)}
 .card-h h2{margin:0;font-size:13px;font-weight:700;letter-spacing:.02em;text-transform:uppercase;color:var(--muted)}
 .card-h .count{font-size:12px;color:var(--faint);background:var(--panel2);border:1px solid var(--line);border-radius:999px;padding:2px 9px}
 .rail>.card{margin-bottom:18px}
 /* app rows */
 .app{display:flex;align-items:center;gap:14px;padding:13px 18px;border-bottom:1px solid var(--line2)}
 .app:last-child{border-bottom:0}
 .app .id{min-width:0;flex:1}
 .app .nm{display:flex;align-items:center;gap:9px}
 .app .nm .name{font-weight:650;font-size:14.5px;letter-spacing:-.1px}
 .pill{font-size:11px;font-weight:700;text-transform:uppercase;letter-spacing:.04em;
  padding:2px 8px;border-radius:999px;border:1px solid transparent}
 .pill.run{background:var(--ok-bg);color:var(--ok-ink)}
 .pill.stop{background:var(--panel2);color:var(--faint);border-color:var(--line)}
 .app .sub{margin-top:3px;font-size:12.5px;color:var(--muted);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
 .app .sub .meta{color:var(--faint)}
 .app .acts{display:flex;align-items:center;gap:7px;flex-wrap:wrap;justify-content:flex-end}
 .app .acts .free{display:inline-flex;align-items:center;gap:4px;font-size:11.5px;color:var(--faint);cursor:pointer}
 .app .acts .free input{accent-color:var(--danger)}
 /* add form */
 .addbody{padding:16px 18px}
 .field{margin-bottom:11px}
 .field label{display:block;font-size:12px;font-weight:600;color:var(--muted);margin-bottom:5px}
 .field input{width:100%;font:inherit;font-size:14px;padding:10px 12px;border-radius:10px;
  border:1px solid var(--line);background:var(--panel2);color:var(--ink)}
 .field input:focus{outline:none;border-color:var(--brand);box-shadow:0 0 0 3px color-mix(in srgb,var(--brand) 22%,transparent)}
 .hint{font-size:12px;color:var(--faint);margin:10px 0 0}
 /* processing */
 .procbody{padding:15px 18px}
 .status-line{display:flex;align-items:center;gap:9px;font-size:13.5px;font-weight:600}
 .spin{width:15px;height:15px;border-radius:50%;border:2px solid var(--line);
  border-top-color:var(--brand);animation:spin .7s linear infinite;flex:none}
 @keyframes spin{to{transform:rotate(360deg)}}
 .steps{list-style:none;margin:12px 0 0;padding:0;position:relative}
 .steps li{position:relative;padding:4px 0 4px 20px;font-size:12.5px;
  font-family:ui-monospace,SFMono-Regular,Menlo,monospace;color:var(--muted)}
 .steps li:before{content:"";position:absolute;left:5px;top:10px;width:6px;height:6px;border-radius:50%;background:var(--brand)}
 .idle{color:var(--faint);font-size:13px;margin:2px 0 0;display:flex;align-items:center;gap:8px}
 .idle .dot{width:7px;height:7px;border-radius:50%;background:var(--faint)}
 .result{margin:12px 0 0;padding:10px 12px;border-radius:10px;background:var(--panel2);
  border:1px solid var(--line);font-size:12.5px;color:var(--muted)}
 .result.err{background:var(--danger-bg);border-color:transparent;color:var(--danger)}
 /* removed */
 .rrow{display:flex;align-items:center;justify-content:space-between;gap:10px;padding:11px 18px;border-bottom:1px solid var(--line2)}
 .rrow:last-child{border-bottom:0} .rrow .rn{font-weight:600;font-size:13.5px}
 .rrow .rp{font-size:11.5px;color:var(--faint);font-family:ui-monospace,monospace}
 .empty{padding:16px 18px;color:var(--faint);font-size:13px}
 .foot{margin-top:22px;font-size:12px;color:var(--faint);text-align:center}
</style></head><body>
<div class="wrap">

 <div class="top">
  <div class="brand">
   <div class="logo">r</div>
   <div><h1>roost control</h1><p>Stop leaves the proxy &amp; tunnel up — this panel stays reachable.</p></div>
  </div>
  <div class="spacer"></div>
  <div class="summary"><span class="dot"></span><b>{{.RunningCount}}</b> running · {{len .Apps}} apps</div>
  <form class="inline" method="post" action="/up">{{if .Token}}<input type="hidden" name="token" value="{{.Token}}">{{end}}<button class="btn btn-ok" {{if .Busy}}disabled{{end}}>Start all</button></form>
  <form class="inline" method="post" action="/down">{{if .Token}}<input type="hidden" name="token" value="{{.Token}}">{{end}}<button class="btn" {{if .Busy}}disabled{{end}}>Stop all</button></form>
 </div>

 <div class="grid">
 <main>
  {{if .Error}}
  <div class="card"><div class="result err" style="margin:16px">status error: {{.Error}}</div></div>
  {{else}}
  <div class="card">
   <div class="card-h"><h2>Applications</h2><span class="count">{{len .Apps}}</span></div>
   {{range .Apps}}
   <div class="app">
    <div class="id">
     <div class="nm">
      <span class="name">{{.Name}}</span>
      {{if eq .State "running"}}<span class="pill run">{{.State}}</span>{{else}}<span class="pill stop">{{.State}}</span>{{end}}
     </div>
     <div class="sub">
      {{if .URL}}<a href="{{.URL}}">{{.URL}}</a>{{end}}
      {{if .Health}}<span class="meta"> · {{.Health}}</span>{{end}}
      {{if .Memory}}<span class="meta"> · {{.Memory}}</span>{{end}}
     </div>
    </div>
    <div class="acts">
     <form class="inline" method="post" action="/app/up"><input type="hidden" name="app" value="{{.Name}}">{{if $.Token}}<input type="hidden" name="token" value="{{$.Token}}">{{end}}<button class="btn btn-sm btn-ok" {{if or $.Busy (eq .State "running")}}disabled{{end}}>Start</button></form>
     <form class="inline" method="post" action="/app/down"><input type="hidden" name="app" value="{{.Name}}">{{if $.Token}}<input type="hidden" name="token" value="{{$.Token}}">{{end}}<button class="btn btn-sm" {{if or $.Busy (ne .State "running")}}disabled{{end}}>Stop</button></form>
     <form class="inline" method="post" action="/remove" onsubmit="return confirm('Remove {{.Name}} from the config?')"><input type="hidden" name="app" value="{{.Name}}">{{if $.Token}}<input type="hidden" name="token" value="{{$.Token}}">{{end}}<label class="free" title="also delete the built image to free disk"><input type="checkbox" name="image" value="on"> free disk</label><button class="btn btn-sm btn-danger" {{if $.Busy}}disabled{{end}}>Remove</button></form>
    </div>
   </div>
   {{else}}
   <div class="empty">No apps configured yet — add one on the right.</div>
   {{end}}
  </div>
  {{end}}
 </main>

 <div class="rail">
  <div class="card">
   <div class="card-h"><h2>Add an app</h2></div>
   <div class="addbody">
    <form method="post" action="/add">
     <div class="field"><label>Host path</label><input type="text" name="path" placeholder="/home/ubuntu/apps/myapp" required></div>
     <div class="field"><label>Hostname <span style="font-weight:400">(optional)</span></label><input type="text" name="domain" placeholder="myapp.example.com"></div>
     {{if .Token}}<input type="hidden" name="token" value="{{.Token}}">{{end}}
     <button class="btn btn-primary" style="width:100%;justify-content:center" {{if .Busy}}disabled{{end}}>Check &amp; add</button>
    </form>
    <p class="hint">Runs <code>roost doctor</code> first; the app is added, built &amp; started only if preflight passes.</p>
   </div>
  </div>

  <div class="card">
   <div class="card-h"><h2>Processing</h2></div>
   <div class="procbody">
    {{if .Busy}}<div class="status-line"><span class="spin"></span>{{.Busy}}…</div>{{else}}<div class="idle"><span class="dot"></span>Idle — no action running</div>{{end}}
    {{if .Steps}}<ul class="steps">{{range .Steps}}<li>{{.}}</li>{{end}}</ul>{{end}}
    {{if and (not .Busy) .Last}}<div class="result">{{.Last}}</div>{{end}}
   </div>
  </div>

  <div class="card">
   <div class="card-h"><h2>Removed apps</h2>{{if .Removed}}<span class="count">{{len .Removed}}</span>{{end}}</div>
   {{if .Removed}}{{range .Removed}}<div class="rrow">
    <div style="min-width:0"><div class="rn">{{.Name}}</div><div class="rp">{{.Path}}</div></div>
    <form class="inline" method="post" action="/add"><input type="hidden" name="path" value="{{.Path}}"><input type="hidden" name="domain" value="{{.Domain}}">{{if $.Token}}<input type="hidden" name="token" value="{{$.Token}}">{{end}}<button class="btn btn-sm btn-primary" {{if $.Busy}}disabled{{end}}>Add</button></form>
   </div>{{end}}{{else}}<div class="empty">None — removing an app lists it here for one-click re-add.</div>{{end}}
  </div>
 </div>
 </div>

 <p class="foot">roost · control panel</p>
</div>
</body></html>`))
