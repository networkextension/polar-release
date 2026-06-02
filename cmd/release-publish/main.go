// Command release-publish is the reference publisher: it cuts a release
// in three hops, keeping the bytes off both dock and release —
//
//	1. POST /release/publish   → {asset_id, put_url}
//	2. PUT the file → provider  (direct, only if not already present)
//	3. POST /release/finalize  → the signed, registered release
//
// Usage:
//
//	release-publish -base https://release.example -token <admin-bearer> \
//	    -module buildings -version 0.0.3 -channel stable \
//	    -platform darwin-arm64 -file ./buildings-svc [-notes "fix X"]
//
// -base / -token fall back to POLAR_RELEASE_BASE / POLAR_RELEASE_ADMIN_TOKEN.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

func main() {
	var (
		base     = flag.String("base", os.Getenv("POLAR_RELEASE_BASE"), "release-svc base URL")
		token    = flag.String("token", os.Getenv("POLAR_RELEASE_ADMIN_TOKEN"), "admin/owner dock session token")
		module   = flag.String("module", "", "module name, e.g. buildings")
		version  = flag.String("version", "", "semver, e.g. 0.0.3")
		channel  = flag.String("channel", "stable", "stable|beta|dev")
		platform = flag.String("platform", "", "e.g. darwin-arm64")
		file     = flag.String("file", "", "artifact file to publish")
		mime     = flag.String("mime", "application/octet-stream", "content-type")
		minHost  = flag.String("min-host", "", "optional minimum host version")
		notes    = flag.String("notes", "", "optional release notes")
	)
	flag.Parse()
	if *base == "" || *token == "" || *module == "" || *version == "" || *platform == "" || *file == "" {
		fmt.Fprintln(os.Stderr, "missing required flag (base, token, module, version, platform, file)")
		flag.Usage()
		os.Exit(2)
	}
	*base = strings.TrimRight(*base, "/")

	// hash + size
	f, err := os.Open(*file)
	must(err, "open file")
	h := sha256.New()
	size, err := io.Copy(h, f)
	must(err, "hash file")
	f.Close()
	sha := hex.EncodeToString(h.Sum(nil))
	fmt.Printf("artifact: sha256=%s size=%d\n", sha, size)

	// 1. publish → grant
	grantBody := map[string]any{
		"module": *module, "version": *version, "channel": *channel,
		"platform": *platform, "sha256": sha, "size_bytes": size, "mime": *mime,
	}
	var grant struct {
		AssetID    int64  `json:"asset_id"`
		Exists     bool   `json:"exists"`
		PutURL     string `json:"put_url"`
		ProviderID int64  `json:"provider_id"`
	}
	doJSON("POST", *base+"/release/publish", *token, grantBody, &grant)
	fmt.Printf("grant: asset_id=%d exists=%v\n", grant.AssetID, grant.Exists)

	// 2. PUT bytes direct to provider (skip on dedup)
	if !grant.Exists && grant.PutURL != "" {
		f2, err := os.Open(*file)
		must(err, "reopen file")
		req, _ := http.NewRequest(http.MethodPut, grant.PutURL, f2)
		req.Header.Set("Content-Type", *mime)
		req.ContentLength = size
		resp, err := http.DefaultClient.Do(req)
		f2.Close()
		must(err, "PUT bytes")
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			fatal(fmt.Sprintf("provider PUT HTTP %d: %s", resp.StatusCode, body))
		}
		fmt.Println("bytes: uploaded direct to provider")
	} else {
		fmt.Println("bytes: already present (dedup) — skipping upload")
	}

	// 3. finalize → signed, registered release
	finBody := map[string]any{
		"module": *module, "version": *version, "channel": *channel, "platform": *platform,
		"asset_id": grant.AssetID, "provider_id": grant.ProviderID,
		"sha256": sha, "size_bytes": size, "min_host": *minHost, "notes": *notes,
	}
	var out map[string]any
	doJSON("POST", *base+"/release/finalize", *token, finBody, &out)
	pretty, _ := json.MarshalIndent(out, "", "  ")
	fmt.Printf("published:\n%s\n", pretty)
}

func doJSON(method, url, token string, body any, out any) {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	must(err, method+" "+url)
	respB, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		fatal(fmt.Sprintf("%s %s -> HTTP %d: %s", method, url, resp.StatusCode, respB))
	}
	if out != nil {
		must(json.Unmarshal(respB, out), "decode response")
	}
}

func must(err error, ctx string) {
	if err != nil {
		fatal(ctx + ": " + err.Error())
	}
}
func fatal(msg string) { fmt.Fprintln(os.Stderr, "release-publish: "+msg); os.Exit(1) }
