package release

import (
	"strings"
	"testing"
)

// TestCanonicalBytes_LegacyBinaryGolden pins the EXACT byte string a binary
// (no format/entrypoint) manifest signs. If this ever changes, every release
// signed before the format field existed stops verifying — so this golden is
// a tripwire, not a mere snapshot.
func TestCanonicalBytes_LegacyBinaryGolden(t *testing.T) {
	m := Manifest{
		Module: "buildings", Version: "0.0.3", Channel: "stable",
		Platform: "darwin-arm64", SHA256: strings.Repeat("ab", 32), Size: 1234,
		MinHost: "0.5.0", Notes: "fix keepalive",
		// Format/Entrypoint deliberately empty — the legacy case.
	}
	want := "polar-release/v1\n" +
		"module=buildings\n" +
		"version=0.0.3\n" +
		"channel=stable\n" +
		"platform=darwin-arm64\n" +
		"sha256=" + strings.Repeat("ab", 32) + "\n" +
		"size=1234\n" +
		"min_host=0.5.0\n"
	if got := string(m.canonicalBytes()); got != want {
		t.Fatalf("legacy canonical bytes drifted — old signatures will break\n got:\n%q\nwant:\n%q", got, want)
	}
}

// TestCanonicalBytes_ArchiveSignsFormat confirms format+entrypoint ARE part of
// the signed bytes for an archive release (so a provider can't flip a binary
// into a script-running pkg without breaking the signature), and that they are
// appended after the legacy fields.
func TestCanonicalBytes_ArchiveSignsFormat(t *testing.T) {
	m := sampleManifest()
	m.Format = "tar.gz"
	m.Entrypoint = "install.sh"
	got := string(m.canonicalBytes())
	if !strings.Contains(got, "format=tar.gz\n") || !strings.Contains(got, "entrypoint=install.sh\n") {
		t.Fatalf("archive canonical bytes missing format/entrypoint:\n%s", got)
	}
	// Flipping format must change the signed bytes.
	bin := sampleManifest()
	if string(bin.canonicalBytes()) == got {
		t.Fatal("archive manifest produced identical bytes to a binary one")
	}
}

// TestSign_ArchiveRoundtrip + tamper: an archive manifest verifies, and
// silently changing its format breaks the signature.
func TestSign_ArchiveRoundtripAndTamper(t *testing.T) {
	s, err := NewEphemeralSigner()
	if err != nil {
		t.Fatal(err)
	}
	m := sampleManifest()
	m.Format, m.Entrypoint = "tar.gz", "install.sh"
	sig := s.SignManifest(m)
	if !VerifyManifest(s.PublicKeyHex(), sig, m) {
		t.Fatal("archive signature failed to verify")
	}
	tampered := m
	tampered.Format = "binary" // attacker tries to downgrade the install path
	if VerifyManifest(s.PublicKeyHex(), sig, tampered) {
		t.Fatal("tampered format verified — consumer could be tricked about install mode")
	}
}

func TestNormalizeArtifactFormat(t *testing.T) {
	cases := []struct {
		inFmt, inEntry     string
		wantFmt, wantEntry string
		wantErr            bool
	}{
		{"", "", "", "", false},                         // legacy binary
		{"binary", "", "", "", false},                   // explicit binary → "" (byte-identical)
		{"binary", "ignored.sh", "", "", false},         // entrypoint dropped for binary
		{"tar.gz", "", "tar.gz", "install.sh", false},   // default entrypoint
		{"TGZ", " setup.sh ", "tgz", "setup.sh", false}, // lowercased + trimmed
		{"zip", "run.sh", "zip", "run.sh", false},
		{"pkg", "", "", "", true}, // Apple .pkg not a generic bundle
		{"rpm", "", "", "", true}, // unsupported
	}
	for _, c := range cases {
		gotFmt, gotEntry, errMsg := normalizeArtifactFormat(c.inFmt, c.inEntry)
		if (errMsg != "") != c.wantErr {
			t.Errorf("normalize(%q,%q) errMsg=%q wantErr=%v", c.inFmt, c.inEntry, errMsg, c.wantErr)
			continue
		}
		if !c.wantErr && (gotFmt != c.wantFmt || gotEntry != c.wantEntry) {
			t.Errorf("normalize(%q,%q) = (%q,%q) want (%q,%q)", c.inFmt, c.inEntry, gotFmt, gotEntry, c.wantFmt, c.wantEntry)
		}
	}
}
