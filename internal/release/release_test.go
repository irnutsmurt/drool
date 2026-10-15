package release

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"

	"drool/internal/agentpki"
)

func TestSignVerify(t *testing.T) {
	pub, privPEM, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	priv, err := ParsePrivateKey(privPEM)
	if err != nil {
		t.Fatal(err)
	}
	bin := []byte("#!fake agent binary\x00\x01")
	s, err := Sign(priv, Manifest{Version: "v0.2.0", OS: "linux", Arch: "amd64", ProtocolMin: 1, ProtocolMax: 1}, bytes.NewReader(bin))
	if err != nil {
		t.Fatal(err)
	}
	trusted := []PublicKey{{Key: pub, Name: "test"}}
	m, err := Verify(s, trusted)
	if err != nil {
		t.Fatal(err)
	}
	if m.Channel != "stable" || m.Size != int64(len(bin)) {
		t.Errorf("manifest %+v", m)
	}
	if err := CheckBinary(m, bytes.NewReader(bin)); err != nil {
		t.Errorf("binary: %v", err)
	}
	// Altered binary.
	alt := append([]byte(nil), bin...)
	alt[0] ^= 1
	if err := CheckBinary(m, bytes.NewReader(alt)); err == nil {
		t.Error("altered binary accepted")
	}
	if err := CheckBinary(m, bytes.NewReader(append(bin, 'x'))); err == nil {
		t.Error("longer binary accepted")
	}
	// Altered manifest.
	bad := *s
	bad.Payload = bytes.Replace(s.Payload, []byte(`"arch":"amd64"`), []byte(`"arch":"arm64"`), 1)
	if _, err := Verify(&bad, trusted); err == nil {
		t.Error("altered manifest accepted")
	}
	// Wrong key.
	other, _, _ := GenerateKey()
	if _, err := Verify(s, []PublicKey{{Key: other}}); !errors.Is(err, ErrUntrustedKey) {
		t.Errorf("untrusted key: %v", err)
	}
	// Same key id but different key (forged id) fails the signature.
	forged := *s
	_, fpriv, _ := ed25519.GenerateKey(nil)
	forged.Signature = ed25519.Sign(fpriv, append([]byte(signDomain), s.Payload...))
	if _, err := Verify(&forged, trusted); err == nil {
		t.Error("signature by another key accepted")
	}
}

func TestKeyFormats(t *testing.T) {
	pub, _, _ := GenerateKey()
	line := FormatPublicKey(pub, "my builds")
	k, err := ParsePublicKey(line)
	if err != nil || !k.Key.Equal(pub) || k.Name != "my builds" {
		t.Fatalf("round trip: %v %+v", err, k)
	}
	keys, err := ParsePublicKeys([]byte("# comment\n\n" + line + "\n"))
	if err != nil || len(keys) != 1 {
		t.Fatalf("parse list: %v", err)
	}
	// An Agent CA key is not a release key.
	_, caKey := agentpki.NewKey()
	if _, err := ParsePrivateKey(agentpki.KeyPEM(caKey)); err == nil || !strings.Contains(err.Error(), "not a DROOL release-signing key") {
		t.Errorf("CA key accepted as release key: %v", err)
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.2.0", "v0.1.9", 1},
		{"0.2.0", "v0.2.0", 0},
		{"v0.2.0-rc1", "v0.2.0", -1},
		{"v0.2.0+abc", "v0.2.0", 0},
		{"v1.0.0", "v0.99.99", 1},
		{"v0.2.0-rc2", "v0.2.0-rc1", 1},
		// Pre-releases part by part, numbers as numbers: release
		// candidates past 9, and development builds git describe names
		// past 100 commits since the tag (CI's release qualification found
		// Shepherds refusing alpha-102 as older than alpha-22).
		{"v1.0.0-rc.10", "v1.0.0-rc.9", 1},
		{"v1.0.0-rc.2", "v1.0.0-rc.10", -1},
		{"v1.0.0-rc10", "v1.0.0-rc9", 1},
		{"v0.1.0-alpha-102-g87a8202", "v0.1.0-alpha-22-g75f2f41", 1},
		{"v0.1.0-alpha-102-g87a8202", "v0.1.0-alpha-97-g1449509", 1},
		{"v0.1.0-alpha-3-gabc1234", "v0.1.0-alpha", 1},
		{"v0.1.0-alpha-102-g87a8202", "v0.1.0-alpha-102-g87a8202", 0},
		{"v1.0.0-1", "v1.0.0-alpha", -1}, // a number before a word
		{"v1.0.0-beta", "v1.0.0-alpha-500-gffffff", 1},
		{"v1.0.0-rc.1", "v1.0.0", -1},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
