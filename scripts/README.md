# Ops scripts — backup & box re-migration

Reference scripts for running a roost fleet on an **always-on Linux box** and
moving it to a new VM. They are plain bash, optional, and separate from the Go
CLI. roost routes through a **Cloudflare Tunnel (outbound)**, so a new VM needs
**no DNS change and no IP anywhere** — only the tunnel token, config, secrets,
data, and app source have to come along.

## `roost-backup.sh` — dumps + encrypted secrets

Runs on the box (drive it with a systemd timer / cron). Every run:

1. **DB dumps** — `mysqldump --all-databases` + `pg_dumpall` → gzipped, kept `KEEP=14`.
2. **Encrypted secrets archive** — tars `~/.roost` (config, `credentials`,
   `apps/*.yml`, `seed.env`, `build/.env` tunnel token), the `roost*` systemd
   units, `rclone.conf`, and a repo manifest, then **age-encrypts** it to the
   public key in `~/.roost/backups/roost-backup.pub`.
3. **Offsite mirror** — when `ROOST_RCLONE_REMOTE` is set (e.g. `r2:roost-backup`),
   dumps go to the remote and the encrypted archive to `<remote>/secrets/`.

The archive is encrypted to a **public** key; only the matching **private** key
(kept offline, e.g. a password manager) can decrypt it — safe to store in R2.

Set up the key once:

```bash
age-keygen -o key.txt                 # SAVE the private key offline, then delete key.txt
grep 'public key' key.txt             # -> age1...  put it in roost-backup.pub
echo 'age1...' > ~/.roost/backups/roost-backup.pub
```

Restore-test any time:

```bash
rclone copy r2:roost-backup/secrets ./ --include 'roost-secrets-*.tar.age'
age -d -i /path/to/private.key roost-secrets-*.tar.age | tar tzf -   # list
```

## `roost-box-bootstrap.sh` — rebuild a fresh box

Run on a clean Ubuntu VM as the target user. Installs deps, pulls + decrypts the
latest secrets archive, restores `~/.roost` + units, installs the roost binary,
clones the app repos from the manifest, then (guarded) brings the stack up and
restores the DB dumps.

Provide these **out of band** (the backup is locked with them):

| Need | How |
|---|---|
| age **private** key | `ROOST_AGE_KEY=/path/to/private.key` |
| rclone R2 config | `~/.config/rclone/rclone.conf` (needed to *fetch* the backup) |
| GitHub SSH key | `~/.ssh/id_ed25519` with read access to the app repos |
| roost binary | `ROOST_BINARY=/path` or `ROOST_BINARY_URL=https://…` |

```bash
# 1) set up + restore config/secrets/repos (no stack start yet)
ROOST_AGE_KEY=~/private.key ROOST_BINARY=~/roost ./roost-box-bootstrap.sh

# 2) once the OLD box's cloudflared is STOPPED, start + restore data
ROOST_AGE_KEY=~/private.key ROOST_BINARY=~/roost CONFIRM_UP=1 ./roost-box-bootstrap.sh
```

**One tunnel, one connector.** Stop the old box's stack before `CONFIRM_UP=1` on
the new one — two `cloudflared` on the same token split traffic between them.

Restore is `roost up --no-seed` → load the DB dumps → pin `state.json` (mark every
app seeded + record the current volume id) so a later boot never reseeds demo
data over the restored production data.
