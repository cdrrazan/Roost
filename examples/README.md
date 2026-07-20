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
| [`full.yml`](full.yml) | every knob: overrides, profiles, Access, defaults, env |
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
