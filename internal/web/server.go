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
	"strconv"
	"strings"
	"sync"

	"github.com/cdrrazan/roost/internal/runner"
)

// parseBytes reads a docker-style size like "180MiB" or "1.2GiB" into bytes.
func parseBytes(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) && (s[i] == '.' || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}
	if i == 0 {
		return 0, false
	}
	num, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0, false
	}
	switch strings.TrimSpace(s[i:]) {
	case "B", "":
		return num, true
	case "KiB", "KB", "kB":
		return num * 1024, true
	case "MiB", "MB":
		return num * 1024 * 1024, true
	case "GiB", "GB":
		return num * 1024 * 1024 * 1024, true
	case "TiB", "TB":
		return num * 1024 * 1024 * 1024 * 1024, true
	}
	return 0, false
}

// parseMem splits a docker MemUsage string ("used / cap") into bytes.
func parseMem(s string) (used, capacity float64, ok bool) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	u, ok1 := parseBytes(parts[0])
	c, ok2 := parseBytes(parts[1])
	if !ok1 || !ok2 || c <= 0 {
		return 0, 0, false
	}
	return u, c, true
}

// memPct is the memory-used percentage (0 when unknown), clamped to 0..100.
func memPct(s string) int {
	u, c, ok := parseMem(s)
	if !ok {
		return 0
	}
	p := int(u/c*100 + 0.5)
	if p > 100 {
		p = 100
	}
	if p < 0 {
		p = 0
	}
	return p
}

// memColor maps memory pressure to a bar colour class. Unknown stays neutral
// (ok), never red.
func memColor(s string) string {
	switch p := memPct(s); {
	case p >= 90:
		return "bad"
	case p >= 70:
		return "warn"
	default:
		return "ok"
	}
}

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
	Groups       []appGroup         // apps bucketed into Main / Utilities / Workers
	Attention    []runner.AppStatus // apps not running — surfaced up top
	Total        int
	RunningCount int
	RunningPct   int
	StoppedCount int
	MemUsed      string // human total used across apps
	MemCap       string // human total cap across apps
	MemPct       int    // total used/cap percentage
	DockerOK     bool   // false when Status() failed (Docker unreachable)
	Busy         string
	Last         string
	Steps        []string     // processing-pane progress lines
	Removed      []RemovedApp // apps removed via the panel, offered for re-add
	Error        string
	Token        string // embedded in the form when non-empty
}

// humanBytes formats a byte count in docker-style IEC units.
func humanBytes(b float64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1fGiB", b/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0fMiB", b/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.0fKiB", b/(1<<10))
	default:
		return fmt.Sprintf("%.0fB", b)
	}
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
		var used, capacity float64
		for _, a := range apps {
			if a.State == "running" {
				view.RunningCount++
			} else {
				view.Attention = append(view.Attention, a)
			}
			if u, c, ok := parseMem(a.Memory); ok {
				used += u
				capacity += c
			}
		}
		view.StoppedCount = view.Total - view.RunningCount
		if view.Total > 0 {
			view.RunningPct = view.RunningCount * 100 / view.Total
		}
		if capacity > 0 {
			view.MemUsed, view.MemCap = humanBytes(used), humanBytes(capacity)
			view.MemPct = int(used/capacity*100 + 0.5)
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

var statusTmpl = template.Must(template.New("status").Funcs(template.FuncMap{
	"humanize": humanize, "slug": slug, "mempct": memPct, "memcolor": memColor,
}).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>roost control</title>
{{if .Busy}}<meta http-equiv="refresh" content="3">{{end}}
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Google+Sans:ital,opsz,wght@0,17..18,400..700;1,17..18,400..700&display=swap" rel="stylesheet">
<style>
 :root{
  --bg1:#fdf1f8; --bg2:#eef1ff; --bg3:#ecfdfb; --bg4:#fff2f1;
  --panel:#ffffff; --panel2:#f8f9fc; --line:#ecedf3; --line2:#f2f3f7; --track:#edeff5;
  --ink:#12141c; --muted:#5f6675; --faint:#98a0b0;
  --brand:#5b54e6; --brand-ink:#fff;
  --ok:#12a150; --amber:#e08a1e; --danger:#e5484d;
  --red-bg:#fff1f2; --red-ink:#d64550; --red-line:#fbdde0;
  --amber-bg:#fff7ed; --amber-ink:#c2790f; --amber-line:#f8e6ce;
  --teal-bg:#ecfdf5; --teal-ink:#0e7a54;
  --indigo-bg:#eef0ff; --indigo-ink:#4f46e5;
  --shadow:0 1px 2px rgba(16,19,34,.04),0 12px 30px -18px rgba(16,19,34,.14);
  --shadow-lg:0 40px 80px -40px rgba(24,20,60,.30);
  --radius:16px;
  --font:"Google Sans","Product Sans","Google Sans Text",-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Inter,system-ui,sans-serif;
  --mono:ui-monospace,SFMono-Regular,Menlo,monospace;
 }
 @media(prefers-color-scheme:dark){:root{
  --bg1:#141021; --bg2:#0f1424; --bg3:#0d1a1e; --bg4:#1a1220;
  --panel:#12161f; --panel2:#161c27; --line:#222a38; --line2:#1b2230; --track:#212836;
  --ink:#e7ecf5; --muted:#9aa4b6; --faint:#68738a;
  --brand:#7c75f5;
  --ok:#37d383; --amber:#e6a84a; --danger:#f0616a;
  --red-bg:#2a1618; --red-ink:#f0868c; --red-line:#3a1e22;
  --amber-bg:#271c10; --amber-ink:#e6b877; --amber-line:#372711;
  --teal-bg:#0f2a1c; --teal-ink:#5bdc90;
  --indigo-bg:#191d38; --indigo-ink:#a9a3ff;
  --shadow:0 1px 2px rgba(0,0,0,.3),0 14px 34px -18px rgba(0,0,0,.7);
  --shadow-lg:0 40px 90px -40px rgba(0,0,0,.8);
 }}
 *{box-sizing:border-box}
 body{margin:0;min-height:100vh;padding:22px;color:var(--ink);font:14.5px/1.55 var(--font);-webkit-font-smoothing:antialiased;
  background:linear-gradient(120deg,var(--bg1),var(--bg2) 38%,var(--bg3) 68%,var(--bg4))}
 a{color:var(--brand);text-decoration:none} a:hover{text-decoration:underline}
 h1,h2,h3{margin:0}
 /* window shell */
 .shell{max-width:1340px;height:calc(100vh - 44px);margin:0 auto;background:var(--panel);border:1px solid var(--line);border-radius:22px;
  box-shadow:var(--shadow-lg);overflow:hidden;display:grid;grid-template-columns:234px minmax(0,1fr)}
 .content{display:flex;flex-direction:column;min-width:0;overflow:hidden}
 /* sidebar — fixed column, scrolls on its own */
 .side{border-right:1px solid var(--line);display:flex;flex-direction:column;padding:16px 12px;overflow-y:auto}
 .brand{display:flex;align-items:center;gap:11px;padding:6px 8px 14px}
 .logo{width:32px;height:32px;border-radius:9px;background:linear-gradient(135deg,#6f67f0,#a855f7 55%,#ec4899);
  display:grid;place-items:center;color:#fff;font-weight:800;box-shadow:var(--shadow)}
 .brand .bt{font-size:14.5px;font-weight:700;letter-spacing:-.2px}
 .brand .bs{font-size:11px;color:var(--faint)}
 .nav{display:flex;flex-direction:column;gap:1px}
 .nav a{display:flex;align-items:center;gap:10px;padding:8px 11px;border-radius:9px;color:var(--muted);font-weight:550;font-size:13px}
 .nav a:hover{background:var(--panel2);color:var(--ink);text-decoration:none}
 .nav a.active{background:var(--indigo-bg);color:var(--indigo-ink)}
 .nav .ico{width:15px;text-align:center;opacity:.85;font-size:13px}
 .navlabel{font-size:10px;text-transform:uppercase;letter-spacing:.09em;color:var(--faint);padding:14px 11px 5px;font-weight:700}
 .side .grow{flex:1;min-height:12px}
 .user{display:flex;align-items:center;gap:10px;padding:10px 8px;border-top:1px solid var(--line);margin-top:8px}
 .avatar{width:30px;height:30px;border-radius:50%;background:var(--indigo-bg);color:var(--indigo-ink);display:grid;place-items:center;font-size:11px;font-weight:700}
 .user .un{font-size:12.5px;font-weight:600} .user .ue{font-size:11px;color:var(--faint);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
 /* topbar */
 .topbar{display:flex;align-items:center;gap:10px;padding:13px 20px;border-bottom:1px solid var(--line);flex-wrap:wrap}
 .burger{display:none;font-size:19px;background:none;border:0;color:var(--ink);cursor:pointer}
 .search{flex:1;min-width:180px;position:relative}
 .search input{width:100%;font:inherit;font-size:13.5px;padding:9px 12px 9px 32px;border-radius:10px;border:1px solid var(--line);background:var(--panel2);color:var(--ink)}
 .search input:focus{outline:none;border-color:var(--brand)}
 .search .si{position:absolute;left:11px;top:50%;transform:translateY(-50%);color:var(--faint);font-size:13px}
 select.filter{font:inherit;font-size:13px;padding:9px 11px;border-radius:10px;border:1px solid var(--line);background:var(--panel2);color:var(--ink);cursor:pointer}
 .toggle{display:inline-flex;background:var(--panel2);border:1px solid var(--line);border-radius:10px;padding:3px}
 .toggle button{font:inherit;font-size:12.5px;font-weight:600;border:0;background:none;color:var(--muted);padding:5px 10px;border-radius:7px;cursor:pointer}
 .toggle button.active{background:var(--panel);color:var(--ink);box-shadow:var(--shadow)}
 /* buttons */
 .btn{font:inherit;font-weight:600;font-size:13px;border:1px solid var(--line);border-radius:10px;padding:8px 13px;cursor:pointer;
  color:var(--ink);background:var(--panel);transition:.12s;white-space:nowrap;display:inline-flex;align-items:center;gap:6px}
 .btn:hover{transform:translateY(-1px)} .btn[disabled]{opacity:.45;cursor:not-allowed;transform:none}
 .btn-sm{padding:6px 10px;font-size:12px;border-radius:9px}
 .btn-primary{background:var(--brand);border-color:var(--brand);color:var(--brand-ink)}
 .btn-ok{background:var(--teal-bg);border-color:transparent;color:var(--teal-ink)}
 .btn-ok:hover{background:var(--ok);color:#fff}
 .btn-danger{background:transparent;color:var(--danger)}
 .btn-danger:hover{background:var(--red-bg);border-color:var(--danger)}
 form.inline{display:inline}
 /* body grid */
 .body{padding:20px;display:grid;grid-template-columns:minmax(0,1fr) 322px;gap:18px;align-items:start;flex:1;overflow-y:auto}
 .card{background:var(--panel);border:1px solid var(--line);border-radius:var(--radius);box-shadow:var(--shadow);margin-bottom:18px}
 .card:last-child{margin-bottom:0}
 .card-h{display:flex;align-items:center;justify-content:space-between;padding:14px 18px;border-bottom:1px solid var(--line2)}
 .card-h h2{font-size:13.5px;font-weight:700;color:var(--ink);display:flex;align-items:center;gap:8px}
 .card-h .csub{font-size:11.5px;color:var(--faint);font-weight:400;margin-top:2px}
 .count{font-size:12px;color:var(--faint);background:var(--panel2);border:1px solid var(--line);border-radius:999px;padding:2px 9px}
 .badge-n{background:var(--danger);color:#fff;border:0;font-weight:700}
 /* needs attention */
 .attn-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(210px,1fr));gap:10px;padding:14px 16px}
 .att{border-radius:12px;padding:11px 13px;border:1px solid}
 .att.red{background:var(--red-bg);border-color:var(--red-line)}
 .att.amber{background:var(--amber-bg);border-color:var(--amber-line)}
 .att-top{display:flex;align-items:center;justify-content:space-between;gap:8px}
 .att-t{font-weight:700;font-size:13px} .att.red .att-t{color:var(--red-ink)} .att.amber .att-t{color:var(--amber-ink)}
 .att-s{font-size:11.5px;color:var(--muted);margin-top:3px}
 .att-b{font-size:10.5px;font-weight:700;text-transform:uppercase;letter-spacing:.03em;padding:2px 7px;border-radius:999px}
 .att.red .att-b{background:#fff;color:var(--red-ink)} .att.amber .att-b{background:#fff;color:var(--amber-ink)}
 @media(prefers-color-scheme:dark){.att.red .att-b,.att.amber .att-b{background:rgba(255,255,255,.08)}}
 .allclear{display:flex;align-items:center;gap:10px;padding:16px 18px;color:var(--teal-ink);font-weight:600;font-size:13.5px}
 .allclear .ico{width:26px;height:26px;border-radius:8px;background:var(--teal-bg);display:grid;place-items:center}
 /* metric cards */
 .metrics{display:grid;grid-template-columns:repeat(3,1fr);gap:14px;margin-bottom:18px}
 .metric{background:var(--panel);border:1px solid var(--line);border-radius:var(--radius);box-shadow:var(--shadow);padding:16px 18px}
 .metric .mh{display:flex;align-items:center;justify-content:space-between}
 .metric .mt{display:flex;align-items:center;gap:8px;font-size:12.5px;font-weight:600;color:var(--muted)}
 .metric .mi{width:26px;height:26px;border-radius:8px;display:grid;place-items:center;font-size:13px}
 .metric.teal .mi{background:var(--teal-bg);color:var(--teal-ink)}
 .metric.amber .mi{background:var(--amber-bg);color:var(--amber-ink)}
 .metric.red .mi{background:var(--red-bg);color:var(--red-ink)}
 .metric .mv{font-size:24px;font-weight:800;letter-spacing:-.5px}
 .metric .msub{font-size:11.5px;color:var(--faint);margin:2px 0 12px}
 .bar{height:7px;background:var(--track);border-radius:999px;overflow:hidden}
 .fill{display:block;height:100%;border-radius:999px}
 .fill.ok{background:linear-gradient(90deg,#18b45c,#12a150)}
 .fill.warn{background:linear-gradient(90deg,#eba33a,#e08a1e)}
 .fill.bad{background:linear-gradient(90deg,#ef5a5f,#e5484d)}
 .fill.teal{background:linear-gradient(90deg,#2bd07f,#12a150)}
 .fill.amber{background:linear-gradient(90deg,#eba33a,#e08a1e)}
 /* server/app rows — grouped: header then a list/grid of items */
 .grouphdr{display:flex;align-items:center;gap:9px;padding:14px 18px 8px;font-size:11px;font-weight:700;text-transform:uppercase;letter-spacing:.06em;color:var(--faint)}
 .grouphdr .gc{margin-left:auto;font-size:11px;background:var(--panel2);border:1px solid var(--line);color:var(--muted);border-radius:999px;padding:2px 9px;text-transform:none;letter-spacing:0}
 .group.hide{display:none}
 .glist.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(214px,1fr));gap:12px;padding:4px 16px 14px}
 .srv{display:flex;flex-direction:column;gap:11px;padding:13px 18px;border-top:1px solid var(--line2)}
 .glist.grid .srv{border:1px solid var(--line);border-radius:14px;padding:14px;background:var(--panel2)}
 .glist.grid .grouphdr{padding-left:4px}
 .glist.grid .srv-top{flex-wrap:wrap;align-items:center}
 .glist.grid .srv-idb{flex:1 1 55%}
 .glist.grid .srv-acts{flex-basis:100%;justify-content:flex-start;margin-top:2px}
 .srv-top{display:flex;align-items:flex-start;gap:12px}
 .srv-idb{min-width:0;flex:1}
 .srv-ico{width:30px;height:30px;border-radius:9px;background:var(--indigo-bg);color:var(--indigo-ink);display:grid;place-items:center;font-size:14px;flex:none;margin-top:1px}
 .srv-nm{display:flex;align-items:center;gap:8px;flex-wrap:wrap}
 .srv-name{font-weight:650;font-size:14px}
 .dot{width:8px;height:8px;border-radius:50%;flex:none}
 .dot.run{background:var(--ok);box-shadow:0 0 0 3px color-mix(in srgb,var(--ok) 22%,transparent)}
 .dot.stop{background:var(--faint)}
 .pill{font-size:10px;font-weight:700;text-transform:uppercase;letter-spacing:.04em;padding:2px 7px;border-radius:999px}
 .pill.run{background:var(--teal-bg);color:var(--teal-ink)}
 .pill.stop{background:var(--red-bg);color:var(--red-ink)}
 .srv-sub{font-size:12px;color:var(--muted);margin-top:3px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
 .srv-bar{width:100%}
 .mlabel{display:flex;justify-content:space-between;font-size:11px;color:var(--muted);margin-bottom:5px}
 .mlabel b{color:var(--ink);font-weight:700}
 .srv-acts{display:flex;align-items:center;gap:8px;justify-content:flex-end;flex:none}
 .seg{display:inline-flex;border:1px solid var(--line);border-radius:9px;overflow:hidden;background:var(--panel)}
 .seg form{display:flex}
 .segbtn{font:inherit;font-size:12.5px;font-weight:600;border:0;background:none;padding:6px 14px;cursor:pointer;color:var(--ink)}
 .seg form+form .segbtn{border-left:1px solid var(--line)}
 .segbtn.go{color:var(--teal-ink)} .segbtn.go:hover:not([disabled]){background:var(--teal-bg)}
 .segbtn.st:hover:not([disabled]){background:var(--panel2)}
 .segbtn[disabled]{opacity:.4;cursor:not-allowed}
 .menu{position:relative}
 .menu>summary{list-style:none} .menu>summary::-webkit-details-marker{display:none} .menu>summary::marker{content:""}
 .kebab{display:inline-flex;align-items:center;justify-content:center;min-width:34px;height:31px;border:1px solid var(--line);border-radius:9px;background:var(--panel);color:var(--muted);cursor:pointer;font-size:16px;line-height:1;letter-spacing:1px}
 .kebab:hover{background:var(--panel2);color:var(--ink)}
 .menu[open] .kebab{background:var(--panel2);color:var(--ink)}
 .menu-pop{position:absolute;right:0;top:calc(100% + 6px);z-index:20;width:216px;background:var(--panel);border:1px solid var(--line);border-radius:12px;box-shadow:var(--shadow-lg);padding:12px}
 .menu-pop .free{display:flex;align-items:center;gap:8px;font-size:12px;color:var(--muted);cursor:pointer;margin-bottom:11px}
 .menu-pop .free span{color:var(--faint)} .menu-pop .free input{accent-color:var(--danger)}
 .applist.grid .srv-acts,.glist.grid .srv-acts{justify-content:flex-start}
 /* right rail */
 .ov{padding:16px 18px}
 .ov-row{display:flex;justify-content:space-between;align-items:center;font-size:12.5px;padding:7px 0;border-bottom:1px solid var(--line2)}
 .ov-row:last-child{border-bottom:0} .ov-row .k{color:var(--muted)} .ov-row .v{font-weight:600}
 .ov-bar{margin:6px 0 14px}
 .ov-bar .mlabel{margin-bottom:6px}
 .procbody{padding:15px 18px}
 .status-line{display:flex;align-items:center;gap:9px;font-size:13.5px;font-weight:600}
 .spin{width:15px;height:15px;border-radius:50%;border:2px solid var(--line);border-top-color:var(--brand);animation:spin .7s linear infinite;flex:none}
 @keyframes spin{to{transform:rotate(360deg)}}
 .steps{list-style:none;margin:12px 0 0;padding:0}
 .steps li{position:relative;padding:4px 0 4px 20px;font-size:12px;font-family:var(--mono);color:var(--muted)}
 .steps li:before{content:"";position:absolute;left:5px;top:9px;width:6px;height:6px;border-radius:50%;background:var(--brand)}
 .idle{color:var(--faint);font-size:13px;display:flex;align-items:center;gap:8px}
 .idle .dot{width:7px;height:7px;border-radius:50%;background:var(--faint)}
 .result{margin:12px 0 0;padding:10px 12px;border-radius:10px;background:var(--panel2);border:1px solid var(--line);font-size:12px;color:var(--muted)}
 .result.err{background:var(--red-bg);border-color:transparent;color:var(--danger)}
 .rrow{display:flex;align-items:center;justify-content:space-between;gap:10px;padding:11px 18px;border-bottom:1px solid var(--line2)}
 .rrow:last-child{border-bottom:0} .rrow .rn{font-weight:600;font-size:13px}
 .rrow .rp{font-size:11px;color:var(--faint);font-family:var(--mono);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
 .empty{padding:16px 18px;color:var(--faint);font-size:13px}
 .footer{border-top:1px solid var(--line);padding:14px 22px;display:flex;justify-content:space-between;gap:12px;flex-wrap:wrap;font-size:12px;color:var(--faint)}
 /* modal */
 dialog{border:1px solid var(--line);border-radius:var(--radius);padding:0;width:min(440px,92vw);background:var(--panel);color:var(--ink);box-shadow:var(--shadow-lg)}
 dialog::backdrop{background:rgba(8,10,16,.5);backdrop-filter:blur(3px)}
 .modal-h{display:flex;align-items:center;justify-content:space-between;padding:16px 18px;border-bottom:1px solid var(--line2)}
 .modal-h h2{font-size:15px} .modal-x{background:none;border:0;font-size:20px;color:var(--faint);cursor:pointer;line-height:1}
 .modal-b{padding:18px}
 .field{margin-bottom:12px}
 .field label{display:block;font-size:12px;font-weight:600;color:var(--muted);margin-bottom:5px}
 .field input{width:100%;font:inherit;font-size:14px;padding:10px 12px;border-radius:10px;border:1px solid var(--line);background:var(--panel2);color:var(--ink)}
 .field input:focus{outline:none;border-color:var(--brand)}
 .hint{font-size:12px;color:var(--faint);margin:10px 0 14px}
 .hide{display:none!important}
 /* responsive */
 @media(max-width:1080px){.body{grid-template-columns:1fr}.metrics{grid-template-columns:1fr}}
 @media(max-width:860px){
  body{padding:0}
  .shell{border-radius:0;border:0;height:100vh;grid-template-columns:1fr}
  .side{position:fixed;left:0;top:0;bottom:0;z-index:30;width:240px;background:var(--panel);transform:translateX(-100%);transition:transform .2s;box-shadow:var(--shadow-lg)}
  body.nav-open .side{transform:none}
  body.nav-open:after{content:"";position:fixed;inset:0;background:rgba(0,0,0,.4);z-index:20}
  .burger{display:inline-block}
 }
 @media(max-width:600px){
  .srv{flex-direction:column;align-items:stretch;gap:11px}
  .srv-acts{justify-content:flex-start}
 }
</style></head><body>
<div class="shell">

 <aside class="side">
  <div class="brand">
   <div class="logo">r</div>
   <div><div class="bt">roost</div><div class="bs">Control panel</div></div>
  </div>
  <nav class="nav">
   <div class="navlabel">Resources</div>
   <a href="#" class="navf active" data-cat=""><span class="ico">▦</span> All apps</a>
   <a href="#" class="navf" data-cat="Main apps"><span class="ico">◆</span> Main apps</a>
   <a href="#" class="navf" data-cat="Utilities"><span class="ico">◈</span> Utilities</a>
   <a href="#" class="navf" data-cat="Workers"><span class="ico">⚙</span> Workers</a>
   <div class="navlabel">Monitoring</div>
   <a href="#attention"><span class="ico">⚠</span> Attention</a>
   <a href="#processing"><span class="ico">◷</span> Activity</a>
   <div class="navlabel">Manage</div>
   <a href="#removed"><span class="ico">↺</span> Removed</a>
   <a href="https://github.com/cdrrazan/roost" target="_blank" rel="noopener"><span class="ico">↗</span> Repository</a>
  </nav>
  <div class="grow"></div>
  <div class="user">
   <div class="avatar">RB</div>
   <div style="min-width:0"><div class="un">Admin</div><div class="ue">roost control</div></div>
  </div>
 </aside>

 <div class="content" id="top">
  <header class="topbar">
   <button class="burger" id="burger" aria-label="Menu">☰</button>
   <div class="search"><span class="si">⌕</span><input id="q" type="text" placeholder="Search apps…" autocomplete="off"></div>
   <select class="filter" id="statusfilter">
    <option value="all">All statuses</option>
    <option value="running">Running</option>
    <option value="stopped">Stopped</option>
   </select>
   <div class="toggle"><button data-view="list">▤ List</button><button data-view="grid">▦ Grid</button></div>
   <form class="inline" method="post" action="/up">{{if .Token}}<input type="hidden" name="token" value="{{.Token}}">{{end}}<button class="btn btn-ok" {{if .Busy}}disabled{{end}}>Start all</button></form>
   <form class="inline" method="post" action="/down">{{if .Token}}<input type="hidden" name="token" value="{{.Token}}">{{end}}<button class="btn" {{if .Busy}}disabled{{end}}>Stop all</button></form>
   <button class="btn btn-primary" id="openadd" {{if .Busy}}disabled{{end}}>＋ New app</button>
  </header>

  <div class="body">
  <main>
   {{if .Error}}
   <div class="card"><div class="result err" style="margin:16px">status error: {{.Error}}</div></div>
   {{else}}

   <section class="card" id="attention">
    <div class="card-h">
     <h2>⚠ Needs attention <span class="csub">{{if .Attention}}apps not currently running{{else}}everything is running{{end}}</span></h2>
     {{if .Attention}}<span class="count badge-n">{{len .Attention}}</span>{{end}}
    </div>
    {{if .Attention}}
    <div class="attn-grid">
     {{range .Attention}}
     <div class="att {{if eq .State "not created"}}amber{{else}}red{{end}}">
      <div class="att-top"><span class="att-t">{{humanize .Name}}</span><span class="att-b">{{.State}}</span></div>
      <div class="att-s">{{if .URL}}{{.URL}}{{else}}background worker{{end}}{{if .Health}} · {{.Health}}{{end}}</div>
     </div>
     {{end}}
    </div>
    {{else}}
    <div class="allclear"><span class="ico">✓</span> All {{.Total}} apps are up and running.</div>
    {{end}}
   </section>

   <div class="metrics">
    <div class="metric teal">
     <div class="mh"><div class="mt"><span class="mi">◉</span> Apps running</div></div>
     <div class="mv">{{.RunningCount}}<span style="font-size:15px;color:var(--faint)">/{{.Total}}</span></div>
     <div class="msub">processes online</div>
     <div class="bar"><span class="fill teal" style="width:{{.RunningPct}}%"></span></div>
    </div>
    <div class="metric amber">
     <div class="mh"><div class="mt"><span class="mi">▤</span> Memory usage</div></div>
     <div class="mv">{{.MemPct}}%</div>
     <div class="msub">{{if .MemCap}}{{.MemUsed}} / {{.MemCap}} used{{else}}usage unavailable{{end}}</div>
     <div class="bar"><span class="fill amber" style="width:{{.MemPct}}%"></span></div>
    </div>
    <div class="metric red">
     <div class="mh"><div class="mt"><span class="mi">◍</span> Stopped</div></div>
     <div class="mv">{{.StoppedCount}}</div>
     <div class="msub">not running{{if not .DockerOK}} · docker unreachable{{end}}</div>
     <div class="bar"><span class="fill bad" style="width:{{if .Total}}{{.StoppedCount}}{{else}}0{{end}}%"></span></div>
    </div>
   </div>

   <section class="card">
    <div class="card-h"><h2>Applications <span class="csub">manage and monitor your fleet</span></h2><span class="count">{{.RunningCount}}/{{.Total}} up</span></div>
    <div class="applist">
    {{range .Groups}}
     <div class="group" data-cat="{{.Title}}">
      <div class="grouphdr" id="{{slug .Title}}">{{.Title}}<span class="gc">{{len .Apps}}</span></div>
      <div class="glist">
      {{range .Apps}}
       <div class="srv" data-name="{{humanize .Name}}" data-state="{{if eq .State "running"}}running{{else}}stopped{{end}}">
        <div class="srv-top">
         <span class="srv-ico">{{if .URL}}◍{{else}}⚙{{end}}</span>
         <div class="srv-idb">
          <div class="srv-nm"><span class="dot {{if eq .State "running"}}run{{else}}stop{{end}}"></span><span class="srv-name">{{humanize .Name}}</span>{{if eq .State "running"}}<span class="pill run">{{.State}}</span>{{else}}<span class="pill stop">{{.State}}</span>{{end}}</div>
          <div class="srv-sub">{{if .URL}}<a href="{{.URL}}">{{.URL}}</a>{{else}}background worker{{end}}{{if .Health}} · {{.Health}}{{end}}</div>
         </div>
         <div class="srv-acts">
          <div class="seg">
           <form class="inline" method="post" action="/app/up"><input type="hidden" name="app" value="{{.Name}}">{{if $.Token}}<input type="hidden" name="token" value="{{$.Token}}">{{end}}<button class="segbtn go" {{if or $.Busy (eq .State "running")}}disabled{{end}}>Start</button></form>
           <form class="inline" method="post" action="/app/down"><input type="hidden" name="app" value="{{.Name}}">{{if $.Token}}<input type="hidden" name="token" value="{{$.Token}}">{{end}}<button class="segbtn st" {{if or $.Busy (ne .State "running")}}disabled{{end}}>Stop</button></form>
          </div>
          <details class="menu">
           <summary class="kebab" title="More actions">⋯</summary>
           <div class="menu-pop">
            <form method="post" action="/remove" onsubmit="return confirm('Remove {{humanize .Name}} from the config?')"><input type="hidden" name="app" value="{{.Name}}">{{if $.Token}}<input type="hidden" name="token" value="{{$.Token}}">{{end}}
             <label class="free"><input type="checkbox" name="image" value="on"> Also delete image <span>(free disk)</span></label>
             <button class="btn btn-sm btn-danger" style="width:100%;justify-content:center" {{if $.Busy}}disabled{{end}}>Remove app</button>
            </form>
           </div>
          </details>
         </div>
        </div>
        {{if .Memory}}<div class="srv-bar"><div class="mlabel">Memory <b>{{mempct .Memory}}%</b></div><div class="bar"><span class="fill {{memcolor .Memory}}" style="width:{{mempct .Memory}}%"></span></div></div>{{end}}
       </div>
      {{end}}
      </div>
     </div>
    {{else}}
     <div class="empty">No apps configured yet — use “New app”.</div>
    {{end}}
    </div>
   </section>
   {{end}}
  </main>

  <aside class="rail">
   <div class="card">
    <div class="card-h"><h2>Overview <span class="csub">fleet at a glance</span></h2></div>
    <div class="ov">
     <div class="ov-bar"><div class="mlabel">Uptime <b>{{.RunningPct}}%</b></div><div class="bar"><span class="fill teal" style="width:{{.RunningPct}}%"></span></div></div>
     <div class="ov-bar"><div class="mlabel">Memory <b>{{.MemPct}}%</b></div><div class="bar"><span class="fill amber" style="width:{{.MemPct}}%"></span></div></div>
     <div class="ov-row"><span class="k">Docker</span><span class="v">{{if .DockerOK}}connected{{else}}unreachable{{end}}</span></div>
     <div class="ov-row"><span class="k">Total apps</span><span class="v">{{.Total}}</span></div>
     <div class="ov-row"><span class="k">Running</span><span class="v">{{.RunningCount}}</span></div>
     <div class="ov-row"><span class="k">Stopped</span><span class="v">{{.StoppedCount}}</span></div>
     {{if .MemCap}}<div class="ov-row"><span class="k">Memory</span><span class="v">{{.MemUsed}} / {{.MemCap}}</span></div>{{end}}
    </div>
   </div>

   <div class="card" id="processing">
    <div class="card-h"><h2>Activity <span class="csub">latest actions</span></h2></div>
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
 var KEY="roost-view", cat="";
 function apply(v){
  document.querySelectorAll(".glist").forEach(function(e){e.classList.toggle("grid",v==="grid")});
  document.querySelectorAll("[data-view]").forEach(function(b){b.classList.toggle("active",b.dataset.view===v)});
 }
 apply(localStorage.getItem(KEY)||"list");
 document.querySelectorAll("[data-view]").forEach(function(b){
  b.addEventListener("click",function(){localStorage.setItem(KEY,b.dataset.view);apply(b.dataset.view);});
 });
 var q=document.getElementById("q"), sf=document.getElementById("statusfilter");
 function filter(){
  var term=(q?q.value:"").toLowerCase(), st=sf?sf.value:"all";
  document.querySelectorAll(".group").forEach(function(g){
   var catOk=!cat||g.dataset.cat===cat, any=false;
   g.querySelectorAll(".srv").forEach(function(r){
    var show=catOk && r.dataset.name.toLowerCase().indexOf(term)>-1 && (st==="all"||r.dataset.state===st);
    r.classList.toggle("hide",!show);
    if(show)any=true;
   });
   g.classList.toggle("hide",!(catOk&&any));
  });
 }
 if(q)q.addEventListener("input",filter);
 if(sf)sf.addEventListener("change",filter);
 // Sidebar category filter (show only that group, no page jump).
 document.querySelectorAll(".navf").forEach(function(a){
  a.addEventListener("click",function(e){
   e.preventDefault();
   cat=a.dataset.cat;
   document.querySelectorAll(".navf").forEach(function(x){x.classList.toggle("active",x===a)});
   filter();
   document.body.classList.remove("nav-open");
  });
 });
 // Close any open kebab menu when clicking elsewhere.
 document.addEventListener("click",function(e){
  document.querySelectorAll("details.menu[open]").forEach(function(d){ if(!d.contains(e.target)) d.removeAttribute("open"); });
 });
 var dlg=document.getElementById("addapp");
 var o=document.getElementById("openadd"), c=document.getElementById("closeadd");
 if(o&&dlg)o.addEventListener("click",function(){dlg.showModal();});
 if(c&&dlg)c.addEventListener("click",function(){dlg.close();});
 if(dlg)dlg.addEventListener("click",function(e){if(e.target===dlg)dlg.close();});
 var burger=document.getElementById("burger");
 if(burger)burger.addEventListener("click",function(){document.body.classList.toggle("nav-open");});
 document.querySelectorAll(".side .nav a:not(.navf)").forEach(function(a){a.addEventListener("click",function(){document.body.classList.remove("nav-open");});});
})();
</script>
</body></html>`))
