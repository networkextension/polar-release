package release

import (
	"strings"
	"testing"
)

func TestCanonicalBytes_Deterministic(t *testing.T) {
	m := sampleManifest()
	if string(m.canonicalBytes()) != string(m.canonicalBytes()) {
		t.Fatal("canonicalBytes not stable across calls")
	}
}

func TestCanonicalBytes_CoversSecurityFields(t *testing.T) {
	m := sampleManifest()
	got := string(m.canonicalBytes())
	for _, want := range []string{
		"module=buildings", "version=0.0.3", "channel=stable",
		"platform=darwin-arm64", "sha256=" + strings.Repeat("ab", 32), "size=1234",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("canonical missing %q\n%s", want, got)
		}
	}
	if strings.Contains(got, "fix keepalive") {
		t.Error("notes must NOT be part of the signed canonical bytes")
	}
}

func TestCanonicalBytes_SHAChangesBytes(t *testing.T) {
	a := sampleManifest()
	b := sampleManifest()
	b.SHA256 = strings.Repeat("cd", 32)
	if string(a.canonicalBytes()) == string(b.canonicalBytes()) {
		t.Fatal("different sha256 produced identical canonical bytes")
	}
}
