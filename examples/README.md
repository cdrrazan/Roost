# roost examples

Each file here is a complete, valid `~/.roost/config.yml` you can copy and
adapt. Try any of them without touching your real config:

```bash
roost --config examples/minimal.yml list
roost --config examples/demo/config.yml detect
```

| File | Shows |
|---|---|
| [`minimal.yml`](minimal.yml) | the smallest useful config: a domain and a list of paths |
| [`explicit.yml`](explicit.yml) | explicit per-app hostnames — what `roost init` writes |
| [`multi-domain.yml`](multi-domain.yml) | one config spanning three domains plus a bare apex |
| [`full.yml`](full.yml) | every knob: overrides, profiles, Access, defaults, env, build_env, seed, `control_host` |
| [`includes/config.yml`](includes/config.yml) | a main file that pulls apps from `apps/*.yml`, one file per feature |
| [`demo/config.yml`](demo/config.yml) | a fully-populated fake setup for a fictional developer |

## Common recipes

**"I just want my side projects online"** — `minimal.yml`. Bare paths use the
global domain: `~/projects/blog` becomes `https://blog.example.com`.

**"One app needs its own domain"** — give that app an explicit `domain:`; the
value is used verbatim and can live in any zone of your Cloudflare account
(`explicit.yml`, `multi-domain.yml`).

**"Some apps are heavy, start them on demand"** — put them in a profile
(`full.yml`), then `roost up` starts only the always-on set and
`roost up --profile extras` brings in the rest.

**"My apps are private"** — set `tunnel.access.emails`. Every routing suffix
gets a Cloudflare Access wall before first exposure (`full.yml`).

**"Run the fleet from a browser"** — set the top-level `control_host:`
(`full.yml`) and roost routes that hostname to the `roost web` control panel.
Front it with Cloudflare Access — the panel starts, stops, adds, and removes
apps, so an unprotected URL is infra control for anyone who reaches it.

**"One config.yml is getting unwieldy"** — split the apps into files and
`include: apps/*.yml` them from the main config (`includes/config.yml`). Each
included file holds only an `apps:` list; the domain, tunnel, and defaults
stay in the main file.

**"Set up the database and seed demo data automatically"** — add `seed:` to an
app (`full.yml`, `demo/config.yml`). Any database-backed app is migrated on
every `up`; `seed: true` also runs the framework's default seed (Rails
`db:seed`), or `seed: "<command>"` runs yours — once per app, `roost up
--reseed` to repeat. Pair it with a `~/.roost/seed.env` (a `0600` `KEY=VALUE`
file, injected into every app container) to seed the same super-admin login
across all of them — see [`docs/configuration.md`](../docs/configuration.md).

**"Bring the stack up clean, without demo data"** — `roost up --no-seed`
migrates every DB app but skips all seeding for that run (it's mutually
exclusive with `--reseed`). Even on a recreated data volume — which normally
re-seeds everything — `--no-seed` leaves the databases empty. Keep schema
creation in `migrate:` (not `seed:`, see the Next.js app in `full.yml`) so the
tables still exist under `--no-seed`; then register your own accounts.
