/* ============================================================================
   Fleet Dashboard — app engine
   Renders the dashboard from window.FLEET (see mock.js) and wires every
   interaction: theme, collapsible panels, command palette (Cmd/Ctrl-K),
   per-app detail drawer, live-updating metrics, start/stop, search & filters.
   Swap the data source in mock.js (or fetch JSON on load) to go live —
   see docs/INTEGRATION.md.
   ============================================================================ */
(function () {
  "use strict";
  var F = window.FLEET;

  /* ---------- helpers ---------- */
  function esc(s) { return String(s).replace(/[&<>"]/g, function (m) { return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[m]; }); }
  function humanize(n) { return n.split(/[-_ ]/).map(function (w) { return w ? w[0].toUpperCase() + w.slice(1) : w; }).join(" "); }
  var TECH = { rails: "Rails", django: "Django", next: "Next.js", node: "Node", sinatra: "Sinatra", static: "Static", mysql: "MySQL", postgres: "Postgres" };
  // Panel settings persist in localStorage (the static demo's stand-in for
  // ~/.roost/panel.json): mask mode + tech-stack label overrides.
  function FS() { try { return JSON.parse(localStorage.getItem("fleet-settings") || "{}"); } catch (e) { return {}; } }
  function maskv(v) { return FS().mask ? "•• hidden ••" : v; }
  function tech(s) { var o = FS().tech || {}; if (o[s]) return o[s]; return TECH[s] || (s ? s[0].toUpperCase() + s.slice(1) : ""); }
  function pctOf(a) { return a.cap ? Math.round(a.mem / a.cap * 100) : 0; }
  function memColor(p) { return p >= 90 ? "bad" : p >= 70 ? "warn" : "ok"; }
  function memStr(a) { return a.cap ? (a.mem + "MiB / " + a.cap + "MiB") : ""; }
  function healthy(a) { return a.state === "running" && (!a.http || a.http === "200"); }
  var $ = function (s, r) { return (r || document).querySelector(s); };

  /* ---------- derived model ---------- */
  function derive() {
    var apps = F.apps, total = 0, running = 0, used = 0, cap = 0;
    apps.forEach(function (a) {
      if (a.worker) return;
      total++;
      if (a.state === "running") running++;
      if (a.cap) { used += a.mem; cap += a.cap; }
    });
    var groups = [
      { title: "Main apps", apps: apps.filter(function (a) { return (a.category || "main") === "main" && !a.worker; }) },
      { title: "Utilities", apps: apps.filter(function (a) { return a.category === "utility" && !a.worker; }) },
      { title: "Workers", apps: apps.filter(function (a) { return a.worker; }) }
    ].filter(function (g) { return g.apps.length; });
    var alerts = [];
    apps.forEach(function (a) {
      if (a.worker) return;
      if (a.state !== "running") alerts.push({ level: "warn", text: humanize(a.name) + " is " + a.state });
      else if (a.http && a.http !== "200") alerts.push({ level: "bad", text: humanize(a.name) + " is up but returns " + a.http });
      else if (pctOf(a) >= 90) alerts.push({ level: "warn", text: humanize(a.name) + " memory at " + pctOf(a) + "%" });
    });
    var open = F.incidents.filter(function (i) { return i.open; });
    // Featured: apps flagged featured (max 2); fall back to the first two
    // non-worker apps so the section is never empty.
    var real = apps.filter(function (a) { return !a.worker; });
    var featured = real.filter(function (a) { return a.featured; }).slice(0, 2);
    if (!featured.length) featured = real.slice(0, 2);
    return {
      total: total, running: running, stopped: total - running,
      runningPct: total ? Math.round(running / total * 100) : 0,
      memUsed: (used / 1024).toFixed(1) + "GiB", memCap: (cap / 1024).toFixed(1) + "GiB",
      memPct: cap ? Math.round(used / cap * 100) : 0,
      groups: groups, alerts: alerts, open: open, featured: featured
    };
  }

  /* ---------- SVG snippets ---------- */
  var ICO_GLOBE = '<svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><line x1="3" y1="12" x2="21" y2="12"/><path d="M12 3a15 15 0 0 1 0 18 15 15 0 0 1 0-18"/></svg>';
  var ICO_WORKER = '<svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="6" y="6" width="12" height="12" rx="2"/><path d="M9 2v2M15 2v2M9 20v2M15 20v2M2 9h2M2 15h2M20 9h2M20 15h2"/></svg>';
  var ICO_GH = '<svg viewBox="0 0 24 24" width="13" height="13" fill="currentColor"><path d="M12 .5C5.7.5.5 5.7.5 12c0 5.1 3.3 9.4 7.9 10.9.6.1.8-.3.8-.6v-2c-3.2.7-3.9-1.5-3.9-1.5-.5-1.3-1.3-1.7-1.3-1.7-1.1-.7.1-.7.1-.7 1.2.1 1.8 1.2 1.8 1.2 1 1.8 2.8 1.3 3.5 1 .1-.8.4-1.3.7-1.6-2.6-.3-5.3-1.3-5.3-5.7 0-1.3.5-2.3 1.2-3.1-.1-.3-.5-1.5.1-3.1 0 0 1-.3 3.3 1.2a11.5 11.5 0 0 1 6 0C17 4.7 18 5 18 5c.6 1.6.2 2.8.1 3.1.8.8 1.2 1.8 1.2 3.1 0 4.4-2.7 5.4-5.3 5.7.4.4.8 1.1.8 2.2v3.3c0 .3.2.7.8.6 4.6-1.5 7.9-5.8 7.9-10.9C23.5 5.7 18.3.5 12 .5z"/></svg>';

  /* ---------- card ---------- */
  function card(a) {
    var running = a.state === "running", pct = pctOf(a), memc = memColor(pct);
    var pill = running ? '<span class="pill run">running</span>' : '<span class="pill stop">' + esc(a.state) + "</span>";
    var rchip = running && a.http ? '<span class="rchip ' + (a.http === "200" ? "up" : "down") + '" title="Live HTTP probe: ' + esc(a.http) + '">' + (a.http === "200" ? "live · " + a.http : esc(a.http)) + "</span>" : "";
    var sub = a.url ? '<a href="' + esc(a.url) + '" target="_blank" rel="noopener">' + esc(a.url) + "</a>" : "background worker";
    if (a.health) sub += " · " + esc(a.health);
    if (a.repo) sub += ' · <a class="repo" href="' + esc(a.repo) + '" target="_blank" rel="noopener" title="Open repository">' + ICO_GH + "Code</a>";
    var tags = "";
    if (a.worker || a.framework || a.db || a.redis || a.runtime) {
      tags = '<div class="tags">' +
        (a.worker ? '<span class="tag worker">worker</span>' : "") +
        (a.framework ? '<span class="tag fw">' + tech(a.framework) + "</span>" : "") +
        (a.db ? '<span class="tag db">' + tech(a.db) + "</span>" : "") +
        (a.redis ? '<span class="tag redis">Redis</span>' : "") +
        (a.runtime ? '<span class="tag rt">' + esc(a.runtime) + "</span>" : "") + "</div>";
    }
    var metrics = running ? '<div class="srv-metrics">' +
      '<span class="me"><i>CPU</i>' + a.cpu.toFixed(2) + "%</span>" +
      (a.cap ? '<span class="me"><i>MEM</i>' + esc(memStr(a)) + "</span>" : "") +
      (a.up ? '<span class="me up"><i>UP</i>' + esc(a.up) + "</span>" : "") + "</div>" : "";
    var bar = a.cap && running ? '<div class="srv-bar"><div class="mlabel">Memory <b>' + pct + '%</b></div><div class="bar"><span class="fill ' + memc + '" style="width:' + pct + '%"></span></div></div>' : "";
    return '<div class="srv" data-name="' + esc(humanize(a.name)) + '" data-app="' + esc(a.name) + '" data-state="' + (running ? "running" : "stopped") + '">' +
      '<div class="srv-top">' +
      '<span class="srv-ico" data-detail="' + esc(a.name) + '" title="View details" role="button" tabindex="0">' + (a.url ? ICO_GLOBE : ICO_WORKER) + "</span>" +
      '<div class="srv-idb">' +
      '<div class="srv-nm"><span class="dot ' + (running ? "run" : "stop") + '"></span><span class="srv-name">' + esc(humanize(a.name)) + "</span>" + pill + rchip + "</div>" +
      '<div class="srv-sub">' + sub + "</div>" + tags +
      "</div>" +
      '<div class="srv-acts"><div class="seg">' +
      '<button class="segbtn go" data-act="start" data-app="' + esc(a.name) + '"' + (running ? " disabled" : "") + ">Start</button>" +
      '<button class="segbtn st" data-act="stop" data-app="' + esc(a.name) + '"' + (running ? "" : " disabled") + ">Stop</button>" +
      "</div>" +
      '<details class="menu"><summary class="kebab" title="More actions">⋯</summary><div class="menu-pop">' +
      '<button type="button" class="menu-item" data-detail="' + esc(a.name) + '">View details</button>' +
      (a.url ? '<a class="menu-item" href="' + esc(a.url) + '" target="_blank" rel="noopener">Open site ↗</a>' : "") +
      '<div class="menu-sep"></div><button type="button" class="menu-item" style="color:var(--danger)" data-act="remove" data-app="' + esc(a.name) + '">Remove app</button>' +
      "</div></details></div>" +
      "</div>" + bar + metrics + "</div>";
  }

  /* ---------- renderers ---------- */
  function renderApps(d) {
    var html = d.groups.map(function (g) {
      var slug = g.title.toLowerCase().replace(/ /g, "-");
      return '<div class="group" data-cat="' + g.title + '"><div class="grouphdr" id="' + slug + '">' + g.title + '<span class="gc">' + g.apps.length + '</span></div><div class="glist">' + g.apps.map(card).join("") + "</div></div>";
    }).join("");
    $("#applist").innerHTML = html || '<div class="empty">No apps configured.</div>';
    $("#upCount").textContent = d.running + "/" + d.total + " up";
  }

  function renderGraphs(d) {
    var spark = F.apps.map(function (a) {
      var p = pctOf(a);
      return '<div class="sb" data-app="' + esc(humanize(a.name)) + '" data-mem="' + (a.cap ? p + "% · " + memStr(a) : "n/a") + '"><i class="fill ' + memColor(p) + '" style="height:' + p + '%"></i></div>';
    }).join("");
    $("#graphs").innerHTML =
      '<div class="gcard teal"><div class="gh"><span class="mi"><svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="7" rx="2"/><rect x="3" y="13" width="18" height="7" rx="2"/></svg></span> Fleet status</div><div class="donutrow"><div class="donut" style="--v:' + d.runningPct + ';--c:var(--ok)"><div class="dc"><div><b>' + d.running + "</b><span>of " + d.total + ' up</span></div></div></div><div class="dleg"><div class="li"><span class="sw" style="background:var(--ok)"></span> Running <b>' + d.running + '</b></div><div class="li"><span class="sw" style="background:var(--track)"></span> Stopped <b>' + d.stopped + '</b></div><div class="li"><span class="sw" style="background:var(--ok)"></span> Docker <b>OK</b></div></div></div></div>' +
      '<div class="gcard amber"><div class="gh"><span class="mi"><svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="6" width="18" height="12" rx="2"/></svg></span> Memory usage</div><div class="donutrow"><div class="donut" style="--v:' + d.memPct + ';--c:var(--amber)"><div class="dc"><div><b>' + d.memPct + "%</b><span>used</span></div></div></div><div class=\"dleg\"><div class=\"li\"><span class=\"sw\" style=\"background:var(--amber)\"></span> Used <b>" + d.memUsed + '</b></div><div class="li"><span class="sw" style="background:var(--track)"></span> Cap <b>' + d.memCap + "</b></div></div></div></div>" +
      '<div class="gcard indigo"><div class="gh"><span class="mi"><svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="3" y1="21" x2="21" y2="21"/><rect x="5" y="11" width="3.4" height="8" rx="1"/><rect x="10.3" y="6" width="3.4" height="13" rx="1"/><rect x="15.6" y="14" width="3.4" height="5" rx="1"/></svg></span> Memory by app</div><div class="spark">' + spark + '<div class="spark-tip" id="sparkTip" hidden></div></div><div class="sparkcap"><span>' + d.total + " apps</span><span>% of cap</span></div></div>";
  }

  function featCard(a) {
    var running = a.state === "running";
    var pill = running ? '<span class="pill run">running</span>' : '<span class="pill stop">' + esc(a.state) + "</span>";
    var rchip = running && a.http ? '<span class="rchip ' + (a.http === "200" ? "up" : "down") + '">' + (a.http === "200" ? "live · " + a.http : esc(a.http)) + "</span>" : "";
    var sub = a.url ? '<a href="' + esc(a.url) + '" target="_blank" rel="noopener">' + esc(a.url) + "</a>" : "background worker";
    var metrics = running ? '<div class="fc-metrics">' +
      '<div class="fc-m"><i>CPU</i><b>' + a.cpu.toFixed(2) + "%</b></div>" +
      (a.cap ? '<div class="fc-m"><i>MEM</i><b>' + esc(memStr(a)) + "</b></div>" : "") +
      (a.up ? '<div class="fc-m"><i>UP</i><b>' + esc(a.up.replace(/^Up /, "")) + "</b></div>" : "") + "</div>"
      : '<div class="fc-metrics"><div class="fc-m off">Stopped — no live metrics</div></div>';
    return '<div class="featcard" data-app="' + esc(a.name) + '">' +
      '<div class="fc-top">' +
      '<span class="srv-ico" data-detail="' + esc(a.name) + '" title="View details" role="button" tabindex="0">' + (a.url ? ICO_GLOBE : ICO_WORKER) + "</span>" +
      '<div class="srv-idb"><div class="srv-nm"><span class="dot ' + (running ? "run" : "stop") + '"></span><span class="srv-name">' + esc(humanize(a.name)) + "</span>" + pill + rchip + "</div>" +
      '<div class="srv-sub">' + sub + "</div></div>" +
      '<span class="fc-star" title="Featured app">★</span>' +
      "</div>" + metrics +
      '<div class="fc-acts"><div class="seg">' +
      '<button class="segbtn go" data-act="start" data-app="' + esc(a.name) + '"' + (running ? " disabled" : "") + ">Start</button>" +
      '<button class="segbtn st" data-act="stop" data-app="' + esc(a.name) + '"' + (running ? "" : " disabled") + ">Stop</button></div>" +
      '<button class="btn btn-sm" data-detail="' + esc(a.name) + '">View details</button></div>' +
      "</div>";
  }
  function renderFeatured(d) {
    var host = $("#featured"); if (!host) return;
    if (!d.featured.length) { host.innerHTML = ""; return; }
    host.innerHTML =
      '<div class="feat-h"><h2><span class="fc-star">★</span> Featured <span class="csub">pinned apps at a glance</span></h2></div>' +
      '<div class="feat-grid">' + d.featured.map(featCard).join("") + "</div>";
  }

  function renderBanners(d) {
    var html = "";
    if (d.open.length) {
      html += '<div class="incbanner"><span class="ib-dot"></span><b>' + d.open.length + " active incident" + (d.open.length > 1 ? "s" : "") + '</b><span class="ib-list">' + d.open.map(function (i) { return i.label + " — " + i.ago; }).join(" · ") + "</span></div>";
    }
    if (d.alerts.length) {
      html += '<div class="alerts">' + d.alerts.map(function (a) { return '<div class="alert ' + a.level + '"><svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M10.3 3.6 1.8 18a2 2 0 0 0 1.7 3h16.9a2 2 0 0 0 1.7-3L13.7 3.6a2 2 0 0 0-3.4 0z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>' + esc(a.text) + "</div>"; }).join("") + "</div>";
    }
    $("#alertHost").innerHTML = html;
  }

  function row(k, v, mono) { return '<div class="ov-row"><span class="k">' + k + '</span><span class="v' + (mono ? " mono" : "") + '">' + v + "</span></div>"; }
  function renderRail(d) {
    $("#ovBody").innerHTML =
      '<div class="ov-bar"><div class="mlabel">Uptime <b>' + d.runningPct + '%</b></div><div class="bar"><span class="fill teal" style="width:' + d.runningPct + '%"></span></div></div>' +
      '<div class="ov-bar"><div class="mlabel">Memory <b>' + d.memPct + '%</b></div><div class="bar"><span class="fill amber" style="width:' + d.memPct + '%"></span></div></div>' +
      row("Docker", "connected") + row("Total apps", d.total) + row("Running", d.running) + row("Stopped", d.stopped) + row("Memory", d.memUsed + " / " + d.memCap);
    var s = F.server, mask = FS().mask;
    $("#serverBody").innerHTML =
      '<div class="ov-bar"><div class="mlabel">Disk <b>' + s.diskPct + '%</b></div><div class="bar"><span class="fill ' + (s.diskPct >= 90 ? "bad" : s.diskPct >= 70 ? "warn" : "ok") + '" style="width:' + s.diskPct + '%"></span></div></div>' +
      row("IP address", maskv(s.ip), true) + row("Host", maskv(s.host)) + row("OS", s.os) + row("CPU / RAM", s.cores + " vCPU · " + s.ram) + row("Uptime", s.uptime) + row("Disk", s.diskUsed + " / " + s.diskCap) +
      (mask ? '<div class="sshbox"><code>ssh •• hidden ••</code></div>'
        : '<div class="sshbox"><code>ssh ' + esc(s.ssh) + '</code><button class="btn btn-sm" data-copy="ssh ' + esc(s.ssh) + '">Copy</button></div>');
    var e = F.edge;
    $("#edgeBody").innerHTML =
      row("Tunnel", e.tunnel + ' <span class="rchip up" style="margin-left:6px">outbound</span>') +
      row("Tunnel ID", maskv(e.tunnelId), true) + row("Account", maskv(e.account), true) +
      row("Access", e.protected ? '<span class="tag fw">protected</span>' : '<span class="tag worker">public</span>') +
      row("Routes", e.hosts.length + " DNS") +
      (mask ? "" : '<div class="edgehosts">' + e.hosts.map(function (h) { return "<code>" + esc(h) + "</code>"; }).join("") + "</div>");
  }

  function renderSidebar() {
    // The sidebar block is for testing alerts only — incidents live on the
    // dedicated incidents page (sidebar → Monitoring → Incidents).
    $("#sideInc").innerHTML =
      '<div class="si-h"><span class="navlabel" style="padding:0">Alerts</span>' +
      '<form class="inline" data-testalert><button class="btn btn-xs">Test alert</button></form></div>';
    var sy = F.system;
    $("#sideSys").innerHTML =
      '<div class="navlabel">System · docker</div>' +
      '<div class="ss-row"><span>Images</span><b>' + sy.images + " · " + sy.imagesSize + "</b></div>" +
      '<div class="ss-row"><span>Containers</span><b>' + sy.containers + "</b></div>" +
      '<div class="ss-row"><span>Volumes</span><b>' + sy.volumes + " · " + sy.volumesSize + "</b></div>" +
      '<div class="ss-row"><span>Reclaimable</span><b>' + sy.reclaimable + "</b></div>";
  }

  function renderIncidents(d) {
    var host = $("#incPage"); if (!host) return;
    var unread = F.incidents.filter(function (i) { return !i.read; }).length;
    var actions =
      (unread ? '<form class="inline" data-markread><button class="btn btn-sm" title="Acknowledge incidents — kept in history, just dimmed">Mark all read (' + unread + ")</button></form>" : "") +
      '<form class="inline" data-testalert><button class="btn btn-sm" title="Send a test alert">Test alert</button></form>';
    var body = F.incidents.length
      ? '<ul class="inclist">' + F.incidents.map(function (i) {
          return '<li class="incrow ' + (i.open ? "open" : "resolved") + (i.read ? " read" : " unread") + '"><span class="incdot"></span>' +
            '<div class="incmain"><div class="inctop"><b>' + esc(i.label) + '</b> <span class="incago">' + esc(i.ago) + "</span>" + (i.read ? "" : '<span class="incnew">new</span>') + "</div>" +
            '<div class="incsub">' + esc(i.detail) + " · since " + esc(i.since) + "</div></div>" +
            '<span class="incbadge ' + (i.open ? "bad" : "ok") + '">' + (i.open ? "open" : "resolved") + "</span></li>";
        }).join("") + "</ul>"
      : '<div class="allclear"><span class="ico">✓</span> No incidents recorded — everything has been healthy.</div>';
    var share =
      '<div class="inc-share" data-summary="' + esc(incidentSummary(d)) + '">' +
      '<span class="csub">Share status</span>' +
      '<button class="btn btn-sm" data-share="copy" title="Copy the status summary + link">⧉ Copy</button>' +
      '<button class="btn btn-sm" data-share="x" title="Post to X">𝕏</button>' +
      '<button class="btn btn-sm" data-share="linkedin" title="Share on LinkedIn">in</button>' +
      '<button class="btn btn-sm" data-share="facebook" title="Share on Facebook">f</button>' +
      '<span class="inc-summary-preview">' + esc(incidentSummary(d)) + "</span></div>";
    host.innerHTML =
      '<section class="card"><div class="card-h"><h2>🔔 Incidents <span class="csub">' +
      (d.open.length ? d.open.length + " active now" : "all clear") + '</span></h2><div class="inc-actions">' + actions + "</div></div>" + share + body + "</section>";
  }

  // incidentSummary composes the one-line status blurb the share buttons post.
  function incidentSummary(d) {
    if (!d.open.length) {
      return "🟢 All systems operational — " + d.running + "/" + d.total + " services up.";
    }
    var parts = d.open.map(function (i) { return i.label + " (" + i.ago + ")"; });
    return "🔴 Fleet: " + d.open.length + " service" + (d.open.length === 1 ? "" : "s") + " affected — " + parts.join(", ") + ".";
  }

  function renderAll() {
    var d = derive();
    if ($("#alertHost")) renderBanners(d);
    if ($("#graphs")) renderGraphs(d);
    if ($("#featured")) renderFeatured(d);
    if ($("#applist")) renderApps(d);
    if ($("#ovBody")) renderRail(d);
    if ($("#sideInc")) renderSidebar();
    if ($("#incPage")) renderIncidents(d);
    attachDynamic();
  }

  /* ---------- interactions attached to freshly-rendered nodes ---------- */
  var view = localStorage.getItem("fleet-view") || "list", cat = "";
  function applyView() {
    document.querySelectorAll(".glist").forEach(function (e) { e.classList.toggle("grid", view === "grid"); });
    document.querySelectorAll("[data-view]").forEach(function (b) { b.classList.toggle("active", b.dataset.view === view); });
  }
  function applyFilter() {
    var sfEl = document.querySelector("#statusfilter .chip-btn.on");
    var q = ($("#q") ? $("#q").value : "").toLowerCase(), sf = sfEl ? sfEl.dataset.val : "all";
    var anyVisible = false;
    document.querySelectorAll(".group").forEach(function (g) {
      var catOk = !cat || g.dataset.cat === cat, any = false;
      g.querySelectorAll(".srv").forEach(function (r) {
        var show = catOk && r.dataset.name.toLowerCase().indexOf(q) > -1 && (sf === "all" || r.dataset.state === sf);
        r.classList.toggle("hide", !show); if (show) { any = true; anyVisible = true; }
      });
      g.classList.toggle("hide", !(catOk && any));
    });
    var fe = document.getElementById("filterEmpty");
    if (fe) {
      fe.classList.toggle("hide", anyVisible);
      if (!anyVisible) {
        var msg = q ? 'No apps match “' + q + '”'
          : sf === "running" ? "No running apps"
          : sf === "stopped" ? "No stopped apps"
          : "No apps to show";
        fe.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="11" cy="11" r="7"/><path d="M21 21l-4.35-4.35"/></svg>' +
          '<div><b>' + msg + '</b><span>Try a different filter or search.</span></div>';
      }
    }
  }
  function attachSpark() {
    var spark = $(".spark"); if (!spark || spark._s) return; spark._s = 1;
    spark.addEventListener("mousemove", function (ev) {
      var tip = $("#sparkTip"); if (!tip) return;
      var sb = ev.target.closest(".sb"); if (!sb) { tip.hidden = true; return; }
      spark.querySelectorAll(".sb.hot").forEach(function (x) { if (x !== sb) x.classList.remove("hot"); });
      sb.classList.add("hot"); tip.innerHTML = "<b>" + sb.dataset.app + "</b>" + sb.dataset.mem;
      var sr = spark.getBoundingClientRect(), br = sb.getBoundingClientRect();
      tip.style.left = (br.left - sr.left + br.width / 2) + "px"; tip.style.top = (br.top - sr.top - 8) + "px"; tip.hidden = false;
    });
    spark.addEventListener("mouseleave", function () { var t = $("#sparkTip"); if (t) t.hidden = true; spark.querySelectorAll(".sb.hot").forEach(function (x) { x.classList.remove("hot"); }); });
  }
  function attachCopy() {
    document.querySelectorAll("[data-copy]").forEach(function (b) {
      if (b._c) return; b._c = 1;
      b.addEventListener("click", function () { (navigator.clipboard ? navigator.clipboard.writeText(b.dataset.copy) : Promise.reject()).then(function () { var t = b.textContent; b.textContent = "Copied"; setTimeout(function () { b.textContent = t; }, 1200); }).catch(function () {}); });
    });
  }
  function attachDynamic() { applyView(); applyFilter(); attachSpark(); attachCopy(); }

  /* ---------- start / stop / remove (demo: mutate model) ---------- */
  document.addEventListener("click", function (e) {
    var b = e.target.closest("[data-act]"); if (!b) return;
    var a = F.apps.find(function (x) { return x.name === b.dataset.app; }); if (!a) return;
    if (b.dataset.act === "start") { a.state = "running"; a.http = a.worker ? "" : "200"; a.up = "Up 1 min"; a.cpu = 1.2; a.mem = Math.round(a.cap * 0.3); }
    else if (b.dataset.act === "stop") { a.state = "exited"; a.http = ""; a.up = ""; a.cpu = 0; a.mem = 0; }
    else if (b.dataset.act === "remove") { if (!confirm("Remove " + humanize(a.name) + " from the config? (demo)")) return; F.apps = F.apps.filter(function (x) { return x !== a; }); }
    renderAll();
  });

  /* ---------- detail drawer ---------- */
  var drawer = $("#drawer");
  function openDrawer(name) {
    var a = F.apps.find(function (x) { return x.name === name; }); if (!a || !drawer) return;
    document.querySelectorAll("details.menu[open]").forEach(function (d) { d.removeAttribute("open"); });
    $("#dr-name").textContent = humanize(a.name);
    var u = $("#dr-url"); if (a.url) { u.textContent = a.url; u.href = a.url; u.style.display = ""; } else u.style.display = "none";
    var cells = [["Status", a.state === "running" ? "Up" : a.state], ["Health", a.health || "—"], ["Image", "fleet-" + a.name], ["Restarts", a.restarts], ["Port", a.framework === "static" ? 80 : 3000], ["Stack", tech(a.framework) + (a.db ? " · " + tech(a.db) : "")]];
    $("#dr-grid").innerHTML = cells.map(function (c) { return '<div class="dr-cell"><div class="kk">' + c[0] + '</div><div class="vv">' + esc(String(c[1])) + "</div></div>"; }).join("");
    $("#dr-env").innerHTML = a.env && a.env.length ? a.env.map(function (k) { return '<span class="tag">' + esc(k) + "</span>"; }).join("") : '<span class="tag">no env keys</span>';
    var mine = F.incidents.filter(function (i) { return i.app === a.name; });
    var incH = $("#dr-incs-h"), incB = $("#dr-incs");
    if (incH && incB) {
      if (mine.length) {
        incH.hidden = false;
        incB.innerHTML = mine.map(function (i) { return '<div class="dr-inc' + (i.open ? " open" : "") + '"><span class="d"></span>' + esc(i.ago) + " · " + esc(i.since) + "</div>"; }).join("");
      } else { incH.hidden = true; incB.innerHTML = ""; }
    }
    var logs = a.state === "running"
      ? "fleet-" + a.name + "-1  | Booting " + tech(a.framework) + "…\nfleet-" + a.name + "-1  | Listening on 0.0.0.0:3000\nfleet-" + a.name + "-1  | GET / 200 12ms\nfleet-" + a.name + "-1  | GET /health 200 2ms"
      : "fleet-" + a.name + "-1  | Process exited (code 1)\nfleet-" + a.name + "-1  | Restart attempt " + a.restarts;
    $("#dr-logs").textContent = logs;
    if (drawer.showModal) drawer.showModal();
  }
  document.addEventListener("click", function (e) { var t = e.target.closest("[data-detail]"); if (!t) return; e.preventDefault(); openDrawer(t.dataset.detail); });
  (function () { var c = $("#dr-close"); if (c && drawer) c.addEventListener("click", function () { drawer.close(); }); if (drawer) drawer.addEventListener("click", function (e) { if (e.target === drawer) drawer.close(); }); })();

  /* ---------- test alert (demo) ---------- */
  document.addEventListener("submit", function (e) {
    var f = e.target; if (!f || !f.hasAttribute("data-testalert")) return;
    e.preventDefault(); var btn = f.querySelector("button"); if (!btn || btn._b) return; btn._b = 1;
    var orig = btn.innerHTML; btn.disabled = true; btn.innerHTML = '<span class="btnspin"></span>Sending…';
    setTimeout(function () { btn.innerHTML = "✓ Sent (demo)"; setTimeout(function () { btn.disabled = false; btn.innerHTML = orig; btn._b = 0; }, 1800); }, 900);
  }, true);

  /* ---------- mark incidents read (history is kept, just dimmed) ---------- */
  document.addEventListener("submit", function (e) {
    var f = e.target; if (!f || !f.hasAttribute("data-markread")) return;
    e.preventDefault();
    F.incidents.forEach(function (i) { i.read = true; });
    renderAll();
  }, true);

  /* ---------- share status summary ---------- */
  document.addEventListener("click", function (e) {
    var b = e.target.closest("[data-share]"); if (!b) return;
    var box = b.closest(".inc-share"), summary = box ? box.dataset.summary : "";
    var pageUrl = location.origin + location.pathname.replace(/[^/]*$/, "") + "status.html";
    var enc = encodeURIComponent, text = summary + " " + pageUrl, url;
    if (b.dataset.share === "copy") {
      var done = function () { var t = b.innerHTML; b.innerHTML = "✓ Copied"; setTimeout(function () { b.innerHTML = t; }, 1200); };
      if (navigator.clipboard) { navigator.clipboard.writeText(text).then(done).catch(function () {}); } else { done(); }
      return;
    }
    if (b.dataset.share === "x") url = "https://twitter.com/intent/tweet?text=" + enc(text);
    else if (b.dataset.share === "linkedin") url = "https://www.linkedin.com/sharing/share-offsite/?url=" + enc(pageUrl);
    else if (b.dataset.share === "facebook") url = "https://www.facebook.com/sharer/sharer.php?u=" + enc(pageUrl) + "&quote=" + enc(summary);
    if (url) window.open(url, "_blank", "noopener,noreferrer,width=600,height=520");
  });

  /* ---------- command palette ---------- */
  var pal = $("#palette"), palQ = $("#pal-q"), palList = $("#pal-list"), palItems = [], palShown = [], palSel = 0;
  var PI_APP = '<svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/></svg>';
  var PI_ACT = '<svg viewBox="0 0 24 24" fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="13 2 3 14 12 14 11 22 21 10 12 10 13 2"/></svg>';
  function palBuild() {
    var items = [
      { label: "Start all apps", kind: "Action", act: function () { F.apps.forEach(function (a) { if (a.state !== "running") { a.state = "running"; a.http = a.worker ? "" : "200"; a.up = "Up 1 min"; a.cpu = 1; a.mem = Math.round(a.cap * 0.3); } }); renderAll(); } },
      { label: "Stop all apps", kind: "Action", act: function () { F.apps.forEach(function (a) { a.state = "exited"; a.http = ""; a.up = ""; a.cpu = 0; a.mem = 0; }); renderAll(); } },
      { label: "Toggle theme", kind: "Action", act: function () { $("#themebtn").click(); } },
      { label: "Toggle sidebar", kind: "Action", act: function () { $("#sidetgl").click(); } },
      { label: "Toggle info panel", kind: "Action", act: function () { $("#railtgl").click(); } }
    ];
    F.apps.forEach(function (a) { items.push({ label: humanize(a.name) + " — details", kind: "App", act: function () { openDrawer(a.name); } }); });
    return items;
  }
  function palRender(term) {
    term = (term || "").toLowerCase();
    palShown = palItems.filter(function (it) { return it.label.toLowerCase().indexOf(term) > -1; }); palSel = 0;
    palList.innerHTML = palShown.length ? palShown.map(function (it, i) { return '<li data-i="' + i + '" class="' + (i === 0 ? "sel" : "") + '"><span class="pi">' + (it.kind === "App" ? PI_APP : PI_ACT) + "</span>" + esc(it.label) + '<span class="pk">' + it.kind + "</span></li>"; }).join("") : '<div class="pal-empty">No matches</div>';
  }
  function palOpen() { palItems = palBuild(); palRender(""); if (pal.showModal) pal.showModal(); setTimeout(function () { palQ.value = ""; palQ.focus(); }, 20); }
  function palMove(dir) { if (!palShown.length) return; palSel = (palSel + dir + palShown.length) % palShown.length; Array.prototype.forEach.call(palList.children, function (li, i) { li.classList.toggle("sel", i === palSel); if (i === palSel && li.scrollIntoView) li.scrollIntoView({ block: "nearest" }); }); }
  function palRun(i) { var it = palShown[i]; if (!it) return; pal.close(); it.act(); }
  document.addEventListener("keydown", function (e) { if ((e.metaKey || e.ctrlKey) && (e.key === "k" || e.key === "K")) { e.preventDefault(); if (pal && pal.open) pal.close(); else if (pal) palOpen(); } });
  if (palQ) { palQ.addEventListener("input", function () { palRender(palQ.value); }); palQ.addEventListener("keydown", function (e) { if (e.key === "ArrowDown") { e.preventDefault(); palMove(1); } else if (e.key === "ArrowUp") { e.preventDefault(); palMove(-1); } else if (e.key === "Enter") { e.preventDefault(); palRun(palSel); } }); }
  if (pal) pal.addEventListener("click", function (e) { if (e.target === pal) pal.close(); });
  if (palList) palList.addEventListener("click", function (e) { var li = e.target.closest("li[data-i]"); if (li) palRun(+li.dataset.i); });
  var palbtn = $("#palbtn"); if (palbtn) palbtn.addEventListener("click", palOpen);

  /* ---------- static-chrome interactions (bound once) ---------- */
  document.querySelectorAll("[data-view]").forEach(function (b) { b.addEventListener("click", function () { view = b.dataset.view; localStorage.setItem("fleet-view", view); applyView(); }); });
  (function () {
    var q = $("#q"), sf = $("#statusfilter");
    if (q) q.addEventListener("input", applyFilter);
    if (sf) sf.addEventListener("click", function (e) {
      var c = e.target.closest(".chip-btn"); if (!c) return;
      sf.querySelectorAll(".chip-btn").forEach(function (x) { x.classList.toggle("on", x === c); });
      applyFilter();
    });
  })();
  document.querySelectorAll(".navf").forEach(function (a) { a.addEventListener("click", function (e) { if (!document.querySelector(".group")) return; /* off-dashboard (e.g. incidents page) → follow href home */ e.preventDefault(); cat = a.dataset.cat; document.querySelectorAll(".navf").forEach(function (x) { x.classList.toggle("active", x === a); }); applyFilter(); document.body.classList.remove("nav-open"); }); });
  document.addEventListener("click", function (e) { document.querySelectorAll("details.menu[open]").forEach(function (d) { if (!d.contains(e.target)) d.removeAttribute("open"); }); });
  (function () { var tb = $("#themebtn"); if (tb) tb.addEventListener("click", function () { var dk = document.documentElement.dataset.theme === "dark" ? "light" : "dark"; document.documentElement.dataset.theme = dk; try { localStorage.setItem("fleet-theme", dk); } catch (e) {} }); })();
  function collapser(id, key, store) { var b = $("#" + id), r = document.documentElement; if (!b) return; var sync = function () { b.classList.toggle("on", r.dataset[key] === "off"); }; sync(); b.addEventListener("click", function () { var off = r.dataset[key] === "off"; if (off) delete r.dataset[key]; else r.dataset[key] = "off"; try { localStorage.setItem(store, off ? "on" : "off"); } catch (e) {} sync(); }); }
  collapser("sidetgl", "side", "fleet-side"); collapser("railtgl", "rail", "fleet-rail");
  (function () { var bg = $("#burger"); if (bg) bg.addEventListener("click", function () { document.body.classList.toggle("nav-open"); }); })();

  /* ---------- topbar bulk actions + toast ---------- */
  function toast(msg) {
    var t = document.createElement("div"); t.className = "toast"; t.textContent = msg;
    document.body.appendChild(t); requestAnimationFrame(function () { t.classList.add("in"); });
    setTimeout(function () { t.classList.remove("in"); setTimeout(function () { t.remove(); }, 250); }, 2200);
  }
  document.querySelectorAll("[data-allact]").forEach(function (b) {
    b.addEventListener("click", function () {
      var start = b.dataset.allact === "start";
      F.apps.forEach(function (a) {
        if (start) { if (a.state !== "running") { a.state = "running"; a.http = a.worker ? "" : "200"; a.up = "Up 1 min"; a.cpu = 1; a.mem = Math.round(a.cap * 0.3); } }
        else { a.state = "exited"; a.http = ""; a.up = ""; a.cpu = 0; a.mem = 0; }
      });
      renderAll(); toast(start ? "Starting all apps…" : "Stopping all apps…");
    });
  });
  (function () { var n = $("#newapp"); if (n) n.addEventListener("click", function () { toast("New-app flow is disabled in the static demo"); }); })();

  /* ---------- live updating metrics ---------- */
  function jitter() {
    if (document.hidden || document.querySelector("dialog[open]") || document.querySelector("details.menu[open]")) return;
    if (document.activeElement && document.activeElement.id === "q") return;
    F.apps.forEach(function (a) {
      if (a.state !== "running") return;
      a.cpu = Math.max(0.1, +(a.cpu + (Math.random() - 0.5) * 2).toFixed(2));
      if (a.cap) { a.mem = Math.min(a.cap, Math.max(20, a.mem + Math.round((Math.random() - 0.5) * 24))); }
    });
    renderAll();
    var d = $("#livedot"); if (d) { d.classList.remove("beat"); void d.offsetWidth; d.classList.add("beat"); }
  }
  setInterval(jitter, 4000);
  document.addEventListener("visibilitychange", function () { if (!document.hidden) jitter(); });

  /* ---------- boot ---------- */
  renderAll();
})();
