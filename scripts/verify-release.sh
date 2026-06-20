#!/usr/bin/env bash
# verify-release.sh — post-deploy smoke for polar-release. Exits non-zero on
# the first failed assertion (so bootstrap-release.sh aborts the deploy).
#
# Tiers:
#   always      — GET /healthz, GET /release/pubkey         (public)
#   if a token  — full publish→pull round trip of a SYNTHETIC pkg on the dev
#                 channel: package → release-publish -format tar.gz → polar-install
#                 (pubkey-pinned, scratch dirs) → assert entrypoint ran + current
#                 symlink flipped → yank the test release. No module build needed.
#
# Env / flags:
#   BASE                       release base URL (default http://127.0.0.1:8101)
#   POLAR_RELEASE_ADMIN_TOKEN  admin/owner dock session OR polar_ API token
#                              (enables the round-trip tier; skipped if unset)
#   PLATFORM                   default <os>-<arch> of this host
#   RELEASE_PUBLISH / POLAR_INSTALL  paths to the CLIs (default: on PATH / ./bin)
#
# Usage: scripts/verify-release.sh [BASE]
set -euo pipefail

BASE="${1:-${BASE:-http://127.0.0.1:8101}}"
BASE="${BASE%/}"
PLATFORM="${PLATFORM:-$(go env GOOS 2>/dev/null || uname -s | tr 'A-Z' 'a-z')-$(go env GOARCH 2>/dev/null || uname -m)}"
RELEASE_PUBLISH="${RELEASE_PUBLISH:-release-publish}"
POLAR_INSTALL="${POLAR_INSTALL:-polar-install}"
PASS=0; FAIL=0
ok()   { echo "  ✓ $*"; PASS=$((PASS+1)); }
bad()  { echo "  ✗ $*" >&2; FAIL=$((FAIL+1)); }
jqget() { python3 -c "import sys,json; print(json.load(sys.stdin).get('$1',''))"; }

echo "verify-release: base=$BASE platform=$PLATFORM"

# ── tier 1: public health ───────────────────────────────────────────────────
echo "[1] healthz"
H="$(curl -fsS --max-time 10 "$BASE/healthz" 2>/dev/null || true)"
[ "$(printf '%s' "$H" | jqget status)" = "ok" ] && ok "status ok (version=$(printf '%s' "$H" | jqget version))" || bad "healthz not ok: $H"

echo "[2] pubkey"
PK="$(curl -fsS --max-time 10 "$BASE/release/pubkey" 2>/dev/null | jqget public_key_hex || true)"
[ "${#PK}" -eq 64 ] && ok "ed25519 pubkey ${PK:0:16}…" || bad "pubkey missing/short: '$PK'"

# ── tier 2: full round trip (needs an admin token) ──────────────────────────
TOK="${POLAR_RELEASE_ADMIN_TOKEN:-}"
if [ -z "$TOK" ]; then
  echo "[3] round-trip SKIPPED (set POLAR_RELEASE_ADMIN_TOKEN to enable)"
else
  if ! command -v "$RELEASE_PUBLISH" >/dev/null 2>&1 || ! command -v "$POLAR_INSTALL" >/dev/null 2>&1; then
    bad "round-trip: release-publish / polar-install not found (set RELEASE_PUBLISH / POLAR_INSTALL)"
  else
    echo "[3] publish→pull round trip (synthetic pkg, dev channel)"
    WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT
    MOD="release-selftest"
    VER="v$(date +%Y%m%d%H%M%S)"
    # synthetic bundle: install.sh writes a marker; flat tar (install.sh at root)
    mkdir -p "$WORK/pkg"
    cat > "$WORK/pkg/install.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
: "${POLAR_NEW_DIR:?}"
echo "$POLAR_VERSION" > "$POLAR_NEW_DIR/selftest.ok"
SH
    chmod +x "$WORK/pkg/install.sh"
    tar -C "$WORK/pkg" -czf "$WORK/pkg.tgz" .

    if "$RELEASE_PUBLISH" -base "$BASE" -token "$TOK" -module "$MOD" -version "$VER" \
         -channel dev -platform "$PLATFORM" -format tar.gz -file "$WORK/pkg.tgz" \
         -notes "verify-release self-test" >"$WORK/pub.log" 2>&1; then
      ok "published $MOD $VER (format=tar.gz accepted)"
    else
      bad "publish failed: $(tail -1 "$WORK/pub.log")"
    fi

    if [ -d "$WORK/inst" ] || mkdir -p "$WORK/inst"; then :; fi
    if POLAR_RELEASE_PUBKEY="$PK" DESTDIR="$WORK/destdir" \
         "$POLAR_INSTALL" -base "$BASE" -module "$MOD" -channel dev \
         -platform "$PLATFORM" -dest "$WORK/inst" >"$WORK/inst.log" 2>&1; then
      LINK="$(readlink "$WORK/inst/current" 2>/dev/null || true)"
      if [ -f "$WORK/inst/$LINK/selftest.ok" ]; then
        ok "polar-install: verified+unpacked, entrypoint ran, current→$LINK"
      else
        bad "round-trip: marker not found (current=$LINK)"
      fi
    else
      bad "polar-install failed: $(tail -1 "$WORK/inst.log")"
    fi

    # cleanup the test release (best-effort)
    curl -fsS -X POST -H "Authorization: Bearer $TOK" -H "Content-Type: application/json" \
      -d "{\"module\":\"$MOD\",\"version\":\"$VER\",\"channel\":\"dev\",\"platform\":\"$PLATFORM\"}" \
      "$BASE/release/yank" >/dev/null 2>&1 && echo "  · yanked test release $MOD $VER"
  fi
fi

echo "verify-release: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
