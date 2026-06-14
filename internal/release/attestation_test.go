package release

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"
)

func TestAttestationCanonicalStable(t *testing.T) {
	a := Attestation{
		Module: "hosts", Version: "abc123", Platform: "darwin-arm64",
		From: "stage", To: "beta", StageRunID: 7, ResultSHA256: "deadbeef",
		Approver: "ci-ai", ApprovedAt: "2026-06-14T10:00:00Z",
	}
	want := "polar-release/attestation/v1\n" +
		"module=hosts\nversion=abc123\nplatform=darwin-arm64\n" +
		"from=stage\nto=beta\nstage_run_id=7\nresult_sha256=deadbeef\n" +
		"approver=ci-ai\napproved_at=2026-06-14T10:00:00Z\n"
	if got := string(a.canonicalBytes()); got != want {
		t.Fatalf("canonicalBytes mismatch:\n got=%q\nwant=%q", got, want)
	}
}

func TestApproverSetVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	other, _, _ := ed25519.GenerateKey(nil)
	a := Attestation{Module: "m", Version: "v", Platform: "p", From: "stage", To: "beta", StageRunID: 1, ResultSHA256: "h", Approver: "x", ApprovedAt: "t"}
	sig := hex.EncodeToString(ed25519.Sign(priv, a.canonicalBytes()))

	// allowlist contains the signer → verifies
	set := NewApproverSet(hex.EncodeToString(other) + "," + hex.EncodeToString(pub))
	if set.Len() != 2 {
		t.Fatalf("expected 2 keys, got %d", set.Len())
	}
	if !set.Verify(sig, a) {
		t.Error("valid signature from allowlisted key should verify")
	}

	// allowlist WITHOUT the signer → rejects
	only := NewApproverSet(hex.EncodeToString(other))
	if only.Verify(sig, a) {
		t.Error("signature from non-allowlisted key must be rejected")
	}

	// tampered attestation (different field) → rejects
	a2 := a
	a2.To = "stable"
	if set.Verify(sig, a2) {
		t.Error("signature must not verify against a mutated attestation")
	}

	// garbage signature → rejects, no panic
	if set.Verify("zzzz", a) {
		t.Error("non-hex signature must be rejected")
	}
}
