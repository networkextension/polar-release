package release

// attestation.go — the promotion "stamp". A signed assertion that a
// specific (module, version, platform) passed the stage gate and is
// approved to move into a fleet-facing channel (beta/stable).
//
// This is a SEPARATE trust chain from the release manifest: the manifest
// is signed by the release key (authenticity of bytes), the attestation
// is signed by an APPROVER key (a human or an AI reviewer) and verified
// against an allowlist (POLAR_RELEASE_APPROVER_KEYS). The registry
// refuses to promote into beta/stable without a valid attestation — so
// "tested on stage + stamped before publish" is enforced, not convention.

import "strconv"

// Attestation is the signed unit. The signature covers canonicalBytes
// (fixed-order, newline-delimited — same discipline as Manifest), so any
// signer/verifier in any language reproduces identical bytes.
type Attestation struct {
	Module       string `json:"module"`
	Version      string `json:"version"`
	Platform     string `json:"platform"`
	From         string `json:"from"`
	To           string `json:"to"`
	StageRunID   int64  `json:"stage_run_id"`
	ResultSHA256 string `json:"result_sha256"`
	Approver     string `json:"approver"`     // who stamped (name/id)
	ApprovedAt   string `json:"approved_at"`  // RFC3339, exact signed string
}

func (a Attestation) canonicalBytes() []byte {
	s := "polar-release/attestation/v1\n" +
		"module=" + a.Module + "\n" +
		"version=" + a.Version + "\n" +
		"platform=" + a.Platform + "\n" +
		"from=" + a.From + "\n" +
		"to=" + a.To + "\n" +
		"stage_run_id=" + strconv.FormatInt(a.StageRunID, 10) + "\n" +
		"result_sha256=" + a.ResultSHA256 + "\n" +
		"approver=" + a.Approver + "\n" +
		"approved_at=" + a.ApprovedAt + "\n"
	return []byte(s)
}
