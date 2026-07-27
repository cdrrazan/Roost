# Roadmap

roost v1 is complete: config → detection → generation → runner → tunnel →
doctor → distribution — plus a **Material Design 3 browser control panel**
(`roost web`) and push-to-`main` auto-deploy. This file tracks where it goes
next. Items are ordered by likely value, not by promise — this is a side
project serving side projects.

## v1.x — polish

- [ ] `roost status` tunnel awareness: distinguish "cloudflared reconnecting
      after wake" (resolves itself in ~5–10s) from "app down" so users don't
      chase a non-problem.
- [ ] `roost doctor --fix` for the safe subset: PATCH `proxied:true`,
      create missing DNS records, chmod credentials.
- [x] `roost down --remove-dns` / `roost uninstall`: clean up only the
      records recorded in `state.json`.
- [x] Framework detection: Laravel, Flask, Astro, SvelteKit.
- [x] Runtime version → base image matrix maintenance (Ruby 3.4, Node 24).
- [x] `roost logs` multiplexing (`roost logs` with no app = all apps).

## v2 — considered, not committed

- **Per-app health checks** in generated compose, surfaced in `status`.
- **Postgres per-app credentials** instead of the shared roost user.
- **`roost share`**: a one-shot temporary hostname for a single app —
  spiritually a nicer `cloudflared tunnel --url`, but with your domain.
- **Windows lifecycle** (Task Scheduler unit) — detection/generation already
  work; only `enable` is macOS/Linux.
- **Remote roost**: the same config driving a cheap VPS instead of a laptop,
  for the day a side project needs to survive a closed lid. This must not
  compromise the local-first core.

## Explicit non-goals (unchanged from the design)

These are boundaries, not backlog:

- No tunnel/proxy implementation of our own — roost orchestrates cloudflared.
- No hosting platform, control plane, accounts, or billing.
- No reimplementation of Docker Compose — generate and shell out.
- Never write into the user's app repositories.
- No silent dependency installation; `doctor` prints install commands.
- No roost daemon supervising the stack — Docker's restart policy is the
  supervisor. (The optional `roost web` panel is a host process you choose to
  run, not a daemon roost manages; it controls the stack, it doesn't babysit it.)

Suggest changes via an issue — see [CONTRIBUTING.md](CONTRIBUTING.md).
