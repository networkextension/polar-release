// Command polar-install is the reference PULL installer: it resolves a module
// release from polar-release and installs it locally — the consumer counterpart
// to release-publish. It needs no dock session (resolve is public); authenticity
// comes from sha256 + the ed25519 manifest signature.
//
//	polar-install -base https://release.example -module buildings \
//	    -channel stable -platform darwin-arm64 -dest ~/.local/polar/buildings
//
// For an archive (format=tar.gz/tgz/zip) it unpacks into <dest>/versions/<ver>/,
// runs the manifest entrypoint, and repoints <dest>/current. For a plain binary
// it swaps -bin (which must be set) and the process exits.
//
// Pin POLAR_RELEASE_PUBKEY to enforce the signature (fail-closed); unset =
// sha256-only. -base / -platform fall back to POLAR_RELEASE_BASE and the host's
// runtime GOOS-GOARCH.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"

	sdk "github.com/networkextension/polar-sdk"
)

func main() {
	var (
		base     = flag.String("base", os.Getenv("POLAR_RELEASE_BASE"), "release-svc base URL")
		module   = flag.String("module", "", "module name, e.g. buildings")
		channel  = flag.String("channel", "stable", "stable|beta|stage|dev")
		platform = flag.String("platform", runtime.GOOS+"-"+runtime.GOARCH, "e.g. darwin-arm64")
		dest     = flag.String("dest", "", "install root for an archive (versions/ + current live here)")
		bin      = flag.String("bin", "", "target path for a binary artifact (required for format=binary)")
	)
	flag.Parse()
	if *base == "" || *module == "" {
		fmt.Fprintln(os.Stderr, "missing required flag (base, module)")
		flag.Usage()
		os.Exit(2)
	}
	*base = strings.TrimRight(*base, "/")

	// resolve (public) → the signed release coordinates + a short-lived dl_url.
	reqURL := fmt.Sprintf("%s/release/resolve?module=%s&channel=%s&platform=%s",
		*base, url.QueryEscape(*module), url.QueryEscape(*channel), url.QueryEscape(*platform))
	resp, err := http.Get(reqURL)
	must(err, "resolve")
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		fatal(fmt.Sprintf("resolve HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body))))
	}

	var r struct {
		Version    string              `json:"version"`
		SHA256     string              `json:"sha256"`
		DLURL      string              `json:"dl_url"`
		Ed25519Sig string              `json:"ed25519_sig"`
		Manifest   sdk.ReleaseManifest `json:"manifest"`
		Pubkey     string              `json:"pubkey"`
	}
	must(json.Unmarshal(body, &r), "decode resolve")
	if r.Version == "" || r.DLURL == "" {
		fatal("resolve returned no version/dl_url")
	}
	fmt.Printf("resolved: %s %s (%s) format=%q sha=%s\n",
		*module, r.Version, *channel, orBinary(r.Manifest.Format), r.SHA256)
	if os.Getenv("POLAR_RELEASE_PUBKEY") == "" {
		fmt.Fprintln(os.Stderr, "warning: POLAR_RELEASE_PUBKEY not set — sha256-only (no authenticity check). resolve pubkey="+r.Pubkey)
	}

	d := &sdk.UpdateDirective{
		Version:    r.Version,
		URL:        r.DLURL,
		SHA256:     r.SHA256,
		Ed25519Sig: r.Ed25519Sig,
		Manifest:   &r.Manifest,
	}

	installed, err := sdk.SelfApply(d, *bin, *dest)
	if err != nil {
		fatal("install: " + err.Error())
	}
	// Only archives return here (a binary swap exits the process inside SelfApply).
	fmt.Printf("installed %s %s -> %s\n", *module, r.Version, installed)
}

func orBinary(f string) string {
	if strings.TrimSpace(f) == "" {
		return "binary"
	}
	return f
}

func must(err error, ctx string) {
	if err != nil {
		fatal(ctx + ": " + err.Error())
	}
}
func fatal(msg string) { fmt.Fprintln(os.Stderr, "polar-install: "+msg); os.Exit(1) }
