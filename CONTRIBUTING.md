# Contributing to roost

Thanks for considering it! roost is small on purpose; contributions that keep
it small are the easiest to land.

## Ground rules (from the design, non-negotiable)

- **TDD.** Write the failing test first. PRs whose implementation commits
  precede their test commits will be asked to restructure.
- **No test may invoke real Docker or the network.** Shell-outs go through
  the `shell.Runner` interface (use `shell.Fake`); Cloudflare API code is
  tested against `httptest.Server`.
- **Two dependencies** (`cobra`, `yaml.v3`) — everything else is stdlib.
  PRs adding viper/testify/resty/Docker-SDK will be declined; it's not
  personal.
- **Never write into the user's app directories.** All artifacts belong under
  `~/.roost/build/`.
- **Errors are actionable.** `app "foo": path does not exist: /x/foo`, never
  a bare stack trace. Doctor findings always carry a remedy.
- Check [ROADMAP.md](ROADMAP.md) — especially the non-goals — before
  proposing features.

## Getting started

```bash
git clone https://github.com/cdrrazan/roost && cd roost
go test ./...            # green, fast, no Docker needed
golangci-lint run ./...  # zero issues expected
go build ./cmd/roost && ./roost --config examples/demo/config.yml list
```

Layout: `internal/config` (schema, hostnames), `internal/detect` (framework
rules + `testdata/` fixtures), `internal/generate` (templates → artifacts),
`internal/runner` (compose orchestration), `internal/tunnel` (Cloudflare),
`internal/doctor`, `internal/lifecycle`, `internal/shell` (the only package
allowed to call `os/exec`), `internal/state`.

## Adding a framework

The most welcome contribution. You'll touch:

1. `internal/detect/testdata/<framework>-app/` — a minimal fixture.
2. `internal/detect/detect_test.go` — the expected Detection (first!).
3. `internal/detect/detect.go` — the rule, in the right priority slot.
4. `internal/generate/templates/` — a Dockerfile template if none fits.
5. The table in `README.md` and `site/index.html`.

Remember the bind rule: start commands must listen on `0.0.0.0`, never
loopback — a loopback bind is a 502 from Caddy with healthy-looking app logs.

## PRs

- One logical change per PR; reference an issue for anything non-obvious.
- `gofmt`, `go vet`, `golangci-lint run`, `go test -race ./...` all clean —
  CI checks the same.
- Commit messages: imperative summary line; body explains *why*.

## Questions / conduct

Open a discussion or issue. Everyone interacting here is bound by the
[Code of Conduct](CODE_OF_CONDUCT.md).
