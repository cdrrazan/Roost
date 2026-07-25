#!/usr/bin/env bash
# roost backup (Oracle box / Linux).
#   1. DB dumps  — MySQL + Postgres from the running stack -> timestamped gzips.
#   2. Secrets   — ~/.roost config/credentials/apps/seed.env/tunnel token +
#                  systemd units + a repo manifest, tarred and AGE-ENCRYPTED to
#                  the public key in roost-backup.pub. Only the offline private
#                  key can decrypt it, so this is safe to mirror offsite.
# Both are pruned to KEEP copies and mirrored to R2 when ROOST_RCLONE_REMOTE set.
set -euo pipefail

BACKUP_DIR="$HOME/.roost/backups"
STAMP="$(date +%Y%m%d-%H%M%S)"
KEEP=14
RCLONE_REMOTE="${ROOST_RCLONE_REMOTE:-}"
PUBKEY_FILE="$BACKUP_DIR/roost-backup.pub"

log() { echo "$(date '+%F %T') $*"; }
mkdir -p "$BACKUP_DIR" "$HOME/.roost/logs"

if ! docker ps >/dev/null 2>&1; then log "skip: docker not available"; exit 0; fi

dumped=0; failed=0
dump() {
  local container="$1" engine="$2" cmd="$3" out="$BACKUP_DIR/$2-$STAMP.sql.gz"
  if ! docker ps --format '{{.Names}}' | grep -q "^${container}\$"; then
    log "$engine SKIP ($container not running)"; return 0
  fi
  if docker exec "$container" sh -c "$cmd" 2>/dev/null | gzip > "$out"; then
    log "$engine -> $out ($(du -h "$out" | cut -f1))"; dumped=1
  else
    rm -f "$out"; failed=1; log "$engine FAIL"
  fi
}
dump roost-mysql-1    mysql    'exec mysqldump -uroot -proost --all-databases --single-transaction --routines --triggers --events'
dump roost-postgres-1 postgres 'exec pg_dumpall -U roost'

# --- encrypted secrets + config archive (age) ---
if command -v age >/dev/null 2>&1 && [ -s "$PUBKEY_FILE" ]; then
  MANIFEST="$BACKUP_DIR/repos.manifest"; : > "$MANIFEST"
  for d in "$HOME"/apps/*/; do
    [ -d "${d}.git" ] || continue
    printf '%s|%s|%s\n' "$d" \
      "$(git -C "$d" remote get-url origin 2>/dev/null || echo)" \
      "$(git -C "$d" branch --show-current 2>/dev/null || echo)" >> "$MANIFEST"
  done
  STAGE="$(mktemp -d)"
  cp -a "$HOME/.roost" "$STAGE/roost"
  find "$STAGE/roost/build" -mindepth 1 ! -name .env -delete 2>/dev/null; rm -rf "$STAGE/roost/logs" "$STAGE/roost/backups" "$STAGE/roost/apps.bak"* 2>/dev/null || true
  mkdir -p "$STAGE/systemd"
  cp "$HOME/.config/systemd/user/"roost*.service "$HOME/.config/systemd/user/"roost*.timer "$STAGE/systemd/" 2>/dev/null || true
  [ -f "$HOME/.config/rclone/rclone.conf" ] && cp "$HOME/.config/rclone/rclone.conf" "$STAGE/rclone.conf"
  cp "$MANIFEST" "$STAGE/repos.manifest"
  SEC="$BACKUP_DIR/roost-secrets-$STAMP.tar.age"
  if tar czf - -C "$STAGE" . | age -R "$PUBKEY_FILE" > "$SEC"; then
    log "secrets -> $SEC ($(du -h "$SEC" | cut -f1))"
  else
    rm -f "$SEC"; failed=1; log "secrets FAIL"
  fi
  rm -rf "$STAGE"
else
  log "secrets SKIP (age or $PUBKEY_FILE missing)"
fi

# prune old copies
for pat in "mysql-"*.sql.gz "postgres-"*.sql.gz "roost-secrets-"*.tar.age; do
  ls -1t $BACKUP_DIR/$pat 2>/dev/null | tail -n +$((KEEP+1)) | xargs -r rm -f
done

# offsite mirror
if [ -n "$RCLONE_REMOTE" ] && command -v rclone >/dev/null 2>&1; then
  rclone copy "$BACKUP_DIR" "$RCLONE_REMOTE" --include "*.sql.gz" --max-age 25h 2>>"$HOME/.roost/logs/backup.log" && log "db mirrored to $RCLONE_REMOTE" || { log "rclone db FAIL"; failed=1; }
  rclone copy "$BACKUP_DIR" "$RCLONE_REMOTE/secrets" --include "roost-secrets-*.tar.age" --max-age 25h 2>>"$HOME/.roost/logs/backup.log" && log "secrets mirrored to $RCLONE_REMOTE/secrets" || { log "rclone secrets FAIL"; failed=1; }
fi

if [ "$dumped" = 1 ] && [ "$failed" = 0 ]; then log "backup OK"; else log "backup incomplete (dumped=$dumped failed=$failed)"; exit 1; fi
