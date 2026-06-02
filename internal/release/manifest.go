package release

// manifest.go — the signed unit of a release. The ed25519 signature is
// computed over a DETERMINISTIC byte encoding of the manifest (see
// canonicalBytes), so a verifier that reconstructs the same fields gets
// the same bytes and can check authenticity with the trusted public key.
//
// What's signed (and therefore tamper-evident): module, version,
// channel, platform, sha256, size. A compromised storage provider can
// swap the bytes, but without the release private key it cannot forge a
// signature that matches the new content hash — so a client that checks
// both (sha256 == provider content key) AND (sig valid over the
// manifest) is protected. Same principle as Apple img4 personalization.

import (
	"encoding/json"
	"strconv"
)

// Manifest is the metadata describing one published artifact. Stored as
// jsonb in the releases row and returned (verbatim) by /release/resolve
// so the client can re-derive canonicalBytes and verify the signature.
type Manifest struct {
	Module   string `json:"module"`
	Version  string `json:"version"`
	Channel  string `json:"channel"`
	Platform string `json:"platform"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	MinHost  string `json:"min_host,omitempty"`
	Notes    string `json:"notes,omitempty"`
}

// canonicalBytes is the exact byte string the ed25519 signature covers.
// It is a fixed-order, newline-delimited, field-prefixed encoding —
// NOT json.Marshal (whose key order + whitespace are not guaranteed
// stable across languages). A verifier in any language reproduces this
// from the manifest fields. Only the security-relevant fields are
// included; notes are advisory and intentionally NOT signed (so they
// can be edited without re-signing).
func (m Manifest) canonicalBytes() []byte {
	s := "polar-release/v1\n" +
		"module=" + m.Module + "\n" +
		"version=" + m.Version + "\n" +
		"channel=" + m.Channel + "\n" +
		"platform=" + m.Platform + "\n" +
		"sha256=" + m.SHA256 + "\n" +
		"size=" + strconv.FormatInt(m.Size, 10) + "\n" +
		"min_host=" + m.MinHost + "\n"
	return []byte(s)
}

// marshalJSON returns the manifest as compact JSON for storage.
func (m Manifest) marshalJSON() ([]byte, error) { return json.Marshal(m) }
