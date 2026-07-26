# Customization guide

Fleet is built to rebrand in minutes. Everything visual flows from **tokens**.

## 1. Brand color

`assets/css/tokens.css` — change the accent, done:

```css
:root { --brand: #5b54e6; }          /* light */
:root[data-theme="dark"] { --brand: #7c75f5; }  /* dark */
```

`--brand` drives buttons, links, active nav, focus rings, the palette, and
selection highlights. Pick a light and a dark shade (the dark one usually a
touch lighter for contrast).

## 2. The full token set

All colors, surfaces, radius, shadows, and fonts live in `tokens.css`, defined
twice — once for light (`:root`) and once for dark (`:root[data-theme="dark"]`):

| token | purpose |
|---|---|
| `--bg`, `--panel`, `--panel2` | page + card surfaces |
| `--ink`, `--muted`, `--faint` | text hierarchy |
| `--line`, `--line2`, `--track` | borders + bar tracks |
| `--brand`, `--brand-ink` | accent + text on accent |
| `--ok`, `--amber`, `--danger` | status colors |
| `--*-bg`, `--*-ink` | tinted chip/badge pairs |
| `--radius`, `--shadow`, `--shadow-lg` | shape + depth |
| `--font`, `--mono` | type |

Never hard-code colors in `fleet.css` — add a token and reference it, so light
and dark both stay correct.

## 3. Logo & name

- **Logo:** replace `assets/img/favicon.svg` (used in the sidebar, tabs, login,
  status). Keep it square; a rounded container is applied for you.
- **Name / tagline:** the sidebar `.brand` block in each page, and the demo
  `brand` object in `mock.js`.

## 4. Fonts

Swap the Google Fonts `<link>` in each page's `<head>` and update `--font` in
`tokens.css`. The design is set for a geometric humanist sans; Inter, Geist, or
Plus Jakarta Sans all drop in cleanly.

## 5. Default light vs dark

`theme.js` picks up the OS preference, then remembers the user's toggle in
`localStorage` under `fleet-theme`. Force a default by setting
`document.documentElement.dataset.theme = "dark"` before paint.

## 6. Density & radius

- Rounder/sharper: change `--radius`.
- Tighter cards: adjust `.card` / `.srv` padding in `fleet.css`.
- The grid card width lives in `.glist.grid` (min column width).

## 7. Adding a page

Copy `settings.html` as a starting shell (sidebar + top bar + `chrome.js`),
mark the active nav item with `class="active"`, and drop your content in
`<main>`. Reuse the component classes shown in `components.html`.

## 8. Renaming the storage keys

Interactivity persists under `fleet-theme`, `fleet-side`, `fleet-rail`,
`fleet-view`. Rename them in `theme.js`, `fleet.js`, and `chrome.js` if you want
a different namespace.
