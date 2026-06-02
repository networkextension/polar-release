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
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

func validChannel(ch string) bool {
	switch ch {
	case "stable", "beta", "dev":
		return true
	}
	return false
}

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
	r.Platform = strings.TrimSpace(r.Platform)
	r.SHA256 = strings.ToLower(strings.TrimSpace(r.SHA256))
}

func (r publishRequest) validate() string {
	switch {
	case r.Module == "" || r.Version == "" || r.Platform == "":
		return "module, version, platform required"
	case !validChannel(r.Channel):
		return "channel must be stable|beta|dev"
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
	if req.Module == "" || req.Version == "" || req.Platform == "" || !validChannel(req.Channel) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "module, version, platform, valid channel required"})
		return
	}
	if !isHexSHA256(req.SHA256) || req.AssetID <= 0 || req.ProviderID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "valid sha256, asset_id, provider_id required"})
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
