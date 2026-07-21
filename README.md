<div align="center">

<img src="assets/banner.svg" alt="roost — every app on your laptop, live on your own domain" width="820">

<br><br>

[![CI](https://github.com/cdrrazan/roost/actions/workflows/ci.yml/badge.svg)](https://github.com/cdrrazan/roost/actions/workflows/ci.yml)
[![Go 1.24+](https://img.shields.io/badge/Go-1.24%2B-00ADD8?logo=go&logoColor=white)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/cdrrazan/roost?include_prereleases)](https://github.com/cdrrazan/roost/releases)
[![Dependencies: 2](https://img.shields.io/badge/deps-cobra%20%2B%20yaml.v3-blue)](go.mod)

**[Website](https://roost.pages.dev) · [Examples](examples/) · [Config reference](docs/configuration.md) · [Roadmap](ROADMAP.md) · [Contributing](CONTRIBUTING.md)**

</div>

---

**roost turns a list of local app folders into running, HTTPS apps on your own domain.**
You tell it *where each app lives* and *what hostname it answers on* — it infers
everything else: framework, port, database, start command, memory cap. Under the
hood it generates Docker Compose, a Caddy reverse proxy, and a Cloudflare Tunnel,
and manages all the DNS via API.

> You never write a Dockerfile, a Compose file, or a tunnel config — and roost
> **never adds a single file to any of your repos**. Every artifact lands under
> `~/.roost/build/`.

```yaml
# ~/.roost/config.yml — this is the whole thing
domain: demo.example.com
apps:
  - ~/projects/blog              # → https://blog.demo.example.com
  - path: ~/work/some-rails-app
    domain: crm.example.com      # explicit hostname, any zone in your account
```

```console
$ roost up
up: blog → https://blog.demo.example.com
up: crm  → https://crm.example.com
```

<div align="center">
<img src="assets/demo.svg" alt="Terminal: roost up brings six apps online over HTTPS; roost add puts a seventh live with no DNS change" width="760">
</div>

<div align="center">
<sub>From two-liners to every-knob configs — plus a <a href="examples/demo/config.yml">fully-populated demo</a> — in <a href="examples/"><code>examples/</code></a>.</sub>
</div>

---

## 🧭 How it works

One command runs a four-stage pipeline; everything downstream is generated.

```mermaid
flowchart LR
    CFG["📝 config.yml<br/><sub>paths + hostnames</sub>"]
    DET["🔍 detect<br/><sub>framework · port · db</sub>"]
    GEN["🏗️ generate<br/><sub>~/.roost/build/*</sub>"]
    RUN["🚀 runner<br/><sub>docker compose -p roost</sub>"]
    CFG --> DET --> GEN --> RUN
```

The generated stack: **one tunnel, one Caddy, one shared database**, and one
container per app — none of them publishing a host port.

```mermaid
flowchart LR
    USER(("🌍 visitor<br/><sub>https://blog.example.com</sub>"))
    EDGE["☁️ Cloudflare edge<br/><sub>TLS · wildcard DNS · Access</sub>"]
    subgraph laptop["💻 your laptop — docker compose -p roost"]
        direction LR
        CFD["cloudflared"]
        CADDY["Caddy<br/><sub>routes by Host header</sub>"]
        A1["blog<br/><sub>rails :3000</sub>"]
        A2["shop<br/><sub>next :3000</sub>"]
        A3["api<br/><sub>django :8000</sub>"]
        DB[("shared MySQL / Postgres<br/><sub>a database per app · no published ports</sub>")]
        CFD --> CADDY
        CADDY --> A1 & A2 & A3
        A1 & A3 --> DB
    end
    USER --> EDGE <--> CFD
```

**One wildcard DNS record per routing suffix**, host-header routing inside. That's
why adding app number seven is a *purely local* change — no DNS call, no dashboard
visit, no new certificate:

```bash
roost add ~/projects/app7 --domain app7.example.com && roost up
```

<details>
<summary><b>The request path, end to end</b></summary>

```mermaid
sequenceDiagram
    participant V as 🌍 Visitor
    participant CF as ☁️ Cloudflare edge
    participant T as cloudflared
    participant C as Caddy
    participant A as your app
    V->>CF: GET https://blog.example.com
    Note over CF: terminates TLS,<br/>matches *.example.com
    CF->>T: forward over the tunnel
    T->>C: http://caddy:80 (Host: blog.example.com)
    Note over C: routes by Host header
    C->>A: http://blog:3000
    A-->>V: 200 (back up the same path)
```
</details>

---

## ✍️ One config, every knob

A bare path is enough; a map opens up per-app overrides. Both forms mix freely.

```yaml
domain: demo.example.com          # fallback suffix for bare-path apps
tunnel:
  name: rserver                   # your tunnel's name (never generated)
  access:
    emails: [me@example.com]      # Cloudflare Access wall before first exposure
defaults:
  memory: 512m                    # per-app default

apps:
  - ~/projects/blog               # 👈 bare path — everything inferred

  - path: ~/work/crm              # 👈 map form — override anything
    domain: crm.example.com       #    verbatim FQDN, any zone in your account
    framework: rails              #    default: detected
    port: 3001                    #    default: framework default
    database: mysql               #    default: detected
    memory: 768m
    profile: extras               #    only starts with --profile extras
    seed: true                    #    migrate + seed on up (once, tracked)
    env:                          #    runtime environment (container)
      SECRET_KEY_BASE: "…"
    build_env:                    #    build-time environment (Docker builder)
      SKIP_ENV_VALIDATION: "1"
```

| Key | Purpose |
|---|---|
| `env:` | **runtime** environment — compose `environment:` |
| `build_env:` | **build-time** environment — `ENV` in the Docker builder stage, for frameworks that validate config during their build (e.g. a Next.js app using `@t3-oss/env` needing `SKIP_ENV_VALIDATION`, or `NPM_CONFIG_LEGACY_PEER_DEPS` for a stubborn install). Bakes into image layers — keep secrets in `env:`. |
| `seed:` | **DB setup on `up`.** Any database-backed app is migrated on every `up` (Rails `db:prepare`, Django `migrate`). `seed: true` also runs the framework's default seed command (Rails `db:seed`) — or `seed: "<command>"` runs yours — **once** per app, recorded in `state.json`; `roost up --reseed` re-runs. Seeds execute with `SEED_DEMO=1` so gated demo seeds fire. |

### 🧩 Split a big fleet across files — `include`

When one file grows unwieldy, move apps into their own files and pull them in.
Each included file carries **only** an `apps:` list; the domain, tunnel, and
defaults stay central.

```yaml
# ~/.roost/config.yml
domain: example.com
include: apps/*.yml               # a glob, or a list of globs
```

```yaml
# ~/.roost/apps/blog.yml
apps:
  - path: ~/projects/blog
    domain: blog.example.com
```

Paths in an included file resolve against *that file's* directory; a pattern
matching no files is an error, never a silent skip. Full schema, resolution
order, and hostname rules: **[configuration reference](docs/configuration.md)**.

### 🔑 One demo login everywhere — `seed.env`

Drop a `~/.roost/seed.env` (mode `0600`, kept out of `config.yml` like every
other secret) and roost injects its `KEY=VALUE` pairs into **every app
container's** environment. Point each app's seed script at those variables and a
single super-admin logs into all of them:

```bash
# ~/.roost/seed.env
SEED_ADMIN_EMAIL=me@example.com
SEED_ADMIN_PASSWORD=one-strong-shared-secret
```

```ruby
# db/seeds.rb, in each app — env-driven, with a standalone fallback
admin_email    = ENV.fetch("SEED_ADMIN_EMAIL", "demo@example.com")
admin_password = ENV.fetch("SEED_ADMIN_PASSWORD", "password123")
```

Blank lines and `#`-comments are ignored; a missing file injects nothing (the
feature is opt-in). A per-app `env:` value with the same key still wins, so any
app can opt out of the shared credential.

---

## 🔍 What gets inferred from a bare path

```mermaid
flowchart TD
    P["📁 a folder"] --> S{signal?}
    S -->|"Gemfile + config/application.rb"| R["rails · :3000"]
    S -->|"Gemfile + config.ru + sinatra"| SI["sinatra · :4567"]
    S -->|"package.json · next"| N["next · :3000"]
    S -->|"package.json · vite"| V["static · :80 (built)"]
    S -->|"package.json · express"| NO["node · :3000"]
    S -->|"manage.py + requirements"| D["django · :8000"]
    S -->|"index.html, no manifest"| ST["static · :80"]
    S -->|"nothing recognized"| E["❌ error: set framework:"]
```

| Signal in the folder | Framework | Port | Start |
|---|---|---|---|
| `Gemfile` + `config/application.rb` | rails | 3000 | puma, bound to `0.0.0.0` |
| `Gemfile` + `config.ru` + sinatra | sinatra | 4567 | rackup |
| `package.json` with `next` | next | 3000 | `npm run start` |
| `package.json` with `vite` | static | 80 | built, served by Caddy |
| `package.json` with `express` | node | 3000 | `npm run start` |
| `manage.py` + requirements/pyproject | django | 8000 | gunicorn |
| `index.html`, no manifest | static | 80 | served by Caddy |

Also inferred: **runtime version** (`.ruby-version`, `engines`, …), **database
need** (from `database.yml`, `DATABASE_URL`, gems), and **memory caps**.
Detection is *explainable* — `roost detect` names the signal that triggered it —
and never guesses silently: an unrecognizable folder is an error telling you to
set `framework:` yourself. Every inferred value is overridable per app.

<details>
<summary><b>Baked-in gotcha handling</b> — the traps roost defuses for you</summary>

- **`RAILS_ASSUME_SSL`** — Cloudflare terminates TLS, so a `force_ssl` app would
  otherwise redirect forever.
- **`0.0.0.0` binds** — a loopback bind gives Caddy a silent 502 while the app
  logs look healthy. Start commands always bind all interfaces.
- **No published ports** — for apps *or* databases; Caddy reaches them on the
  internal network only.
- **`WEB_CONCURRENCY=1`** — single-user local Rails workloads don't need a worker
  pool.
- **Staggered starts** — six apps don't spike your CPU at once on first build.
- **Multi-database Rails** — a per-app database user so apps that connect as their
  own username and use Solid Cache/Queue/Cable (sibling `<app>_*` databases) just
  work.
- **Compiled vs interpreted** — Rails/Django/Sinatra source is bind-mounted so a
  `restart` picks up edits; next/node/static build into the image (mounting the
  host source would shadow the build).
- **Self-healing proxy** — after (re)starting containers, Caddy is reloaded and
  `cloudflared` refreshed on new routes, so the proxy never serves stale
  upstreams or 404s a just-added zone.
- **DB ready on `up`** — database-backed apps are migrated every `up`, and
  `seed:` apps are seeded once (tracked in `state.json`, `--reseed` to repeat),
  so a fresh box comes up with a working, populated database — no manual
  `exec … db:prepare` afterwards.
- **The SSL-depth trap** — free Universal SSL covers **one** subdomain level;
  `app.demo.example.com` needs ACM or a flatter name. `roost doctor` flags it.
</details>

---

## 🌙 The honest part: this is not hosting

Your apps **roost** while the laptop is open and leave when it closes. Lid shut,
machine asleep, on a plane — your apps are down. roost is a **local-first preview
and personal-hosting** tool for demos, side projects, and sharing work in
progress. It is not a replacement for a server; when the laptop wakes,
`cloudflared` reconnects within ~5–10 seconds and everything is live again.

---

## ⚡ 60-second quickstart

**Prerequisites** (one-time): your domain is on Cloudflare with nameservers
pointed there, and Docker is installed. roost automates everything else.

```bash
brew install cdrrazan/tap/roost   # or: curl -fsSL https://raw.githubusercontent.com/cdrrazan/roost/main/install.sh | sh
```

```mermaid
flowchart LR
    I["roost init<br/><sub>pick domain,<br/>scan for apps</sub>"] --> AU["roost auth login<br/><sub>paste API token</sub>"]
    AU --> DO["roost doctor<br/><sub>preflight checks</sub>"]
    DO --> TS["roost tunnel setup<br/><sub>tunnel + DNS</sub>"]
    TS --> UP["roost up<br/><sub>build · start · route</sub>"]
    UP --> EN["roost enable<br/><sub>start at login</sub>"]
```

```bash
roost init          # picks your domain from your live zone list, scans a folder for apps
roost auth login    # paste an API token (init links the exact page + scopes)
roost doctor        # Docker running? token scopes? SSL depth? DNS shadowing?
roost tunnel setup  # creates the tunnel + every DNS record via API
roost up            # generate, build, start, route
roost enable        # start everything at login
```

<details>
<summary><b>Installing from source</b></summary>

A standard Go build — Go 1.24+, no build scripts, two dependencies fetched
automatically. No Go? `brew install go` (macOS), `sudo dnf install golang`
(Fedora), or the tarball from [go.dev/dl](https://go.dev/dl/) — then put
`~/go/bin` on your `PATH`.

```bash
git clone https://github.com/cdrrazan/roost && cd roost
go install ./cmd/roost            # installs to ~/go/bin

# or build a binary and place it yourself:
go build -o roost ./cmd/roost && sudo install -m 0755 roost /usr/local/bin/roost

# optional: stamp the version instead of "dev"
go build -ldflags "-X main.version=$(git describe --tags --always)" -o roost ./cmd/roost
```

Once published: `go install github.com/cdrrazan/roost/cmd/roost@latest`.
`cloudflared` is **not** needed on the host — roost runs it as a container.
`go test ./...` is a fast, network-free sanity check before installing.
</details>

---

## 🧰 Commands

**Setup**
| Command | What it does |
|---|---|
| `roost init` | interactive setup; writes `~/.roost/config.yml` with explicit hostnames |
| `roost auth login` | store the API token (`~/.roost/credentials`, `0600`) |
| `roost doctor` | preflight: every failure comes with a specific fix |
| `roost tunnel setup [--adopt] [--force]` | tunnel + all DNS records via API |
| `roost tunnel access` | Cloudflare Access policy across every suffix |

**Everyday**
| Command | What it does |
|---|---|
| `roost up [--profile p] [--reseed]` / `down` | start (staggered), migrate + seed DB apps / stop the whole stack |
| `roost start <app>` / `stop <app>` / `restart <app>` | act on a single app's container |
| `roost status` / `logs <app> [-f]` | state, health, memory, URLs / container logs |
| `roost add <path>` / `remove <name>` | edit the app list (comments preserved) |
| `roost list` / `detect` | resolved apps + URLs / framework detection with its signal |
| `roost generate` | write `~/.roost/build/*` without starting anything |
| `roost enable` / `disable` | boot-on-login via launchd / systemd `--user` |

---

## 🤝 What roost manages vs what you do

| 🧑 You, once ever | 🤖 roost, every time |
|---|---|
| Point your domain's nameservers at Cloudflare | Creates the tunnel and writes its ingress |
| Create one API token — `roost init` links the exact page + scopes | Creates **every DNS record** via API |
| | Applies Access policies across every suffix |
| | Runs `cloudflared` as a container |

There is no "now add this CNAME in your dashboard" step — that would contradict
roost holding `Zone:DNS:Edit`.

---

## ⚖️ How it compares

| | **roost** | DockFlare | TunnelDock / companion | Coolify |
|---|---|---|---|---|
| **Input** | ✅ a list of source folders | 🏷️ containers + labels | 🏷️ containers + labels | 🌐 git repos, web UI |
| **Dockerfiles / Compose** | ✅ **generated for you** | ❌ you write them | ❌ you write them | ⚠️ buildpacks / you |
| **DNS strategy** | ✅ one wildcard per suffix, zero calls per app | ⚠️ per-hostname records | ⚠️ per-hostname records | ⚠️ your server's DNS |
| **Adding an app** | ✅ local change only | ❌ edit labels, API calls | ❌ edit labels, API calls | ⚠️ UI / git |
| **Runs on** | 💻 your laptop | 🖥️ your Docker host | 🖥️ your Docker host | ☁️ a server you operate |

roost's distinction: it starts from **source paths, not containers**. The
label-driven tools automate tunnel routing for containers you already maintain;
roost generates the entire container layer from your code and treats the tunnel
as an implementation detail.

---

## 🔐 Security defaults

- **Access wall before exposure** — set `tunnel.access.emails` and every suffix
  gets a Cloudflare Access login gate *before* first exposure. Hostnames leak via
  Certificate Transparency logs within hours, so public tunnels get scanned fast.
  Without it, roost prints exactly which hostnames are public.
- **Nothing publishes ports** — apps and databases are reachable only on the
  internal Docker network.
- **Token stays out of config** — `~/.roost/credentials` (`0600` enforced) or
  `$CLOUDFLARE_API_TOKEN`, never in `config.yml`.
- **No silent overwrites** — roost refuses to overwrite DNS records it didn't
  create (`--force`) or adopt a tunnel it didn't create (`--adopt`).

Full threat model and reporting: **[SECURITY.md](SECURITY.md)**.

---

## 🗂️ Layout on disk

```
~/.roost/
├── config.yml        # the file you edit (may include: more app files)
├── apps/*.yml        # optional per-feature app files pulled in via include
├── credentials       # CF API token, 0600
├── seed.env          # optional shared demo creds, injected into every app, 0600
├── state.json        # tunnel ID, account, created DNS records
├── build/            # ALL generated artifacts (compose.yml, Caddyfile, dockerfiles/)
└── logs/
```

Your app repos are never touched. Uninstalling is `roost down && roost disable`
and deleting `~/.roost`.

---

## ❓ FAQ — lifecycle & your data

Where does app data live, and what's safe? Every database runs in the shared
MySQL/Postgres container, and its files live in a **named Docker volume**
(`roost-mysql-data`) — decoupled from the containers. Rebuilding images or
recreating containers never touches it; only deleting the volume does.

<details>
<summary><b>If I stop Docker / close the laptop, do my apps go down?</b></summary>

Yes. Docker Desktop off (or laptop asleep) stops the engine, so every container
— apps, Caddy, MySQL, `cloudflared` — stops and all hostnames go unreachable.
**Your data is safe** on disk. When Docker starts again the containers carry
`restart: unless-stopped`, so they come back automatically *if they were running
when it stopped*. For hands-off recovery, enable Docker Desktop's **Start on
login** (Settings → General) — roost has no daemon of its own; Docker's restart
policy is the supervisor.
</details>

<details>
<summary><b>Does <code>roost down</code> then <code>roost up</code> drop my databases?</b></summary>

No. `roost down` stops and removes the *containers*, not the named volume.
`roost up` recreates the containers and reattaches the same `roost-mysql-data`
volume — every database, user, and row is intact. No migrate, no re-seed needed.
The same is true of an image rebuild after you change app code.
</details>

<details>
<summary><b>Which Docker cleanups are safe, and which wipe my data?</b></summary>

| Action | Databases |
|---|---|
| `roost down` / `roost up` / image rebuild | ✅ kept |
| `docker system prune -f` (containers + dangling images) | ✅ kept — named volumes are left alone |
| `docker compose down -v` | ❌ dropped |
| `docker volume rm roost-mysql-data` | ❌ dropped |
| `docker system prune --volumes` / `--all --volumes` | ❌ dropped |
| Docker Desktop → Troubleshoot → **Clean / Purge data** | ❌ **everything** gone |

To reclaim space without losing data, use `docker system prune -f` — it keeps
named volumes.
</details>

<details>
<summary><b>What if I hit Docker Desktop's "Clean / Purge data"?</b></summary>

That's the nuclear option: it wipes the entire Docker VM — **all containers,
images, and named volumes**, including `roost-mysql-data`. Every database and
all demo data is gone, and every image is deleted. The next `roost up` rebuilds
all images from scratch, `mysql-init.sql` recreates empty databases and users,
and Rails apps need a fresh `db:migrate` + re-seed. Back the volume up first if
you want it reversible:

```bash
docker run --rm -v roost-mysql-data:/data -v "$PWD":/backup alpine \
  tar czf /backup/roost-mysql-data.tar.gz -C /data .
```
</details>

---

## 🌱 Project

- **[Examples](examples/)** — runnable configs from minimal to every-knob, plus a
  [demo with fake data](examples/demo/config.yml) and an
  [`include` walkthrough](examples/includes/).
- **[Website](https://roost.pages.dev)** — one-page overview ([source](site/)).
- **[Roadmap](ROADMAP.md)** — what's next, and the non-goals that keep roost small.
- **[Contributing](CONTRIBUTING.md)** — house rules (TDD, no real Docker/network in
  tests, two dependencies) and how to add a framework.
- **[Security policy](SECURITY.md)** · **[Code of Conduct](CODE_OF_CONDUCT.md)**
- **Support** — via [GitHub Sponsors](https://github.com/sponsors/cdrrazan).

Built with **Go 1.24+**, exactly **two dependencies** (`cobra`, `yaml.v3`).
`go test ./...` runs the whole suite — no test touches Docker or the network
(shell calls go through a fake; the Cloudflare API is `httptest`). TDD is the
house rule: failing test first.

## 📄 License

[MIT](LICENSE) © [Rajan Bhattarai](https://github.com/cdrrazan)
