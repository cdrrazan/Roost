# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What roost is

A Go CLI that turns a list of local application folders into running, HTTPS-accessible apps on the user's own domain. From `~/.roost/config.yml` (paths + hostnames) it infers framework, port, database, start command, and memory cap, then generates a Docker Compose stack, a Caddy reverse proxy, and a Cloudflare Tunnel, managing all DNS via the Cloudflare API. It **never writes into the user's app repos** — every generated artifact lands under `~/.roost/build/`.

## Commands

```bash
go test ./...                 # whole suite, fast, no Docker or network
go test -race ./...           # what CI runs
go test ./internal/detect/    # one package
go test -run TestPlan ./internal/tunnel/   # one test
gofmt -l .                    # must be empty (CI fails otherwise)
go vet ./...
golangci-lint run ./...       # v2 config in .golangci.yml; zero issues expected
go build ./cmd/roost && ./roost --config examples/demo/config.yml list   # smoke test against demo config
```

Release binaries are built by GoReleaser (`.goreleaser.yaml`) on tag push; version is stamped via `-ldflags "-X main.version=..."`.

## Non-negotiable house rules (from CONTRIBUTING.md)

- **TDD.** Failing test first — implementation commits must not precede their test commits.
- **No test may touch real Docker or the network.** Shell-outs go through the `shell.Runner` interface (use `shell.Fake`); Cloudflare API code is tested against `httptest.Server`.
- **Exactly two dependencies:** `cobra` and `yaml.v3`. Everything else is stdlib. Do not add viper/testify/resty/Docker-SDK.
- **Never write into the user's app directories** — artifacts go under `~/.roost/build/`.
- **Errors are actionable**, never a bare stack trace (`app "foo": path does not exist: /x/foo`). Every doctor finding carries a remedy.
- Start commands must bind `0.0.0.0`, never loopback — a loopback bind is a silent 502 from Caddy with healthy-looking app logs.

## Architecture

Commands live in `cmd/roost/` (cobra tree assembled in `root.go`); business logic is in `internal/` packages that take injected dependencies so nothing but `internal/shell` calls `os/exec`.

The core pipeline (see `roost up` in `cmd/roost/run.go`):

```
config.yml → detect (framework/port/db) → generate (~/.roost/build/*) → runner (docker compose -p roost)
```

- **`internal/config`** — loads/validates `config.yml`, resolves app paths to absolute dirs, resolves each app to exactly one FQDN. `FindConfig` resolution order: `--config` flag → `$ROOST_CONFIG` → `./roost.yml` → `~/.roost/config.yml` (first hit wins). The optional top-level `include:` key (glob or list) pulls `apps:` from other files — each included file carries only `apps:` (other keys rejected), its paths resolve against its own dir, and its apps append after the main file's in pattern order. `internal/config/edit.go` mutates the app list while preserving comments (`roost add`/`remove`).
- **`internal/detect`** — infers framework/port/start/db/runtime from folder signals. Explainable: every `Detection` names the signal that triggered it; an unrecognizable folder is an explicit error, never a silent guess. Rules are priority-ordered; fixtures live in `testdata/<framework>-app/`.
- **`internal/generate`** — `Plan()` turns config + resolved apps into `[]App`; `Generate()` renders `compose.yml`, per-app Dockerfiles, the `Caddyfile`, and DB init scripts from `templates/*.tmpl` (embedded via `embed`). Per-app `env:` is runtime (compose `environment:`); `build_env:` is build-time (`ENV` in the Dockerfile builder stage, injected into all four generated templates) for frameworks that validate env during their build (e.g. Next.js `@t3-oss/env` needing `SKIP_ENV_VALIDATION`). Per-app `migrate:` (`MigrateSpec`, bool-or-string) controls the setup step: absent/`true` → framework `db:prepare`/`migrate` (`App.SetupCommand`); `false` → skip it (for images whose entrypoint self-migrates — roost running a second concurrent `db:prepare` races them and a Rails multi-db app dies with "No database selected"); a string overrides the command.
- **`internal/runner`** — orchestrates `docker compose` for the generated stack via a `shell.Runner`. **There is no roost daemon**; Docker's restart policy is the supervisor. Handles up (staggered starts) / down / status / logs / start / stop / restart (per-app), and profile selection (`AppSelected`). After (re)creating app containers it reloads Caddy (`reloadProxy`) so the proxy never serves stale upstreams. `Prepare()` runs each DB app's idempotent setup command (`generate.App.SetupCommand`, e.g. Rails `db:prepare`) on every up, then — for apps with a `SeedCommand`, via `sh -lc` with `SEED_DEMO=1` — seeds once, gated by an injected `shouldSeed`/`onSeeded` pair the up command backs with `state.Seeded` (so `roost up` seeds each app once; `--reseed` forces). A failed seed exec is never marked seeded. `MysqlVolumeID()` (`docker volume inspect roost_roost-mysql-data`) lets the up command detect a recreated data volume and reset the seeded set.
- **`internal/tunnel`** — Cloudflare API client (`client.go`), tunnel ensure/adopt logic (`ensure.go`), and DNS record planning (`plan.go`). One wildcard DNS record per routing suffix + host-header routing inside, which is why adding an app is a purely local change (no per-app DNS call). Refuses to overwrite DNS or adopt tunnels it didn't create without `--force`/`--adopt`.
- **`internal/state`** — persists roost's remote-side ownership in `~/.roost/state.json` (tunnel ID, account, created DNS records) so down/uninstall clean up only what roost made. Also tracks `Seeded` (apps already seeded) via `HasSeeded`/`MarkSeeded` so `roost up` seeds each app once, plus `MysqlVolumeID` — `SyncMysqlVolume(id)` clears `Seeded` when the data volume's identity changes (Clean/Purge, `volume rm`) so a wiped DB is re-seeded instead of skipped.
- **`internal/doctor`** — preflight checks (Docker running, token scopes, SSL depth, DNS shadowing); every `Finding` has a severity and a specific remedy. The multi-level-subdomain SSL trap check matters: free Universal SSL covers one subdomain level only.
- **`internal/lifecycle`** — boot-on-login unit: launchd on macOS, systemd `--user` on Linux. Both just run `roost up` at login.
- **`internal/shell`** — the only package permitted to call `os/exec`. `Runner` interface with `Exec` (real) and `Fake` (records calls, answers via hooks) implementations.

## Runtime layout on disk (`~/.roost/`)

`config.yml` (only file the user edits) · `credentials` (CF API token, mode 0600 enforced — also read from `$CLOUDFLARE_API_TOKEN`, never in config.yml) · `seed.env` (optional shared demo creds, `KEY=VALUE`, 0600; `generate.loadSeedEnv` reads it next to `build/` and `appEnv` injects the pairs into every non-static app's compose `environment:`, below per-app `env:` so an app override wins) · `state.json` · `build/` (all generated artifacts) · `logs/`.

## Adding a framework

Touch, in order: `internal/detect/testdata/<framework>-app/` (fixture) → `internal/detect/detect_test.go` (expected `Detection`, first) → `internal/detect/detect.go` (rule, correct priority slot) → `internal/generate/templates/` (Dockerfile template if none fits) → the tables in `README.md` and `site/index.html`.
