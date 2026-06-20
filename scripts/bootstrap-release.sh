#!/usr/bin/env bash
# bootstrap-release.sh — all-in-one: stand up polar-release on a fresh macOS box.
#
#   1. brew + deps (go, postgresql, redis, nginx)        [--skip-brew]
#   2. createdb polar_release                            [--skip-db]
#   3. build release-svc/release-publish/polar-install,
#      install binary + launchd wrapper/plist/env, start  [--skip-svc]
#   4. nginx vhost + reload                              [--skip-nginx]
#   5. 检表: assert schema landed after boot
#   6. verify-release.sh (smoke + optional round trip)   [--skip-verify]
#
# Idempotent: re-running upgrades the binary and leaves config/secrets/DB alone.
# Run from a polar-release checkout: scripts/bootstrap-release.sh
#
# Key env (see scripts/launchd/release-svc.env.sample for the full set):
#   PG_FORMULA   postgresql formula            (default postgresql@17)
#   ENV_FILE     service env file              (default ~/.local/share/release-svc.env)
#   LISTEN       release-svc listen            (default 127.0.0.1:8101)
#   POLAR_RELEASE_ADMIN_TOKEN  passed to verify-release.sh for the round trip (optional)
set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
PG_FORMULA="${PG_FORMULA:-postgresql@17}"
BIN_DIR="$HOME/.local/bin"
SHARE_DIR="$HOME/.local/share"
AGENTS="$HOME/Library/LaunchAgents"
ENV_FILE="${ENV_FILE:-$SHARE_DIR/release-svc.env}"
LISTEN="${LISTEN:-127.0.0.1:8101}"
LABEL="polar.release-svc"
NGINX_DIR="${NGINX_DIR:-/opt/homebrew/etc/nginx/servers}"
DB_NAME="${DB_NAME:-polar_release}"

SKIP_BREW=0 SKIP_DB=0 SKIP_SVC=0 SKIP_NGINX=0 SKIP_VERIFY=0
for a in "$@"; do case "$a" in
  --skip-brew) SKIP_BREW=1 ;; --skip-db) SKIP_DB=1 ;; --skip-svc) SKIP_SVC=1 ;;
  --skip-nginx) SKIP_NGINX=1 ;; --skip-verify) SKIP_VERIFY=1 ;;
  --upgrade) SKIP_BREW=1; SKIP_DB=1; SKIP_NGINX=1 ;;   # just rebuild+swap the binary
  -h|--help) sed -n '2,30p' "$0"; exit 0 ;;
  *) echo "unknown flag: $a" >&2; exit 2 ;;
esac; done

say() { printf '\n\033[1m== %s ==\033[0m\n' "$*"; }
have() { command -v "$1" >/dev/null 2>&1; }

# ── 1. brew + deps ──────────────────────────────────────────────────────────
if [ "$SKIP_BREW" = 0 ]; then
  say "1. brew + deps"
  have brew || { echo "Homebrew missing — install from https://brew.sh first"; exit 1; }
  for f in go "$PG_FORMULA" redis nginx; do
    if brew list --versions "$f" >/dev/null 2>&1; then echo "  · $f present"; else echo "  + installing $f"; brew install "$f"; fi
  done
  brew services start "$PG_FORMULA" >/dev/null 2>&1 || true
  brew services start redis        >/dev/null 2>&1 || true
  echo "  go=$(go version | awk '{print $3}')  pg=$(brew list --versions "$PG_FORMULA" | awk '{print $2}')"
else say "1. brew — skipped"; fi

# ── 2. database ─────────────────────────────────────────────────────────────
if [ "$SKIP_DB" = 0 ]; then
  say "2. database ($DB_NAME)"
  PSQL="$(ls /opt/homebrew/opt/"$PG_FORMULA"/bin/psql 2>/dev/null || command -v psql || true)"
  CREATEDB="$(dirname "$PSQL")/createdb"
  if "$PSQL" -lqt 2>/dev/null | cut -d'|' -f1 | grep -qw "$DB_NAME"; then
    echo "  · $DB_NAME exists"
  else
    echo "  + createdb $DB_NAME"; "$CREATEDB" "$DB_NAME"
  fi
else say "2. database — skipped"; fi

# ── 3. build + install service ──────────────────────────────────────────────
if [ "$SKIP_SVC" = 0 ]; then
  say "3. build + install release-svc"
  SHA="$(git -C "$REPO" rev-parse --short HEAD 2>/dev/null || echo dev)"
  mkdir -p "$BIN_DIR" "$SHARE_DIR" "$AGENTS"
  for cmd in release-svc release-publish polar-install; do
    echo "  + go build $cmd ($SHA)"
    ( cd "$REPO" && CGO_ENABLED=0 go build -ldflags "-w -s -X main.version=$SHA" -o "$BIN_DIR/$cmd" "./cmd/$cmd" )
  done
  install -m 0755 "$REPO/scripts/launchd/polar-release-svc-launch.sh" "$BIN_DIR/polar-release-svc-launch.sh"
  install -m 0644 "$REPO/scripts/launchd/polar.release-svc.plist" "$AGENTS/$LABEL.plist"
  if [ ! -f "$ENV_FILE" ]; then
    install -m 0600 "$REPO/scripts/launchd/release-svc.env.sample" "$ENV_FILE"
    # mint a persistent signing key so the pubkey is stable across restarts
    KEY="$(openssl rand -hex 32)"
    /usr/bin/sed -i '' "s|^POLAR_RELEASE_SIGNING_KEY=.*|POLAR_RELEASE_SIGNING_KEY=$KEY|" "$ENV_FILE"
    echo "  + seeded $ENV_FILE (generated signing key) — FILL IN POLAR_PLUGIN_TOKEN, then re-run"
    grep -q '^POLAR_PLUGIN_TOKEN=polar_plugin_REPLACE_ME' "$ENV_FILE" && { echo "  ! POLAR_PLUGIN_TOKEN still placeholder — stopping before start"; exit 0; }
  else
    echo "  · $ENV_FILE present (left untouched)"
  fi
  echo "  → (re)start launchd $LABEL"
  launchctl kickstart -k "gui/$(id -u)/$LABEL" 2>/dev/null \
    || launchctl bootstrap "gui/$(id -u)" "$AGENTS/$LABEL.plist"
else say "3. service — skipped"; fi

# ── 4. nginx vhost ──────────────────────────────────────────────────────────
if [ "$SKIP_NGINX" = 0 ] && [ -d "$NGINX_DIR" ]; then
  say "4. nginx vhost"
  DST="$NGINX_DIR/release.4950.store.conf"
  if have sudo && [ ! -w "$NGINX_DIR" ]; then SUDO=sudo; else SUDO=; fi
  $SUDO install -m 0644 "$REPO/scripts/nginx/release.4950.store.conf" "$DST"
  NGINX_BIN="$(command -v nginx || echo /opt/homebrew/bin/nginx)"
  $SUDO "$NGINX_BIN" -t && $SUDO "$NGINX_BIN" -s reload && echo "  · nginx reloaded"
else say "4. nginx — skipped"; fi

# ── 5. 检表 (schema auto-applies at boot; assert it landed) ──────────────────
say "5. 检表 ($DB_NAME schema)"
PSQL="$(ls /opt/homebrew/opt/"$PG_FORMULA"/bin/psql 2>/dev/null || command -v psql || true)"
sleep 2  # let the freshly (re)started svc apply schemaSQL
MISSING="$("$PSQL" -d "$DB_NAME" -tAc "
  SELECT string_agg(t, ',') FROM (
    SELECT unnest(ARRAY['releases','stage_runs','promotion_attestations']) AS t
    EXCEPT SELECT tablename FROM pg_tables WHERE schemaname='public'
  ) m;" 2>/dev/null || echo "QUERY_FAILED")"
if [ -z "$MISSING" ]; then
  echo "  · releases / stage_runs / promotion_attestations all present"
  "$PSQL" -d "$DB_NAME" -tAc "SELECT 1 FROM pg_indexes WHERE indexname='idx_releases_resolve'" | grep -q 1 \
    && echo "  · idx_releases_resolve present" || echo "  ! idx_releases_resolve missing (resolve hot path)"
else
  echo "  ✗ missing tables: $MISSING — is release-svc running? (tail /tmp/polar.release-svc.log)" >&2
  exit 1
fi

# ── 6. verify ───────────────────────────────────────────────────────────────
if [ "$SKIP_VERIFY" = 0 ]; then
  say "6. post-deploy verify"
  RELEASE_PUBLISH="$BIN_DIR/release-publish" POLAR_INSTALL="$BIN_DIR/polar-install" \
    bash "$REPO/scripts/verify-release.sh" "http://$LISTEN"
else say "6. verify — skipped"; fi

say "bootstrap-release: done"
