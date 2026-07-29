package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/cdrrazan/roost/internal/config"
	"github.com/cdrrazan/roost/internal/doctor"
	"github.com/cdrrazan/roost/internal/generate"
	"github.com/cdrrazan/roost/internal/notify"
	"github.com/cdrrazan/roost/internal/runner"
	"github.com/cdrrazan/roost/internal/shell"
	"github.com/cdrrazan/roost/internal/source"
	"github.com/cdrrazan/roost/internal/state"
	"github.com/cdrrazan/roost/internal/web"
)

// stackController is the real web.Controller: it drives the stack the same way
// the up/down/status commands do, so the panel and the CLI stay in lockstep.
type stackController struct {
	cmd   *cobra.Command
	flags *rootFlags

	// reachability probe cache — probing the public URLs hits Cloudflare on
	// every call, so results are cached briefly and shared by the page load
	// and the live /api/status poll.
	reachMu sync.Mutex
	reachAt time.Time
	reach   map[string]reachResult
}

type reachResult struct {
	code string
	ok   bool
}

// reachTTL is how long a reachability probe result is reused before re-probing.
const reachTTL = 25 * time.Second

var _ web.Controller = (*stackController)(nil)

func (c *stackController) Status() ([]runner.AppStatus, error) {
	apps, _, err := loadPlanned(c.cmd, c.flags)
	if err != nil {
		return nil, err
	}
	r, err := newRunner()
	if err != nil {
		return nil, err
	}
	statuses, err := r.Status(apps)
	if err != nil {
		return nil, err
	}
	// Enrich each app with a browsable link to its git origin. Display-only,
	// derived from the checked-out repo — no config to fill in for every app.
	paths := make(map[string]string, len(apps))
	for _, a := range apps {
		paths[a.Name] = a.Path
	}
	for i := range statuses {
		statuses[i].Repo = repoURL(paths[statuses[i].Name])
	}
	// Real end-to-end reachability: does the public URL actually answer?
	reach := c.reachability(statuses)
	for i := range statuses {
		if r, ok := reach[statuses[i].Name]; ok {
			statuses[i].HTTP = r.code
			statuses[i].Reachable = r.ok
		}
	}
	return statuses, nil
}

// reachability returns a per-app HTTP probe result for every running,
// non-worker app, cached for reachTTL so the live poll doesn't hammer the
// edge. Results are keyed by app name.
func (c *stackController) reachability(apps []runner.AppStatus) map[string]reachResult {
	c.reachMu.Lock()
	defer c.reachMu.Unlock()
	if c.reach != nil && time.Since(c.reachAt) < reachTTL {
		return c.reach
	}
	out := map[string]reachResult{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	client := &http.Client{Timeout: 4 * time.Second}
	for _, a := range apps {
		if a.Worker || a.State != "running" || !strings.HasPrefix(a.URL, "http") {
			continue
		}
		wg.Add(1)
		go func(name, url string) {
			defer wg.Done()
			res := probeURL(client, url)
			mu.Lock()
			out[name] = res
			mu.Unlock()
		}(a.Name, a.URL)
	}
	wg.Wait()
	c.reach = out
	c.reachAt = time.Now()
	return out
}

// probeURL does one GET and classifies the result. Any HTTP answer means the
// app is up — even a 401/302 is the app serving — except gateway errors
// (502/503/504), which mean the upstream is down behind a healthy proxy.
func probeURL(client *http.Client, url string) reachResult {
	resp, err := client.Get(url)
	if err != nil {
		if strings.Contains(err.Error(), "Timeout") || strings.Contains(err.Error(), "deadline") {
			return reachResult{code: "timeout", ok: false}
		}
		return reachResult{code: "error", ok: false}
	}
	defer func() { _ = resp.Body.Close() }()
	ok := resp.StatusCode != http.StatusBadGateway &&
		resp.StatusCode != http.StatusServiceUnavailable &&
		resp.StatusCode != http.StatusGatewayTimeout
	return reachResult{code: fmt.Sprintf("%d", resp.StatusCode), ok: ok}
}

// repoURL reads path/.git/config and returns the origin remote normalized to a
// browsable https URL (any host, any clone protocol). Returns "" when the path
// isn't a git repo, has no origin, or the remote can't be parsed — the panel
// simply omits the link then.
func repoURL(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(path, ".git", "config"))
	if err != nil {
		return ""
	}
	var inOrigin bool
	var raw string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			inOrigin = line == `[remote "origin"]`
			continue
		}
		if inOrigin && strings.HasPrefix(line, "url") {
			if _, v, ok := strings.Cut(line, "="); ok {
				raw = strings.TrimSpace(v)
				break
			}
		}
	}
	return normalizeRemote(raw)
}

// normalizeRemote turns any git remote URL into a browsable https URL, dropping
// a trailing .git. Handles scp-style (git@host:owner/repo.git), ssh:// and
// https:// forms. Returns "" for anything it can't confidently rewrite.
func normalizeRemote(raw string) string {
	if raw == "" {
		return ""
	}
	raw = strings.TrimSuffix(raw, ".git")
	switch {
	case strings.HasPrefix(raw, "https://"):
		return raw
	case strings.HasPrefix(raw, "ssh://"):
		rest := strings.TrimPrefix(raw, "ssh://")
		if i := strings.Index(rest, "@"); i >= 0 {
			rest = rest[i+1:] // drop git@
		}
		return "https://" + rest
	case strings.HasPrefix(raw, "git@"):
		// git@host:owner/repo -> https://host/owner/repo
		rest := strings.TrimPrefix(raw, "git@")
		host, pathPart, ok := strings.Cut(rest, ":")
		if !ok {
			return ""
		}
		return "https://" + host + "/" + pathPart
	default:
		return ""
	}
}

// appResumer is the runner subset Up needs: resume one stopped app container.
type appResumer interface {
	Resume(app string) error
}

// startApps resumes every stopped app container (fast — no rebuild), the
// counterpart to stopApps. Errors are joined so one failure doesn't skip the
// rest.
func startApps(r appResumer, apps []generate.App) error {
	var errs []error
	for _, app := range apps {
		if err := r.Resume(app.Name); err != nil {
			errs = append(errs, fmt.Errorf("start %s: %w", app.Name, err))
		}
	}
	return errors.Join(errs...)
}

// Up resumes the app containers the panel's Stop paused. It is a fast runtime
// toggle, not a provisioner: it does NOT rebuild images or regenerate
// artifacts, and it leaves shared infrastructure (which Stop never touched)
// alone. Initial provisioning and config changes are `roost up` on the CLI.
func (c *stackController) Up() error {
	apps, _, err := loadPlanned(c.cmd, c.flags)
	if err != nil {
		return err
	}
	if len(apps) == 0 {
		return fmt.Errorf("nothing to run: no app resolves to a hostname")
	}
	r, err := newRunner()
	if err != nil {
		return err
	}
	return startApps(r, apps)
}

// appStopper is the runner subset Down needs: stop one app container.
type appStopper interface {
	Stop(app string) error
}

// stopApps stops every app container while leaving shared infrastructure
// (Caddy, cloudflared, databases) running. Errors are joined so one failure
// doesn't skip the rest.
func stopApps(r appStopper, apps []generate.App) error {
	var errs []error
	for _, app := range apps {
		if err := r.Stop(app.Name); err != nil {
			errs = append(errs, fmt.Errorf("stop %s: %w", app.Name, err))
		}
	}
	return errors.Join(errs...)
}

// Down stops the app containers only. It deliberately does NOT run the full
// `roost down` (which also stops Caddy + cloudflared): that would kill the
// tunnel route to this very panel, leaving no way to start again from the web.
// The CLI `roost down` is still the way to take everything down.
func (c *stackController) Down() error {
	apps, _, err := loadPlanned(c.cmd, c.flags)
	if err != nil {
		return err
	}
	r, err := newRunner()
	if err != nil {
		return err
	}
	return stopApps(r, apps)
}

// resolveAppName reports nil only if name matches a configured app. It is the
// gate on the per-app panel actions: without it a crafted POST could name an
// infra service (caddy, cloudflared) and the panel would stop the tunnel to
// itself.
func resolveAppName(apps []generate.App, name string) error {
	for _, a := range apps {
		if a.Name == name {
			return nil
		}
	}
	return fmt.Errorf("unknown app %q", name)
}

// StartApp resumes one stopped app container (fast — no rebuild), after
// checking the name resolves to a configured app.
func (c *stackController) StartApp(name string) error {
	apps, _, err := loadPlanned(c.cmd, c.flags)
	if err != nil {
		return err
	}
	if err := resolveAppName(apps, name); err != nil {
		return err
	}
	r, err := newRunner()
	if err != nil {
		return err
	}
	return r.Resume(name)
}

// DeployApp pulls the app's git checkout (fast-forward only), then rebuilds
// and recreates just that container — the panel's redeploy button for a
// repo-backed app. It reuses the same deployApp that `roost deploy` runs.
func (c *stackController) DeployApp(name string, emit func(string)) error {
	_, resolved, _, err := loadResolved(c.flags)
	if err != nil {
		return err
	}
	var path string
	for i := range resolved {
		if resolved[i].Name == name {
			path = resolved[i].Path
		}
	}
	if path == "" {
		return fmt.Errorf("app %q not found", name)
	}
	r, err := newRunner()
	if err != nil {
		return err
	}
	emit("git pull + rebuild + restart " + name)
	if err := deployApp(shell.Exec{}, r, path, name); err != nil {
		return err
	}
	emit("deployed " + name)
	return nil
}

// StopApp stops one app container, leaving shared infrastructure running, after
// checking the name resolves to a configured app.
func (c *stackController) StopApp(name string) error {
	apps, _, err := loadPlanned(c.cmd, c.flags)
	if err != nil {
		return err
	}
	if err := resolveAppName(apps, name); err != nil {
		return err
	}
	r, err := newRunner()
	if err != nil {
		return err
	}
	return r.Stop(name)
}

// AddApp adds an app to the running stack from the panel: a doctor preflight
// gate, then config edit, artifact regen, and build + start of just the new
// container(s). Every phase streams a line to emit so the processing pane shows
// progress. It regenerates and builds only — it does not touch other apps.
//
// Security note: path is an arbitrary host path the panel user typed, so this
// builds and runs whatever Dockerfile lives there. That is by design (the panel
// is a host-side admin tool) and is why the panel must sit behind Cloudflare
// Access and the on/off token — an unauthenticated caller here is code
// execution on the box.
func (c *stackController) AddApp(path, domain, repo string, emit func(string)) error {
	emit("preflight: running roost doctor")
	findings := runDoctor(c.flags, shell.Exec{})
	if doctor.HasFailures(findings) {
		return fmt.Errorf("preflight failed — %s", firstFailureMessage(findings))
	}
	emit("preflight passed")

	before, _, err := loadPlanned(c.cmd, c.flags)
	if err != nil {
		return err
	}
	beforeNames := appNameSet(before)

	cfgPath, err := config.FindConfig(c.flags.configPath)
	if err != nil {
		return err
	}

	// A repo URL clones into ~/.roost/sources/<name> and becomes the path;
	// otherwise the user gave a host path already on disk.
	if repo != "" {
		dest, err := source.PathFor(source.NameFromRepo(repo))
		if err != nil {
			return err
		}
		emit("cloning " + repo + " → " + dest)
		if err := source.Clone(shell.Exec{}, repo, dest); err != nil {
			return err
		}
		path = dest
	}

	emit("adding to config: " + path)
	if err := config.AddApp(cfgPath, path, domain, repo); err != nil {
		return err
	}

	apps, opts, err := loadPlanned(c.cmd, c.flags)
	if err != nil {
		return err
	}
	dir, err := buildDir()
	if err != nil {
		return err
	}
	emit("generating artifacts")
	if _, err := generate.Generate(dir, apps, opts); err != nil {
		return err
	}

	newNames := diffNewApps(beforeNames, apps)
	if len(newNames) == 0 {
		return fmt.Errorf("added %q to config but no new app resolved to a hostname — set a domain for it", path)
	}
	r := runner.New(dir)
	for _, n := range newNames {
		emit("building + starting " + n)
		if err := r.Start(n); err != nil {
			return err
		}
	}
	c.clearRemoved(newNames)
	emit("done — " + newNames[0] + " is live")
	return nil
}

// RemoveApp tears one app out of the running stack: stop + remove its container
// (optionally its image), drop it from the config, record it for one-click
// re-add, and regenerate artifacts so compose no longer references it. Shared
// database volumes are never touched — an app's data outlives its removal.
func (c *stackController) RemoveApp(name string, deleteImage bool, emit func(string)) error {
	apps, _, err := loadPlanned(c.cmd, c.flags)
	if err != nil {
		return err
	}
	var target *generate.App
	for i := range apps {
		if apps[i].Name == name {
			target = &apps[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("unknown app %q", name)
	}

	r, err := newRunner()
	if err != nil {
		return err
	}
	emit("stopping + removing container: " + name)
	if err := r.Remove(name, deleteImage); err != nil {
		return err
	}
	if deleteImage {
		emit("deleted image roost-" + name)
	}

	cfgPath, err := config.FindConfig(c.flags.configPath)
	if err != nil {
		return err
	}
	emit("removing from config")
	if err := config.RemoveApp(cfgPath, name); err != nil {
		return err
	}

	if st, sp, err := c.loadState(); err == nil {
		st.MarkRemoved(state.RemovedApp{Name: name, Path: target.Path, Domain: target.FQDN})
		if err := st.Save(sp); err != nil {
			emit("warning: could not record for re-add: " + err.Error())
		}
	}

	// Regenerate so the removed app leaves the compose file. Skipped when the
	// config is now empty (nothing to generate) — best-effort either way.
	if apps2, opts, lerr := loadPlanned(c.cmd, c.flags); lerr == nil && len(apps2) > 0 {
		if dir, derr := buildDir(); derr == nil {
			emit("regenerating artifacts")
			_, _ = generate.Generate(dir, apps2, opts)
		}
	}
	emit("done — moved to the Removed list")
	return nil
}

// ServerInfo gathers host metadata for the panel's Server card: disk usage
// (statfs of the home filesystem), hostname/OS/uptime/CPU/RAM, and the
// display-only IP + SSH login from the config's `server:` block. Best-effort —
// anything it can't read is left empty and the template hides that row.
func (c *stackController) ServerInfo() web.ServerInfo {
	si := web.ServerInfo{Cores: runtime.NumCPU(), OS: osPretty(), Uptime: hostUptime(), RAM: hostMemTotal()}
	if h, err := os.Hostname(); err == nil {
		si.Host = h
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "/"
	}
	si.DiskUsed, si.DiskCap, si.DiskPct = diskUsage(home)
	if cfgPath, err := config.FindConfig(c.flags.configPath); err == nil {
		if cfg, err := config.Load(cfgPath); err == nil {
			si.IP, si.Label = cfg.Server.IP, cfg.Server.Label
			if cfg.Server.IP != "" {
				user := cfg.Server.SSHUser
				if user == "" {
					user = "root"
				}
				si.SSH = user + "@" + cfg.Server.IP
			}
		}
	}
	return si
}

// AppDetail returns one app's container detail + recent logs for the drawer.
// Unknown names are rejected so the panel can't read arbitrary containers.
func (c *stackController) AppDetail(name string) (web.AppDetail, error) {
	apps, _, err := loadPlanned(c.cmd, c.flags)
	if err != nil {
		return web.AppDetail{}, err
	}
	var app *generate.App
	for i := range apps {
		if apps[i].Name == name {
			app = &apps[i]
			break
		}
	}
	if app == nil {
		return web.AppDetail{}, fmt.Errorf("unknown app %q", name)
	}
	d := web.AppDetail{
		Name:      app.Name,
		Port:      app.Port,
		Framework: app.Framework,
		Database:  app.Database,
	}
	if app.FQDN != "" {
		d.URL = "https://" + app.FQDN
	}
	for k := range app.Env {
		d.EnvKeys = append(d.EnvKeys, k)
	}
	sort.Strings(d.EnvKeys)

	r, err := newRunner()
	if err != nil {
		return d, nil // static detail is still useful without a runner
	}
	if info, err := r.AppInfo(name); err == nil {
		d.Image, d.Status, d.Health, d.Restarts = info.Image, info.Status, info.Health, info.Restarts
	}
	if logs, err := r.LogTail(name, 200); err == nil {
		d.Logs = logs
	}
	return d, nil
}

// SystemInfo reports docker's disk accounting via `docker system df`.
// Best-effort: any failure yields a zero value, which hides the card.
func (c *stackController) SystemInfo() web.SystemInfo {
	out, err := shell.Exec{}.Run("docker", "system", "df", "--format", "json")
	if err != nil {
		return web.SystemInfo{}
	}
	var si web.SystemInfo
	for _, line := range strings.Split(out.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var d struct {
			Type        string `json:"Type"`
			TotalCount  string `json:"TotalCount"`
			Size        string `json:"Size"`
			Reclaimable string `json:"Reclaimable"`
		}
		if err := json.Unmarshal([]byte(line), &d); err != nil {
			continue
		}
		n := atoiSafe(d.TotalCount)
		switch d.Type {
		case "Images":
			si.Images, si.ImagesSize, si.Reclaimable = n, d.Size, d.Reclaimable
		case "Containers":
			si.Containers = n
		case "Local Volumes":
			si.Volumes, si.VolumesSize = n, d.Size
		case "Build Cache":
			si.BuildCache = d.Size
		}
	}
	return si
}

// EdgeInfo reports roost's Cloudflare tunnel + DNS facts from state.json and
// config. Best-effort: a zero value hides the Edge card.
func (c *stackController) EdgeInfo() web.EdgeInfo {
	st, _, err := c.loadState()
	if err != nil || st.TunnelName == "" {
		return web.EdgeInfo{}
	}
	e := web.EdgeInfo{
		TunnelName: st.TunnelName,
		TunnelID:   shortID(st.TunnelID),
		Account:    shortID(st.AccountID),
	}
	seen := map[string]bool{}
	for _, r := range st.Records {
		if r.Name != "" && !seen[r.Name] {
			seen[r.Name] = true
			e.Hosts = append(e.Hosts, r.Name)
		}
	}
	if cfgPath, err := config.FindConfig(c.flags.configPath); err == nil {
		if cfg, err := config.Load(cfgPath); err == nil {
			e.Protected = cfg.Tunnel.Access != nil && len(cfg.Tunnel.Access.Emails) > 0
		}
	}
	// Live connector health for the Edge card (connected/reconnecting/down).
	if r, err := newRunner(); err == nil {
		e.TunnelState = string(r.TunnelStatus())
	}
	return e
}

// shortID truncates a long CF id/hash for display (first 12 chars).
func shortID(s string) string {
	if len(s) > 12 {
		return s[:12] + "…"
	}
	return s
}

// atoiSafe parses a count string, returning 0 on error.
func atoiSafe(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

// diskUsage returns used/total (human) and the used percentage for the
// filesystem holding path, via statfs.
func diskUsage(path string) (used, capacity string, pct int) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return "", "", 0
	}
	bs := uint64(st.Bsize)
	total := st.Blocks * bs
	avail := st.Bavail * bs
	if total == 0 {
		return "", "", 0
	}
	u := total - avail
	return humanGiB(u), humanGiB(total), int(float64(u)/float64(total)*100 + 0.5)
}

// humanGiB formats bytes in IEC units (GiB when large enough, else MiB).
func humanGiB(b uint64) string {
	if b >= 1<<30 {
		return fmt.Sprintf("%.0fGiB", float64(b)/(1<<30))
	}
	return fmt.Sprintf("%.0fMiB", float64(b)/(1<<20))
}

// osPretty reads PRETTY_NAME from /etc/os-release (Linux), else the GOOS.
func osPretty() string {
	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
				return strings.Trim(v, "\"")
			}
		}
	}
	if runtime.GOOS == "" {
		return ""
	}
	return strings.ToUpper(runtime.GOOS[:1]) + runtime.GOOS[1:]
}

// hostUptime reads /proc/uptime (Linux) and humanizes it; empty elsewhere.
func hostUptime() string {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return ""
	}
	var secs float64
	if _, err := fmt.Sscanf(string(data), "%f", &secs); err != nil || secs <= 0 {
		return ""
	}
	s := int(secs)
	switch d, h, m := s/86400, (s%86400)/3600, (s%3600)/60; {
	case d > 0:
		return fmt.Sprintf("%dd %dh", d, h)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	default:
		return fmt.Sprintf("%dm", m)
	}
}

// hostMemTotal reads MemTotal from /proc/meminfo (Linux); empty elsewhere.
func hostMemTotal() string {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			var kb uint64
			if _, err := fmt.Sscanf(line, "MemTotal: %d kB", &kb); err == nil && kb > 0 {
				return humanGiB(kb * 1024)
			}
		}
	}
	return ""
}

// RemovedApps returns the apps the panel has removed, for one-click re-add.
func (c *stackController) RemovedApps() ([]web.RemovedApp, error) {
	st, _, err := c.loadState()
	if err != nil {
		return nil, err
	}
	out := make([]web.RemovedApp, 0, len(st.Removed))
	for _, r := range st.Removed {
		out = append(out, web.RemovedApp{Name: r.Name, Path: r.Path, Domain: r.Domain})
	}
	return out, nil
}

// appNameSet indexes app names for membership tests.
func appNameSet(apps []generate.App) map[string]bool {
	m := make(map[string]bool, len(apps))
	for _, a := range apps {
		m[a.Name] = true
	}
	return m
}

// diffNewApps returns the names present in after but not before — the app(s) an
// add introduced.
func diffNewApps(before map[string]bool, after []generate.App) []string {
	var out []string
	for _, a := range after {
		if !before[a.Name] {
			out = append(out, a.Name)
		}
	}
	return out
}

// firstFailureMessage renders the first doctor failure as an actionable line
// (message + remedy) for the panel's processing pane.
func firstFailureMessage(findings []doctor.Finding) string {
	for _, f := range findings {
		if f.Level == doctor.Fail {
			if f.Remedy != "" {
				return f.Message + " (fix: " + f.Remedy + ")"
			}
			return f.Message
		}
	}
	return "see `roost doctor`"
}

// loadState opens roost's state.json under the real home directory.
func (c *stackController) loadState() (*state.State, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, "", err
	}
	p := state.Path(home)
	st, err := state.Load(p)
	return st, p, err
}

// clearRemoved drops re-added apps from the removed list, persisting only when
// something changed. Best-effort: a state write failure must not fail an
// otherwise-successful add.
func (c *stackController) clearRemoved(names []string) {
	st, sp, err := c.loadState()
	if err != nil {
		return
	}
	changed := false
	for _, n := range names {
		before := len(st.Removed)
		st.ClearRemoved(n)
		if len(st.Removed) != before {
			changed = true
		}
	}
	if changed {
		_ = st.Save(sp)
	}
}

// newWebCmd serves the control panel. It is a long-running process, meant to be
// supervised (systemd/launchd) *outside* the stack it controls and fronted by
// Cloudflare Access. Default bind is loopback; expose it only through the
// tunnel, never 0.0.0.0 on an untrusted network.
func newWebCmd(flags *rootFlags) *cobra.Command {
	var addr, token string
	cmd := &cobra.Command{
		Use:   "web",
		Short: "Serve a control panel (stack on/off + status) over HTTP",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if token == "" {
				token = os.Getenv("ROOST_WEB_TOKEN")
			}
			ctrl := &stackController{cmd: cmd, flags: flags}
			panel := web.NewServer(ctrl, token)
			if store, err := newPanelStore(); err == nil {
				panel.SetSettingsStore(store)
			}
			// The notifier is rebuilt from panel settings on every save, so
			// changing the recipient on the settings page takes effect live.
			panel.SetMailerFactory(func(s web.Settings) web.Notifier {
				m := mailerFromSettings(flags, s)
				if !m.Enabled() {
					return nil
				}
				return m
			})
			if m := mailerFromSettings(flags, panelSettings(flags)); m.Enabled() {
				cmd.Printf("incident email alerts enabled → %s\n", strings.Join(m.To, ", "))
			}
			panel.StartMonitor(2 * time.Minute)
			srv := &http.Server{
				Addr:              addr,
				Handler:           panel.Handler(),
				ReadHeaderTimeout: 10 * time.Second,
			}
			cmd.Printf("roost web listening on %s\n", addr)
			return srv.ListenAndServe()
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:4600", "address to listen on")
	cmd.Flags().StringVar(&token, "token", "", "shared secret required for on/off actions (or $ROOST_WEB_TOKEN)")
	return cmd
}

// buildMailer assembles the incident notifier from the config's notify: block
// plus $ROOST_SMTP_PASSWORD (never in config). Returns a disabled mailer (and
// empty recipient string) when notifications aren't configured.
func buildMailer(flags *rootFlags) (notify.Mailer, string) {
	cfgPath, err := config.FindConfig(flags.configPath)
	if err != nil {
		return notify.Mailer{}, ""
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return notify.Mailer{}, ""
	}
	n := cfg.Notify
	port := n.SMTPPort
	if port == 0 {
		port = 587
	}
	pass := os.Getenv("ROOST_SMTP_PASSWORD")
	// An SMTP login with no password would just fail auth on every incident;
	// keep alerts cleanly off until $ROOST_SMTP_PASSWORD is set.
	if n.SMTPUser != "" && pass == "" {
		return notify.Mailer{}, ""
	}
	m := notify.Mailer{
		Host: n.SMTPHost, Port: port,
		User: n.SMTPUser, Pass: pass,
		From: n.From, To: n.Email,
	}
	return m, strings.Join(n.Email, ", ")
}

// panelSettings loads the current panel settings (defaults when none saved).
func panelSettings(_ *rootFlags) web.Settings {
	store, err := newPanelStore()
	if err != nil {
		return web.DefaultSettings()
	}
	s, err := store.Load()
	if err != nil {
		return web.DefaultSettings()
	}
	return s
}

// mailerFromSettings builds the incident notifier from panel settings, falling
// back to the config.yml notify: block when the settings page hasn't set a host
// (backward compatible). The password always comes from $ROOST_SMTP_PASSWORD —
// never panel.json, never config.yml.
func mailerFromSettings(flags *rootFlags, s web.Settings) notify.Mailer {
	if s.SMTPHost == "" {
		m, _ := buildMailer(flags)
		return m
	}
	pass := os.Getenv("ROOST_SMTP_PASSWORD")
	port := s.SMTPPort
	if port == 0 {
		port = 587
	}
	// An SMTP login with no password would fail auth on every incident; keep
	// alerts cleanly off until $ROOST_SMTP_PASSWORD is set.
	if s.SMTPUser != "" && pass == "" {
		return notify.Mailer{}
	}
	return notify.Mailer{
		Host: s.SMTPHost, Port: port,
		User: s.SMTPUser, Pass: pass,
		From: s.SMTPFrom, To: s.EmailTo,
	}
}
