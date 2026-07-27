# Security Policy

## Reporting a vulnerability

Please **do not open a public issue** for security problems. Email
**irajanbhattarai@gmail.com** with the details, or use GitHub's private
[security advisory](https://github.com/cdrrazan/roost/security/advisories/new)
form. You should hear back within a week; fixes for confirmed issues ship as
a patch release with credit (unless you prefer otherwise).

## Supported versions

Only the latest release receives security fixes.

## roost's security model — what to expect

roost exposes local apps to the public internet by design. The defaults try
to make the sharp edges hard to cut yourself on:

- **Credentials.** The Cloudflare API token lives in `$CLOUDFLARE_API_TOKEN`
  or `~/.roost/credentials`; roost enforces mode 0600 on read and refuses
  group/world-readable files. Tokens are never written to `config.yml`. The
  cloudflared connector token is stored in `~/.roost/build/.env` (0600).
- **Edge auth.** With `tunnel.access.emails` set, every routing suffix gets a
  Cloudflare Access application before first exposure. Without it, roost
  prints exactly which hostnames are publicly reachable — hostnames leak via
  Certificate Transparency logs within hours of certificate issuance.
- **No published ports.** Neither apps nor databases publish host ports;
  everything is reached over the Compose-internal network. Databases are
  additionally never reachable from outside the Docker network.
- **Containers run as non-root** in generated Dockerfiles, with source
  bind-mounted read-only.
- **No destructive remote changes.** roost refuses to overwrite DNS records
  it did not create (requires `--force`) and refuses to adopt tunnels it did
  not create (requires `--adopt`).
- **The `roost web` control panel is loopback by default** (`127.0.0.1:4600`).
  It can start, stop, add, and remove apps, so it is only reachable remotely
  when you set `control_host:` to route it through the tunnel — and that route
  is meant to sit behind **Cloudflare Access**. A `--token` / `$ROOST_WEB_TOKEN`
  bearer check gates the mutating actions as defense-in-depth. The panel shows
  env **key names only**, never values, and the incident-email SMTP password
  is read only from `$ROOST_SMTP_PASSWORD`, never from `config.yml`.

## Known trade-offs (not vulnerabilities)

- Database credentials inside the Compose network are shared and simple
  (`roost`/`roost`); the network is not reachable externally. Hardening is on
  the [roadmap](ROADMAP.md).
- roost shells out to `docker` and `cloudflared`; it trusts the binaries on
  your PATH.
- Apps you expose are your own code — roost cannot make an insecure app safe.
  Access policies are the mitigation.
- Exposing `control_host:` **without Cloudflare Access** means anyone who
  reaches the URL can stop/start your stack and build whatever Dockerfile
  lives at a path they add. Access (or keeping the panel loopback-only) is the
  mitigation; the `--token` check alone is not a substitute for edge auth.
