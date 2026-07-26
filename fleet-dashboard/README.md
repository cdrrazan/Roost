<div align="center">

# ⛵ Fleet — Control Dashboard Template

**A premium, zero-build admin dashboard for self-hosted app fleets.**
Live metrics · real reachability · incidents · command palette · light & dark — in pure HTML/CSS/JS.

`no framework` · `no build step` · `~30 KB CSS` · `light + dark` · `fully responsive`

### [▶ Live demo](https://cdrrazan.github.io/Roost/) · [Components](https://cdrrazan.github.io/Roost/components.html) · [Status page](https://cdrrazan.github.io/Roost/status.html)

</div>

---

## 📸 Preview

![Fleet dashboard — light](docs/screenshots/dashboard-light.png)

<div align="center">

| Dark | Components |
|---|---|
| ![Dark](docs/screenshots/dashboard-dark.png) | ![Components](docs/screenshots/components.png) |
| **Public status** | **Settings** |
| ![Status](docs/screenshots/status.png) | ![Settings](docs/screenshots/settings.png) |

</div>

---

## Why Fleet

Most admin templates are a pile of charts with no story. Fleet is modeled on a **real** operations panel: it shows a fleet of apps, whether each is *actually serving traffic*, live CPU/memory/network, incidents with a timeline, and one-keystroke control. Drop it in, point it at your data, ship.

It's **vanilla** on purpose — open `index.html` and it runs. No `npm install`, no bundler, no framework lock-in. Theme it by editing **one file**.

## ✨ Features

- **Live dashboard** — per-app cards with status, tech badges, CPU %, memory, network, uptime, and a memory sparkline. Metrics update live.
- **Real reachability chips** — "live · 200" vs "502" so you see what's *actually* serving, not just "container up".
- **Incidents** — active-incident banner, an all-clear/open indicator, and a timeline with durations. Plus a **Test alert** affordance.
- **Command palette** — ⌘K / Ctrl-K fuzzy search over apps and actions with full keyboard nav.
- **Detail drawer** — click any app for image, status, restarts, env keys, and recent logs.
- **Collapsible panels** — pin the brand + bottom cluster; the nav scrolls; the info rail folds away to stretch the workspace.
- **Public status page** — a controls-free, secret-free `status.html` you can share.
- **6 pages** — dashboard, status, login, settings, component library, 404.
- **Light & dark** — the viewer's choice, remembered. Both are hand-tuned.
- **One-file theming** — change your brand color in `assets/css/tokens.css`; everything follows.

## 📦 What's inside

```
fleet-dashboard/
├─ index.html          # the dashboard
├─ status.html         # public status page
├─ login.html          # sign-in screen
├─ settings.html       # preferences (theme, alerts, thresholds)
├─ components.html     # component library / style guide
├─ 404.html            # not-found page
├─ assets/
│  ├─ css/
│  │  ├─ tokens.css    # ← design tokens: EDIT THIS TO REBRAND
│  │  └─ fleet.css     # layout, shell, components
│  ├─ js/
│  │  ├─ theme.js      # theme + layout init (no-flash)
│  │  ├─ mock.js       # ← demo data (swap for your API)
│  │  ├─ fleet.js      # dashboard engine + interactions
│  │  └─ chrome.js     # shared chrome for non-dashboard pages
│  └─ img/favicon.svg
├─ docs/
│  ├─ INTEGRATION.md   # wire it to a real backend
│  └─ CUSTOMIZE.md     # theming, rebranding, adding pages
├─ README.md · CHANGELOG.md · PRO.md · LICENSE
```

## 🚀 Quick start

No build. Just open it — or serve the folder:

```bash
# option A: open index.html directly in a browser
# option B: any static server
python3 -m http.server 8080     # → http://localhost:8080
npx serve .                      # or this
```

The demo runs entirely from `assets/js/mock.js`. To go live, replace that data (or fetch JSON on load) — see **[docs/INTEGRATION.md](docs/INTEGRATION.md)**.

## 🎨 Make it yours

Open **`assets/css/tokens.css`** and change one line:

```css
--brand: #5b54e6;   /* your accent — buttons, links, active nav, focus */
```

Everything re-themes automatically, in light and dark. Swap the logo in `assets/img/favicon.svg` and the brand text in the sidebar. Full guide: **[docs/CUSTOMIZE.md](docs/CUSTOMIZE.md)**.

## 🧭 Browser support

Modern evergreen browsers (Chrome, Edge, Firefox, Safari). Uses native `<dialog>`, CSS grid, `color-mix()`, and `conic-gradient` — no polyfills.

## 📄 License

The template is **open source under the [MIT License](LICENSE)** — use it in personal and commercial projects freely.

Need the Figma source, extra themes, React/Vue ports, or priority support? See **[PRO.md](PRO.md)**.

## 🙌 Credits

Built by [Rajan Bhattarai](https://github.com/cdrrazan). If Fleet saves you time, a ⭐ is appreciated.
