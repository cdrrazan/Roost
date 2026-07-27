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
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

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

// tech maps a framework/database/runtime key to its display name for badges.
func tech(s string) string {
	switch strings.ToLower(s) {
	case "rails":
		return "Rails"
	case "django":
		return "Django"
	case "next":
		return "Next.js"
	case "node":
		return "Node"
	case "sinatra":
		return "Sinatra"
	case "static":
		return "Static"
	case "mysql":
		return "MySQL"
	case "postgres":
		return "Postgres"
	default:
		if s == "" {
			return ""
		}
		return strings.ToUpper(s[:1]) + s[1:]
	}
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

	// ServerInfo returns host metadata for the panel's Server card (disk,
	// host/OS/uptime, and the configured IP + SSH login). Best-effort:
	// unknown fields are left empty.
	ServerInfo() ServerInfo

	// SystemInfo returns docker-level disk usage (images/containers/volumes
	// and reclaimable space) for the System card. Best-effort; a zero value
	// hides the card.
	SystemInfo() SystemInfo

	// EdgeInfo returns Cloudflare tunnel/DNS facts (from roost's state) for
	// the Edge card. Best-effort; a zero value hides the card.
	EdgeInfo() EdgeInfo

	// AppDetail returns one app's container detail (image, status, restart
	// count, env keys, recent logs) for the drawer. It must reject unknown
	// names so the panel can't read arbitrary containers.
	AppDetail(name string) (AppDetail, error)
}

// AppDetail is the per-app drawer payload, serialized to JSON.
type AppDetail struct {
	Name      string        `json:"name"`
	URL       string        `json:"url"`
	Image     string        `json:"image"`
	Status    string        `json:"status"`
	Health    string        `json:"health"`
	Restarts  int           `json:"restarts"`
	Port      int           `json:"port"`
	Framework string        `json:"framework"`
	Database  string        `json:"database"`
	EnvKeys   []string      `json:"envKeys"`
	Logs      string        `json:"logs"`
	Incidents []AppIncident `json:"incidents"` // this app's outage history, newest first
}

// AppIncident is one rendered incident row for the drawer's status section.
type AppIncident struct {
	Open bool   `json:"open"`
	Text string `json:"text"`
}

// SystemInfo is docker's disk accounting, shown on the System card.
type SystemInfo struct {
	Images      int
	ImagesSize  string
	Containers  int
	Volumes     int
	VolumesSize string
	BuildCache  string
	Reclaimable string
}

// EdgeInfo describes roost's Cloudflare edge: the tunnel and the routes it
// carries. All fields best-effort from ~/.roost/state.json + config.
type EdgeInfo struct {
	TunnelName string
	TunnelID   string   // short form
	Account    string   // short form
	Hosts      []string // DNS records / routing suffixes roost created
	Protected  bool     // Cloudflare Access is configured in front
	// TunnelState is the live connector health: "connected",
	// "reconnecting" (re-establishing after a wake — transient), "down", or
	// "" when unknown/not running. Refreshed with the rail every 5s.
	TunnelState string
}

// Alert is one dashboard warning surfaced in the banner.
type Alert struct {
	Level string // "warn" or "bad"
	Text  string
}

// RemovedApp is a previously-removed app the panel offers to re-add.
type RemovedApp struct {
	Name   string
	Path   string
	Domain string
}

// ServerInfo is host metadata shown in the panel's Server card (private page).
type ServerInfo struct {
	IP       string // public IP (from config)
	SSH      string // ssh <user>@<ip> (from config)
	Label    string // provider / shape / region (from config)
	Host     string // hostname
	OS       string // pretty OS name
	Uptime   string // humanized
	Cores    int
	RAM      string // total RAM, human
	DiskUsed string
	DiskCap  string
	DiskPct  int
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

	// trend is an in-memory per-app CPU% history (most recent last, capped),
	// accumulated across status polls to draw sparklines. No persistence — it
	// lives only for the panel process's lifetime.
	trendMu sync.Mutex
	trend   map[string][]float64

	// events is a rolling in-memory audit log of panel actions (most recent
	// first, capped), rendered as the activity timeline. Guarded by mu.
	events []event

	// Incident monitor state (guarded by mu). health is the last-known
	// per-app healthy flag; roostDown tracks a control-plane outage; primed
	// suppresses the email burst for states already broken at startup.
	notifier  Notifier
	health    map[string]bool
	roostDown bool
	primed    bool
	incidents []Incident
}

// Notifier delivers an incident alert (e.g. by email). A nil notifier disables
// delivery; incidents are still recorded and displayed.
type Notifier interface {
	Notify(subject, body string) error
}

// Incident is a detected outage: an app down/degraded, or the whole control
// plane unreachable. Resolved is zero while still open.
type Incident struct {
	App      string // "" = whole roost (control plane)
	Kind     string // "down", "degraded", "control"
	Detail   string
	Since    time.Time
	Resolved time.Time
}

// incidentsCap bounds the retained incident history.
const incidentsCap = 40

// SetNotifier attaches an incident notifier (called before Serve).
func (s *Server) SetNotifier(n Notifier) { s.notifier = n }

// StartMonitor runs incident detection on an interval in a background
// goroutine, so outages are caught (and emailed) even with no browser open.
func (s *Server) StartMonitor(interval time.Duration) {
	go func() {
		s.checkIncidents() // prime immediately
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			s.checkIncidents()
		}
	}()
}

// checkIncidents runs one detection pass: it reads status, diffs each app's
// health against the last pass, records opened/resolved incidents, and returns
// after firing notifications for any transitions. Notifications are sent
// outside the lock so a slow SMTP server can't stall status rendering.
func (s *Server) checkIncidents() {
	apps, err := s.ctrl.Status()
	now := time.Now()
	type note struct{ subject, body string }
	var out []note

	s.mu.Lock()
	if s.health == nil {
		s.health = map[string]bool{}
	}
	primed := s.primed
	link := "\n\nPanel: open your roost control panel."

	if err != nil {
		if !s.roostDown {
			s.roostDown = true
			s.openIncident("", "control", err.Error(), now)
			if primed {
				out = append(out, note{"Roost · control plane unreachable",
					"roost web can't reach Docker:\n" + err.Error() + "\n\n" + now.Format(time.RFC1123) + link})
			}
		}
	} else {
		if s.roostDown {
			s.roostDown = false
			s.resolveIncident("", now)
			if primed {
				out = append(out, note{"Roost · control plane recovered",
					"Docker is reachable again.\n\n" + now.Format(time.RFC1123) + link})
			}
		}
		for _, a := range apps {
			if a.Worker {
				continue
			}
			healthy := a.State == "running" && (a.HTTP == "" || a.Reachable)
			prev, seen := s.health[a.Name]
			s.health[a.Name] = healthy
			if healthy {
				if seen && !prev {
					dur := s.resolveIncident(a.Name, now)
					out = append(out, note{"Roost · " + humanize(a.Name) + " recovered",
						humanize(a.Name) + " is back up" + downFor(dur) + ".\n\n" + a.URL + "\n" + now.Format(time.RFC1123) + link})
				}
				continue
			}
			// unhealthy
			if !seen || prev {
				s.openIncident(a.Name, kindFor(a), detailFor(a), now)
				if primed {
					out = append(out, note{"Roost · " + humanize(a.Name) + " is " + shortState(a),
						humanize(a.Name) + " is " + detailFor(a) + ".\n\n" + a.URL + "\n" + now.Format(time.RFC1123) + link})
				}
			}
		}
	}
	s.primed = true
	s.mu.Unlock()

	if s.notifier != nil {
		for _, m := range out {
			_ = s.notifier.Notify(m.subject, m.body)
		}
	}
}

// openIncident records a new open incident for app+kind, unless one is already
// open for that app (dedup). Newest first, capped.
func (s *Server) openIncident(app, kind, detail string, at time.Time) {
	for i := range s.incidents {
		if s.incidents[i].App == app && s.incidents[i].Resolved.IsZero() {
			return
		}
	}
	s.incidents = append([]Incident{{App: app, Kind: kind, Detail: detail, Since: at}}, s.incidents...)
	if len(s.incidents) > incidentsCap {
		s.incidents = s.incidents[:incidentsCap]
	}
}

// resolveIncident closes the open incident for app and returns how long it was
// open (0 if none was open).
func (s *Server) resolveIncident(app string, at time.Time) time.Duration {
	for i := range s.incidents {
		if s.incidents[i].App == app && s.incidents[i].Resolved.IsZero() {
			s.incidents[i].Resolved = at
			return at.Sub(s.incidents[i].Since)
		}
	}
	return 0
}

func kindFor(a runner.AppStatus) string {
	if a.State == "running" {
		return "degraded"
	}
	return "down"
}

func shortState(a runner.AppStatus) string {
	if a.State == "running" {
		return "degraded"
	}
	return "down"
}

func detailFor(a runner.AppStatus) string {
	if a.State == "running" && !a.Reachable {
		return "up but returning HTTP " + a.HTTP
	}
	return a.State
}

// compactDur renders a duration as "45s" / "6m" / "2.1h".
func compactDur(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%.1fh", d.Hours())
	}
}

func downFor(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf(" after %ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf(" after %dm", int(d.Minutes()))
	default:
		return fmt.Sprintf(" after %.1fh", d.Hours())
	}
}

// event is one entry in the activity timeline.
type event struct {
	At   time.Time
	Text string
	OK   bool
}

// trendCap is how many CPU samples per app the sparkline retains.
const trendCap = 40

// eventsCap is how many recent actions the activity timeline retains.
const eventsCap = 24

// addEvent records a completed action in the timeline (newest first).
func (s *Server) addEvent(text string, ok bool) {
	s.events = append([]event{{At: time.Now(), Text: text, OK: ok}}, s.events...)
	if len(s.events) > eventsCap {
		s.events = s.events[:eventsCap]
	}
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
	mux.HandleFunc("POST /test-alert", s.guard(s.handleTestAlert))
	mux.HandleFunc("GET /incidents", s.handleIncidentsPage)
	mux.HandleFunc("POST /incidents/clear", s.guard(s.handleClearIncidents))
	mux.HandleFunc("GET /api/app", s.handleAppDetail)
	mux.HandleFunc("GET /status", s.handleStatusPage)
	return mux
}

// publicView is the read-only status-page payload. It deliberately carries no
// secrets (no IP/SSH/env/logs/tunnel ids) so the page is safe to expose.
type publicView struct {
	Apps      []publicApp
	Total     int
	Up        int
	AllOK     bool
	Incidents []string // open incidents, "Label — down 6m"
	Generated string
}

type publicApp struct {
	Name      string
	State     string
	Reachable bool
	HTTP      string
	Up        string
	URL       string
}

// handleStatusPage renders a controls-free public status page. Same host, so
// it sits behind Cloudflare Access by default; add an Access bypass for the
// /status path to make it truly public.
func (s *Server) handleStatusPage(w http.ResponseWriter, _ *http.Request) {
	apps, err := s.ctrl.Status()
	view := publicView{Generated: time.Now().Format("2006-01-02 15:04 MST")}
	if err == nil {
		view.AllOK = true
		for _, a := range apps {
			if a.Worker {
				continue
			}
			view.Total++
			ok := a.State == "running" && (a.HTTP == "" || a.Reachable)
			if a.State == "running" {
				view.Up++
			}
			if !ok {
				view.AllOK = false
			}
			view.Apps = append(view.Apps, publicApp{
				Name: humanize(a.Name), State: a.State, Reachable: a.Reachable,
				HTTP: a.HTTP, Up: a.Up, URL: a.URL,
			})
		}
	} else {
		view.AllOK = false
	}
	now := time.Now()
	s.mu.Lock()
	for _, in := range s.incidents {
		if in.Resolved.IsZero() {
			label := "Control plane"
			if in.App != "" {
				label = humanize(in.App)
			}
			view.Incidents = append(view.Incidents, label+" — down "+compactDur(now.Sub(in.Since)))
		}
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := publicTmpl.Execute(w, view); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleTestAlert sends a test notification so the user can confirm email
// delivery without waiting for a real outage.
func (s *Server) handleTestAlert(w http.ResponseWriter, r *http.Request) {
	s.runAction(w, r, "sending test alert", func(emit func(string)) error {
		if s.notifier == nil {
			return fmt.Errorf("email alerts not configured — set a notify: block and $ROOST_SMTP_PASSWORD")
		}
		emit("sending test email…")
		return s.notifier.Notify("Roost · Test alert",
			"This is a test alert from your roost control panel.\n"+
				"If you're reading this, incident email notifications are working.\n\n"+
				time.Now().Format(time.RFC1123))
	})
}

// handleClearIncidents drops resolved incidents from the history, leaving open
// ones untouched (you can't dismiss a live outage). Guarded — it mutates panel
// state — and redirects back to the incidents page.
func (s *Server) handleClearIncidents(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	kept := s.incidents[:0]
	for _, in := range s.incidents {
		if in.Resolved.IsZero() {
			kept = append(kept, in)
		}
	}
	s.incidents = kept
	s.mu.Unlock()
	http.Redirect(w, r, "/incidents", http.StatusSeeOther)
}

// handleAppDetail serves one app's drawer payload as JSON. Read-only, so it
// needs no token guard; the controller rejects unknown names. The Server folds
// in that app's own incident history (which lives in panel state, not the
// controller) so the drawer doubles as the app's status page.
func (s *Server) handleAppDetail(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "missing name", http.StatusBadRequest)
		return
	}
	detail, err := s.ctrl.AppDetail(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	detail.Incidents = s.appIncidents(name)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(detail)
}

// appIncidents returns one app's incident history (newest first) as rendered
// strings for the drawer, e.g. "down 6m · since Jul 27 15:04" or
// "resolved after 3m · Jul 27 15:10".
func (s *Server) appIncidents(name string) []AppIncident {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []AppIncident
	for _, in := range s.incidents {
		if in.App != name {
			continue
		}
		if in.Resolved.IsZero() {
			out = append(out, AppIncident{Open: true,
				Text: "down " + compactDur(now.Sub(in.Since)) + " · since " + in.Since.Format("Jan 2 15:04")})
		} else {
			out = append(out, AppIncident{
				Text: "resolved after " + compactDur(in.Resolved.Sub(in.Since)) + " · " + in.Resolved.Format("Jan 2 15:04")})
		}
	}
	return out
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
			s.addEvent(verb+" failed", false)
		} else {
			s.last = verb + " complete"
			s.addEvent(verb+" complete", true)
		}
		s.busy = ""
		s.mu.Unlock()
	}()

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

type statusView struct {
	Apps          []runner.AppStatus
	Groups        []appGroup         // apps bucketed into Main / Utilities / Workers
	Attention     []runner.AppStatus // apps not running — surfaced up top
	Total         int
	RunningCount  int
	RunningPct    int
	StoppedCount  int
	MemUsed       string // human total used across apps
	MemCap        string // human total cap across apps
	MemPct        int    // total used/cap percentage
	DockerOK      bool   // false when Status() failed (Docker unreachable)
	Busy          string
	Last          string
	Steps         []string     // processing-pane progress lines
	Removed       []RemovedApp // apps removed via the panel, offered for re-add
	Server        ServerInfo   // host metadata for the Server card
	System        SystemInfo   // docker disk usage for the System card
	Edge          EdgeInfo     // Cloudflare tunnel/DNS facts for the Edge card
	Alerts        []Alert      // warnings surfaced in the top banner
	Sparks        map[string]template.HTML
	Events        []eventView    // activity timeline (newest first)
	Incidents     []incidentView // detected outages (open first)
	OpenIncidents int
	Resolved      int    // count of resolved (clearable) incidents
	Page          string // "dashboard" (default) or "incidents"
	Error         string
	Token         string // embedded in the form when non-empty
}

// eventView is one rendered timeline entry.
type eventView struct {
	Time string
	Text string
	OK   bool
}

// incidentView is one rendered incident row.
type incidentView struct {
	Label  string // "Keeparu" or "Control plane"
	Detail string
	Since  string // clock time it opened
	Ago    string // "down 6m" / "resolved after 3m"
	Open   bool
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

// handleStatus renders the dashboard; handleIncidentsPage renders the same
// shell (both sidebars intact) with the incidents timeline in place of the
// dashboard body. Both share buildStatusView so the two pages never drift.
func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	s.renderPage(w, "dashboard")
}

func (s *Server) handleIncidentsPage(w http.ResponseWriter, _ *http.Request) {
	s.renderPage(w, "incidents")
}

func (s *Server) renderPage(w http.ResponseWriter, page string) {
	view := s.buildStatusView()
	view.Page = page
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := statusTmpl.Execute(w, view); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) buildStatusView() statusView {
	s.mu.Lock()
	view := statusView{Busy: s.busy, Last: s.last, Token: s.token,
		Steps: append([]string(nil), s.steps...)}
	for _, e := range s.events {
		view.Events = append(view.Events, eventView{Time: e.At.Format("15:04:05"), Text: e.Text, OK: e.OK})
	}
	now := time.Now()
	for _, in := range s.incidents {
		label := "Control plane"
		if in.App != "" {
			label = humanize(in.App)
		}
		iv := incidentView{Label: label, Detail: in.Detail, Since: in.Since.Format("Jan 2 15:04"), Open: in.Resolved.IsZero()}
		if iv.Open {
			iv.Ago = "down " + compactDur(now.Sub(in.Since))
			view.OpenIncidents++
		} else {
			iv.Ago = "resolved after " + compactDur(in.Resolved.Sub(in.Since))
			view.Resolved++
		}
		view.Incidents = append(view.Incidents, iv)
	}
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
		view.Alerts = buildAlerts(apps)
		view.Sparks = s.recordAndRenderTrends(apps)
	}
	if removed, err := s.ctrl.RemovedApps(); err == nil {
		view.Removed = removed
	}
	view.Server = s.ctrl.ServerInfo()
	view.System = s.ctrl.SystemInfo()
	view.Edge = s.ctrl.EdgeInfo()
	if view.Server.DiskPct >= 90 {
		view.Alerts = append(view.Alerts, Alert{Level: "bad", Text: fmt.Sprintf("Disk %d%% full on %s", view.Server.DiskPct, view.Server.Host)})
	} else if view.Server.DiskPct >= 80 {
		view.Alerts = append(view.Alerts, Alert{Level: "warn", Text: fmt.Sprintf("Disk %d%% full", view.Server.DiskPct)})
	}
	return view
}

// buildAlerts turns the fleet state into banner warnings: stopped apps and
// apps that are running but fail their HTTP probe (the silent-502 case).
func buildAlerts(apps []runner.AppStatus) []Alert {
	var alerts []Alert
	for _, a := range apps {
		if a.Worker {
			continue
		}
		switch {
		case a.State != "running":
			alerts = append(alerts, Alert{Level: "warn", Text: humanize(a.Name) + " is " + a.State})
		case a.HTTP != "" && !a.Reachable:
			alerts = append(alerts, Alert{Level: "bad", Text: humanize(a.Name) + " is up but returns " + a.HTTP})
		case memPct(a.Memory) >= 90:
			alerts = append(alerts, Alert{Level: "warn", Text: humanize(a.Name) + " memory at " + strconv.Itoa(memPct(a.Memory)) + "%"})
		}
	}
	return alerts
}

// recordAndRenderTrends appends each app's current CPU sample to the ring and
// returns a per-app inline sparkline SVG keyed by app name.
func (s *Server) recordAndRenderTrends(apps []runner.AppStatus) map[string]template.HTML {
	s.trendMu.Lock()
	defer s.trendMu.Unlock()
	if s.trend == nil {
		s.trend = map[string][]float64{}
	}
	out := map[string]template.HTML{}
	live := map[string]bool{}
	for _, a := range apps {
		live[a.Name] = true
		v := parseCPU(a.CPU)
		h := append(s.trend[a.Name], v)
		if len(h) > trendCap {
			h = h[len(h)-trendCap:]
		}
		s.trend[a.Name] = h
		if a.State == "running" {
			out[a.Name] = sparkSVG(h)
		}
	}
	// Drop history for apps that no longer exist.
	for name := range s.trend {
		if !live[name] {
			delete(s.trend, name)
		}
	}
	return out
}

// parseCPU turns docker's "2.75%" into 2.75; returns 0 on anything unparseable.
func parseCPU(s string) float64 {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "%"))
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

// sparkSVG renders a CPU history as a tiny inline sparkline. Needs ≥2 points;
// scales to the max sample so low-but-varying usage is still visible.
func sparkSVG(h []float64) template.HTML {
	if len(h) < 2 {
		return ""
	}
	const w, ht = 96.0, 22.0
	max := 1.0
	for _, v := range h {
		if v > max {
			max = v
		}
	}
	var b strings.Builder
	step := w / float64(len(h)-1)
	for i, v := range h {
		x := float64(i) * step
		y := ht - (v/max)*(ht-2) - 1
		if i == 0 {
			fmt.Fprintf(&b, "M%.1f %.1f", x, y)
		} else {
			fmt.Fprintf(&b, " L%.1f %.1f", x, y)
		}
	}
	return template.HTML(fmt.Sprintf(
		`<svg class="cpuspark" viewBox="0 0 %.0f %.0f" preserveAspectRatio="none"><path d="%s"/></svg>`,
		w, ht, b.String()))
}

var statusTmpl = template.Must(template.New("status").Funcs(template.FuncMap{
	"humanize": humanize, "slug": slug, "mempct": memPct, "memcolor": memColor, "tech": tech,
}).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>roost control</title>
<script>(function(){try{var r=document.documentElement,t=localStorage.getItem("roost-theme")||(matchMedia("(prefers-color-scheme:dark)").matches?"dark":"light");r.dataset.theme=t;if(localStorage.getItem("roost-side")==="off")r.dataset.side="off";if(localStorage.getItem("roost-rail")==="off")r.dataset.rail="off";}catch(e){}})();</script>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Google+Sans:ital,opsz,wght@0,17..18,400..700;1,17..18,400..700&display=swap" rel="stylesheet">
<link rel="icon" type="image/svg+xml" href="data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHZpZXdCb3g9IjAgMCA0MCA0MCIgZmlsbD0ibm9uZSI+PHJlY3Qgd2lkdGg9IjQwIiBoZWlnaHQ9IjQwIiByeD0iMTEiIGZpbGw9InVybCgjcmcpIi8+PHBhdGggZD0iTTEwLjUgMTkuMiBMMjAgMTEgTDI5LjUgMTkuMiIgc3Ryb2tlPSIjZmZmIiBzdHJva2Utd2lkdGg9IjIuNiIgc3Ryb2tlLWxpbmVjYXA9InJvdW5kIiBzdHJva2UtbGluZWpvaW49InJvdW5kIi8+PHBhdGggZD0iTTEzLjQgMTguNCBWMjguNiBIMjYuNiBWMTguNCIgc3Ryb2tlPSIjZmZmIiBzdHJva2Utd2lkdGg9IjIuNiIgc3Ryb2tlLWxpbmVjYXA9InJvdW5kIiBzdHJva2UtbGluZWpvaW49InJvdW5kIi8+PHBhdGggZD0iTTE3LjQgMjguNiBWMjQgYTIuNiAyLjYgMCAwIDEgNS4yIDAgVjI4LjYiIHN0cm9rZT0iI2ZmZiIgc3Ryb2tlLXdpZHRoPSIyLjIiIHN0cm9rZS1saW5lY2FwPSJyb3VuZCIgc3Ryb2tlLWxpbmVqb2luPSJyb3VuZCIvPjxkZWZzPjxsaW5lYXJHcmFkaWVudCBpZD0icmciIHgxPSIwIiB5MT0iMCIgeDI9IjEiIHkyPSIxIj48c3RvcCBzdG9wLWNvbG9yPSIjOGI4M2Y3Ii8+PHN0b3Agb2Zmc2V0PSIuNTUiIHN0b3AtY29sb3I9IiM1YjU0ZTYiLz48c3RvcCBvZmZzZXQ9IjEiIHN0b3AtY29sb3I9IiM0MzM4Y2EiLz48L2xpbmVhckdyYWRpZW50PjwvZGVmcz48L3N2Zz4K">
<style>
 /* Material Design 3 tonal system (kept in sync with the Fleet template) */
 :root{
  color-scheme:light;
  --md-primary:#5b54e6; --md-on-primary:#ffffff;
  --md-primary-container:#e4e0ff; --md-on-primary-container:#16008a;
  --md-secondary:#5d5c72; --md-secondary-container:#e3e0f9; --md-on-secondary-container:#191a2c;
  --md-tertiary:#0e7a54; --md-tertiary-container:#a8f2cd; --md-on-tertiary-container:#00210f;
  --md-error:#ba1a1a; --md-on-error:#ffffff; --md-error-container:#ffdad6; --md-on-error-container:#410002;
  --md-warning:#8a5300; --md-warning-container:#ffddb0; --md-on-warning-container:#2b1700;
  --md-surface:#faf8ff; --md-surface-container-lowest:#ffffff; --md-surface-container-low:#f4f2fc;
  --md-surface-container:#eeecf6; --md-surface-container-high:#e8e6f1; --md-surface-container-highest:#e3e0eb;
  --md-on-surface:#1b1b21; --md-on-surface-variant:#48464f; --md-outline:#79767f; --md-outline-variant:#cac4d0;
  --elev-1:0 1px 2px rgba(16,19,34,.28),0 1px 3px 1px rgba(16,19,34,.10);
  --elev-2:0 1px 2px rgba(16,19,34,.28),0 2px 6px 2px rgba(16,19,34,.10);
  --elev-3:0 1px 3px rgba(16,19,34,.28),0 4px 8px 3px rgba(16,19,34,.12);
  --elev-4:0 2px 3px rgba(16,19,34,.28),0 6px 10px 4px rgba(16,19,34,.12);
  --elev-5:0 4px 4px rgba(16,19,34,.28),0 8px 12px 6px rgba(16,19,34,.14);
  --state-hover:.08; --state-focus:.10; --state-press:.10;
  --radius-xs:8px; --radius-sm:12px; --radius:16px; --radius-lg:20px; --radius-xl:28px; --radius-full:999px;
  --md-standard:cubic-bezier(.2,0,0,1); --md-emphasized:cubic-bezier(.2,0,0,1);
  --font:"Google Sans","Product Sans","Google Sans Text",Roboto,-apple-system,BlinkMacSystemFont,"Segoe UI",Inter,system-ui,sans-serif;
  --mono:ui-monospace,SFMono-Regular,"Roboto Mono",Menlo,monospace;
  /* legacy aliases → MD3 */
  --bg:var(--md-surface); --bg1:var(--md-surface-container-low); --bg2:var(--md-surface-container); --bg3:var(--md-surface-container-high); --bg4:var(--md-surface-container-high);
  --panel:var(--md-surface-container-low); --panel2:var(--md-surface-container);
  --line:var(--md-outline-variant); --line2:var(--md-surface-container-high); --track:var(--md-surface-container-high);
  --ink:var(--md-on-surface); --muted:var(--md-on-surface-variant); --faint:var(--md-outline);
  --brand:var(--md-primary); --brand-ink:var(--md-on-primary);
  --ok:#1f8b4c; --amber:#a86a00; --danger:var(--md-error);
  --red-bg:var(--md-error-container); --red-ink:#8c1010; --red-line:color-mix(in srgb,var(--md-error) 30%,transparent);
  --amber-bg:var(--md-warning-container); --amber-ink:var(--md-on-warning-container); --amber-line:color-mix(in srgb,var(--amber) 30%,transparent);
  --teal-bg:var(--md-tertiary-container); --teal-ink:var(--md-on-tertiary-container);
  --indigo-bg:var(--md-primary-container); --indigo-ink:var(--md-on-primary-container);
  --shadow:var(--elev-1); --shadow-lg:var(--elev-3);
  --ease:var(--md-standard); --spring:cubic-bezier(.34,1.4,.5,1);
 }
 :root[data-theme="dark"]{
  color-scheme:dark;
  --md-primary:#c7bfff; --md-on-primary:#29018f; --md-primary-container:#4239a5; --md-on-primary-container:#e5deff;
  --md-secondary:#c6c3dc; --md-secondary-container:#454458; --md-on-secondary-container:#e3e0f9;
  --md-tertiary:#5bdca0; --md-tertiary-container:#00522f; --md-on-tertiary-container:#8bf9c4;
  --md-error:#ffb4ab; --md-on-error:#690005; --md-error-container:#93000a; --md-on-error-container:#ffdad6;
  --md-warning:#ffb95a; --md-warning-container:#5c3d00; --md-on-warning-container:#ffddb0;
  --md-surface:#131218; --md-surface-container-lowest:#0d0e13; --md-surface-container-low:#1b1b21;
  --md-surface-container:#201f26; --md-surface-container-high:#2a2930; --md-surface-container-highest:#35343b;
  --md-on-surface:#e5e1e9; --md-on-surface-variant:#c9c5d0; --md-outline:#938f99; --md-outline-variant:#48464f;
  --elev-1:0 1px 2px rgba(0,0,0,.5),0 1px 3px 1px rgba(0,0,0,.3);
  --elev-2:0 1px 2px rgba(0,0,0,.5),0 2px 6px 2px rgba(0,0,0,.3);
  --elev-3:0 1px 3px rgba(0,0,0,.5),0 4px 8px 3px rgba(0,0,0,.35);
  --elev-4:0 2px 3px rgba(0,0,0,.5),0 6px 10px 4px rgba(0,0,0,.35);
  --elev-5:0 4px 4px rgba(0,0,0,.5),0 8px 12px 6px rgba(0,0,0,.4);
  --ok:#5bdc90; --amber:#ffb95a; --danger:var(--md-error); --red-ink:#ffb4ab;
 }
 *{box-sizing:border-box}
 html{background:var(--bg)}
 body{margin:0;height:100vh;overflow:hidden;color:var(--ink);font:15px/1.55 var(--font);-webkit-font-smoothing:antialiased;background:var(--bg)}
 a{color:var(--brand);text-decoration:none} a:hover{text-decoration:underline}
 h1,h2,h3{margin:0}
 /* full-screen shell */
 .shell{width:100vw;height:100vh;background:var(--bg);overflow:hidden;display:grid;grid-template-columns:246px minmax(0,1fr)}
 .content{display:flex;flex-direction:column;min-width:0;overflow:hidden}
 /* sidebar — fixed column, scrolls on its own */
 .side{background:var(--panel);border-right:1px solid var(--line);display:flex;flex-direction:column;padding:16px 12px;overflow:hidden}
 .brand{display:flex;align-items:center;gap:11px;padding:6px 8px 14px;flex:none}
 .logo{width:36px;height:36px;border-radius:10px;box-shadow:var(--shadow);flex:none;overflow:hidden}
 .logo svg{width:100%;height:100%;display:block}
 .brand .bt{font-size:15.5px;font-weight:700;letter-spacing:-.2px}
 .brand .bs{font-size:12px;color:var(--faint)}
 .nav{display:flex;flex-direction:column;gap:1px;flex:1 1 auto;min-height:0;overflow-y:auto;overflow-x:hidden;margin:0 -4px;padding:0 4px}
 .nav a{display:flex;align-items:center;gap:11px;padding:9px 11px;border-radius:9px;color:var(--muted);font-weight:550;font-size:14px}
 .nav a:hover{background:var(--panel2);color:var(--ink);text-decoration:none}
 .nav a.active{background:var(--indigo-bg);color:var(--indigo-ink)}
 .nav .ico{width:18px;height:18px;flex:none;display:inline-flex;align-items:center;justify-content:center;color:var(--faint)}
 .nav .ico svg{width:18px;height:18px;stroke:currentColor}
 .nav a:hover .ico,.nav a.active .ico{color:currentColor}
 .navlabel{font-size:10.5px;text-transform:uppercase;letter-spacing:.09em;color:var(--faint);padding:15px 11px 5px;font-weight:700}
 .side .grow{display:none}
 .sideinc,.sidesys{flex:none}
 .side .user{flex:none}
 .sideinc{padding:11px;margin:12px 0 8px;border:1px solid var(--line);border-radius:11px;background:var(--panel2)}
 .sideinc .si-h{display:flex;align-items:center;justify-content:space-between;margin-bottom:8px}
 .sideinc .si-list{list-style:none;margin:0 0 2px;padding:0;display:flex;flex-direction:column;gap:6px}
 .sideinc .si-list li{font-size:11.5px;color:var(--muted);padding-left:13px;position:relative;line-height:1.4}
 .sideinc .si-list li::before{content:"";position:absolute;left:0;top:5px;width:6px;height:6px;border-radius:50%;background:var(--ok)}
 .sideinc .si-list li.bad::before{background:var(--danger)}
 .sideinc .si-list li b{color:var(--ink);font-weight:600}
 .sideinc .si-link{display:block;font-size:12px;color:var(--muted);text-decoration:none;padding:2px 0 4px}
 .sideinc .si-link:hover{color:var(--ink);text-decoration:underline}
 .nav .navpip{margin-left:auto;font-size:11px;font-weight:700;min-width:18px;height:18px;padding:0 5px;border-radius:999px;background:var(--danger);color:#fff;display:inline-flex;align-items:center;justify-content:center}
 .sidesys{padding:10px 11px;margin:0 0 8px;border:1px solid var(--line);border-radius:11px;background:var(--panel2)}
 .sidesys .navlabel{padding:0 0 7px}
 .sidesys .ss-row{display:flex;justify-content:space-between;gap:8px;font-size:12px;color:var(--muted);padding:3px 0}
 .sidesys .ss-row b{color:var(--ink);font-weight:600;text-align:right}
 .user{display:flex;align-items:center;gap:10px;padding:10px 8px;border-top:1px solid var(--line);margin-top:8px}
 .avatar{width:34px;height:34px;border-radius:50%;overflow:hidden;flex:none;background:var(--indigo-bg)}
 .avatar img{width:100%;height:100%;object-fit:cover;display:block}
 .user .un{font-size:13.5px;font-weight:600} .user .ue{font-size:11.5px;color:var(--faint);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
 /* topbar */
 .topbar{display:flex;align-items:center;gap:10px;padding:13px 20px;border-bottom:1px solid var(--line);flex-wrap:wrap;background:var(--panel);flex:none}
 .burger{display:none;font-size:19px;background:none;border:0;color:var(--ink);cursor:pointer}
 .search{flex:1;min-width:180px;position:relative}
 .search input{width:100%;font:inherit;font-size:13.5px;padding:9px 12px 9px 32px;border-radius:10px;border:1px solid var(--line);background:var(--panel2);color:var(--ink)}
 .search input:focus{outline:none;border-color:var(--brand)}
 .search .si{position:absolute;left:11px;top:50%;transform:translateY(-50%);color:var(--faint);font-size:13px}
 select.filter{font:inherit;font-size:13px;padding:9px 11px;border-radius:10px;border:1px solid var(--line);background:var(--panel2);color:var(--ink);cursor:pointer}
 .toggle{display:inline-flex;background:var(--panel2);border:1px solid var(--line);border-radius:10px;padding:3px}
 .toggle button{font:inherit;font-size:12.5px;font-weight:600;border:0;background:none;color:var(--muted);padding:5px 10px;border-radius:7px;cursor:pointer}
 .toggle button.active{background:var(--panel);color:var(--ink);box-shadow:var(--shadow)}
 .iconbtn{width:37px;height:37px;border:1px solid var(--line);border-radius:10px;background:var(--panel);color:var(--muted);display:inline-flex;align-items:center;justify-content:center;cursor:pointer;flex:none}
 .iconbtn:hover{color:var(--ink);background:var(--panel2)}
 .iconbtn.on{background:var(--indigo-bg);color:var(--indigo-ink);border-color:transparent}
 .iconbtn.palk{width:auto;padding:0 10px} .iconbtn.palk kbd{font:inherit;font-size:11px;font-weight:600;color:var(--muted)}
 .iconbtn svg{width:18px;height:18px;stroke:currentColor}
 .iconbtn .sun{display:none} :root[data-theme="dark"] .iconbtn .moon{display:none} :root[data-theme="dark"] .iconbtn .sun{display:inline-flex}
 .user .logout{margin-left:auto;flex:none;width:32px;height:32px;border-radius:9px;color:var(--faint);display:inline-flex;align-items:center;justify-content:center}
 .user .logout:hover{background:var(--panel2);color:var(--danger);text-decoration:none}
 .user .logout svg{width:17px;height:17px;stroke:currentColor}
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
 /* main scrolls; rail is a fixed panel that scrolls on its own */
 .body{flex:1;min-height:0;display:grid;grid-template-columns:minmax(0,1fr) 340px;overflow:hidden}
 main{overflow-y:auto;padding:20px;min-width:0}
 .rail{overflow-y:auto;height:100%;padding:20px;border-left:1px solid var(--line);background:var(--bg)}
 /* collapsible sidebars — desktop only (below the breakpoints the columns
    already stack / overlay, so collapsing is meaningless there) */
 .shell{transition:grid-template-columns .18s ease}
 .body{transition:grid-template-columns .18s ease}
 @media(min-width:861px){
  :root[data-side="off"] .shell{grid-template-columns:0 minmax(0,1fr)}
  :root[data-side="off"] .side{padding-left:0;padding-right:0;border-right:0;overflow:hidden}
  :root[data-side="off"] .side>*{opacity:0}
 }
 @media(min-width:1081px){
  :root[data-rail="off"] .body{grid-template-columns:minmax(0,1fr) 0}
  :root[data-rail="off"] .rail{padding-left:0;padding-right:0;border-left:0;overflow:hidden}
  :root[data-rail="off"] .rail>*{opacity:0}
 }
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
 :root[data-theme="dark"] .att.red .att-b,:root[data-theme="dark"] .att.amber .att-b{background:rgba(255,255,255,.08)}
 .allclear{display:flex;align-items:center;gap:10px;padding:16px 18px;color:var(--teal-ink);font-weight:600;font-size:13.5px}
 .allclear .ico{width:26px;height:26px;border-radius:8px;background:var(--teal-bg);display:grid;place-items:center}
 /* metric cards */
 /* stat graphs row — two gauges + a per-app memory bar chart */
 .graphs{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:14px;margin-bottom:18px}
 .gcard{background:var(--panel);border:1px solid var(--line);border-radius:var(--radius);box-shadow:var(--shadow);padding:15px 17px}
 .gcard .gh{display:flex;align-items:center;gap:8px;font-size:12.5px;font-weight:600;color:var(--muted);margin-bottom:14px}
 .gcard .gh .mi{width:28px;height:28px;border-radius:8px;display:grid;place-items:center}
 .gcard.teal .mi{background:var(--teal-bg);color:var(--teal-ink)}
 .gcard.amber .mi{background:var(--amber-bg);color:var(--amber-ink)}
 .gcard.indigo .mi{background:var(--indigo-bg);color:var(--indigo-ink)}
 .gcard .mi svg{width:16px;height:16px;stroke:currentColor}
 .donutrow{display:flex;align-items:center;gap:16px}
 .donut{--v:0;--c:var(--ok);width:96px;height:96px;flex:none;border-radius:50%;position:relative;background:conic-gradient(var(--c) calc(var(--v)*1%),var(--track) 0)}
 .donut::after{content:"";position:absolute;inset:11px;border-radius:50%;background:var(--panel);z-index:0}
 .donut .dc{position:absolute;inset:0;z-index:1;display:grid;place-items:center;text-align:center;line-height:1.05}
 .donut .dc b{font-size:21px;font-weight:800;letter-spacing:-.5px}
 .donut .dc span{display:block;font-size:10px;color:var(--faint);margin-top:2px}
 .dleg{display:flex;flex-direction:column;gap:9px;font-size:12.5px;color:var(--muted);min-width:0;flex:1}
 .dleg .li{display:flex;align-items:center;gap:8px} .dleg .li b{color:var(--ink);font-weight:700;margin-left:auto}
 .dleg .sw{width:9px;height:9px;border-radius:3px;flex:none}
 .spark{position:relative;display:flex;align-items:flex-end;gap:3px;height:82px}
 .spark .sb{flex:1;min-width:2px;height:100%;background:var(--track);border-radius:3px;display:flex;align-items:flex-end;overflow:hidden;cursor:pointer;transition:filter .1s}
 .spark .sb:hover{filter:brightness(1.12)}
 .spark .sb.hot{outline:2px solid var(--indigo-ink);outline-offset:1px}
 .spark .sb i{display:block;width:100%;min-height:2px;border-radius:3px}
 .spark-tip{position:absolute;z-index:20;pointer-events:none;transform:translate(-50%,-100%);background:var(--ink);color:var(--panel);font-size:11px;line-height:1.35;padding:6px 9px;border-radius:8px;box-shadow:var(--shadow);white-space:nowrap}
 .spark-tip b{display:block;font-size:12px;font-weight:600}
 .spark-tip::after{content:"";position:absolute;left:50%;top:100%;transform:translateX(-50%);border:5px solid transparent;border-top-color:var(--ink)}
 .sparkcap{display:flex;justify-content:space-between;font-size:10.5px;color:var(--faint);margin-top:9px}
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
 .glist.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(300px,1fr));gap:16px;padding:6px 18px 18px}
 .srv{display:flex;flex-direction:column;gap:11px;padding:13px 18px;border-top:1px solid var(--line2)}
 .glist.grid .srv{border:1px solid var(--line);border-radius:14px;padding:18px;gap:14px;background:var(--panel2)}
 .glist.grid .grouphdr{padding-left:4px}
 .glist.grid .srv-top{flex-wrap:wrap;align-items:center}
 .glist.grid .srv-idb{flex:1 1 55%}
 .glist.grid .srv-acts{flex-basis:100%;justify-content:flex-start;margin-top:2px}
 .srv-top{display:flex;align-items:flex-start;gap:12px}
 .srv-idb{min-width:0;flex:1}
 .srv-ico{width:32px;height:32px;border-radius:9px;background:var(--indigo-bg);color:var(--indigo-ink);display:grid;place-items:center;flex:none;margin-top:1px}
 .srv-ico svg{width:17px;height:17px;stroke:currentColor}
 .srv-nm{display:flex;align-items:center;gap:8px;flex-wrap:wrap}
 .srv-name{font-weight:650;font-size:14px}
 .dot{width:8px;height:8px;border-radius:50%;flex:none}
 .dot.run{background:var(--ok);box-shadow:0 0 0 3px color-mix(in srgb,var(--ok) 22%,transparent)}
 .dot.stop{background:var(--faint)}
 .pill{font-size:10px;font-weight:700;text-transform:uppercase;letter-spacing:.04em;padding:2px 7px;border-radius:999px}
 .pill.run{background:var(--teal-bg);color:var(--teal-ink)}
 .pill.stop{background:var(--red-bg);color:var(--red-ink)}
 .srv-sub{font-size:12px;color:var(--muted);margin-top:3px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
 .srv-sub .repo{display:inline-flex;align-items:center;gap:3px;color:var(--muted);text-decoration:none;font-weight:500}
 .srv-sub .repo:hover{color:var(--ink)}
 .srv-sub .repo svg{flex:none}
 .livedot{font-size:10.5px;font-weight:700;letter-spacing:.05em;text-transform:uppercase;color:var(--teal-ink);display:inline-flex;align-items:center;gap:5px;margin-left:auto}
 .livedot::before{content:"";width:7px;height:7px;border-radius:50%;background:var(--ok);box-shadow:0 0 0 0 var(--ok);animation:pulse 2s infinite}
 .livedot.beat::before{animation:none;background:var(--ok);box-shadow:0 0 0 4px color-mix(in srgb,var(--ok) 30%,transparent)}
 @keyframes pulse{0%{box-shadow:0 0 0 0 color-mix(in srgb,var(--ok) 55%,transparent)}70%{box-shadow:0 0 0 6px transparent}100%{box-shadow:0 0 0 0 transparent}}
 .rchip{font-size:10.5px;font-weight:700;padding:2px 8px;border-radius:999px;display:inline-flex;align-items:center;gap:5px;letter-spacing:.02em}
 .rchip::before{content:"";width:6px;height:6px;border-radius:50%;background:currentColor;box-shadow:0 0 0 3px color-mix(in srgb,currentColor 22%,transparent)}
 .rchip.up{background:var(--teal-bg);color:var(--teal-ink)}
 .rchip.down{background:var(--red-bg);color:var(--danger)}
 .rchip.warn{background:var(--amber-bg);color:var(--amber-ink)}
 .tags{display:flex;flex-wrap:wrap;gap:5px;margin-top:7px}
 .tag{font-size:10.5px;font-weight:600;padding:2px 7px;border-radius:6px;letter-spacing:.01em;line-height:1.5;border:1px solid transparent}
 .tag.fw{background:var(--indigo-bg);color:var(--indigo-ink)}
 .tag.db{background:var(--teal-bg);color:var(--teal-ink)}
 .tag.redis{background:var(--red-bg);color:var(--danger)}
 .tag.rt{background:var(--panel2);color:var(--muted);border-color:var(--line)}
 .tag.worker{background:var(--amber-bg);color:var(--amber-ink)}
 .srv-metrics{display:flex;flex-wrap:wrap;gap:14px;margin-top:10px;padding-top:10px;border-top:1px solid var(--line);font-size:12px;color:var(--ink);font-variant-numeric:tabular-nums}
 .srv-metrics .me{display:inline-flex;align-items:center;gap:6px}
 .srv-metrics .me i{font-style:normal;font-size:9.5px;font-weight:700;letter-spacing:.06em;color:var(--faint)}
 .srv-metrics .me.up{margin-left:auto;color:var(--muted)}
 .cpuspark{width:60px;height:16px;overflow:visible}
 .cpuspark path{fill:none;stroke:var(--indigo-ink);stroke-width:1.5;vector-effect:non-scaling-stroke;stroke-linejoin:round;stroke-linecap:round}
 .incbanner{display:flex;align-items:center;gap:10px;padding:13px 16px;border-radius:12px;margin-bottom:14px;background:var(--red-bg);color:var(--danger);border:1px solid color-mix(in srgb,var(--danger) 35%,transparent);font-size:13.5px}
 .incbanner .ib-dot{width:9px;height:9px;border-radius:50%;background:var(--danger);flex:none;box-shadow:0 0 0 0 var(--danger);animation:pulse 1.8s infinite}
 .incbanner .ib-list{color:var(--ink);opacity:.8;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
 /* incidents page */
 #incidents-page .inc-actions{display:flex;gap:8px;align-items:center}
 .inclist{list-style:none;margin:6px 0 0;padding:0;display:flex;flex-direction:column}
 .incrow{display:flex;align-items:flex-start;gap:12px;padding:13px 6px;border-top:1px solid var(--line2)}
 .incrow:first-child{border-top:0}
 .incrow .incdot{width:9px;height:9px;border-radius:50%;flex:none;margin-top:5px;background:var(--ok)}
 .incrow.open .incdot{background:var(--danger);box-shadow:0 0 0 0 var(--danger);animation:pulse 1.8s infinite}
 .incrow .incmain{min-width:0;flex:1}
 .incrow .inctop{display:flex;align-items:baseline;gap:9px;flex-wrap:wrap}
 .incrow .inctop b{font-weight:650;font-size:14px}
 .incrow .incago{font-size:12px;color:var(--muted)}
 .incrow .incsub{font-size:12.5px;color:var(--faint);margin-top:2px}
 .incrow .incbadge{margin-left:auto;flex:none;font-size:11px;font-weight:600;padding:2px 9px;border-radius:999px}
 .incrow .incbadge.bad{background:var(--red-bg);color:var(--danger)}
 .incrow .incbadge.ok{background:var(--teal-bg);color:var(--teal-ink)}
 /* incidents in the app drawer */
 .dr-incs{margin:4px 0 16px;display:flex;flex-direction:column;gap:7px}
 .dr-inc{display:flex;align-items:center;gap:9px;font-size:12.5px;color:var(--muted);padding:9px 12px;border-radius:10px;background:var(--panel2);border:1px solid var(--line)}
 .dr-inc .d{width:8px;height:8px;border-radius:50%;flex:none;background:var(--ok)}
 .dr-inc.open .d{background:var(--danger)}
 .dr-inc.open{color:var(--ink)}
 .count.bad{background:var(--red-bg);color:var(--danger);border-color:transparent}
 .count.ok{background:var(--teal-bg);color:var(--teal-ink);border-color:transparent}
 .idet{color:var(--faint)}
 .alerts{display:flex;flex-direction:column;gap:8px;margin-bottom:16px}
 .alert{display:flex;align-items:center;gap:9px;padding:10px 14px;border-radius:12px;font-size:13px;font-weight:550;border:1px solid transparent}
 .alert svg{width:16px;height:16px;flex:none}
 .alert.warn{background:var(--amber-bg);color:var(--amber-ink);border-color:color-mix(in srgb,var(--amber-ink) 22%,transparent)}
 .alert.bad{background:var(--red-bg);color:var(--danger);border-color:color-mix(in srgb,var(--danger) 25%,transparent)}
 .edgehosts{display:flex;flex-wrap:wrap;gap:5px;margin-top:8px}
 .edgehosts code{font-size:10.5px;background:var(--panel2);border:1px solid var(--line);border-radius:6px;padding:2px 7px;color:var(--muted)}
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
 .ov-row .v.mono{font-family:var(--mono);font-size:12px;font-weight:600}
 .sshbox{display:flex;align-items:center;gap:8px;margin-top:12px;background:var(--panel2);border:1px solid var(--line);border-radius:10px;padding:7px 8px 7px 11px}
 .sshbox code{font-family:var(--mono);font-size:11.5px;color:var(--ink);flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
 .sshbox button{flex:none;padding:5px 11px}
 .ov-bar{margin:6px 0 14px}
 .ov-bar .mlabel{margin-bottom:6px}
 .procbody{padding:15px 18px}
 .timeline{list-style:none;margin:12px 0 0;padding:12px 0 0;border-top:1px solid var(--line);display:flex;flex-direction:column;gap:9px}
 .timeline li{display:flex;align-items:baseline;gap:10px;font-size:12.5px;position:relative;padding-left:15px}
 .timeline li::before{content:"";position:absolute;left:0;top:6px;width:7px;height:7px;border-radius:50%;background:var(--ok)}
 .timeline li.bad::before{background:var(--danger)}
 .timeline .tt{font-size:11px;color:var(--faint);font-variant-numeric:tabular-nums;flex:none}
 .timeline .tx{color:var(--ink)}
 .status-line{display:flex;align-items:center;gap:9px;font-size:13.5px;font-weight:600}
 .spin{width:15px;height:15px;border-radius:50%;border:2px solid var(--line);border-top-color:var(--brand);animation:spin .7s linear infinite;flex:none}
 .btnspin{display:inline-block;width:12px;height:12px;border-radius:50%;border:2px solid color-mix(in srgb,currentColor 35%,transparent);border-top-color:currentColor;animation:spin .7s linear infinite;vertical-align:-2px;margin-right:5px}
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
 /* command palette */
 dialog.palette{width:min(560px,94vw);margin:12vh auto auto;border-radius:16px;padding:0;overflow:hidden}
 .pal-in{display:flex;align-items:center;gap:10px;padding:15px 18px;border-bottom:1px solid var(--line2)}
 .pal-i{color:var(--faint);font-size:15px}
 .pal-in input{flex:1;font:inherit;font-size:15px;border:0;background:none;color:var(--ink);outline:none}
 .pal-list{list-style:none;margin:0;padding:7px;max-height:52vh;overflow-y:auto}
 .pal-list li{display:flex;align-items:center;gap:11px;padding:10px 12px;border-radius:10px;cursor:pointer;font-size:13.5px}
 .pal-list li .pk{font-size:10px;font-weight:700;letter-spacing:.05em;text-transform:uppercase;color:var(--faint);margin-left:auto}
 .pal-list li.sel{background:var(--indigo-bg);color:var(--indigo-ink)}
 .pal-list li.sel .pk{color:var(--indigo-ink)}
 .pal-list li .pi{width:16px;height:16px;flex:none;color:var(--faint);display:inline-flex}
 .pal-list li.sel .pi{color:currentColor}
 .pal-list li svg{width:16px;height:16px;stroke:currentColor;fill:none;stroke-width:2;stroke-linecap:round;stroke-linejoin:round}
 .pal-empty{padding:18px;text-align:center;color:var(--faint);font-size:13px}
 .pal-foot{display:flex;gap:16px;padding:9px 16px;border-top:1px solid var(--line2);font-size:11px;color:var(--faint)}
 .pal-foot kbd{background:var(--panel2);border:1px solid var(--line);border-radius:5px;padding:1px 5px;font:inherit;font-size:10px;margin-right:3px}
 /* kebab menu items */
 .menu-item{display:block;width:100%;text-align:left;font:inherit;font-size:13px;font-weight:550;color:var(--ink);background:none;border:0;border-radius:8px;padding:8px 10px;cursor:pointer}
 .menu-item:hover{background:var(--panel2);text-decoration:none;color:var(--ink)}
 .menu-sep{height:1px;background:var(--line);margin:8px 0}
 .srv-ico[role=button]{cursor:pointer}
 /* detail drawer — right-side slide-in */
 dialog.drawer{width:min(560px,96vw);max-height:none;height:100vh;margin:0 0 0 auto;border-radius:0;border-left:1px solid var(--line);border-top:0;border-right:0;border-bottom:0;display:flex;flex-direction:column}
 dialog.drawer[open]{animation:drawin .18s ease}
 @keyframes drawin{from{transform:translateX(24px);opacity:.4}to{transform:none;opacity:1}}
 .drawer-h{display:flex;align-items:flex-start;justify-content:space-between;padding:18px 20px;border-bottom:1px solid var(--line2)}
 .drawer-h h2{font-size:17px}
 .dr-url{font-size:12.5px;color:var(--indigo-ink);display:inline-block;margin-top:3px}
 .drawer-b{padding:18px 20px;overflow-y:auto}
 .dr-grid{display:grid;grid-template-columns:1fr 1fr;gap:10px;margin-bottom:16px}
 .dr-cell{background:var(--panel2);border:1px solid var(--line);border-radius:10px;padding:10px 12px}
 .dr-cell .kk{font-size:10px;font-weight:700;letter-spacing:.06em;text-transform:uppercase;color:var(--faint)}
 .dr-cell .vv{font-size:13.5px;font-weight:600;margin-top:3px;word-break:break-word}
 .dr-env{display:flex;flex-wrap:wrap;gap:5px;margin-bottom:16px}
 .dr-env .tag{background:var(--panel2);border:1px solid var(--line);color:var(--muted)}
 .dr-logs-h{font-size:11px;font-weight:700;letter-spacing:.05em;text-transform:uppercase;color:var(--faint);margin-bottom:8px}
 .dr-logs{background:#0b0e14;color:#c9d3e3;font:12px/1.55 ui-monospace,Menlo,monospace;padding:14px;border-radius:10px;overflow:auto;max-height:46vh;white-space:pre-wrap;word-break:break-word;margin:0}
 .field{margin-bottom:12px}
 .field label{display:block;font-size:12px;font-weight:600;color:var(--muted);margin-bottom:5px}
 .field input{width:100%;font:inherit;font-size:14px;padding:10px 12px;border-radius:10px;border:1px solid var(--line);background:var(--panel2);color:var(--ink)}
 .field input:focus{outline:none;border-color:var(--brand)}
 .hint{font-size:12px;color:var(--faint);margin:10px 0 14px}
 .hide{display:none!important}
 .flash{animation:flash 1.2s ease}
 @keyframes flash{0%,60%{box-shadow:0 0 0 3px color-mix(in srgb,var(--brand) 45%,transparent)}100%{box-shadow:0 0 0 0 transparent}}
 /* responsive */
 @media(max-width:1080px){
  .body{grid-template-columns:1fr;overflow-y:auto}
  main{overflow:visible}
  .rail{overflow:visible;height:auto;border-left:0;border-top:1px solid var(--line)}
  .graphs{grid-template-columns:1fr}
  .railtgl{display:none}
 }
 @media(max-width:860px){
  .sidetgl{display:none}
  .shell{grid-template-columns:1fr}
  .side{position:fixed;left:0;top:0;bottom:0;z-index:30;width:240px;background:var(--panel);transform:translateX(-100%);transition:transform .2s;box-shadow:var(--shadow-lg)}
  body.nav-open .side{transform:none}
  body.nav-open:after{content:"";position:fixed;inset:0;background:rgba(0,0,0,.4);z-index:20}
  .burger{display:inline-block}
 }
 @media(max-width:600px){
  .srv{flex-direction:column;align-items:stretch;gap:11px}
  .srv-acts{justify-content:flex-start}
 }
 /* ===== MATERIAL DESIGN 3 LAYER (in sync with the Fleet template) ===== */
 :focus{outline:none}
 a:focus-visible,button:focus-visible,input:focus-visible,select:focus-visible,summary:focus-visible,[tabindex]:focus-visible{outline:2px solid var(--md-primary);outline-offset:2px}
 button,summary{-webkit-tap-highlight-color:transparent}
 ::selection{background:color-mix(in srgb,var(--md-primary) 26%,transparent)}
 h1,h2,h3{letter-spacing:-.01em}
 .btn,.iconbtn,.nav a,.segbtn,.kebab,.toggle button,.menu-item,.pal-list li,.chip-btn{position:relative;isolation:isolate;overflow:hidden}
 .btn::after,.iconbtn::after,.nav a::after,.segbtn::after,.kebab::after,.toggle button::after,.menu-item::after,.pal-list li::after{content:"";position:absolute;inset:0;border-radius:inherit;background:currentColor;opacity:0;transition:opacity .15s var(--md-standard);pointer-events:none;z-index:-1}
 .btn:hover::after,.iconbtn:hover::after,.nav a:hover::after,.segbtn:hover::after,.kebab:hover::after,.toggle button:hover::after,.menu-item:hover::after,.pal-list li:hover::after{opacity:var(--state-hover)}
 .btn:active::after,.iconbtn:active::after,.nav a:active::after{opacity:var(--state-press)}
 .ripple-ink{position:absolute;border-radius:50%;background:currentColor;opacity:.24;transform:scale(0);pointer-events:none;z-index:-1;animation:ripple-out .55s var(--md-standard)}
 @keyframes ripple-out{to{transform:scale(1);opacity:0}}
 .card{border:none;background:var(--md-surface-container-low);box-shadow:var(--elev-1);border-radius:var(--radius);transition:box-shadow .25s var(--md-standard)}
 .card:hover{box-shadow:var(--elev-2)}
 .card-h{border-bottom-color:var(--md-outline-variant)}
 .gcard{border:none;background:var(--md-surface-container-low);box-shadow:var(--elev-1);border-radius:var(--radius);transition:box-shadow .25s var(--md-standard)}
 .gcard:hover{box-shadow:var(--elev-2)}
 .glist.grid .srv{border:1px solid var(--md-outline-variant);background:var(--md-surface-container-lowest);border-radius:var(--radius-sm)}
 .glist.grid .srv:hover{background:var(--md-surface-container-low);box-shadow:var(--elev-2)}
 .btn{border-radius:var(--radius-full);padding:9px 20px;font-weight:600;border:1px solid var(--md-outline-variant);background:transparent;color:var(--md-primary);transition:box-shadow .18s var(--md-standard),background .18s var(--md-standard)}
 .btn:hover{transform:none;box-shadow:none}
 .btn-primary{background:var(--md-primary);color:var(--md-on-primary);border-color:transparent}
 .btn-primary:hover{box-shadow:var(--elev-1)}
 .btn-ok{background:var(--ok);color:#fff;border-color:transparent}
 .btn-ok:hover{background:var(--ok);color:#fff;box-shadow:var(--elev-1)}
 :root[data-theme="dark"] .btn-ok{color:#08160e}
 .empty-state{display:flex;flex-direction:column;align-items:center;gap:9px;padding:44px 18px;text-align:center}
 .empty-state svg{width:34px;height:34px;color:var(--md-outline)}
 .empty-state b{font-size:14px;font-weight:600;color:var(--md-on-surface)}
 .empty-state span{display:block;font-size:12.5px;color:var(--md-on-surface-variant);margin-top:2px}
 .btn-danger{background:transparent;color:var(--md-error);border-color:transparent}
 .iconbtn{border-radius:var(--radius-full);border:none;background:transparent;color:var(--md-on-surface-variant);width:40px;height:40px}
 .iconbtn:hover{background:transparent;border:none}
 .iconbtn.on{background:var(--md-primary-container);color:var(--md-on-primary-container)}
 .iconbtn.palk{border:1px solid var(--md-outline-variant)}
 .kebab{border-radius:var(--radius-full);border:none;background:transparent}
 .menu[open] .kebab{background:var(--md-primary-container);color:var(--md-on-primary-container)}
 .nav a{border-radius:var(--radius-full);padding:10px 16px;font-weight:600}
 .nav a.active{background:var(--md-secondary-container);color:var(--md-on-secondary-container)}
 .nav a.active::before{display:none}
 .search input{border-radius:var(--radius-full);border:1px solid transparent;background:var(--md-surface-container-high)}
 .search input:focus{border-color:var(--md-primary);box-shadow:none;background:var(--md-surface-container-high)}
 .chipset{display:inline-flex;gap:8px;align-items:center}
 .chip-btn{font:inherit;font-size:13px;font-weight:600;cursor:pointer;height:34px;padding:0 14px;display:inline-flex;align-items:center;gap:6px;border-radius:var(--radius-xs);border:1px solid var(--md-outline-variant);background:transparent;color:var(--md-on-surface-variant);transition:background .15s var(--md-standard),color .15s var(--md-standard)}
 .chip-btn.on{background:var(--md-secondary-container);color:var(--md-on-secondary-container);border-color:transparent;padding-left:10px}
 .chip-btn.on::before{content:"✓";font-size:13px;font-weight:700}
 .tag{border-radius:var(--radius-xs);font-weight:600}
 .tag.rt{border:1px solid var(--md-outline-variant);background:transparent}
 .pill,.rchip,.count{border-radius:var(--radius-full)}
 .toggle{border-radius:var(--radius-full);border:1px solid var(--md-outline-variant);background:transparent;padding:0;overflow:hidden}
 .toggle button{border-radius:0;padding:8px 16px}
 .toggle button.active{background:var(--md-secondary-container);color:var(--md-on-secondary-container);box-shadow:none}
 .seg{border-radius:var(--radius-full);border-color:var(--md-outline-variant);overflow:hidden}
 .topbar{background:var(--md-surface);border-bottom:1px solid var(--md-outline-variant);transition:box-shadow .2s var(--md-standard),background .2s var(--md-standard);z-index:5}
 .topbar.scrolled{box-shadow:var(--elev-2);border-bottom-color:transparent;background:var(--md-surface-container-low)}
 .side{background:var(--md-surface);border-right:1px solid var(--md-outline-variant)}
 .rail{background:var(--md-surface)}
 .bar{background:var(--md-surface-container-high);border-radius:var(--radius-full)}
 .fill{border-radius:var(--radius-full)}
 .fill.ok,.fill.teal{background:var(--ok)} .fill.warn,.fill.amber{background:var(--amber)} .fill.bad{background:var(--danger)}
 .donut{--track:var(--md-surface-container-high)} .donut::after{background:var(--md-surface-container-low)}
 .spark .sb{background:var(--md-surface-container-high);border-radius:6px} .spark .sb i{border-radius:6px 6px 3px 3px}
 .cpuspark path{stroke:var(--md-primary)}
 .srv-ico{border-radius:var(--radius-sm);background:var(--md-primary-container);color:var(--md-on-primary-container)}
 .card-h h2{font-size:16px}
 dialog{background:var(--md-surface-container-high);border:none;box-shadow:var(--elev-3);border-radius:var(--radius-xl)}
 dialog.drawer{border-radius:0;box-shadow:var(--elev-5)}
 .menu-pop{background:var(--md-surface-container-high);border:none;box-shadow:var(--elev-2);border-radius:var(--radius-sm)}
 :root[data-theme="dark"] .incbanner{background:color-mix(in srgb,var(--md-error-container) 55%,var(--md-surface));color:var(--md-on-error-container);border-color:transparent}
 :root[data-theme="dark"] .alert.bad,:root[data-theme="dark"] .att.red{background:color-mix(in srgb,var(--md-error-container) 50%,var(--md-surface));color:var(--md-on-error-container);border-color:transparent}
 :root[data-theme="dark"] .alert.warn,:root[data-theme="dark"] .att.amber{background:color-mix(in srgb,var(--md-warning-container) 50%,var(--md-surface));color:var(--md-on-warning-container);border-color:transparent}
 @media(prefers-reduced-motion:reduce){*,*::before,*::after{animation-duration:.001ms!important;transition-duration:.001ms!important}}
</style></head><body>
<div class="shell">

 <aside class="side">
  <div class="brand">
   <div class="logo"><svg viewBox="0 0 40 40" fill="none"><rect width="40" height="40" rx="11" fill="url(#rg)"/><path d="M10.5 19.2 L20 11 L29.5 19.2" stroke="#fff" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round"/><path d="M13.4 18.4 V28.6 H26.6 V18.4" stroke="#fff" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round"/><path d="M17.4 28.6 V24 a2.6 2.6 0 0 1 5.2 0 V28.6" stroke="#fff" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"/><defs><linearGradient id="rg" x1="0" y1="0" x2="1" y2="1"><stop stop-color="#8b83f7"/><stop offset=".55" stop-color="#5b54e6"/><stop offset="1" stop-color="#4338ca"/></linearGradient></defs></svg></div>
   <div><div class="bt">roost</div><div class="bs">Control panel</div></div>
  </div>
  <nav class="nav">
   <div class="navlabel">Resources</div>
   <a href="/" class="navf{{if ne .Page "incidents"}} active{{end}}" data-cat=""><span class="ico"><svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/></svg></span> All apps</a>
   <a href="/" class="navf" data-cat="Main apps"><span class="ico"><svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3l2.6 5.3 5.9.9-4.25 4.1 1 5.8L12 16.9 6.75 19.6l1-5.8L3.5 9.2l5.9-.9z"/></svg></span> Main apps</a>
   <a href="/" class="navf" data-cat="Utilities"><span class="ico"><svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="4" y1="8" x2="20" y2="8"/><circle cx="9" cy="8" r="2.3"/><line x1="4" y1="16" x2="20" y2="16"/><circle cx="15" cy="16" r="2.3"/></svg></span> Utilities</a>
   <a href="/" class="navf" data-cat="Workers"><span class="ico"><svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 8v8a2 2 0 0 1-1 1.73l-7 4a2 2 0 0 1-2 0l-7-4A2 2 0 0 1 3 16V8a2 2 0 0 1 1-1.73l7-4a2 2 0 0 1 2 0l7 4A2 2 0 0 1 21 8z"/><path d="M3.3 7 12 12l8.7-5"/><line x1="12" y1="22" x2="12" y2="12"/></svg></span> Workers</a>
   <div class="navlabel">Monitoring</div>
   <a href="/#attention" data-scroll="attention"><span class="ico"><svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M10.3 3.6 1.8 18a2 2 0 0 0 1.7 3h16.9a2 2 0 0 0 1.7-3L13.7 3.6a2 2 0 0 0-3.4 0z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg></span> Attention</a>
   <a href="/#processing" data-scroll="processing"><span class="ico"><svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M22 12h-4l-3 9L9 3l-3 9H2"/></svg></span> Activity</a>
   <a href="/incidents"{{if eq .Page "incidents"}} class="active"{{end}}><span class="ico"><svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M18 8a6 6 0 0 0-12 0c0 7-3 9-3 9h18s-3-2-3-9"/><path d="M13.7 21a2 2 0 0 1-3.4 0"/></svg></span> Incidents{{if .OpenIncidents}} <span class="navpip">{{.OpenIncidents}}</span>{{end}}</a>
   <a href="/status" target="_blank" rel="noopener"><span class="ico"><svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 2a10 10 0 1 0 10 10"/><path d="M12 6v6l4 2"/></svg></span> Status page ↗</a>
   <div class="navlabel">Manage</div>
   <a href="#removed" data-scroll="removed"><span class="ico"><svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 3v5h5"/><path d="M3.05 13A9 9 0 1 0 6 5.3L3 8"/></svg></span> Removed</a>
   <a href="https://github.com/cdrrazan/roost" target="_blank" rel="noopener"><span class="ico"><svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="6" y1="3" x2="6" y2="15"/><circle cx="18" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><path d="M18 9a9 9 0 0 1-9 9"/></svg></span> Repository</a>
  </nav>
  <div class="grow"></div>
  <div class="sideinc" id="incidents">
   <div class="si-h"><span class="navlabel" style="padding:0">Alerts</span>{{if .OpenIncidents}}<span class="count bad">{{.OpenIncidents}} open</span>{{else}}<span class="count ok">all clear</span>{{end}}</div>
   <a class="si-link" href="/incidents">View incidents{{if .OpenIncidents}} · {{.OpenIncidents}} active{{end}} →</a>
   <form class="inline" method="post" action="/test-alert">{{if .Token}}<input type="hidden" name="token" value="{{.Token}}">{{end}}<button class="btn btn-sm" style="width:100%;justify-content:center;margin-top:8px" title="Send a test email to confirm alerts work" {{if .Busy}}disabled{{end}}>Test alert</button></form>
  </div>
  {{if .System.Images}}
  <div class="sidesys">
   <div class="navlabel">System · docker</div>
   <div class="ss-row"><span>Images</span><b>{{.System.Images}}{{if .System.ImagesSize}} · {{.System.ImagesSize}}{{end}}</b></div>
   <div class="ss-row"><span>Containers</span><b>{{.System.Containers}}</b></div>
   <div class="ss-row"><span>Volumes</span><b>{{.System.Volumes}}{{if .System.VolumesSize}} · {{.System.VolumesSize}}{{end}}</b></div>
   {{if .System.Reclaimable}}<div class="ss-row"><span>Reclaimable</span><b>{{.System.Reclaimable}}</b></div>{{end}}
  </div>
  {{end}}
  <div class="user">
   <div class="avatar"><img src="https://github.com/cdrrazan.png?size=80" alt="Rajan Bhattarai" referrerpolicy="no-referrer"></div>
   <div style="min-width:0;flex:1"><div class="un">Rajan Bhattarai</div><div class="ue">@cdrrazan</div></div>
   <a class="logout" id="logout" href="/cdn-cgi/access/logout" title="Sign out (Cloudflare Access)" aria-label="Sign out"><svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><polyline points="16 17 21 12 16 7"/><line x1="21" y1="12" x2="9" y2="12"/></svg></a>
  </div>
 </aside>

 <div class="content" id="top">
  <header class="topbar">
   <button class="burger" id="burger" aria-label="Menu">☰</button>
   <button class="iconbtn sidetgl" id="sidetgl" title="Collapse sidebar" aria-label="Collapse sidebar"><svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="16" rx="2"/><line x1="9" y1="4" x2="9" y2="20"/></svg></button>
   <div class="search"><span class="si">⌕</span><input id="q" type="text" placeholder="Search apps…" autocomplete="off"></div>
   <div class="chipset" id="statusfilter" role="group" aria-label="Filter by status">
    <button class="chip-btn on" data-val="all">All</button>
    <button class="chip-btn" data-val="running">Running</button>
    <button class="chip-btn" data-val="stopped">Stopped</button>
   </div>
   <button class="iconbtn palk" id="palbtn" title="Command palette (⌘K)" aria-label="Command palette"><kbd>⌘K</kbd></button>
   <button class="iconbtn" id="themebtn" title="Toggle light / dark" aria-label="Toggle theme"><span class="moon"><svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12.8A9 9 0 1 1 11.2 3 7 7 0 0 0 21 12.8z"/></svg></span><span class="sun"><svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="4.5"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/></svg></span></button>
   <div class="toggle"><button data-view="list">▤ List</button><button data-view="grid">▦ Grid</button></div>
   <form class="inline" method="post" action="/up">{{if .Token}}<input type="hidden" name="token" value="{{.Token}}">{{end}}<button class="btn btn-ok" {{if .Busy}}disabled{{end}}>Start all</button></form>
   <form class="inline" method="post" action="/down">{{if .Token}}<input type="hidden" name="token" value="{{.Token}}">{{end}}<button class="btn" {{if .Busy}}disabled{{end}}>Stop all</button></form>
   <button class="btn btn-primary" id="openadd" {{if .Busy}}disabled{{end}}>＋ New app</button>
   <button class="iconbtn railtgl" id="railtgl" title="Collapse info panel" aria-label="Collapse info panel"><svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="16" rx="2"/><line x1="15" y1="4" x2="15" y2="20"/></svg></button>
  </header>

  <div class="body">
  <main>
   {{if eq .Page "incidents"}}
   <section class="card" id="incidents-page">
    <div class="card-h">
     <h2>🔔 Incidents <span class="csub">{{if .OpenIncidents}}{{.OpenIncidents}} active now{{else}}all clear{{end}}</span></h2>
     <div class="inc-actions">
      {{if gt .Resolved 0}}<form class="inline" method="post" action="/incidents/clear">{{if .Token}}<input type="hidden" name="token" value="{{.Token}}">{{end}}<button class="btn btn-sm" title="Remove resolved incidents from the history">Clear resolved ({{.Resolved}})</button></form>{{end}}
      <form class="inline" method="post" action="/test-alert">{{if .Token}}<input type="hidden" name="token" value="{{.Token}}">{{end}}<button class="btn btn-sm" title="Send a test email to confirm alerts work" {{if .Busy}}disabled{{end}}>Test alert</button></form>
     </div>
    </div>
    {{if .Incidents}}
    <ul class="inclist">
     {{range .Incidents}}
     <li class="incrow {{if .Open}}open{{else}}resolved{{end}}">
      <span class="incdot"></span>
      <div class="incmain">
       <div class="inctop"><b>{{.Label}}</b> <span class="incago">{{.Ago}}</span></div>
       <div class="incsub">{{.Detail}} · since {{.Since}}</div>
      </div>
      <span class="incbadge {{if .Open}}bad{{else}}ok{{end}}">{{if .Open}}open{{else}}resolved{{end}}</span>
     </li>
     {{end}}
    </ul>
    {{else}}
    <div class="allclear"><span class="ico">✓</span> No incidents recorded — everything has been healthy.</div>
    {{end}}
   </section>
   {{else}}
   {{if .Error}}
   <div class="card"><div class="result err" style="margin:16px">status error: {{.Error}}</div></div>
   {{else}}

   {{if .OpenIncidents}}<div class="incbanner"><span class="ib-dot"></span><b>{{.OpenIncidents}} active incident{{if gt .OpenIncidents 1}}s{{end}}</b><span class="ib-list">{{range .Incidents}}{{if .Open}}{{.Label}} — {{.Ago}} · {{end}}{{end}}</span></div>{{end}}

   {{if .Alerts}}<div class="alerts">{{range .Alerts}}<div class="alert {{.Level}}"><svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M10.3 3.6 1.8 18a2 2 0 0 0 1.7 3h16.9a2 2 0 0 0 1.7-3L13.7 3.6a2 2 0 0 0-3.4 0z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>{{.Text}}</div>{{end}}</div>{{end}}

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

   <div class="graphs">
    <div class="gcard teal">
     <div class="gh"><span class="mi"><svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="7" rx="2"/><rect x="3" y="13" width="18" height="7" rx="2"/><line x1="7" y1="7.5" x2="7.01" y2="7.5"/><line x1="7" y1="16.5" x2="7.01" y2="16.5"/></svg></span> Fleet status</div>
     <div class="donutrow">
      <div class="donut" style="--v:{{.RunningPct}};--c:var(--ok)"><div class="dc"><div><b>{{.RunningCount}}</b><span>of {{.Total}} up</span></div></div></div>
      <div class="dleg">
       <div class="li"><span class="sw" style="background:var(--ok)"></span> Running <b>{{.RunningCount}}</b></div>
       <div class="li"><span class="sw" style="background:var(--track)"></span> Stopped <b>{{.StoppedCount}}</b></div>
       <div class="li"><span class="sw" style="background:{{if .DockerOK}}var(--ok){{else}}var(--danger){{end}}"></span> Docker <b>{{if .DockerOK}}OK{{else}}down{{end}}</b></div>
      </div>
     </div>
    </div>
    <div class="gcard amber">
     <div class="gh"><span class="mi"><svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="6" width="18" height="12" rx="2"/><line x1="7" y1="18" x2="7" y2="21"/><line x1="12" y1="18" x2="12" y2="21"/><line x1="17" y1="18" x2="17" y2="21"/><line x1="7" y1="3" x2="7" y2="6"/><line x1="12" y1="3" x2="12" y2="6"/><line x1="17" y1="3" x2="17" y2="6"/></svg></span> Memory usage</div>
     <div class="donutrow">
      <div class="donut" style="--v:{{.MemPct}};--c:var(--amber)"><div class="dc"><div><b>{{.MemPct}}%</b><span>used</span></div></div></div>
      <div class="dleg">
       <div class="li"><span class="sw" style="background:var(--amber)"></span> Used <b>{{if .MemCap}}{{.MemUsed}}{{else}}—{{end}}</b></div>
       <div class="li"><span class="sw" style="background:var(--track)"></span> Cap <b>{{if .MemCap}}{{.MemCap}}{{else}}—{{end}}</b></div>
      </div>
     </div>
    </div>
    <div class="gcard indigo">
     <div class="gh"><span class="mi"><svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="3" y1="21" x2="21" y2="21"/><rect x="5" y="11" width="3.4" height="8" rx="1"/><rect x="10.3" y="6" width="3.4" height="13" rx="1"/><rect x="15.6" y="14" width="3.4" height="5" rx="1"/></svg></span> Memory by app</div>
     <div class="spark">
      {{range .Apps}}<div class="sb" data-app="{{humanize .Name}}" data-mem="{{if .Memory}}{{mempct .Memory}}% · {{.Memory}}{{else}}n/a{{end}}"><i class="fill {{memcolor .Memory}}" style="height:{{mempct .Memory}}%"></i></div>{{end}}
      <div class="spark-tip" id="sparkTip" hidden></div>
     </div>
     <div class="sparkcap"><span>{{.Total}} apps</span><span>% of cap</span></div>
    </div>
   </div>

   <section class="card">
    <div class="card-h"><h2>Applications <span class="csub">manage and monitor your fleet</span></h2><span id="livedot" class="livedot" title="Live — auto-refreshing every 5s">live</span><span class="count">{{.RunningCount}}/{{.Total}} up</span></div>
    <div class="applist">
     <div class="empty-state hide" id="filterEmpty"></div>
    {{range .Groups}}
     <div class="group" data-cat="{{.Title}}">
      <div class="grouphdr" id="{{slug .Title}}">{{.Title}}<span class="gc">{{len .Apps}}</span></div>
      <div class="glist">
      {{range .Apps}}
       <div class="srv" data-name="{{humanize .Name}}" data-app="{{.Name}}" data-state="{{if eq .State "running"}}running{{else}}stopped{{end}}">
        <div class="srv-top">
         <span class="srv-ico" data-detail="{{.Name}}" title="View details" role="button" tabindex="0">{{if .URL}}<svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><line x1="3" y1="12" x2="21" y2="12"/><path d="M12 3a15 15 0 0 1 0 18 15 15 0 0 1 0-18"/></svg>{{else}}<svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="6" y="6" width="12" height="12" rx="2"/><path d="M9 2v2M15 2v2M9 20v2M15 20v2M2 9h2M2 15h2M20 9h2M20 15h2"/></svg>{{end}}</span>
         <div class="srv-idb">
          <div class="srv-nm"><span class="dot {{if eq .State "running"}}run{{else}}stop{{end}}"></span><span class="srv-name">{{humanize .Name}}</span>{{if eq .State "running"}}<span class="pill run">{{.State}}</span>{{else}}<span class="pill stop">{{.State}}</span>{{end}}{{if and (eq .State "running") .HTTP}}<span class="rchip {{if .Reachable}}up{{else}}down{{end}}" title="Live HTTP probe: {{.HTTP}}">{{if .Reachable}}live · {{.HTTP}}{{else}}{{.HTTP}}{{end}}</span>{{end}}</div>
          <div class="srv-sub">{{if .URL}}<a href="{{.URL}}">{{.URL}}</a>{{else}}background worker{{end}}{{if .Health}} · {{.Health}}{{end}}{{if .Repo}} · <a class="repo" href="{{.Repo}}" target="_blank" rel="noopener" title="Open repository"><svg viewBox="0 0 24 24" width="13" height="13" fill="currentColor"><path d="M12 .5C5.7.5.5 5.7.5 12c0 5.1 3.3 9.4 7.9 10.9.6.1.8-.3.8-.6v-2c-3.2.7-3.9-1.5-3.9-1.5-.5-1.3-1.3-1.7-1.3-1.7-1.1-.7.1-.7.1-.7 1.2.1 1.8 1.2 1.8 1.2 1 1.8 2.8 1.3 3.5 1 .1-.8.4-1.3.7-1.6-2.6-.3-5.3-1.3-5.3-5.7 0-1.3.5-2.3 1.2-3.1-.1-.3-.5-1.5.1-3.1 0 0 1-.3 3.3 1.2a11.5 11.5 0 0 1 6 0C17 4.7 18 5 18 5c.6 1.6.2 2.8.1 3.1.8.8 1.2 1.8 1.2 3.1 0 4.4-2.7 5.4-5.3 5.7.4.4.8 1.1.8 2.2v3.3c0 .3.2.7.8.6 4.6-1.5 7.9-5.8 7.9-10.9C23.5 5.7 18.3.5 12 .5z"/></svg>Code</a>{{end}}</div>
          {{if or .Framework .Database .Redis .Runtime .Worker}}<div class="tags">{{if .Worker}}<span class="tag worker">worker</span>{{end}}{{if .Framework}}<span class="tag fw">{{tech .Framework}}</span>{{end}}{{if .Database}}<span class="tag db">{{tech .Database}}</span>{{end}}{{if .Redis}}<span class="tag redis">Redis</span>{{end}}{{if .Runtime}}<span class="tag rt">{{.Runtime}}</span>{{end}}</div>{{end}}
         </div>
         <div class="srv-acts">
          <div class="seg">
           <form class="inline" method="post" action="/app/up"><input type="hidden" name="app" value="{{.Name}}">{{if $.Token}}<input type="hidden" name="token" value="{{$.Token}}">{{end}}<button class="segbtn go" {{if or $.Busy (eq .State "running")}}disabled{{end}}>Start</button></form>
           <form class="inline" method="post" action="/app/down"><input type="hidden" name="app" value="{{.Name}}">{{if $.Token}}<input type="hidden" name="token" value="{{$.Token}}">{{end}}<button class="segbtn st" {{if or $.Busy (ne .State "running")}}disabled{{end}}>Stop</button></form>
          </div>
          <details class="menu">
           <summary class="kebab" title="More actions">⋯</summary>
           <div class="menu-pop">
            <button type="button" class="menu-item" data-detail="{{.Name}}">View details</button>
            {{if .URL}}<a class="menu-item" href="{{.URL}}" target="_blank" rel="noopener">Open site ↗</a>{{end}}
            <div class="menu-sep"></div>
            <form method="post" action="/remove" onsubmit="return confirm('Remove {{humanize .Name}} from the config?')"><input type="hidden" name="app" value="{{.Name}}">{{if $.Token}}<input type="hidden" name="token" value="{{$.Token}}">{{end}}
             <label class="free"><input type="checkbox" name="image" value="on"> Also delete image <span>(free disk)</span></label>
             <button class="btn btn-sm btn-danger" style="width:100%;justify-content:center" {{if $.Busy}}disabled{{end}}>Remove app</button>
            </form>
           </div>
          </details>
         </div>
        </div>
        {{if .Memory}}<div class="srv-bar"><div class="mlabel">Memory <b>{{mempct .Memory}}%</b></div><div class="bar"><span class="fill {{memcolor .Memory}}" style="width:{{mempct .Memory}}%"></span></div></div>{{end}}
        {{if eq .State "running"}}<div class="srv-metrics">{{if .CPU}}<span class="me"><i>CPU</i>{{.CPU}}{{with index $.Sparks .Name}} {{.}}{{end}}</span>{{end}}{{if .Memory}}<span class="me"><i>MEM</i>{{.Memory}}</span>{{end}}{{if .Net}}<span class="me"><i>NET</i>{{.Net}}</span>{{end}}{{if .Up}}<span class="me up"><i>UP</i>{{.Up}}</span>{{end}}</div>{{end}}
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

   <div class="card" id="server">
    <div class="card-h"><h2>Server</h2>{{if .Server.Label}}<span class="csub">{{.Server.Label}}</span>{{end}}</div>
    <div class="ov">
     {{if .Server.DiskCap}}<div class="ov-bar"><div class="mlabel">Disk <b>{{.Server.DiskPct}}%</b></div><div class="bar"><span class="fill {{if ge .Server.DiskPct 90}}bad{{else if ge .Server.DiskPct 70}}warn{{else}}ok{{end}}" style="width:{{.Server.DiskPct}}%"></span></div></div>{{end}}
     {{if .Server.IP}}<div class="ov-row"><span class="k">IP address</span><span class="v mono">{{.Server.IP}}</span></div>{{end}}
     {{if .Server.Host}}<div class="ov-row"><span class="k">Host</span><span class="v">{{.Server.Host}}</span></div>{{end}}
     {{if .Server.OS}}<div class="ov-row"><span class="k">OS</span><span class="v">{{.Server.OS}}</span></div>{{end}}
     {{if .Server.Cores}}<div class="ov-row"><span class="k">CPU / RAM</span><span class="v">{{.Server.Cores}} vCPU{{if .Server.RAM}} · {{.Server.RAM}}{{end}}</span></div>{{end}}
     {{if .Server.Uptime}}<div class="ov-row"><span class="k">Uptime</span><span class="v">{{.Server.Uptime}}</span></div>{{end}}
     {{if .Server.DiskCap}}<div class="ov-row"><span class="k">Disk</span><span class="v">{{.Server.DiskUsed}} / {{.Server.DiskCap}}</span></div>{{end}}
     {{if .Server.SSH}}<div class="sshbox"><code>ssh {{.Server.SSH}}</code><button class="btn btn-sm" data-copy="ssh {{.Server.SSH}}" title="Copy login command">Copy</button></div>{{end}}
     {{if not .Server.IP}}<p class="empty" style="padding:2px 0 0">Set a <code>server:</code> block in config.yml to show IP + SSH login.</p>{{end}}
    </div>
   </div>

   {{if .Edge.TunnelName}}
   <div class="card" id="edge">
    <div class="card-h"><h2>Edge</h2><span class="csub">Cloudflare tunnel</span></div>
    <div class="ov">
     <div class="ov-row"><span class="k">Tunnel</span><span class="v">{{.Edge.TunnelName}} <span class="rchip up" style="margin-left:6px">outbound</span></span></div>
     {{if .Edge.TunnelState}}<div class="ov-row"><span class="k">Connection</span><span class="v">{{if eq .Edge.TunnelState "connected"}}<span class="rchip up">connected</span>{{else if eq .Edge.TunnelState "reconnecting"}}<span class="rchip warn">reconnecting…</span>{{else if eq .Edge.TunnelState "down"}}<span class="rchip down">down</span>{{else}}<span class="rchip">unknown</span>{{end}}</span></div>
     {{if eq .Edge.TunnelState "reconnecting"}}<div class="csub" style="margin-top:2px">cloudflared is re-establishing after a wake — brief 502s here are the edge, not your apps (~5–10s).</div>{{end}}{{end}}
     {{if .Edge.TunnelID}}<div class="ov-row"><span class="k">Tunnel ID</span><span class="v mono">{{.Edge.TunnelID}}</span></div>{{end}}
     {{if .Edge.Account}}<div class="ov-row"><span class="k">Account</span><span class="v mono">{{.Edge.Account}}</span></div>{{end}}
     <div class="ov-row"><span class="k">Access</span><span class="v">{{if .Edge.Protected}}<span class="tag fw">protected</span>{{else}}<span class="tag worker">public</span>{{end}}</span></div>
     {{if .Edge.Hosts}}<div class="ov-row"><span class="k">Routes</span><span class="v">{{len .Edge.Hosts}} DNS</span></div>
     <div class="edgehosts">{{range .Edge.Hosts}}<code>{{.}}</code>{{end}}</div>{{end}}
    </div>
   </div>
   {{end}}

   <div class="card" id="processing">
    <div class="card-h"><h2>Activity <span class="csub">latest actions</span></h2></div>
    <div class="procbody">
     {{if .Busy}}<div class="status-line"><span class="spin"></span>{{.Busy}}…</div>{{else}}<div class="idle"><span class="dot"></span>Idle — no action running</div>{{end}}
     {{if .Steps}}<ul class="steps">{{range .Steps}}<li>{{.}}</li>{{end}}</ul>{{end}}
     {{if and (not .Busy) .Last}}<div class="result">{{.Last}}</div>{{end}}
     {{if .Events}}<ul class="timeline">{{range .Events}}<li class="{{if .OK}}ok{{else}}bad{{end}}"><span class="tt">{{.Time}}</span><span class="tx">{{.Text}}</span></li>{{end}}</ul>{{end}}
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

<dialog id="palette" class="palette">
 <div class="pal-in"><span class="pal-i">⌘</span><input id="pal-q" type="text" placeholder="Search apps and actions…" autocomplete="off"></div>
 <ul class="pal-list" id="pal-list"></ul>
 <div class="pal-foot"><span><kbd>↑</kbd><kbd>↓</kbd> navigate</span><span><kbd>↵</kbd> select</span><span><kbd>esc</kbd> close</span></div>
</dialog>

<dialog id="drawer" class="drawer">
 <div class="drawer-h">
  <div><h2 id="dr-name">App</h2><a id="dr-url" href="#" target="_blank" rel="noopener" class="dr-url"></a></div>
  <button class="modal-x" id="dr-close" aria-label="Close">×</button>
 </div>
 <div class="drawer-b">
  <div class="dr-grid" id="dr-grid"></div>
  <div class="dr-env" id="dr-env"></div>
  <div class="dr-logs-h" id="dr-incs-h" hidden>Incidents</div>
  <div class="dr-incs" id="dr-incs"></div>
  <div class="dr-logs-h">Recent logs</div>
  <pre class="dr-logs" id="dr-logs">Loading…</pre>
 </div>
</dialog>

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
  var on=sf?sf.querySelector(".chip-btn.on"):null;
  var term=(q?q.value:"").toLowerCase(), st=on?on.dataset.val:"all";
  var anyVisible=false;
  document.querySelectorAll(".group").forEach(function(g){
   var catOk=!cat||g.dataset.cat===cat, any=false;
   g.querySelectorAll(".srv").forEach(function(r){
    var show=catOk && r.dataset.name.toLowerCase().indexOf(term)>-1 && (st==="all"||r.dataset.state===st);
    r.classList.toggle("hide",!show);
    if(show){any=true;anyVisible=true;}
   });
   g.classList.toggle("hide",!(catOk&&any));
  });
  var fe=document.getElementById("filterEmpty");
  if(fe){
   fe.classList.toggle("hide",anyVisible);
   if(!anyVisible){
    var msg=term?'No apps match “'+term+'”':st==="running"?"No running apps":st==="stopped"?"No stopped apps":"No apps to show";
    fe.innerHTML='<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="11" cy="11" r="7"/><path d="M21 21l-4.35-4.35"/></svg><div><b>'+msg+'</b><span>Try a different filter or search.</span></div>';
   }
  }
 }
 if(q)q.addEventListener("input",filter);
 if(sf)sf.addEventListener("click",function(e){var c=e.target.closest(".chip-btn");if(!c)return;sf.querySelectorAll(".chip-btn").forEach(function(x){x.classList.toggle("on",x===c)});filter();});
 // MD3 ripple + app-bar elevation
 var RSEL=".btn,.iconbtn,.nav a,.segbtn,.kebab,.toggle button,.menu-item,.pal-list li,.chip-btn";
 if(!matchMedia("(prefers-reduced-motion:reduce)").matches){
  document.addEventListener("pointerdown",function(e){
   if(e.button!==0)return; var h=e.target.closest(RSEL); if(!h||h.hasAttribute("disabled"))return;
   var r=h.getBoundingClientRect(), s=Math.max(r.width,r.height)*2, ink=document.createElement("span");
   ink.className="ripple-ink"; ink.style.width=ink.style.height=s+"px";
   ink.style.left=(e.clientX-r.left-s/2)+"px"; ink.style.top=(e.clientY-r.top-s/2)+"px";
   h.appendChild(ink); ink.addEventListener("animationend",function(){ink.remove();});
  },{passive:true});
 }
 var bar=document.querySelector(".topbar"), sc=document.querySelector("main");
 if(bar&&sc){var ons=function(){bar.classList.toggle("scrolled",sc.scrollTop>2)};sc.addEventListener("scroll",ons,{passive:true});ons();}
 // Sidebar category filter (show only that group, no page jump).
 document.querySelectorAll(".navf").forEach(function(a){
  a.addEventListener("click",function(e){
   // Off the dashboard (e.g. the incidents page) there are no groups to
   // filter — let the link fall through and navigate home.
   if(!document.querySelector(".group"))return;
   e.preventDefault();
   cat=a.dataset.cat;
   document.querySelectorAll(".navf").forEach(function(x){x.classList.toggle("active",x===a)});
   filter();
   document.body.classList.remove("nav-open");
  });
 });
 // Sidebar jump links: scroll the target into view (works inside the
 // independently-scrolling columns) and flash it so the click always reads.
 function flash(el){ el.classList.remove("flash"); void el.offsetWidth; el.classList.add("flash"); setTimeout(function(){el.classList.remove("flash");},1300); }
 document.querySelectorAll("[data-scroll]").forEach(function(a){
  a.addEventListener("click",function(e){
   var el=document.getElementById(a.dataset.scroll);
   // Target missing (e.g. on the incidents page) → follow the href home.
   if(el){ e.preventDefault(); el.scrollIntoView({behavior:"smooth",block:"nearest"}); flash(el); }
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

 // Per-app detail drawer.
 function esc(s){return s.replace(/[&<>"]/g,function(m){return {"&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;"}[m];});}
 var drawer=document.getElementById("drawer");
 function openDrawer(name){
  if(!drawer)return;
  document.querySelectorAll("details.menu[open]").forEach(function(d){d.removeAttribute("open");});
  document.getElementById("dr-name").textContent=name;
  document.getElementById("dr-grid").innerHTML="";
  document.getElementById("dr-env").innerHTML="";
  document.getElementById("dr-incs").innerHTML="";
  document.getElementById("dr-incs-h").hidden=true;
  document.getElementById("dr-logs").textContent="Loading…";
  if(drawer.showModal)drawer.showModal();
  fetch("/api/app?name="+encodeURIComponent(name)).then(function(r){return r.ok?r.json():Promise.reject();}).then(function(d){
   document.getElementById("dr-name").textContent=d.name;
   var u=document.getElementById("dr-url");
   if(d.url){u.textContent=d.url;u.href=d.url;u.style.display="";}else{u.style.display="none";}
   var cells=[["Status",d.status||"—"],["Health",d.health||"—"],["Image",d.image||"—"],["Restarts",d.restarts],["Port",d.port||"—"],["Stack",(d.framework||"—")+(d.database?" · "+d.database:"")]];
   document.getElementById("dr-grid").innerHTML=cells.map(function(c){return '<div class="dr-cell"><div class="kk">'+c[0]+'</div><div class="vv">'+esc(String(c[1]))+'</div></div>';}).join("");
   var env=document.getElementById("dr-env");
   env.innerHTML=(d.envKeys&&d.envKeys.length)?d.envKeys.map(function(k){return '<span class="tag">'+esc(k)+'</span>';}).join(""):'<span class="tag">no env keys</span>';
   var incs=document.getElementById("dr-incs"), incsH=document.getElementById("dr-incs-h");
   if(d.incidents&&d.incidents.length){
    incsH.hidden=false;
    incs.innerHTML=d.incidents.map(function(i){return '<div class="dr-inc'+(i.open?' open':'')+'"><span class="d"></span>'+esc(i.text)+'</div>';}).join("");
   }else{incsH.hidden=true;incs.innerHTML="";}
   var lg=document.getElementById("dr-logs"); lg.textContent=d.logs||"(no recent logs)"; lg.scrollTop=lg.scrollHeight;
  }).catch(function(){document.getElementById("dr-logs").textContent="Failed to load details.";});
 }
 document.addEventListener("click",function(e){
  var t=e.target.closest("[data-detail]"); if(!t)return;
  e.preventDefault(); openDrawer(t.dataset.detail);
 });
 var drc=document.getElementById("dr-close");
 if(drc&&drawer)drc.addEventListener("click",function(){drawer.close();});
 if(drawer)drawer.addEventListener("click",function(e){if(e.target===drawer)drawer.close();});

 // Command palette (⌘K / Ctrl+K).
 var pal=document.getElementById("palette"),palQ=document.getElementById("pal-q"),palList=document.getElementById("pal-list");
 var ICON_APP='<svg viewBox="0 0 24 24"><rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/></svg>';
 var ICON_ACT='<svg viewBox="0 0 24 24"><polyline points="13 2 3 14 12 14 11 22 21 10 12 10 13 2"/></svg>';
 var palItems=[],palShown=[],palSel=0;
 function palBuild(){
  var a=[
   ["Start all apps",function(){var f=document.querySelector('form[action="/up"]');if(f)f.submit();}],
   ["Stop all apps",function(){var f=document.querySelector('form[action="/down"]');if(f)f.submit();}],
   ["New app…",function(){var d=document.getElementById("addapp");if(d&&d.showModal)d.showModal();}],
   ["Toggle theme",function(){var t=document.getElementById("themebtn");if(t)t.click();}],
   ["Toggle sidebar",function(){var t=document.getElementById("sidetgl");if(t)t.click();}],
   ["Toggle info panel",function(){var t=document.getElementById("railtgl");if(t)t.click();}]
  ];
  var items=a.map(function(x){return {label:x[0],kind:"Action",act:x[1]};});
  document.querySelectorAll(".srv[data-app]").forEach(function(s){
   var name=s.dataset.app;
   items.push({label:s.dataset.name+" — details",kind:"App",act:function(){openDrawer(name);}});
  });
  return items;
 }
 function palRender(term){
  term=(term||"").toLowerCase();
  palShown=palItems.filter(function(it){return it.label.toLowerCase().indexOf(term)>-1;});
  palSel=0;
  if(!palShown.length){palList.innerHTML='<div class="pal-empty">No matches</div>';return;}
  palList.innerHTML=palShown.map(function(it,i){return '<li data-i="'+i+'" class="'+(i===0?"sel":"")+'"><span class="pi">'+(it.kind==="App"?ICON_APP:ICON_ACT)+'</span>'+esc(it.label)+'<span class="pk">'+it.kind+'</span></li>';}).join("");
 }
 function palOpen(){ palItems=palBuild(); palRender(""); if(pal.showModal)pal.showModal(); setTimeout(function(){palQ.value="";palQ.focus();},20); }
 function palMove(d){ if(!palShown.length)return; palSel=(palSel+d+palShown.length)%palShown.length; Array.prototype.forEach.call(palList.children,function(li,i){li.classList.toggle("sel",i===palSel); if(i===palSel&&li.scrollIntoView)li.scrollIntoView({block:"nearest"});}); }
 function palRun(i){ var it=palShown[i]; if(!it)return; pal.close(); it.act(); }
 document.addEventListener("keydown",function(e){
  if((e.metaKey||e.ctrlKey)&&(e.key==="k"||e.key==="K")){e.preventDefault(); if(pal&&pal.open)pal.close(); else if(pal)palOpen();}
 });
 if(palQ){
  palQ.addEventListener("input",function(){palRender(palQ.value);});
  palQ.addEventListener("keydown",function(e){
   if(e.key==="ArrowDown"){e.preventDefault();palMove(1);}
   else if(e.key==="ArrowUp"){e.preventDefault();palMove(-1);}
   else if(e.key==="Enter"){e.preventDefault();palRun(palSel);}
  });
 }
 if(pal)pal.addEventListener("click",function(e){if(e.target===pal)pal.close();});
 if(palList)palList.addEventListener("click",function(e){var li=e.target.closest("li[data-i]");if(li)palRun(+li.dataset.i);});
 var palbtn=document.getElementById("palbtn");
 if(palbtn)palbtn.addEventListener("click",palOpen);

 // Test-alert button: post via fetch and show an in-button spinner (no reload).
 document.addEventListener("submit",function(e){
  var f=e.target;
  if(!f||f.getAttribute("action")!=="/test-alert")return;
  e.preventDefault();
  var btn=f.querySelector("button");
  if(!btn||btn._busy)return; btn._busy=1;
  var orig=btn.innerHTML; btn.disabled=true; btn.innerHTML='<span class="btnspin"></span>Sending…';
  fetch("/test-alert",{method:"POST",body:new FormData(f)})
   .then(function(r){btn.innerHTML=r.ok?"✓ Sent":"Failed";})
   .catch(function(){btn.innerHTML="Failed";})
   .finally(function(){setTimeout(function(){btn.disabled=false;btn.innerHTML=orig;btn._busy=0;},2000);});
 },true);
 // After Cloudflare Access clears the session, return to the app root so the
 // login page shows again (instead of the generic "logged out" page). Built
 // from the current origin so it works on any host.
 var lo=document.getElementById("logout");
 if(lo)lo.href="/cdn-cgi/access/logout?returnTo="+encodeURIComponent(location.origin+"/");
 // Copy-to-clipboard (e.g. the ssh login command). Re-run after a live swap.
 function attachCopy(){
  document.querySelectorAll("[data-copy]").forEach(function(b){
   if(b._c)return; b._c=1;
   b.addEventListener("click",function(){
    (navigator.clipboard?navigator.clipboard.writeText(b.dataset.copy):Promise.reject()).then(function(){
     var t=b.textContent; b.textContent="Copied"; setTimeout(function(){b.textContent=t;},1200);
    }).catch(function(){});
   });
  });
 }
 var tb=document.getElementById("themebtn");
 if(tb)tb.addEventListener("click",function(){
  var d=document.documentElement.dataset.theme==="dark"?"light":"dark";
  document.documentElement.dataset.theme=d;
  try{localStorage.setItem("roost-theme",d);}catch(e){}
 });
 // Memory-by-app hover tooltip: name + RAM for the bar under the cursor.
 function attachSpark(){
  var spark=document.querySelector(".spark"); if(!spark||spark._s)return; spark._s=1;
  spark.addEventListener("mousemove",function(ev){
   var tip=document.getElementById("sparkTip"); if(!tip)return;
   var sb=ev.target.closest(".sb");
   if(!sb){tip.hidden=true;return;}
   var prev=spark.querySelector(".sb.hot"); if(prev&&prev!==sb)prev.classList.remove("hot");
   sb.classList.add("hot");
   tip.innerHTML="<b>"+sb.dataset.app+"</b>"+sb.dataset.mem;
   var sr=spark.getBoundingClientRect(),br=sb.getBoundingClientRect();
   tip.style.left=(br.left-sr.left+br.width/2)+"px";
   tip.style.top=(br.top-sr.top-8)+"px";
   tip.hidden=false;
  });
  spark.addEventListener("mouseleave",function(){
   var tip=document.getElementById("sparkTip"); if(tip)tip.hidden=true;
   var h=spark.querySelector(".sb.hot"); if(h)h.classList.remove("hot");
  });
 }
 // Collapse / expand the left sidebar and right info panel so main can stretch.
 function collapser(btnId,dataKey,storeKey){
  var b=document.getElementById(btnId),r=document.documentElement;
  if(!b)return;
  var sync=function(){b.classList.toggle("on",r.dataset[dataKey]==="off");};
  sync();
  b.addEventListener("click",function(){
   var off=r.dataset[dataKey]==="off";
   if(off){delete r.dataset[dataKey];}else{r.dataset[dataKey]="off";}
   try{localStorage.setItem(storeKey,off?"on":"off");}catch(e){}
   sync();
  });
 }
 collapser("sidetgl","side","roost-side");
 collapser("railtgl","rail","roost-rail");
 var burger=document.getElementById("burger");
 if(burger)burger.addEventListener("click",function(){document.body.classList.toggle("nav-open");});
 document.querySelectorAll(".side .nav a:not(.navf)").forEach(function(a){a.addEventListener("click",function(){document.body.classList.remove("nav-open");});});

 // Reattach listeners + reapply view/filter to freshly rendered main/rail.
 function initDynamic(){ apply(localStorage.getItem(KEY)||"list"); filter(); attachSpark(); attachCopy(); }
 initDynamic();

 // Live auto-refresh: poll the page and swap only main + rail, preserving
 // scroll, view mode, search/filter, and any open menu or dialog. One source
 // of truth (the server template) — no client-side rendering to drift.
 var busy=false;
 function refresh(){
  if(busy||document.hidden)return;
  if(document.querySelector("dialog[open]"))return;         // adding an app
  if(document.querySelector("details.menu[open]"))return;   // a menu is open
  if(document.activeElement&&document.activeElement.id==="q")return; // typing search
  busy=true;
  fetch(location.pathname,{headers:{"X-Roost-Poll":"1"},cache:"no-store"})
   .then(function(r){return r.ok?r.text():Promise.reject(r.status);})
   .then(function(html){
    var doc=new DOMParser().parseFromString(html,"text/html");
    var nm=doc.querySelector("main"),nr=doc.querySelector(".rail");
    var cm=document.querySelector("main"),cr=document.querySelector(".rail");
    if(!nm||!cm)return;                                      // auth redirect etc — skip
    var sTop=cm.scrollTop,rTop=cr?cr.scrollTop:0;
    cm.innerHTML=nm.innerHTML;
    if(nr&&cr)cr.innerHTML=nr.innerHTML;
    cm.scrollTop=sTop; if(cr)cr.scrollTop=rTop;
    initDynamic();
    var d=document.getElementById("livedot"); if(d){d.classList.remove("beat");void d.offsetWidth;d.classList.add("beat");}
   })
   .catch(function(){})
   .finally(function(){busy=false;});
 }
 setInterval(refresh,5000);
 document.addEventListener("visibilitychange",function(){ if(!document.hidden)refresh(); });
})();
</script>
</body></html>`))

// publicTmpl is the standalone, controls-free status page. Self-contained
// styling; carries only app name + up/down + reachability (no secrets).
var publicTmpl = template.Must(template.New("public").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Status · roost</title>
<link rel="icon" type="image/svg+xml" href="data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHZpZXdCb3g9IjAgMCA0MCA0MCIgZmlsbD0ibm9uZSI+PHJlY3Qgd2lkdGg9IjQwIiBoZWlnaHQ9IjQwIiByeD0iMTEiIGZpbGw9InVybCgjcmcpIi8+PHBhdGggZD0iTTEwLjUgMTkuMiBMMjAgMTEgTDI5LjUgMTkuMiIgc3Ryb2tlPSIjZmZmIiBzdHJva2Utd2lkdGg9IjIuNiIgc3Ryb2tlLWxpbmVjYXA9InJvdW5kIiBzdHJva2UtbGluZWpvaW49InJvdW5kIi8+PHBhdGggZD0iTTEzLjQgMTguNCBWMjguNiBIMjYuNiBWMTguNCIgc3Ryb2tlPSIjZmZmIiBzdHJva2Utd2lkdGg9IjIuNiIgc3Ryb2tlLWxpbmVjYXA9InJvdW5kIiBzdHJva2UtbGluZWpvaW49InJvdW5kIi8+PHBhdGggZD0iTTE3LjQgMjguNiBWMjQgYTIuNiAyLjYgMCAwIDEgNS4yIDAgVjI4LjYiIHN0cm9rZT0iI2ZmZiIgc3Ryb2tlLXdpZHRoPSIyLjIiIHN0cm9rZS1saW5lY2FwPSJyb3VuZCIgc3Ryb2tlLWxpbmVqb2luPSJyb3VuZCIvPjxkZWZzPjxsaW5lYXJHcmFkaWVudCBpZD0icmciIHgxPSIwIiB5MT0iMCIgeDI9IjEiIHkyPSIxIj48c3RvcCBzdG9wLWNvbG9yPSIjOGI4M2Y3Ii8+PHN0b3Agb2Zmc2V0PSIuNTUiIHN0b3AtY29sb3I9IiM1YjU0ZTYiLz48c3RvcCBvZmZzZXQ9IjEiIHN0b3AtY29sb3I9IiM0MzM4Y2EiLz48L2xpbmVhckdyYWRpZW50PjwvZGVmcz48L3N2Zz4K">
<style>
 :root{--bg:#f6f7f9;--panel:#fff;--line:#e6e8ee;--ink:#12141c;--muted:#5f6675;--ok:#12a150;--bad:#e5484d;--font:'Google Sans',system-ui,-apple-system,Segoe UI,Roboto,sans-serif}
 @media(prefers-color-scheme:dark){:root{--bg:#0a0d13;--panel:#12151d;--line:#232838;--ink:#e7ecf5;--muted:#9aa4b6}}
 *{box-sizing:border-box} body{margin:0;background:var(--bg);color:var(--ink);font:15px/1.55 var(--font);-webkit-font-smoothing:antialiased}
 .wrap{max-width:640px;margin:0 auto;padding:56px 20px}
 .hd{display:flex;align-items:center;gap:14px;margin-bottom:26px}
 .logo{width:40px;height:40px;border-radius:11px;flex:none;overflow:hidden}
 h1{font-size:20px;margin:0} .sub{color:var(--muted);font-size:13px;margin-top:2px}
 .banner{display:flex;align-items:center;gap:11px;padding:16px 18px;border-radius:14px;font-weight:600;margin-bottom:22px;background:var(--panel);border:1px solid var(--line)}
 .banner .d{width:11px;height:11px;border-radius:50%} .banner.ok .d{background:var(--ok)} .banner.bad .d{background:var(--bad)}
 .incs{margin:-8px 0 22px;display:flex;flex-direction:column;gap:6px}
 .inc{font-size:13px;color:var(--bad);padding:9px 14px;border-radius:10px;background:var(--panel);border:1px solid var(--line)}
 .list{background:var(--panel);border:1px solid var(--line);border-radius:14px;overflow:hidden}
 .row{display:flex;align-items:center;gap:12px;padding:14px 18px;border-top:1px solid var(--line)}
 .row:first-child{border-top:0}
 .row .nm{font-weight:600} .row .u{color:var(--muted);font-size:12px;margin-left:2px}
 .row .st{margin-left:auto;display:inline-flex;align-items:center;gap:6px;font-size:12.5px;font-weight:600}
 .row .st .d{width:8px;height:8px;border-radius:50%}
 .st.up{color:var(--ok)} .st.up .d{background:var(--ok)}
 .st.down{color:var(--bad)} .st.down .d{background:var(--bad)}
 .ft{text-align:center;color:var(--muted);font-size:12px;margin-top:22px}
 .ft a{color:var(--muted)} .ft a:hover{color:var(--ink);text-decoration:underline}
 .ft .back{color:var(--ink);font-weight:600}
 a{color:inherit;text-decoration:none} a.nm:hover{text-decoration:underline}
</style></head>
<body><div class="wrap">
 <div class="hd">
  <div class="logo"><svg viewBox="0 0 40 40" fill="none"><rect width="40" height="40" rx="11" fill="url(#g)"/><path d="M10.5 19.2 L20 11 L29.5 19.2" stroke="#fff" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round"/><path d="M13.4 18.4 V28.6 H26.6 V18.4" stroke="#fff" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round"/><path d="M17.4 28.6 V24 a2.6 2.6 0 0 1 5.2 0 V28.6" stroke="#fff" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"/><defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1"><stop stop-color="#8b83f7"/><stop offset=".55" stop-color="#5b54e6"/><stop offset="1" stop-color="#4338ca"/></linearGradient></defs></svg></div>
  <div><h1>Service status</h1><div class="sub">{{.Up}}/{{.Total}} services operational</div></div>
 </div>
 <div class="banner {{if .AllOK}}ok{{else}}bad{{end}}"><span class="d"></span>{{if .AllOK}}All systems operational{{else}}Some services are degraded{{end}}</div>
 {{if .Incidents}}<div class="incs">{{range .Incidents}}<div class="inc">{{.}}</div>{{end}}</div>{{end}}
 <div class="list">
  {{range .Apps}}<div class="row">
   {{if .URL}}<a class="nm" href="{{.URL}}" target="_blank" rel="noopener">{{.Name}}</a>{{else}}<span class="nm">{{.Name}}</span>{{end}}
   {{if .Up}}<span class="u">· {{.Up}}</span>{{end}}
   {{if and (eq .State "running") (or (eq .HTTP "") .Reachable)}}<span class="st up"><span class="d"></span>Operational</span>{{else if eq .State "running"}}<span class="st down"><span class="d"></span>Degraded{{if .HTTP}} ({{.HTTP}}){{end}}</span>{{else}}<span class="st down"><span class="d"></span>Down</span>{{end}}
  </div>{{end}}
 </div>
 <div class="ft">
  <div><a class="back" href="/">← Control panel</a></div>
  <div style="margin-top:8px">Updated {{.Generated}} · powered by <a href="https://github.com/cdrrazan/roost" target="_blank" rel="noopener">roost</a> · built by <a href="https://github.com/cdrrazan" target="_blank" rel="noopener">Rajan Bhattarai</a></div>
 </div>
</div></body></html>`))
