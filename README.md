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
POST /release/finalize   {…,asset_id,provider_id,min_host?,notes?}                  # → {release}            (admin)
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
