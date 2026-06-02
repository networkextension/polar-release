package release

import (
	"crypto/ed25519"
	"encoding/hex"
	"strings"
	"testing"
)

func sampleManifest() Manifest {
	return Manifest{
		Module: "buildings", Version: "0.0.3", Channel: "stable",
		Platform: "darwin-arm64", SHA256: strings.Repeat("ab", 32), Size: 1234,
		MinHost: "0.5.0", Notes: "fix keepalive",
	}
}

func TestSign_Verify_Roundtrip(t *testing.T) {
	s, err := NewEphemeralSigner()
	if err != nil {
		t.Fatal(err)
	}
	m := sampleManifest()
	sig := s.SignManifest(m)
	if !VerifyManifest(s.PublicKeyHex(), sig, m) {
		t.Fatal("fresh signature failed to verify")
	}
}

func TestVerify_RejectsTamperedSHA(t *testing.T) {
	s, _ := NewEphemeralSigner()
	m := sampleManifest()
	sig := s.SignManifest(m)
	m.SHA256 = strings.Repeat("cd", 32) // attacker swaps content hash
	if VerifyManifest(s.PublicKeyHex(), sig, m) {
		t.Fatal("tampered sha256 verified — provider could swap bytes")
	}
}

func TestVerify_RejectsWrongKey(t *testing.T) {
	s1, _ := NewEphemeralSigner()
	s2, _ := NewEphemeralSigner()
	m := sampleManifest()
	if VerifyManifest(s2.PublicKeyHex(), s1.SignManifest(m), m) {
		t.Fatal("signature verified under the wrong public key")
	}
}

// Notes are advisory and NOT covered by the signature — editing them
// must not break verification.
func TestVerify_IgnoresNotes(t *testing.T) {
	s, _ := NewEphemeralSigner()
	m := sampleManifest()
	sig := s.SignManifest(m)
	m.Notes = "totally different notes"
	if !VerifyManifest(s.PublicKeyHex(), sig, m) {
		t.Fatal("changing notes broke verification; notes must not be signed")
	}
}

func TestSignerFromSeedHex_Deterministic(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i)
	}
	hexSeed := hex.EncodeToString(seed)
	a, err := NewSignerFromSeedHex(hexSeed)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewSignerFromSeedHex(hexSeed)
	if err != nil {
		t.Fatal(err)
	}
	if a.PublicKeyHex() != b.PublicKeyHex() {
		t.Fatal("same seed produced different public keys")
	}
	m := sampleManifest()
	if a.SignManifest(m) != b.SignManifest(m) {
		t.Fatal("same seed produced different signatures")
	}
}

func TestSignerFromSeedHex_Rejects(t *testing.T) {
	if _, err := NewSignerFromSeedHex(""); err != errNoSeed {
		t.Errorf("empty seed should return errNoSeed, got %v", err)
	}
	if _, err := NewSignerFromSeedHex("nothex"); err == nil {
		t.Error("non-hex seed should error")
	}
	if _, err := NewSignerFromSeedHex("abcd"); err == nil {
		t.Error("short seed should error")
	}
}
