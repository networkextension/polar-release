# polar-release

The Polar **publish system** — a thin control plane that turns module
install/update into a first-class, signed, content-addressed release
flow. It sits on top of the **assets** data plane and **dock** identity:

| layer | owner | responsibility |
|---|---|---|
| bytes | **assets** | dumb content-addressed store; signed PUT + GET; replication |
| registry | **polar-release** (this) | versions / channels / platforms → artifact; publish + resolve; ed25519 manifest signing |
| identity | **dock** | authenticate publishers + clients |

Design: `doc/arch/release-system.md` in polar-dock. polar-release **never
streams artifact bytes** — it only mints upload/download grants (through
dock's `/internal/v1/assets/*`) and holds metadata + signatures. Bytes
flow publisher↔provider (upload) and client↔provider (download) directly.

## API

```
GET  /healthz
GET  /release/pubkey                              # {algo, public_key_hex}  (public)
GET  /release/resolve?module&channel&platform     # → version, sha256, ed25519_sig, manifest, dl_url  (public)
POST /release/publish    {module,version,channel,platform,sha256,size_bytes,mime}   # → {asset_id, put_url}  (admin)
POST /release/finalize   {…,asset_id,provider_id,min_host?,notes?,format?,entrypoint?}  # → {release}        (admin)
POST /release/yank       {module,version,channel,platform}                          # admin
GET  /release/list?module[&channel&platform]                                        # admin
```

Writes require an owner/admin dock session (Bearer). Reads are public:
the security boundary is the **short-lived signed provider URL** plus the
**ed25519 signature** over the manifest — a compromised provider cannot
serve a binary the release key didn't sign, even with a matching hash.

## Publish flow (bytes bypass dock + release)

```
publisher                 release                  assets/provider
  │ POST /release/publish ──►│ mint signed PUT (dock) ──►│
  │ ◄── {asset_id, put_url} ─│◄──────────────────────────│
  │ PUT bytes ───────────────────────────────────────────►│  (provider verifies sig+size, recomputes sha)
  │ POST /release/finalize ─►│ confirm ready; ed25519-sign; insert row
  │ ◄── {release} ───────────│
```

Use the bundled CLI:

```bash
release-publish -base https://release.example -token <admin-session> \
  -module buildings -version 0.0.3 -channel stable \
  -platform darwin-arm64 -file ./buildings-svc -notes "fix keepalive exit code"
```

## Install / update flow (bytes bypass dock + release)

```bash
curl -s 'https://release.example/release/resolve?module=buildings&channel=stable&platform=darwin-arm64'
# → { version, sha256, ed25519_sig, manifest, dl_url, pubkey }
# client: GET dl_url → bytes ; verify sha256==content-key AND ed25519_sig over manifest
```

The OTA self-update loop and the module marketplace become consumers of
`/release/resolve` (they stop carrying the artifact URL themselves).

## Artifact formats: binary vs pkg (archive + install script)

The registry is payload-agnostic — it stores opaque bytes keyed by sha256 and
signs the manifest, so the artifact can be a single binary **or** a package
that bundles files plus an install script. The signature covers the whole
file's sha256, so the script inside an archive is just as tamper-evident as a
binary.

The manifest carries two signed fields that tell the consumer how to install:

| `format` | meaning | consumer |
|---|---|---|
| `` (empty) / `binary` | a single executable | SDK `SelfUpdate` — swap + restart |
| `tar.gz` / `tgz` / `zip` | archive bundle; run `entrypoint` after unpack | SDK `SelfInstall` |

`entrypoint` (default `install.sh`) is the path **inside** the archive to run.
Both fields are part of the ed25519-signed canonical bytes — but only when
non-empty, so a plain binary's signed bytes are unchanged from before these
fields existed (old signatures keep verifying). An Apple `.pkg` is a distinct
platform installer, not one of these bundles — ship "a pkg with an install
script" as a `tar.gz`/`zip` containing `install.sh`.

Publish a pkg (same three hops, just add `-format`):

```bash
release-publish -base https://release.example -token <admin> \
  -module buildings -version 0.0.3 -channel dev \
  -platform darwin-arm64 -format tar.gz -file ./buildings-bundle.tgz \
  [-entrypoint install.sh]
```

Install side (SDK): `SelfInstall(directive, destRoot)` verifies sha256 + (when
`POLAR_RELEASE_PUBKEY` is pinned) ed25519, unpacks into
`<destRoot>/versions/<version>/`, runs the entrypoint there with
`POLAR_MODULE/POLAR_VERSION/POLAR_PLATFORM/POLAR_NEW_DIR/POLAR_PREV_DIR` in the
env, and on exit 0 atomically repoints `<destRoot>/current` at it. The script
is arbitrary code, so it runs **only after** verification, archive entries are
path-traversal-checked, and operators SHOULD pin the release pubkey on any host
that runs install scripts. `SelfApply(d, binPath, destRoot)` dispatches by
`format` (binary→`SelfUpdate`, archive→`SelfInstall`) so a consumer adopts pkg
with one call.

### Tooling

```bash
# package any module into a release pkg (tar.gz + DESTDIR-aware, activation-gated install.sh)
scripts/package-pkg.sh ~/github/Polar-/modules/polar-buildings darwin arm64
# → /tmp/buildings-<sha>-darwin-arm64.tar.gz   (publish with release-publish -format tar.gz)

# pull-install a module (no dock session; resolve is public). Pin the pubkey to enforce auth.
POLAR_RELEASE_PUBKEY=<hex> DESTDIR=~/.local/polar/buildings \
  polar-install -base https://release.example -module buildings \
    -channel stable -platform darwin-arm64 -dest ~/.local/polar/buildings
# add POLAR_PKG_ACTIVATE=1 to let the bundled install.sh (re)load the launchd job.
```

### pkg over OTA-through-dock

Works with **no dock change**: dock's `resolvePluginUpdateViaRelease` forwards the
resolve manifest **verbatim** (`Manifest json.RawMessage`), so `format`/`entrypoint`
survive the heartbeat directive untouched. A self-updating plugin opts in by
calling `sdk.SelfApply(res.Update, selfBinPath(), selfDestRoot())` instead of
`SelfUpdate` — binary releases behave exactly as before; archive releases are
unpacked + run their entrypoint. (polar-buildings is the reference adopter.)

## Deploy (macOS launchd)

```bash
createdb -O ideamesh polar_release        # schema auto-applies at boot
openssl rand -hex 32                        # → POLAR_RELEASE_SIGNING_KEY (persist it!)
# mint a 'release' plugin token in dock /admin-plugins.html
cp scripts/launchd/release-svc.env.sample ~/.local/share/release-svc.env   # edit, chmod 600
install -m755 release-svc scripts/launchd/polar-release-svc-launch.sh /Users/local/.local/bin/
cp scripts/launchd/polar.release-svc.plist ~/Library/LaunchAgents/
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/polar.release-svc.plist
curl -sf http://127.0.0.1:8101/healthz
```

## Build

```bash
go test ./...
CGO_ENABLED=0 go build -o release-svc ./cmd/release-svc
CGO_ENABLED=0 go build -o release-publish ./cmd/release-publish
```
