# Changelog

All notable changes to Fleet are documented here. Format: [Keep a Changelog](https://keepachangelog.com/); versioning: [SemVer](https://semver.org/).

## [1.2.0] — 2026-07-27

### Added
- **Dedicated Incidents page** (`incidents.html`, sidebar → Monitoring → Incidents):
  reuses the dashboard shell — both sidebars intact — and lists the full
  open/resolved history with durations, an active-count header, and a
  **Clear resolved** button. New `Incidents` nav item with an open-count pip.
- **Per-app incident history** in each app's detail drawer — a record's card now
  doubles as its status page.

### Changed
- The sidebar `Incidents` block is now **Alerts** — a Test-alert control plus a
  *View incidents →* link. The live incident list moved to the dedicated page
  (the sidebar block is for testing alerts only).
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
