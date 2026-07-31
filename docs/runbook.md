# roost runbook — developer & ops notes

Copy-paste commands, grouped by task. `<app>` = app name, `<path>` = host dir,
`<url>` = git URL, `<host>` = FQDN. Superuser for Postgres is **`roost`**.

- [Git workflow](#git-workflow)
- [Sync the roost binary (local + box)](#sync-the-roost-binary-local--box)
- [Add an app](#add-an-app)
- [Update an app from GitHub](#update-an-app-from-github)
- [Forked app: own Dockerfile + Postgres](#forked-app-own-dockerfile--postgres)
- [Stateful app with persistent files — paperless-ngx](#stateful-app-with-persistent-files--paperless-ngx)
- [Common ops](#common-ops)
- [Two environments (laptop dev + box prod)](#two-environments-laptop-dev--box-prod)

---

## Git workflow

```bash
git switch -c feat/x develop          # never commit to main
# ...edit; TDD: failing test first...
go test ./... && gofmt -l . && go vet ./...   # must be clean
git commit -m "feat(x): ..."          # conventional commits
git push -u origin feat/x             # PR -> merge to develop; then PR develop -> main
```

Keep a fork current with upstream:

```bash
git remote add upstream <upstream-url>   # once
git fetch upstream
git switch main && git merge --ff-only upstream/main
git push origin main
```

## Sync the roost binary (local + box)

```bash
# --- local ---
git pull --ff-only
go install ./cmd/roost                # -> ~/go/bin/roost
# or system-wide:
go build -o roost ./cmd/roost && sudo install -m 0755 roost /usr/local/bin/roost
# macOS: relaunch `roost web` to pick it up

# --- box (automatic) ---
# merging to `main` triggers .github/workflows/deploy-web.yml:
#   build for box arch -> scp -> install -> restart roost-web
# nothing to run by hand once DEPLOY_SSH_KEY / DEPLOY_HOST / DEPLOY_USER secrets are set.
```

## Add an app

```bash
# detected framework (rails|next|django|flask|laravel|node|static)
roost add <path> --domain <host>
roost up

# clone a GitHub repo — roost owns the checkout under ~/.roost/sources/<name>
roost add --repo <url> --name <app> --domain <host>
roost up

roost list          # resolved apps + URLs
roost detect        # framework + the signal that triggered it
```

Panel: **Add app** form takes a GitHub URL *or* a host path (not both), gated by
`roost doctor`.

## Update an app from GitHub

```bash
roost deploy <app>          # git pull --ff-only + rebuild + restart just <app>
# panel: app menu -> "Pull & redeploy"  == same thing

# manually-cloned fork (own Dockerfile, NOT added with --repo):
git -C <path> pull --ff-only
cd ~/.roost/build && docker compose -p roost up -d --build <app>
docker exec roost-caddy-1 caddy reload --config /etc/caddy/Caddyfile
```

## Forked app: own Dockerfile + Postgres

The `memos` / `joplin` pattern — a stack roost doesn't detect and/or its own build.

```bash
# 1. source on host (shallow clone is fine on a box)
git clone --depth 1 <url> <path>

# 2. root Dockerfile — roost only detects a file literally named "Dockerfile"
cp <path>/Dockerfile.server <path>/Dockerfile     # if the real build file is elsewhere
#   build must be self-contained (whole app in-image). If a repo .dockerignore
#   excludes a package you need, add <path>/Dockerfile.<name>.dockerignore.

# 3. app entry -> ~/.roost/apps/<app>.yml
#      framework: node        # override skips detection; root Dockerfile builds it
#      port: <p>              # app's listen port (must bind 0.0.0.0)
#      database: postgres
#      migrate: false         # app self-migrates on boot
#      env:  <app's OWN db vars pointing at roost Postgres, e.g. POSTGRES_* / *_DSN>
roost generate

# 4. Postgres role: auto-created ONLY on a fresh volume.
#    Existing volume (any prior app) => create by hand with roost's exact line:
grep -A1 '<app>' ~/.roost/build/postgres-init.sql
docker exec roost-postgres-1 psql -U roost -c "CREATE ROLE <app> LOGIN CREATEDB PASSWORD 'rp_<derived>';"
docker exec roost-postgres-1 psql -U roost -c 'CREATE DATABASE "<app>" OWNER <app>;'
#    password is deterministic: rp_ + sha256("roost-pg:<app>")[:24]
#    -> copy it from postgres-init.sql so it matches DATABASE_URL + your env:

# 5. build + start + route
cd ~/.roost/build && docker compose -p roost up -d --build <app>
docker exec roost-caddy-1 caddy reload --config /etc/caddy/Caddyfile
```

Static front-end SPA (server URL set in-app, not baked): serve its `dist/` as a
`framework: static` app at its own host — no port, no db. If it calls the backend
cross-origin, the backend must send CORS for the SPA's origin.

## Stateful app with persistent files — paperless-ngx

paperless-ngx keeps documents, the search index and thumbnails **on disk**, so
it needs `volumes:` — without them a rebuild wipes the library. It also wants
Redis + Postgres, both of which roost already provides. Uses the prebuilt image
via a one-line wrapper Dockerfile (no source compile).

```bash
# 1. fork -> clone into a source dir roost owns (or any path)
git clone --depth 1 https://github.com/<you>/paperless-ngx ~/.roost/sources/paperless

# 2. root Dockerfile = thin wrapper over the official image (fast, no build)
printf 'FROM ghcr.io/paperless-ngx/paperless-ngx:latest\n' > ~/.roost/sources/paperless/Dockerfile

# 3. app entry -> ~/.roost/apps/paperless.yml   (box: docs.byaru.com; local: docs-local.byaru.com)
cat > ~/.roost/apps/paperless.yml <<'YAML'
apps:
  - path: ~/.roost/sources/paperless
    name: paperless
    domain: docs.byaru.com
    category: utility
    framework: django       # override: skips detection; wrapper image builds it
    port: 8000
    database: postgres
    redis: true
    migrate: false          # paperless migrates itself on boot
    memory: 1g
    volumes:
      - data:/usr/src/paperless/data      # app data + search index (named vol)
      - media:/usr/src/paperless/media    # the document library (named vol)
      - /srv/paperless/consume:/usr/src/paperless/consume   # drop files here to import
    env:
      PAPERLESS_URL: https://docs.byaru.com     # also sets CSRF trusted origin
      PAPERLESS_PORT: "8000"
      PAPERLESS_REDIS: redis://redis:6379
      PAPERLESS_DBHOST: postgres
      PAPERLESS_DBNAME: paperless
      PAPERLESS_DBUSER: paperless
      PAPERLESS_DBPASS: rp_bc708bb73c00939d707bfcf4   # = roost's role pw for "paperless"
      PAPERLESS_OCR_LANGUAGE: eng
      PAPERLESS_TIME_ZONE: Asia/Kathmandu
      PAPERLESS_SECRET_KEY: change-me-long-random     # keep this stable
YAML
roost generate

# 4. Postgres role — auto-created only on a fresh volume; on an existing box
#    volume create it by hand (pw MUST match PAPERLESS_DBPASS above):
docker exec roost-postgres-1 psql -U roost -c "CREATE ROLE paperless LOGIN CREATEDB PASSWORD 'rp_bc708bb73c00939d707bfcf4';"
docker exec roost-postgres-1 psql -U roost -c 'CREATE DATABASE "paperless" OWNER paperless;'
#    (verify roost's derived pw: grep -A1 paperless ~/.roost/build/postgres-init.sql)

# 5. build + start + route
cd ~/.roost/build && docker compose -p roost up -d --build paperless
docker exec roost-caddy-1 caddy reload --config /etc/caddy/Caddyfile

# 6. create the first admin (paperless has no signup)
docker exec -it roost-paperless-1 python manage.py createsuperuser
```

`PAPERLESS_DBPASS` above is roost's deterministic role password for the app name
`paperless` (`rp_` + `sha256("roost-pg:paperless")[:24]`). Rename the app → the
password changes; recompute it from `postgres-init.sql`. Immich is **not** run
under roost (it needs a special pgvecto.rs Postgres + its own ML container) —
run it as its own compose stack and only reverse-proxy it.

## Common ops

```bash
# stack
roost up ; roost down ; roost status ; roost logs [<app>] -f
roost start <app> ; roost stop <app> ; roost restart <app>

# rebuild ONE app's image (env/Dockerfile change)
cd ~/.roost/build && docker compose -p roost up -d --build <app>

# recreate ONE app WITHOUT rebuild (env-only change)
cd ~/.roost/build && docker compose -p roost up -d <app>

# caddy reload after a route change
docker exec roost-caddy-1 caddy reload --config /etc/caddy/Caddyfile

# Postgres (superuser = roost)
docker exec roost-postgres-1 psql -U roost -tc "SELECT rolname FROM pg_roles;"
docker exec roost-postgres-1 psql -U roost -d <app> -c '\dt'

# panel: a category: change only shows after a restart (categories read at startup)
systemctl --user restart roost-web            # Linux box

# DNS / tunnel for the standard (wildcard) case
roost tunnel setup                            # tunnel + all DNS records via API

# disk (box)
df -h / ; docker system df ; docker builder prune -f
```

## Two environments (laptop dev + box prod)

Run both at once — **separate tunnel + hostnames per machine**, never two
connectors on one tunnel.

```text
box  ~/.roost/config.yml : tunnel.name rserver        apps -> app.example.com
mac  ~/.roost/config.yml : tunnel.name rserver-local  apps -> app-local.example.com
```

```bash
ssh -i ~/.ssh/oracle-roost ubuntu@<box-ip>    # reach the box
```

Rule: **one cloudflared per tunnel**. Each env has isolated Docker volumes (its
own Postgres/MySQL) — data does not cross; use an app's own sync if you need it.
See [README → Where to run it](../README.md#-where-to-run-it--laptop-server-or-both).
