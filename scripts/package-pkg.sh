#!/usr/bin/env bash
# package-pkg.sh — build a Polar module and emit a release pkg (tar.gz + install.sh)
# that polar-release can distribute and the SDK SelfInstall can apply.
#
# Usage:
#   scripts/package-pkg.sh <module-dir> [os] [arch]
#   scripts/package-pkg.sh ~/github/Polar-/modules/polar-buildings darwin arm64
#
# Output: $OUT (default /tmp/<svc>-<ver>-<os>-<arch>.tar.gz). Publish it with:
#   release-publish -format tar.gz -channel dev -module <svc> -platform <os>-<arch> -file <tar.gz>
#
# The generated install.sh is DESTDIR-aware and activation-gated:
#   DESTDIR (default $HOME)     — install root; binary→$DESTDIR/.local/bin
#   POLAR_PKG_ACTIVATE=1        — only then does it (re)load launchd (touch the live service)
# so installing into a scratch dir for testing never disturbs a running service.

set -euo pipefail

MOD_DIR="${1:?usage: package-pkg.sh <module-dir> [os] [arch]}"
OS="${2:-darwin}"
ARCH="${3:-arm64}"
MOD_DIR="$(cd "$MOD_DIR" && pwd)"

SVC="$(basename "$MOD_DIR")"; SVC="${SVC#polar-}"   # polar-buildings → buildings
CMD_DIR="$MOD_DIR/cmd/${SVC}-svc"
[ -d "$CMD_DIR" ] || { echo "package-pkg: no $CMD_DIR" >&2; exit 1; }

SHA="$(git -C "$MOD_DIR" rev-parse --short HEAD 2>/dev/null || echo dev)"
STAGE="$(mktemp -d)"; trap 'rm -rf "$STAGE"' EXIT
PKG="$STAGE/${SVC}-pkg"
mkdir -p "$PKG/bin" "$PKG/lib" "$PKG/share"

echo "package-pkg: building ${SVC}-svc ($OS/$ARCH, sha=$SHA)"
( cd "$MOD_DIR" && CGO_ENABLED=0 GOOS="$OS" GOARCH="$ARCH" go build \
    -ldflags "-w -s -X main.version=$SHA" -o "$PKG/bin/${SVC}-svc" "./cmd/${SVC}-svc" )

# Reuse the module's launchd artifacts when present; else synthesize minimal ones.
LD="$MOD_DIR/scripts/launchd"
if [ -f "$LD/polar-${SVC}-svc-launch.sh" ]; then
  cp "$LD/polar-${SVC}-svc-launch.sh" "$PKG/bin/"
else
  cat > "$PKG/bin/polar-${SVC}-svc-launch.sh" <<EOF
#!/usr/bin/env bash
set -euo pipefail
ENV_FILE="\${HOME}/.local/share/${SVC}-svc.env"
if [[ -f "\${ENV_FILE}" ]]; then set -a; source "\${ENV_FILE}"; set +a; fi
export PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin:\${PATH:-}"
exec "\${HOME}/.local/bin/${SVC}-svc"
EOF
fi
chmod +x "$PKG/bin/polar-${SVC}-svc-launch.sh"
[ -f "$LD/polar.${SVC}-svc.plist" ]  && cp "$LD/polar.${SVC}-svc.plist"  "$PKG/lib/"
[ -f "$LD/${SVC}-svc.env.sample" ]   && cp "$LD/${SVC}-svc.env.sample"   "$PKG/share/" || true

# install.sh — SVC name baked in; $HOME/$DESTDIR resolved at install time.
cat > "$PKG/install.sh" <<EOF
#!/usr/bin/env bash
# install.sh — place the ${SVC}-svc binary + launchd job. DESTDIR-aware,
# activation-gated. Run via SDK SelfInstall (cwd = this dir) or by hand.
set -euo pipefail
HERE="\$(cd "\$(dirname "\$0")" && pwd)"
DESTDIR="\${DESTDIR:-\$HOME}"
BIN_DIR="\$DESTDIR/.local/bin"
SHARE_DIR="\$DESTDIR/.local/share"
AGENTS="\$DESTDIR/Library/LaunchAgents"
mkdir -p "\$BIN_DIR" "\$SHARE_DIR" "\$AGENTS"

echo "install: ${SVC}-svc -> \$BIN_DIR"
install -m 0755 "\$HERE/bin/${SVC}-svc" "\$BIN_DIR/${SVC}-svc"
install -m 0755 "\$HERE/bin/polar-${SVC}-svc-launch.sh" "\$BIN_DIR/polar-${SVC}-svc-launch.sh"
[ -f "\$HERE/lib/polar.${SVC}-svc.plist" ] && install -m 0644 "\$HERE/lib/polar.${SVC}-svc.plist" "\$AGENTS/polar.${SVC}-svc.plist" || true
if [ -f "\$HERE/share/${SVC}-svc.env.sample" ] && [ ! -f "\$SHARE_DIR/${SVC}-svc.env" ]; then
  install -m 0600 "\$HERE/share/${SVC}-svc.env.sample" "\$SHARE_DIR/${SVC}-svc.env"
  echo "install: seeded \$SHARE_DIR/${SVC}-svc.env (fill in secrets)"
fi

if [ "\${POLAR_PKG_ACTIVATE:-0}" = "1" ]; then
  echo "install: activating launchd job polar.${SVC}-svc"
  launchctl kickstart -k "gui/\$(id -u)/polar.${SVC}-svc" 2>/dev/null \\
    || launchctl bootstrap "gui/\$(id -u)" "\$AGENTS/polar.${SVC}-svc.plist" 2>/dev/null || true
else
  echo "install: POLAR_PKG_ACTIVATE!=1 — files staged, launchd NOT touched"
fi
echo "install: done (${SVC}-svc @ \$BIN_DIR)"
EOF
chmod +x "$PKG/install.sh"

VER_FULL="$SHA"
OUT="${OUT:-/tmp/${SVC}-${VER_FULL}-${OS}-${ARCH}.tar.gz}"
# Tar the CONTENTS flat (install.sh at the archive root) — SelfInstall looks
# for the entrypoint at the top of the unpacked version dir, not one level deep.
tar -C "$PKG" -czf "$OUT" .
echo "package-pkg: wrote $OUT"
tar -tzf "$OUT"
