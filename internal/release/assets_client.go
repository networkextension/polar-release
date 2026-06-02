package release

// assets_client.go — release's thin client onto dock's data-plane
// surface (/internal/v1/assets/*, added in polar-dock #395). release
// NEVER streams artifact bytes; it only mints grants + reads metadata.
// The bytes flow publisher↔provider (upload) and client↔provider
// (download) directly. Auth is the plugin HMAC the sdk.Client already
// carries (release authenticates to dock as plugin "release").

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// uploadGrant is the dock response to POST /internal/v1/assets/upload-grant.
type uploadGrant struct {
	AssetID    int64  `json:"asset_id"`
	SHA256     string `json:"sha256"`
	Exists     bool   `json:"exists"`
	PutURL     string `json:"put_url"`
	ProviderID int64  `json:"provider_id"`
}

// mintUploadGrant asks dock to allocate a content-addressed platform
// asset (workspace_id empty → platform-owned) and, unless the bytes are
// already present, return a scoped signed-PUT URL the publisher uses to
// upload straight to the provider.
func (p *Plugin) mintUploadGrant(module, platform, version, sha256 string, size int64, mime string) (*uploadGrant, error) {
	if mime == "" {
		mime = "application/octet-stream"
	}
	body := map[string]any{
		"workspace_id": "", // platform-owned artifact
		"kind":         "release",
		"name":         fmt.Sprintf("%s/%s", module, platform),
		"version":      version,
		"sha256":       sha256,
		"size_bytes":   size,
		"mime":         mime,
		"visibility":   "public",
		"uploaded_by":  "release",
	}
	resp, err := p.Dock.Do(http.MethodPost, "/internal/v1/assets/upload-grant", body)
	if err != nil {
		return nil, err
	}
	var g uploadGrant
	if err := decodeJSON(resp, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// finalizeAsset tells dock the bytes landed on the provider (flip the
// asset location to ready). No-op-safe to call when exists=true.
func (p *Plugin) finalizeAsset(assetID, providerID int64) error {
	resp, err := p.Dock.Do(http.MethodPost, "/internal/v1/assets/finalize", map[string]any{
		"asset_id":    assetID,
		"provider_id": providerID,
	})
	if err != nil {
		return err
	}
	return decodeJSON(resp, nil)
}

// mintDownloadURL asks dock for a short-lived signed provider GET URL so
// the client streams the artifact straight from the provider.
func (p *Plugin) mintDownloadURL(assetID int64) (string, error) {
	resp, err := p.Dock.Do(http.MethodGet,
		"/internal/v1/assets/"+strconv.FormatInt(assetID, 10)+"/download-url?workspace_id="+url.QueryEscape(""), nil)
	if err != nil {
		return "", err
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := decodeJSON(resp, &out); err != nil {
		return "", err
	}
	return out.URL, nil
}

// decodeJSON reads a 2xx body into out (out may be nil), else returns an
// error wrapping the body.
func decodeJSON(resp *http.Response, out any) error {
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("dock HTTP %d: %s", resp.StatusCode, string(b))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}
