package release

// signing.go — the release signing identity. polar-release holds an
// ed25519 private key; it signs every published manifest. Clients
// (agents, plugins, the marketplace) carry the matching PUBLIC key
// (pinned / rotated via dock) and verify, so a malicious or compromised
// storage provider cannot serve a binary the release service didn't
// bless — even with a valid-looking content hash.
//
// Key material: POLAR_RELEASE_SIGNING_KEY = hex of the 32-byte ed25519
// SEED. Generate one with:
//
//	openssl rand -hex 32
//
// If unset, a throwaway key is generated at boot and its public half is
// logged — fine for a dev box, but every restart rotates it, so set the
// env in any real deployment (clients pin the public key).

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
)

// Signer wraps an ed25519 private key + exposes the public half.
type Signer struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
}

// NewSignerFromSeedHex builds a Signer from a hex-encoded 32-byte seed.
// Empty seedHex returns (nil, errNoSeed) so the caller can decide to
// generate an ephemeral key + warn.
var errNoSeed = errors.New("release: no signing seed set")

func NewSignerFromSeedHex(seedHex string) (*Signer, error) {
	if seedHex == "" {
		return nil, errNoSeed
	}
	seed, err := hex.DecodeString(seedHex)
	if err != nil {
		return nil, fmt.Errorf("release: signing key not hex: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("release: signing seed must be %d bytes (got %d)", ed25519.SeedSize, len(seed))
	}
	priv := ed25519.NewKeyFromSeed(seed)
	return &Signer{priv: priv, pub: priv.Public().(ed25519.PublicKey)}, nil
}

// NewEphemeralSigner generates a fresh keypair (dev/boot fallback).
func NewEphemeralSigner() (*Signer, error) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, err
	}
	return &Signer{priv: priv, pub: pub}, nil
}

// PublicKeyHex is the value clients pin to verify manifests.
func (s *Signer) PublicKeyHex() string { return hex.EncodeToString(s.pub) }

// SignManifest returns the hex ed25519 signature over the manifest's
// canonical bytes.
func (s *Signer) SignManifest(m Manifest) string {
	return hex.EncodeToString(ed25519.Sign(s.priv, m.canonicalBytes()))
}

// VerifyManifest checks a hex signature against a manifest using a hex
// public key. Exposed so tests (and future client code) can verify the
// same way an agent would.
func VerifyManifest(pubHex, sigHex string, m Manifest) bool {
	pub, err := hex.DecodeString(pubHex)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), m.canonicalBytes(), sig)
}
