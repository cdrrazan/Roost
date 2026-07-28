# Changelog

All notable changes to Fleet are documented here. Format: [Keep a Changelog](https://keepachangelog.com/); versioning: [SemVer](https://semver.org/).

## [1.6.0] — 2026-07-28

### Added
- **Featured apps** — a pinned strip of up to **two** highlighted apps between the
  stat gauges and the Applications list. **Star any app card** to feature it
  (persisted to `localStorage` `fleet-favorites`); `featured: true` in the data
  is the initial pick, falling back to the first two non-worker apps. Each card
  shows status, reachability, live CPU/MEM/uptime, and quick start/stop + detail.
  New `.feat` / `.featcard` / `.favbtn` styles.

## [1.5.0] — 2026-07-27

### Changed
- **Compact sidebar Alerts box** — the Test-alert button now sits inline with the
  label (one tight row instead of a full-width button), roughly halving its
  height. New `.btn-xs` button size.
- Docs refreshed: 7 pages (incidents added), settings/mask/share/mark-read
  documented in the README and integration guide.

## [1.4.0] — 2026-07-27

### Changed
- Incidents page: **Clear resolved** → **Mark all read**. Incidents are no longer
  deleted — the full history is kept and acknowledged entries render dimmed
  (unread ones carry a `new` badge). The button shows the unread count.

## [1.3.0] — 2026-07-27

### Added
- **Functional settings** (localStorage — the demo's stand-in for a backend):
  default view & theme, a **mask mode** that hides IP / SSH / host / tunnel ids on
  the Server & Edge cards (safe screen-sharing), **email subject/body templates**
  (`{app} {status} {detail} {url} {time}`), and **tech-stack label overrides**
  (`rails=Ruby on Rails`). Saved values apply on the dashboard.
- **Share status** on the Incidents page — copy / X / LinkedIn / Facebook buttons
  that post a one-line summary of the current status plus the status-page link.

## [1.2.0] — 2026-07-27

### Added
- **Dedicated Incidents page** (`incidents.html`, sidebar → Monitoring → Incidents):
  reuses the dashboard shell — both sidebars intact — and lists the full
  open/resolved history with durations, an active-count header, and a
  **Clear resolved** button. New `Incidents` nav item with an open-count pip.
- **Per-app incident history** in each app's detail drawer — a record's card now
  doubles as its status page.

### Changed
- The sidebar `Incidents` block is now **Alerts** — a Test-alert control only.
  The incident list moved to the dedicated page; the sidebar is for testing
  alerts (reach the page via Monitoring → Incidents).
- Resource nav links navigate home when clicked off the dashboard.

## [1.1.0] — 2026-07-26

### Changed
- **Premium polish pass.** Layered card depth with hover-lift, gradient primary
  buttons with brand glow, active-nav accent bar, branded `:focus-visible` rings,
  thin custom scrollbars, spring button press, refined chip/tag micro-interactions,
  and a staggered card entrance (guarded against the live-refresh tick, and fully
  disabled under `prefers-reduced-motion`).
- Added skeleton-shimmer, motion, ring, gradient, and hover-shadow design tokens
  to `tokens.css` (light + dark).

### Fixed
- Component-library color swatches were invisible (inline `<span>` chips ignored
  `height`/`width`); now render as blocks.

## [1.0.0] — 2026-07-26

### Added
- Initial release. 6 pages: dashboard, public status, login, settings, component library, 404.
- Live-updating app cards: status, tech badges, CPU / memory / network / uptime, memory sparkline.
- Real HTTP reachability chips (live · 200 / 502).
- Incidents: active-incident banner, open/all-clear indicator, timeline with durations, Test alert.
- Command palette (⌘K) with keyboard navigation.
- Per-app detail drawer (image, restarts, env keys, recent logs).
- Collapsible sidebar (pinned brand + bottom cluster, scrolling nav) and info rail.
- Light / dark themes with one-file theming via `tokens.css`.
- Zero-build, dependency-free vanilla HTML/CSS/JS.
- Docs: integration guide, customization guide.
