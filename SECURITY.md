# Security Policy

## Reporting a vulnerability

Please **do not open a public issue** for security problems. Email
**cdrrazan@gmail.com** with the details, or use GitHub's private
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

## Known trade-offs (not vulnerabilities)

- Database credentials inside the Compose network are shared and simple
  (`roost`/`roost`); the network is not reachable externally. Hardening is on
  the [roadmap](ROADMAP.md).
- roost shells out to `docker` and `cloudflared`; it trusts the binaries on
  your PATH.
- Apps you expose are your own code — roost cannot make an insecure app safe.
  Access policies are the mitigation.
