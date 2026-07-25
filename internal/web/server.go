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
	"strings"
	"sync"

	"github.com/cdrrazan/roost/internal/runner"
)

// humanize turns an app's config name into a display label: "-"/"_" become
// spaces and each word is capitalised. "sure-worker" → "Sure Worker".
func humanize(name string) string {
	fields := strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' || r == ' ' })
	for i, w := range fields {
		fields[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(fields, " ")
}

// slug lowercases a section title into an anchor id: "Main apps" → "main-apps".
func slug(s string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), " ", "-"))
}

// appGroup is one titled section of the app list.
type appGroup struct {
	Title string
	Apps  []runner.AppStatus
}

// groupApps buckets apps into Main apps / Utilities / Workers by category,
// preserving input order within each bucket and omitting empty buckets. An
// unknown or empty category falls back to Main apps.
func groupApps(apps []runner.AppStatus) []appGroup {
	buckets := map[string][]runner.AppStatus{}
	for _, a := range apps {
		buckets[groupTitle(a.Category)] = append(buckets[groupTitle(a.Category)], a)
	}
	var out []appGroup
	for _, title := range []string{"Main apps", "Utilities", "Workers"} {
		if apps := buckets[title]; len(apps) > 0 {
			out = append(out, appGroup{Title: title, Apps: apps})
		}
	}
	return out
}

// groupTitle maps a category string to its section title.
func groupTitle(category string) string {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "worker", "workers":
		return "Workers"
	case "utility", "utilities", "util":
		return "Utilities"
	default: // "main", "app", "", anything unknown
		return "Main apps"
	}
}

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
	Groups       []appGroup // apps bucketed into Main / Utilities / Workers
	Total        int
	RunningCount int
	DockerOK     bool // false when Status() failed (Docker unreachable)
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
		view.DockerOK = true
		view.Apps = apps
		view.Groups = groupApps(apps)
		view.Total = len(apps)
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

var statusTmpl = template.Must(template.New("status").Funcs(template.FuncMap{"humanize": humanize, "slug": slug}).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>roost control</title>
{{if .Busy}}<meta http-equiv="refresh" content="2">{{end}}
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Google+Sans:ital,opsz,wght@0,17..18,400..700;1,17..18,400..700&display=swap" rel="stylesheet">
<style>
 :root{
  --bg:#f3f4f7; --panel:#ffffff; --panel2:#f7f8fa; --line:#e6e8ee; --line2:#eef0f4;
  --ink:#0f1222; --muted:#666d7d; --faint:#98a0b0;
  --brand:#4f46e5; --brand-ink:#fff; --brand-soft:#ecebfc;
  --ok:#16a34a; --ok-bg:#e8f7ef; --ok-ink:#0f7a37;
  --danger:#dc2626; --danger-bg:#fdecec;
  --shadow:0 1px 2px rgba(16,19,34,.04),0 10px 26px -14px rgba(16,19,34,.14);
  --radius:16px;
  --font:"Google Sans","Product Sans","Google Sans Text",-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Inter,system-ui,sans-serif;
  --mono:ui-monospace,SFMono-Regular,Menlo,monospace;
 }
 @media(prefers-color-scheme:dark){:root{
  --bg:#0a0d13; --panel:#111620; --panel2:#151b26; --line:#212836; --line2:#1b2230;
  --ink:#e7ecf5; --muted:#9aa4b6; --faint:#68738a;
  --brand:#7c75f5; --brand-soft:#1b1e3a;
  --ok:#37d383; --ok-bg:#0f2a1c; --ok-ink:#5bdc90;
  --danger:#f0616a; --danger-bg:#2a1618;
  --shadow:0 1px 2px rgba(0,0,0,.3),0 14px 34px -16px rgba(0,0,0,.7);
 }}
 *{box-sizing:border-box}
 body{margin:0;background:var(--bg);color:var(--ink);font:14.5px/1.55 var(--font);-webkit-font-smoothing:antialiased}
 a{color:var(--brand);text-decoration:none} a:hover{text-decoration:underline}
 h1,h2,h3{margin:0}
 /* shell */
 .shell{display:grid;grid-template-columns:246px minmax(0,1fr);min-height:100vh}
 .content{display:flex;flex-direction:column;min-width:0}
 /* sidebar */
 .side{background:var(--panel);border-right:1px solid var(--line);display:flex;flex-direction:column;
  position:sticky;top:0;height:100vh;padding:18px 14px}
 .brand{display:flex;align-items:center;gap:11px;padding:6px 8px 16px}
 .logo{width:34px;height:34px;border-radius:10px;background:linear-gradient(135deg,var(--brand),#8b5cf6);
  display:grid;place-items:center;color:#fff;font-weight:800;box-shadow:var(--shadow)}
 .brand .bt{font-size:15px;font-weight:700;letter-spacing:-.2px}
 .brand .bs{font-size:11.5px;color:var(--faint)}
 .nav{display:flex;flex-direction:column;gap:2px;margin-top:6px}
 .nav a{display:flex;align-items:center;gap:9px;padding:9px 11px;border-radius:10px;color:var(--muted);font-weight:550;font-size:13.5px}
 .nav a:hover{background:var(--panel2);color:var(--ink);text-decoration:none}
 .nav .ico{width:16px;text-align:center;opacity:.8}
 .navlabel{font-size:10.5px;text-transform:uppercase;letter-spacing:.08em;color:var(--faint);padding:14px 11px 5px;font-weight:700}
 .side .grow{flex:1}
 .statusbox{border:1px solid var(--line);border-radius:12px;padding:12px;background:var(--panel2)}
 .statusbox .st{font-size:10.5px;text-transform:uppercase;letter-spacing:.06em;color:var(--faint);font-weight:700;margin-bottom:8px}
 .srow{display:flex;align-items:center;gap:8px;font-size:12.5px;color:var(--muted);padding:3px 0}
 .srow b{margin-left:auto;color:var(--ink);font-weight:600}
 .sdot{width:8px;height:8px;border-radius:50%;flex:none}
 .sdot.ok{background:var(--ok)} .sdot.bad{background:var(--danger)} .sdot.warn{background:#e0a325}
 /* topbar */
 .topbar{position:sticky;top:0;z-index:5;background:color-mix(in srgb,var(--bg) 78%,transparent);
  backdrop-filter:blur(8px);border-bottom:1px solid var(--line);display:flex;align-items:center;gap:12px;
  padding:13px 22px;flex-wrap:wrap}
 .topbar .burger{display:none;font-size:20px;background:none;border:0;color:var(--ink);cursor:pointer}
 .topbar .tt{font-size:16px;font-weight:700;letter-spacing:-.2px}
 .topbar .ts{font-size:12px;color:var(--muted)}
 .topbar .spacer{flex:1}
 .toggle{display:inline-flex;background:var(--panel2);border:1px solid var(--line);border-radius:10px;padding:3px}
 .toggle button{font:inherit;font-size:12.5px;font-weight:600;border:0;background:none;color:var(--muted);
  padding:5px 11px;border-radius:7px;cursor:pointer;display:inline-flex;align-items:center;gap:5px}
 .toggle button.active{background:var(--panel);color:var(--ink);box-shadow:var(--shadow)}
 /* buttons */
 .btn{font:inherit;font-weight:600;font-size:13px;border:1px solid var(--line);border-radius:10px;
  padding:8px 14px;cursor:pointer;color:var(--ink);background:var(--panel);transition:.12s;
  white-space:nowrap;display:inline-flex;align-items:center;gap:6px}
 .btn:hover{transform:translateY(-1px)} .btn:active{transform:none}
 .btn[disabled]{opacity:.45;cursor:not-allowed;transform:none}
 .btn-sm{padding:6px 11px;font-size:12.5px;border-radius:9px}
 .btn-primary{background:var(--brand);border-color:var(--brand);color:var(--brand-ink)}
 .btn-ok{background:var(--ok-bg);border-color:transparent;color:var(--ok-ink)}
 .btn-ok:hover{background:var(--ok);color:#fff}
 .btn-danger{background:transparent;color:var(--danger)}
 .btn-danger:hover{background:var(--danger-bg);border-color:var(--danger)}
 form.inline{display:inline}
 /* body grid */
 .body{padding:22px;display:grid;grid-template-columns:minmax(0,1fr) 336px;gap:20px;align-items:start;flex:1}
 .card{background:var(--panel);border:1px solid var(--line);border-radius:var(--radius);box-shadow:var(--shadow);margin-bottom:20px}
 .card:last-child{margin-bottom:0}
 .card-h{display:flex;align-items:center;justify-content:space-between;padding:14px 18px;border-bottom:1px solid var(--line2)}
 .card-h h2{font-size:13px;font-weight:700;letter-spacing:.02em;color:var(--ink)}
 .count{font-size:12px;color:var(--faint);background:var(--panel2);border:1px solid var(--line);border-radius:999px;padding:2px 9px}
 /* app list / grid */
 .applist.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(232px,1fr));gap:12px;padding:14px}
 .app{display:flex;align-items:center;gap:14px;padding:13px 18px;border-bottom:1px solid var(--line2)}
 .app:last-child{border-bottom:0}
 .applist.grid .app{flex-direction:column;align-items:stretch;gap:11px;border:1px solid var(--line);border-radius:12px;padding:14px;background:var(--panel2)}
 .app .id{min-width:0;flex:1}
 .app .nm{display:flex;align-items:center;gap:9px}
 .app .nm .name{font-weight:650;font-size:14.5px;letter-spacing:-.1px}
 .pill{font-size:10.5px;font-weight:700;text-transform:uppercase;letter-spacing:.04em;padding:2px 8px;border-radius:999px;border:1px solid transparent}
 .pill.run{background:var(--ok-bg);color:var(--ok-ink)}
 .pill.stop{background:var(--panel);color:var(--faint);border-color:var(--line)}
 .app .sub{margin-top:3px;font-size:12.5px;color:var(--muted);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
 .app .sub .meta{color:var(--faint)}
 .app .acts{display:flex;align-items:center;gap:7px;flex-wrap:wrap;justify-content:flex-end}
 .applist.grid .app .acts{justify-content:flex-start}
 .app .acts .free{display:inline-flex;align-items:center;gap:4px;font-size:11px;color:var(--faint);cursor:pointer}
 .app .acts .free input{accent-color:var(--danger)}
 /* processing */
 .procbody{padding:15px 18px}
 .status-line{display:flex;align-items:center;gap:9px;font-size:13.5px;font-weight:600}
 .spin{width:15px;height:15px;border-radius:50%;border:2px solid var(--line);border-top-color:var(--brand);animation:spin .7s linear infinite;flex:none}
 @keyframes spin{to{transform:rotate(360deg)}}
 .steps{list-style:none;margin:12px 0 0;padding:0}
 .steps li{position:relative;padding:4px 0 4px 20px;font-size:12.5px;font-family:var(--mono);color:var(--muted)}
 .steps li:before{content:"";position:absolute;left:5px;top:10px;width:6px;height:6px;border-radius:50%;background:var(--brand)}
 .idle{color:var(--faint);font-size:13px;display:flex;align-items:center;gap:8px}
 .idle .dot{width:7px;height:7px;border-radius:50%;background:var(--faint)}
 .result{margin:12px 0 0;padding:10px 12px;border-radius:10px;background:var(--panel2);border:1px solid var(--line);font-size:12.5px;color:var(--muted)}
 .result.err{background:var(--danger-bg);border-color:transparent;color:var(--danger)}
 /* removed */
 .rrow{display:flex;align-items:center;justify-content:space-between;gap:10px;padding:11px 18px;border-bottom:1px solid var(--line2)}
 .rrow:last-child{border-bottom:0} .rrow .rn{font-weight:600;font-size:13.5px}
 .rrow .rp{font-size:11px;color:var(--faint);font-family:var(--mono);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
 .empty{padding:16px 18px;color:var(--faint);font-size:13px}
 /* footer */
 .footer{border-top:1px solid var(--line);padding:16px 22px;display:flex;justify-content:space-between;gap:12px;flex-wrap:wrap;font-size:12px;color:var(--faint)}
 /* modal */
 dialog{border:1px solid var(--line);border-radius:var(--radius);padding:0;width:min(440px,92vw);background:var(--panel);color:var(--ink);box-shadow:0 24px 60px -20px rgba(0,0,0,.4)}
 dialog::backdrop{background:rgba(8,10,16,.55);backdrop-filter:blur(2px)}
 .modal-h{display:flex;align-items:center;justify-content:space-between;padding:16px 18px;border-bottom:1px solid var(--line2)}
 .modal-h h2{font-size:15px} .modal-x{background:none;border:0;font-size:20px;color:var(--faint);cursor:pointer;line-height:1}
 .modal-b{padding:18px}
 .field{margin-bottom:12px}
 .field label{display:block;font-size:12px;font-weight:600;color:var(--muted);margin-bottom:5px}
 .field input{width:100%;font:inherit;font-size:14px;padding:10px 12px;border-radius:10px;border:1px solid var(--line);background:var(--panel2);color:var(--ink)}
 .field input:focus{outline:none;border-color:var(--brand);box-shadow:0 0 0 3px color-mix(in srgb,var(--brand) 22%,transparent)}
 .hint{font-size:12px;color:var(--faint);margin:10px 0 14px}
 /* responsive */
 @media(max-width:1040px){.body{grid-template-columns:1fr}}
 @media(max-width:820px){
  .shell{grid-template-columns:1fr}
  .side{position:fixed;left:0;top:0;z-index:30;width:246px;transform:translateX(-100%);transition:transform .2s;box-shadow:var(--shadow)}
  body.nav-open .side{transform:none}
  body.nav-open:after{content:"";position:fixed;inset:0;background:rgba(0,0,0,.4);z-index:20}
  .topbar .burger{display:inline-block}
 }
 @media(max-width:560px){
  .body{padding:16px}
  .app{flex-direction:column;align-items:stretch;gap:10px}
  .app .acts{justify-content:flex-start}
  .toggle{order:5}
 }
</style></head><body>
<div class="shell">

 <aside class="side">
  <div class="brand">
   <div class="logo">r</div>
   <div><div class="bt">roost</div><div class="bs">control panel</div></div>
  </div>
  <nav class="nav">
   <a href="#top"><span class="ico">▚</span> Overview</a>
   <div class="navlabel">Applications</div>
   <a href="#main-apps"><span class="ico">◆</span> Main apps</a>
   <a href="#utilities"><span class="ico">◈</span> Utilities</a>
   <a href="#workers"><span class="ico">⚙</span> Workers</a>
   <div class="navlabel">Manage</div>
   <a href="#removed"><span class="ico">↺</span> Removed</a>
   <a href="https://github.com/cdrrazan/roost" target="_blank" rel="noopener"><span class="ico">↗</span> Repository</a>
  </nav>
  <div class="grow"></div>
  <div class="statusbox">
   <div class="st">Status</div>
   <div class="srow"><span class="sdot ok"></span>Server <b>online</b></div>
   <div class="srow"><span class="sdot {{if .DockerOK}}ok{{else}}bad{{end}}"></span>Docker <b>{{if .DockerOK}}connected{{else}}unreachable{{end}}</b></div>
   <div class="srow"><span class="sdot {{if and .DockerOK (eq .RunningCount .Total)}}ok{{else if .RunningCount}}warn{{else}}bad{{end}}"></span>Site <b>{{.RunningCount}}/{{.Total}} up</b></div>
  </div>
 </aside>

 <div class="content" id="top">
  <header class="topbar">
   <button class="burger" id="burger" aria-label="Menu">☰</button>
   <div><div class="tt">Dashboard</div><div class="ts">{{.RunningCount}} of {{.Total}} apps running</div></div>
   <div class="spacer"></div>
   <div class="toggle"><button data-view="list">▤ List</button><button data-view="grid">▦ Grid</button></div>
   <form class="inline" method="post" action="/up">{{if .Token}}<input type="hidden" name="token" value="{{.Token}}">{{end}}<button class="btn btn-ok" {{if .Busy}}disabled{{end}}>Start all</button></form>
   <form class="inline" method="post" action="/down">{{if .Token}}<input type="hidden" name="token" value="{{.Token}}">{{end}}<button class="btn" {{if .Busy}}disabled{{end}}>Stop all</button></form>
   <button class="btn btn-primary" id="openadd" {{if .Busy}}disabled{{end}}>＋ Add app</button>
  </header>

  <div class="body">
  <main>
   {{if .Error}}
   <div class="card"><div class="result err" style="margin:16px">status error: {{.Error}}</div></div>
   {{else}}{{range .Groups}}
   <section class="card" id="{{slug .Title}}">
    <div class="card-h"><h2>{{.Title}}</h2><span class="count">{{len .Apps}}</span></div>
    <div class="applist">
    {{range .Apps}}
     <div class="app">
      <div class="id">
       <div class="nm">
        <span class="name">{{humanize .Name}}</span>
        {{if eq .State "running"}}<span class="pill run">{{.State}}</span>{{else}}<span class="pill stop">{{.State}}</span>{{end}}
       </div>
       <div class="sub">
        {{if .URL}}<a href="{{.URL}}">{{.URL}}</a>{{else}}<span class="meta">background worker</span>{{end}}
        {{if .Health}}<span class="meta"> · {{.Health}}</span>{{end}}
        {{if .Memory}}<span class="meta"> · {{.Memory}}</span>{{end}}
       </div>
      </div>
      <div class="acts">
       <form class="inline" method="post" action="/app/up"><input type="hidden" name="app" value="{{.Name}}">{{if $.Token}}<input type="hidden" name="token" value="{{$.Token}}">{{end}}<button class="btn btn-sm btn-ok" {{if or $.Busy (eq .State "running")}}disabled{{end}}>Start</button></form>
       <form class="inline" method="post" action="/app/down"><input type="hidden" name="app" value="{{.Name}}">{{if $.Token}}<input type="hidden" name="token" value="{{$.Token}}">{{end}}<button class="btn btn-sm" {{if or $.Busy (ne .State "running")}}disabled{{end}}>Stop</button></form>
       <form class="inline" method="post" action="/remove" onsubmit="return confirm('Remove {{humanize .Name}} from the config?')"><input type="hidden" name="app" value="{{.Name}}">{{if $.Token}}<input type="hidden" name="token" value="{{$.Token}}">{{end}}<label class="free" title="also delete the built image to free disk"><input type="checkbox" name="image" value="on"> free disk</label><button class="btn btn-sm btn-danger" {{if $.Busy}}disabled{{end}}>Remove</button></form>
      </div>
     </div>
    {{end}}
    </div>
   </section>
   {{else}}
   <div class="card"><div class="empty">No apps configured yet — use “Add app”.</div></div>
   {{end}}{{end}}
  </main>

  <aside class="rail">
   <div class="card" id="processing">
    <div class="card-h"><h2>Processing</h2></div>
    <div class="procbody">
     {{if .Busy}}<div class="status-line"><span class="spin"></span>{{.Busy}}…</div>{{else}}<div class="idle"><span class="dot"></span>Idle — no action running</div>{{end}}
     {{if .Steps}}<ul class="steps">{{range .Steps}}<li>{{.}}</li>{{end}}</ul>{{end}}
     {{if and (not .Busy) .Last}}<div class="result">{{.Last}}</div>{{end}}
    </div>
   </div>
   <div class="card" id="removed">
    <div class="card-h"><h2>Removed apps</h2>{{if .Removed}}<span class="count">{{len .Removed}}</span>{{end}}</div>
    {{if .Removed}}{{range .Removed}}<div class="rrow">
     <div style="min-width:0"><div class="rn">{{humanize .Name}}</div><div class="rp">{{.Path}}</div></div>
     <form class="inline" method="post" action="/add"><input type="hidden" name="path" value="{{.Path}}"><input type="hidden" name="domain" value="{{.Domain}}">{{if $.Token}}<input type="hidden" name="token" value="{{$.Token}}">{{end}}<button class="btn btn-sm btn-primary" {{if $.Busy}}disabled{{end}}>Add</button></form>
    </div>{{end}}{{else}}<div class="empty">None — removing an app lists it here for one-click re-add.</div>{{end}}
   </div>
  </aside>
  </div>

  <footer class="footer">
   <span>roost — self-host a fleet on your own domain.</span>
   <span><a href="https://github.com/cdrrazan/roost" target="_blank" rel="noopener">GitHub repository</a> · built by <a href="https://github.com/cdrrazan" target="_blank" rel="noopener">Rajan Bhattarai</a></span>
  </footer>
 </div>
</div>

<dialog id="addapp">
 <div class="modal-h"><h2>Add an app</h2><button class="modal-x" id="closeadd" aria-label="Close">×</button></div>
 <form method="post" action="/add">
  <div class="modal-b">
   <div class="field"><label>Host path</label><input type="text" name="path" placeholder="/home/ubuntu/apps/myapp" required autofocus></div>
   <div class="field"><label>Hostname <span style="font-weight:400;color:var(--faint)">(optional)</span></label><input type="text" name="domain" placeholder="myapp.example.com"></div>
   {{if .Token}}<input type="hidden" name="token" value="{{.Token}}">{{end}}
   <p class="hint">Runs <code>roost doctor</code> first; the app is added, built &amp; started only if preflight passes.</p>
   <button class="btn btn-primary" style="width:100%;justify-content:center" {{if .Busy}}disabled{{end}}>Check &amp; add</button>
  </div>
 </form>
</dialog>

<script>
(function(){
 var KEY="roost-view";
 function apply(v){
  document.querySelectorAll(".applist").forEach(function(e){e.classList.toggle("grid",v==="grid")});
  document.querySelectorAll("[data-view]").forEach(function(b){b.classList.toggle("active",b.dataset.view===v)});
 }
 apply(localStorage.getItem(KEY)||"list");
 document.querySelectorAll("[data-view]").forEach(function(b){
  b.addEventListener("click",function(){localStorage.setItem(KEY,b.dataset.view);apply(b.dataset.view);});
 });
 var dlg=document.getElementById("addapp");
 var o=document.getElementById("openadd"), c=document.getElementById("closeadd");
 if(o&&dlg)o.addEventListener("click",function(){dlg.showModal();});
 if(c&&dlg)c.addEventListener("click",function(){dlg.close();});
 if(dlg)dlg.addEventListener("click",function(e){if(e.target===dlg)dlg.close();});
 var burger=document.getElementById("burger");
 if(burger)burger.addEventListener("click",function(){document.body.classList.toggle("nav-open");});
 document.querySelectorAll(".side .nav a").forEach(function(a){a.addEventListener("click",function(){document.body.classList.remove("nav-open");});});
})();
</script>
</body></html>`))
