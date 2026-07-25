#!/usr/bin/env bash
# roost box bootstrap — rebuild a fresh Ubuntu box into a running roost stack
# from the encrypted R2 backup produced by roost-backup.sh.
#
# Run as the target user (e.g. `ubuntu`) on a clean VM. roost routes via a
# Cloudflare Tunnel, so the VM's IP does not matter — no DNS change is needed.
#
# You must provide THREE things out of band (the backup is locked with them, so
# they cannot live inside it):
#   1. age private key   — from your password manager        (ROOST_AGE_KEY)
#   2. rclone R2 config  — at ~/.config/rclone/rclone.conf    (needed to FETCH the backup)
#   3. GitHub SSH key    — at ~/.ssh/id_ed25519, read access to the app repos
#   4. a roost binary    — ROOST_BINARY=/path or ROOST_BINARY_URL=https://...
#
# Env:
#   ROOST_AGE_KEY        (required) path to the age private key file
#   ROOST_RCLONE_REMOTE  (default r2:roost-backup)
#   ROOST_BINARY         path to a roost binary to install to /usr/local/bin
#   ROOST_BINARY_URL     OR a URL to download it from
#   CONFIRM_UP=1         actually run `roost up` + restore data. cloudflared runs
#                        from ONE box only — STOP the old box's stack first, or
#                        the two connectors split traffic. Default: skip (dry set-up).
set -euo pipefail
log(){ echo "$(date '+%F %T') • $*"; }
die(){ echo "ERROR: $*" >&2; exit 1; }

REMOTE="${ROOST_RCLONE_REMOTE:-r2:roost-backup}"
: "${ROOST_AGE_KEY:?set ROOST_AGE_KEY to your age private key file}"
[ -s "$ROOST_AGE_KEY" ] || die "age key file not found: $ROOST_AGE_KEY"

# 1. deps
log "installing deps (age, rclone, git, jq, docker)"
sudo apt-get update -qq
sudo apt-get install -y -qq age rclone git jq curl ca-certificates python3 >/dev/null
if ! command -v docker >/dev/null; then
  curl -fsSL https://get.docker.com | sudo sh
  sudo usermod -aG docker "$USER"
  log "added $USER to docker group — re-login (or run CONFIRM_UP phase in a fresh shell)"
fi
rclone listremotes | grep -q "^${REMOTE%%:*}:" \
  || die "rclone remote '${REMOTE%%:*}' not configured — put your rclone.conf at ~/.config/rclone/rclone.conf first"

# 2. fetch + decrypt the latest secrets archive
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
log "fetching latest secrets archive from $REMOTE/secrets"
rclone copy "$REMOTE/secrets" "$TMP" --include "roost-secrets-*.tar.age"
SEC="$(ls -1t "$TMP"/roost-secrets-*.tar.age 2>/dev/null | head -1 || true)"
[ -n "${SEC:-}" ] || die "no secrets archive in $REMOTE/secrets"
log "decrypting $(basename "$SEC")"
mkdir -p "$TMP/x"
age -d -i "$ROOST_AGE_KEY" "$SEC" | tar xzf - -C "$TMP/x"
[ -f "$TMP/x/roost/config.yml" ] || die "archive missing roost/config.yml — wrong age key?"

# 3. restore ~/.roost + systemd units + rclone.conf
log "restoring ~/.roost"
rm -rf "$HOME/.roost"; cp -a "$TMP/x/roost" "$HOME/.roost"
chmod 600 "$HOME/.roost/credentials" "$HOME/.roost/seed.env" "$HOME/.roost/build/.env" 2>/dev/null || true
mkdir -p "$HOME/.config/systemd/user" "$HOME/.config/rclone"
cp "$TMP/x/systemd/"*.service "$TMP/x/systemd/"*.timer "$HOME/.config/systemd/user/" 2>/dev/null || true
[ -f "$HOME/.config/rclone/rclone.conf" ] || cp "$TMP/x/rclone.conf" "$HOME/.config/rclone/rclone.conf" 2>/dev/null || true

# 4. install the roost binary
if [ -n "${ROOST_BINARY:-}" ]; then
  sudo install -m0755 "$ROOST_BINARY" /usr/local/bin/roost
elif [ -n "${ROOST_BINARY_URL:-}" ]; then
  curl -fsSL "$ROOST_BINARY_URL" -o "$TMP/roost"; sudo install -m0755 "$TMP/roost" /usr/local/bin/roost
elif ! command -v roost >/dev/null; then
  die "no roost binary: set ROOST_BINARY or ROOST_BINARY_URL"
fi
log "roost: $(roost version)"

# 5. clone the app repos from the manifest
if [ -f "$TMP/x/repos.manifest" ]; then
  log "cloning app repos (needs the GitHub SSH key)"
  while IFS='|' read -r path url branch; do
    [ -n "$path" ] && [ -n "$url" ] || continue
    dir="${path%/}"
    if [ -d "$dir/.git" ]; then log "  exists: $dir"; else
      git clone "$url" "$dir" || die "clone failed: $url (is the SSH deploy key present + authorized?)"
    fi
    [ -n "$branch" ] && git -C "$dir" checkout "$branch" 2>/dev/null || true
  done < "$TMP/x/repos.manifest"
fi

# 6-8. bring the stack up + restore data (guarded)
if [ "${CONFIRM_UP:-0}" = 1 ]; then
  docker info >/dev/null 2>&1 || die "docker not usable by $USER yet — re-login (group change) and re-run with CONFIRM_UP=1"

  log "roost up --no-seed (first run builds images; can take several minutes)"
  roost up --no-seed        # --no-seed: never write demo data over restored data

  log "restoring latest DB dumps from $REMOTE"
  DTMP="$(mktemp -d)"
  rclone copy "$REMOTE" "$DTMP" --include "mysql-*.sql.gz" --include "postgres-*.sql.gz"
  MY="$(ls -1t "$DTMP"/mysql-*.sql.gz 2>/dev/null | head -1 || true)"
  PG="$(ls -1t "$DTMP"/postgres-*.sql.gz 2>/dev/null | head -1 || true)"
  if [ -n "${MY:-}" ] && docker ps --format '{{.Names}}' | grep -q '^roost-mysql-1$'; then
    gunzip -c "$MY" | docker exec -i roost-mysql-1 mysql -uroot -proost && log "  mysql restored ($(basename "$MY"))"
  fi
  if [ -n "${PG:-}" ] && docker ps --format '{{.Names}}' | grep -q '^roost-postgres-1$'; then
    # pg_dumpall may warn on pre-existing roles; that's fine, data still loads.
    gunzip -c "$PG" | docker exec -i roost-postgres-1 psql -U roost -d postgres >/dev/null 2>&1 || true
    log "  postgres restored ($(basename "$PG"))"
  fi
  rm -rf "$DTMP"

  # Pin seeding so a later boot never reseeds demo data over the restored data:
  # mark every configured app as already-seeded and record the current mysql
  # volume identity (so roost's volume-drift reseed never triggers).
  VOLID="$(docker volume inspect roost_roost-mysql-data --format '{{.CreatedAt}}' 2>/dev/null || echo)"
  python3 - "$HOME/.roost/state.json" "$VOLID" <<'PY'
import json,sys,glob,os,re
p,vol=sys.argv[1],sys.argv[2]
st=json.load(open(p)) if os.path.exists(p) else {}
names=set()
for f in glob.glob(os.path.expanduser('~/.roost/apps/*.yml'))+[os.path.expanduser('~/.roost/config.yml')]:
    try: txt=open(f).read()
    except OSError: continue
    names.update(re.findall(r'^\s*name:\s*(\S+)', txt, re.M))
st['seeded']=sorted(names)
if vol: st['mysql_volume_id']=vol
json.dump(st, open(p,'w'), indent=2)
print('state.json: seeded=%d apps, mysql_volume_id pinned' % len(names))
PY
  log "stack is up and data is restored"
else
  log "SET-UP ONLY — skipped 'roost up' and data restore."
  log "Re-run with CONFIRM_UP=1 once the OLD box's cloudflared is STOPPED"
  log "(two connectors on one tunnel token split traffic)."
fi

# 9. enable boot units + firewall for the web panel
log "enabling boot units + linger"
systemctl --user daemon-reload 2>/dev/null || true
loginctl enable-linger "$USER" 2>/dev/null || sudo loginctl enable-linger "$USER" 2>/dev/null || true
for u in roost.service roost-web.service roost-backup.timer; do
  systemctl --user enable "$u" 2>/dev/null || true
done
# allow the web panel port (4600) from the docker bridge so Caddy can reach it
sudo iptables -C INPUT -p tcp -s 172.16.0.0/12 --dport 4600 -j ACCEPT 2>/dev/null \
  || sudo iptables -I INPUT -p tcp -s 172.16.0.0/12 --dport 4600 -j ACCEPT 2>/dev/null || true

log "done. verify:  roost status  &&  systemctl --user status roost-web"
