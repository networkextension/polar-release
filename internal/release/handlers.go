package release

// handlers.go — the REST surface (doc/arch/release-system.md §5).
//
// Publish is two-step so bytes bypass everything:
//   POST /release/publish  → {asset_id, put_url}   (mints a signed PUT)
//   PUT bytes → provider                            (publisher↔provider)
//   POST /release/finalize → {release}              (signs + registers)
//
// Resolve is the install/update read path:
//   GET /release/resolve?module&channel&platform
//     → {version, sha256, ed25519_sig, manifest, dl_url}  (signed GET)

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

func validChannel(ch string) bool {
	switch ch {
	case "stable", "beta", "stage", "dev":
		return true
	}
	return false
}

// gatedChannel reports whether promoting INTO ch requires a signed stamp.
// dev is the publish entry point and stage is the test target — both free;
// beta and stable are fleet-facing and gated.
func gatedChannel(ch string) bool { return ch == "beta" || ch == "stable" }

func isHexSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// ── pubkey ──────────────────────────────────────────────────────

func (p *Plugin) handlePubkey(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"algo":           "ed25519",
		"public_key_hex": p.Signer.PublicKeyHex(),
	})
}

// ── publish (step 1: mint upload grant) ─────────────────────────

type publishRequest struct {
	Module    string `json:"module"`
	Version   string `json:"version"`
	Channel   string `json:"channel"`
	Platform  string `json:"platform"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
	Mime      string `json:"mime"`
}

func (r *publishRequest) normalize() {
	r.Module = strings.TrimSpace(r.Module)
	r.Version = strings.TrimSpace(r.Version)
	r.Channel = strings.TrimSpace(r.Channel)
	if r.Channel == "" {
		r.Channel = "dev"
	}
	r.Platform = strings.TrimSpace(r.Platform)
	r.SHA256 = strings.ToLower(strings.TrimSpace(r.SHA256))
}

func (r publishRequest) validate() string {
	switch {
	case r.Module == "" || r.Version == "" || r.Platform == "":
		return "module, version, platform required"
	case r.Channel != "dev":
		// P2 gate: publish is the dev entry point only. Higher channels are
		// reached via /release/promote (which requires a stage-test + stamp).
		return "publish only to channel=dev; use /release/promote for stage|beta|stable"
	case !isHexSHA256(r.SHA256):
		return "sha256 must be 64 hex chars"
	case r.SizeBytes <= 0:
		return "size_bytes must be > 0"
	}
	return ""
}

func (p *Plugin) handlePublish(c *gin.Context) {
	var req publishRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad json: " + err.Error()})
		return
	}
	req.normalize()
	if msg := req.validate(); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
		return
	}
	grant, err := p.mintUploadGrant(req.Module, req.Platform, req.Version, req.SHA256, req.SizeBytes, req.Mime)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "upload grant failed: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"asset_id":    grant.AssetID,
		"sha256":      grant.SHA256,
		"exists":      grant.Exists,
		"put_url":     grant.PutURL,
		"provider_id": grant.ProviderID,
		"next":        "PUT the bytes to put_url (skip if exists=true), then POST /release/finalize",
	})
}

// ── finalize (step 2: confirm bytes + sign + register) ──────────

type finalizeRequest struct {
	Module     string `json:"module"`
	Version    string `json:"version"`
	Channel    string `json:"channel"`
	Platform   string `json:"platform"`
	AssetID    int64  `json:"asset_id"`
	ProviderID int64  `json:"provider_id"`
	SHA256     string `json:"sha256"`
	SizeBytes  int64  `json:"size_bytes"`
	MinHost    string `json:"min_host"`
	Notes      string `json:"notes"`
	Format     string `json:"format"`     // "" / "binary" / "tar.gz" / "tgz" / "zip"
	Entrypoint string `json:"entrypoint"` // script inside an archive (default install.sh)
}

// normalizeArtifactFormat canonicalizes a publisher-supplied format +
// entrypoint into the (format, entrypoint, errMsg) the manifest stores.
//   - "" / "binary"  → ("", "", "")  — the legacy byte-identical binary path
//   - archive format → (format, entrypoint-or-"install.sh", "")
//   - anything else  → ("", "", "<reason>")
//
// Keeping binary as "" (not "binary") means canonicalBytes for a plain
// binary is unchanged from before this field existed. Archive formats are
// the unpack-and-run-a-script bundles the SDK SelfInstall handles; an Apple
// .pkg is a distinct platform installer and is intentionally not one of them
// (ship a "pkg with an install script" as tar.gz/zip containing install.sh).
func normalizeArtifactFormat(format, entrypoint string) (string, string, string) {
	format = strings.ToLower(strings.TrimSpace(format))
	entrypoint = strings.TrimSpace(entrypoint)
	switch format {
	case "", "binary":
		return "", "", ""
	case "tar.gz", "tgz", "zip":
		if entrypoint == "" {
			entrypoint = "install.sh"
		}
		return format, entrypoint, ""
	default:
		return "", "", "format must be one of: binary, tar.gz, tgz, zip"
	}
}

func (p *Plugin) handleFinalize(c *gin.Context) {
	var req finalizeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad json: " + err.Error()})
		return
	}
	req.Module = strings.TrimSpace(req.Module)
	req.Version = strings.TrimSpace(req.Version)
	req.Channel = strings.TrimSpace(req.Channel)
	req.Platform = strings.TrimSpace(req.Platform)
	req.SHA256 = strings.ToLower(strings.TrimSpace(req.SHA256))
	if req.Channel == "" {
		req.Channel = "dev"
	}
	if req.Module == "" || req.Version == "" || req.Platform == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "module, version, platform required"})
		return
	}
	if req.Channel != "dev" {
		// P2 gate: same as publish — finalize only lands a release on dev.
		c.JSON(http.StatusBadRequest, gin.H{"error": "finalize only to channel=dev; use /release/promote for stage|beta|stable"})
		return
	}
	if !isHexSHA256(req.SHA256) || req.AssetID <= 0 || req.ProviderID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "valid sha256, asset_id, provider_id required"})
		return
	}
	format, entrypoint, fmtErr := normalizeArtifactFormat(req.Format, req.Entrypoint)
	if fmtErr != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmtErr})
		return
	}
	// 1. confirm the bytes landed on the provider (idempotent).
	if err := p.finalizeAsset(req.AssetID, req.ProviderID); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "asset finalize failed: " + err.Error()})
		return
	}
	// 2. build + sign the manifest.
	m := Manifest{
		Module: req.Module, Version: req.Version, Channel: req.Channel,
		Platform: req.Platform, SHA256: req.SHA256, Size: req.SizeBytes,
		MinHost: strings.TrimSpace(req.MinHost), Notes: strings.TrimSpace(req.Notes),
		Format: format, Entrypoint: entrypoint,
	}
	rel := &Release{
		Module: m.Module, Version: m.Version, Channel: m.Channel, Platform: m.Platform,
		ArtifactID: req.AssetID, SHA256: m.SHA256, Ed25519Sig: p.Signer.SignManifest(m), Manifest: m,
	}
	// 3. register.
	if err := p.insertRelease(rel); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "insert release: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"release": rel})
}

// ── resolve (install/update read) ───────────────────────────────

func (p *Plugin) handleResolve(c *gin.Context) {
	module := strings.TrimSpace(c.Query("module"))
	channel := strings.TrimSpace(c.Query("channel"))
	platform := strings.TrimSpace(c.Query("platform"))
	if channel == "" {
		channel = "stable"
	}
	if module == "" || platform == "" || !validChannel(channel) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "module, platform, valid channel required"})
		return
	}
	rel, err := p.latestRelease(module, channel, platform)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "lookup failed"})
		return
	}
	if rel == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no release for module/channel/platform"})
		return
	}
	dlURL, err := p.mintDownloadURL(rel.ArtifactID)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "download url failed: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"module":      rel.Module,
		"version":     rel.Version,
		"channel":     rel.Channel,
		"platform":    rel.Platform,
		"sha256":      rel.SHA256,
		"size":        rel.Manifest.Size,
		"ed25519_sig": rel.Ed25519Sig,
		"manifest":    rel.Manifest,
		"dl_url":      dlURL,
		"pubkey":      p.Signer.PublicKeyHex(),
	})
}

// ── yank ────────────────────────────────────────────────────────

type yankRequest struct {
	Module   string `json:"module"`
	Version  string `json:"version"`
	Channel  string `json:"channel"`
	Platform string `json:"platform"`
}

func (p *Plugin) handleYank(c *gin.Context) {
	var req yankRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad json"})
		return
	}
	if req.Module == "" || req.Version == "" || req.Platform == "" || !validChannel(req.Channel) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "module, version, platform, valid channel required"})
		return
	}
	n, err := p.yankRelease(req.Module, req.Version, req.Channel, req.Platform)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "yank failed"})
		return
	}
	if n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "no matching un-yanked release"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"yanked": n})
}

// ── list (admin inspection) ─────────────────────────────────────

func (p *Plugin) handleList(c *gin.Context) {
	module := strings.TrimSpace(c.Query("module"))
	if module == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "module required"})
		return
	}
	rows, err := p.listReleases(module, strings.TrimSpace(c.Query("channel")), strings.TrimSpace(c.Query("platform")))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"releases": rows})
}

// ── P2 promotion gate: stage-result · stage-run · stamp · promote ───

// stageResultRequest records the outcome of running the candidate on the
// stage zone (the full deploy + migration + smoke). result is opaque
// JSON; result_sha256 (computed here over the received bytes) is what the
// stamp binds, so a stamp can't be replayed against a different result.
type stageResultRequest struct {
	Module       string          `json:"module"`
	Version      string          `json:"version"`
	Platform     string          `json:"platform"`
	Passed       bool            `json:"passed"`
	MigrationsOK bool            `json:"migrations_ok"`
	Destructive  bool            `json:"destructive"`
	BackupRef    string          `json:"backup_ref"`
	Result       json.RawMessage `json:"result"`
}

func (p *Plugin) handleStageResult(c *gin.Context) {
	var req stageResultRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad json: " + err.Error()})
		return
	}
	req.Module = strings.TrimSpace(req.Module)
	req.Version = strings.TrimSpace(req.Version)
	req.Platform = strings.TrimSpace(req.Platform)
	if req.Module == "" || req.Version == "" || req.Platform == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "module, version, platform required"})
		return
	}
	result := req.Result
	if len(result) == 0 {
		result = json.RawMessage("{}")
	}
	sum := sha256.Sum256(result)
	resultSHA := hex.EncodeToString(sum[:])
	sr := &StageRun{
		Module: req.Module, Version: req.Version, Platform: req.Platform,
		Passed: req.Passed, MigrationsOK: req.MigrationsOK, Destructive: req.Destructive,
		BackupRef: strings.TrimSpace(req.BackupRef), Result: result, ResultSHA256: resultSHA,
	}
	if err := p.insertStageRun(sr); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "insert stage run: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"stage_run_id": sr.ID, "result_sha256": resultSHA, "passed": sr.Passed})
}

func (p *Plugin) handleStageRun(c *gin.Context) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Query("id")), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "valid id required"})
		return
	}
	sr, err := p.getStageRun(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "lookup failed"})
		return
	}
	if sr == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no stage run"})
		return
	}
	c.JSON(http.StatusOK, sr)
}

// stampRequest is a signed promotion attestation. The CLI builds the
// Attestation, signs canonicalBytes with an approver key, and posts it
// here; the registry only VERIFIES (against the allowlist) and stores.
type stampRequest struct {
	Module       string `json:"module"`
	Version      string `json:"version"`
	Platform     string `json:"platform"`
	From         string `json:"from"`
	To           string `json:"to"`
	StageRunID   int64  `json:"stage_run_id"`
	ResultSHA256 string `json:"result_sha256"`
	Approver     string `json:"approver"`
	ApproverKind string `json:"approver_kind"`
	ApprovedAt   string `json:"approved_at"`
	Signature    string `json:"signature"`
}

func (p *Plugin) handleStamp(c *gin.Context) {
	var req stampRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad json: " + err.Error()})
		return
	}
	req.Module = strings.TrimSpace(req.Module)
	req.Version = strings.TrimSpace(req.Version)
	req.Platform = strings.TrimSpace(req.Platform)
	req.From = strings.TrimSpace(req.From)
	req.To = strings.TrimSpace(req.To)
	if req.Module == "" || req.Version == "" || req.Platform == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "module, version, platform required"})
		return
	}
	if !validChannel(req.From) || !gatedChannel(req.To) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "from must be a valid channel; to must be beta|stable"})
		return
	}
	if p.Approvers == nil || p.Approvers.Len() == 0 {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no approver keys configured (POLAR_RELEASE_APPROVER_KEYS)"})
		return
	}
	sr, err := p.getStageRun(req.StageRunID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "stage run lookup failed"})
		return
	}
	if sr == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "stage run not found"})
		return
	}
	if !sr.Passed {
		c.JSON(http.StatusBadRequest, gin.H{"error": "stage run did not pass — cannot stamp"})
		return
	}
	if sr.ResultSHA256 != strings.TrimSpace(req.ResultSHA256) ||
		sr.Module != req.Module || sr.Version != req.Version || sr.Platform != req.Platform {
		c.JSON(http.StatusBadRequest, gin.H{"error": "stamp does not match the referenced stage run"})
		return
	}
	att := Attestation{
		Module: req.Module, Version: req.Version, Platform: req.Platform,
		From: req.From, To: req.To, StageRunID: req.StageRunID,
		ResultSHA256: sr.ResultSHA256, Approver: strings.TrimSpace(req.Approver),
		ApprovedAt: strings.TrimSpace(req.ApprovedAt),
	}
	if !p.Approvers.Verify(req.Signature, att) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "signature is not from a trusted approver"})
		return
	}
	kind := strings.TrimSpace(req.ApproverKind)
	if kind != "ai" {
		kind = "human"
	}
	if err := p.insertAttestation(att, kind, strings.TrimSpace(req.Signature)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "insert attestation: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"stamped": true, "from": att.From, "to": att.To, "approver": att.Approver, "kind": kind})
}

// promoteRequest moves a known version between channels. Into beta/stable
// it is refused unless a valid stamp exists. The artifact bytes are
// unchanged; the manifest is re-signed for the new channel.
type promoteRequest struct {
	Module   string `json:"module"`
	Version  string `json:"version"`
	Platform string `json:"platform"`
	From     string `json:"from"`
	To       string `json:"to"`
}

func (p *Plugin) handlePromote(c *gin.Context) {
	var req promoteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad json: " + err.Error()})
		return
	}
	req.Module = strings.TrimSpace(req.Module)
	req.Version = strings.TrimSpace(req.Version)
	req.Platform = strings.TrimSpace(req.Platform)
	req.From = strings.TrimSpace(req.From)
	req.To = strings.TrimSpace(req.To)
	if req.Module == "" || req.Version == "" || req.Platform == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "module, version, platform required"})
		return
	}
	if !validChannel(req.From) || !validChannel(req.To) || req.From == req.To {
		c.JSON(http.StatusBadRequest, gin.H{"error": "from/to must be distinct valid channels"})
		return
	}
	if req.To == "dev" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "dev is the publish entry point; cannot promote into it"})
		return
	}
	// THE GATE: beta/stable require a valid stamp over a passed stage run.
	if gatedChannel(req.To) {
		ok, err := p.findValidAttestation(req.Module, req.Version, req.Platform, req.From, req.To)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "attestation lookup failed"})
			return
		}
		if !ok {
			c.JSON(http.StatusForbidden, gin.H{"error": "refused: no valid stamp for " + req.From + "→" + req.To + " — run stage-test + stamp first"})
			return
		}
	}
	src, err := p.getReleaseByVersion(req.Module, req.Version, req.From, req.Platform)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "source lookup failed"})
		return
	}
	if src == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no " + req.Module + " " + req.Version + " on " + req.From + "/" + req.Platform})
		return
	}
	// re-sign the manifest for the target channel (channel is signed).
	m := src.Manifest
	m.Channel = req.To
	rel := &Release{
		Module: m.Module, Version: m.Version, Channel: req.To, Platform: m.Platform,
		ArtifactID: src.ArtifactID, SHA256: src.SHA256, Ed25519Sig: p.Signer.SignManifest(m), Manifest: m,
	}
	if err := p.insertRelease(rel); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "insert release: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"promoted": true, "from": req.From, "to": req.To, "release": rel})
}
