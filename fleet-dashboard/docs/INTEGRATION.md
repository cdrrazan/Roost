# Integration guide — going live

The demo renders from `assets/js/mock.js`. To connect real data you only touch
the **data**, not the UI. Two approaches:

## 1. Fetch JSON on load (recommended)

Serve a JSON payload shaped like `window.FLEET` from your backend, then hydrate
before `fleet.js` runs. Replace the `mock.js` include in `index.html` with:

```html
<script>
  // Provide window.FLEET before fleet.js runs.
  window.__ready = fetch("/api/fleet")
    .then(function (r) { return r.json(); })
    .then(function (data) { window.FLEET = data; });
</script>
<script src="assets/js/fleet.js" defer></script>
```

…and wrap the boot call, or simpler: keep `mock.js` as a fallback and overwrite
`window.FLEET` from your endpoint, then call the exposed `renderAll()` (export it
from `fleet.js` by adding `window.renderAll = renderAll;` before boot).

## 2. Server-render the data object

If you already render HTML server-side (Go `html/template`, Rails ERB, Django,
Laravel…), emit the data object inline:

```html
<script>window.FLEET = {{ your_json_here }};</script>
<script src="assets/js/fleet.js"></script>
```

This is exactly how the original backend behind this design works — it renders
the same structures server-side and lets the JS handle interactions.

## The data contract

`window.FLEET` — see `mock.js` for a full example. Shapes:

### `apps[]`
| field | type | notes |
|---|---|---|
| `name` | string | machine name (slug) |
| `category` | `"main"`\|`"utility"`\|`"worker"` | grouping |
| `framework`,`db`,`runtime` | string | badge labels (`""` to hide) |
| `redis` | bool | shows a Redis badge |
| `state` | `"running"`\|`"exited"`\|… | non-running ⇒ "down" |
| `url` | string | public URL (`""` for workers) |
| `repo` | string | code-host link (`""` to hide) |
| `http` | string | probe result: `"200"`, `"502"`, `""` |
| `cpu` | number | percent |
| `mem`,`cap` | number | MiB used / cap |
| `up` | string | uptime label |
| `restarts` | number | shown in the drawer |
| `health` | string | e.g. `"healthy"` |
| `env` | string[] | env **key names** (never values) |
| `worker` | bool | renders under Workers, no route |
| `featured` | bool | pins the app to the Featured strip (max 2; first two apps if none flagged) |

### `server`, `system`, `edge`, `incidents`
See `mock.js`. `incidents[]` items: `{ app, label, kind, detail, since, ago, open }`
— `open:true` drives the active-incident banner and marks the row live on the
**incidents page**. A `read` flag (set by **Mark all read**) dims acknowledged
rows without deleting them; `app` links an incident to its card's detail drawer.
The share buttons on the incidents page compose a one-line summary from this list.

### Settings (`localStorage`)
The settings page persists to `localStorage`: `fleet-theme` / `fleet-view`, and a
`fleet-settings` JSON `{ mask, recipient, emailSubject, emailBody, tech:{} }`.
`fleet.js` reads it to mask IP / SSH / host / tunnel ids on the cards and to apply
tech-stack label overrides. Swap this for your own backend settings if you like.

## Wiring the actions

In the demo, Start/Stop/Remove mutate the local model. Point them at your API:

- In `fleet.js`, the `[data-act]` click handler is the single place per-app
  actions run — replace the model mutation with `fetch("/apps/"+name+"/start", {method:"POST"})` then re-fetch + `renderAll()`.
- The **Test alert** submit (`[data-testalert]`) → POST your `/test-alert`.
- Bulk **Start all / Stop all** (`[data-allact]`) → your bulk endpoints.

## Live updates

The demo jitters numbers every 4s. For real data, either:
- poll your JSON endpoint on an interval and `renderAll()`, or
- open an **SSE / WebSocket** stream and call `renderAll()` on each message.

Keep the 4s-ish cadence; the UI is built to re-render cheaply.

## Reachability & incidents (backend side)

The "live · 200 / 502" chips and incidents come from **your** backend probing
each app's URL and diffing health between polls. The original implementation
does an HTTP GET per app (cached briefly) and records an incident on a
healthy→down / down→healthy transition — mirror that server-side and feed the
results into `apps[].http` and `incidents[]`.
