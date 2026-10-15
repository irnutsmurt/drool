// Package release implements signed DROOL agent releases (ARCHITECTURE-ALPHA
// §23.10). It is a trust system of its own: the release-signing key is not
// the Agent CA, not the browser TLS key and not an OpenSSH CA, and its
// private half never lives on the controller. Agents verify release
// manifests and binaries themselves, so a compromised controller without
// the signing key can't push arbitrary code as an "update".
package release

import (
	"bytes"
	"cmp"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Product is the only product release manifests may name.
const Product = "drool-agent"

// Manifest describes one agent build. It is the exact signed content.
type Manifest struct {
	Format      int       `json:"format"` // 1
	Product     string    `json:"product"`
	Version     string    `json:"version"`
	Channel     string    `json:"channel"` // stable, beta, ...
	OS          string    `json:"os"`
	Arch        string    `json:"arch"`
	ProtocolMin uint32    `json:"protocol_min"`
	ProtocolMax uint32    `json:"protocol_max"`
	SHA256      string    `json:"sha256"` // of the binary, hex
	Size        int64     `json:"size"`
	MinFrom     string    `json:"min_upgrade_from,omitempty"` // oldest version that may upgrade straight to this one
	IssuedAt    time.Time `json:"issued_at"`
}

// Signed is a manifest's exact bytes with one signature.
type Signed struct {
	Payload   []byte `json:"payload"`
	KeyID     string `json:"key_id"`
	Signature []byte `json:"signature"`
}

const signDomain = "drool-agent-release-v1\x00"

// KeyID is a short, stable identifier of a release public key.
func KeyID(pub ed25519.PublicKey) string {
	s := sha256.Sum256(pub)
	return hex.EncodeToString(s[:8])
}

// --- keys ---

// PublicKey is a trusted release public key with a label.
type PublicKey struct {
	Key  ed25519.PublicKey
	Name string
}

// ID returns the key id.
func (p PublicKey) ID() string { return KeyID(p.Key) }

const pubPrefix = "drool-release-ed25519"

// FormatPublicKey renders the one-line public key format:
// "drool-release-ed25519 <base64> <name>".
func FormatPublicKey(pub ed25519.PublicKey, name string) string {
	return strings.TrimSpace(pubPrefix + " " + base64.StdEncoding.EncodeToString(pub) + " " + name)
}

// ParsePublicKey parses the one-line format.
func ParsePublicKey(line string) (PublicKey, error) {
	f := strings.Fields(strings.TrimSpace(line))
	if len(f) < 2 || f[0] != pubPrefix {
		return PublicKey{}, errors.New(`not a DROOL release public key (expected "drool-release-ed25519 <base64> [name]")`)
	}
	b, err := base64.StdEncoding.DecodeString(f[1])
	if err != nil || len(b) != ed25519.PublicKeySize {
		return PublicKey{}, errors.New("invalid release public key")
	}
	return PublicKey{Key: ed25519.PublicKey(b), Name: strings.Join(f[2:], " ")}, nil
}

// ParsePublicKeys parses one key per non-empty, non-comment line.
func ParsePublicKeys(data []byte) ([]PublicKey, error) {
	var out []PublicKey
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		k, err := ParsePublicKey(l)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, nil
}

const privType = "DROOL RELEASE SIGNING KEY"

// GenerateKey creates a release-signing key pair; the private key is PEM.
func GenerateKey() (ed25519.PublicKey, []byte, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, nil, err
	}
	return pub, pem.EncodeToMemory(&pem.Block{Type: privType, Bytes: der}), nil
}

// ParsePrivateKey parses a release-signing private key. It refuses other
// PEM types, so an Agent CA or TLS key can't be used by mistake.
func ParsePrivateKey(data []byte) (ed25519.PrivateKey, error) {
	blk, _ := pem.Decode(data)
	if blk == nil || blk.Type != privType {
		return nil, errors.New("not a DROOL release-signing key (PEM type " + privType + " expected)")
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, err
	}
	ek, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("release-signing key is not Ed25519")
	}
	return ek, nil
}

// --- signing ---

// Sign builds and signs a manifest for the binary read from r.
func Sign(key ed25519.PrivateKey, m Manifest, r io.Reader) (*Signed, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return nil, err
	}
	m.Format, m.Product = 1, Product
	m.SHA256, m.Size = hex.EncodeToString(h.Sum(nil)), n
	if m.IssuedAt.IsZero() {
		m.IssuedAt = time.Now().UTC()
	}
	if err := m.check(); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	pub := key.Public().(ed25519.PublicKey)
	return &Signed{Payload: payload, KeyID: KeyID(pub), Signature: ed25519.Sign(key, append([]byte(signDomain), payload...))}, nil
}

var versionRE = regexp.MustCompile(`^v?\d+\.\d+\.\d+([-+][0-9A-Za-z.+-]+)?$`)

func (m *Manifest) check() error {
	switch {
	case m.Format != 1:
		return fmt.Errorf("unsupported release manifest format %d", m.Format)
	case m.Product != Product:
		return fmt.Errorf("release manifest is for %q, not %s", m.Product, Product)
	case !versionRE.MatchString(m.Version):
		return fmt.Errorf("invalid release version %q", m.Version)
	case m.OS == "" || m.Arch == "":
		return errors.New("release manifest lacks os/arch")
	case m.ProtocolMin == 0 || m.ProtocolMax < m.ProtocolMin:
		return errors.New("release manifest has an invalid protocol range")
	case len(m.SHA256) != 64 || m.Size <= 0:
		return errors.New("release manifest lacks the binary's sha256/size")
	case m.MinFrom != "" && !versionRE.MatchString(m.MinFrom):
		return fmt.Errorf("invalid min_upgrade_from %q", m.MinFrom)
	}
	if m.Channel == "" {
		m.Channel = "stable"
	}
	return nil
}

// ErrUntrustedKey means no trusted key matches the signature's key id.
var ErrUntrustedKey = errors.New("the release is signed by a key this agent does not trust")

// Verify checks the signature against trusted keys and returns the manifest.
func Verify(s *Signed, trusted []PublicKey) (*Manifest, error) {
	if s == nil || len(s.Payload) == 0 {
		return nil, errors.New("empty release manifest")
	}
	var key *PublicKey
	for i := range trusted {
		if trusted[i].ID() == s.KeyID {
			key = &trusted[i]
			break
		}
	}
	if key == nil {
		return nil, fmt.Errorf("%w (key id %s)", ErrUntrustedKey, s.KeyID)
	}
	if !ed25519.Verify(key.Key, append([]byte(signDomain), s.Payload...), s.Signature) {
		return nil, errors.New("release manifest signature is invalid")
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(s.Payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("release manifest: %w", err)
	}
	if err := m.check(); err != nil {
		return nil, err
	}
	return &m, nil
}

// CheckBinary verifies that the bytes read from r are exactly the binary
// the manifest describes.
func CheckBinary(m *Manifest, r io.Reader) error {
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(r, m.Size+1))
	if err != nil {
		return err
	}
	if n != m.Size {
		return fmt.Errorf("binary size %d does not match the signed size %d", n, m.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != m.SHA256 {
		return fmt.Errorf("binary sha256 %s does not match the signed %s", got[:16], m.SHA256[:16])
	}
	return nil
}

// --- versions ---

// CompareVersions compares "v1.2.3[-pre][+build]" versions: numeric
// fields first, then a release sorts after its pre-releases, and
// pre-releases by their parts (comparePre). Build metadata is ignored.
func CompareVersions(a, b string) int {
	pa, prea := splitVersion(a)
	pb, preb := splitVersion(b)
	for i := range 3 {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case prea == preb:
		return 0
	case prea == "":
		return 1
	case preb == "":
		return -1
	}
	return comparePre(prea, preb)
}

// comparePre orders pre-releases part by part, the parts separated by '.',
// '-' and the change from letters to digits: numbers numerically, words as
// text, a number before a word, and a prefix before what continues it. So
// "rc.10" is after "rc.9", and a development build git describe names
// "alpha-102-g…" after "alpha-97-g…" (comparing the whole text had it
// before once main passed 100 commits since the tag, and Shepherds refused
// the update as a downgrade).
func comparePre(a, b string) int {
	pa, pb := preParts(a), preParts(b)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, y := pa[i], pb[i]
		nx, errx := strconv.ParseUint(x, 10, 64)
		ny, erry := strconv.ParseUint(y, 10, 64)
		switch {
		case errx == nil && erry == nil:
			if nx != ny {
				return cmp.Compare(nx, ny)
			}
		case errx == nil:
			return -1
		case erry == nil:
			return 1
		case x != y:
			return strings.Compare(x, y)
		}
	}
	return cmp.Compare(len(pa), len(pb))
}

// preParts splits a pre-release at '.', '-' and where letters change to
// digits or back: "alpha-102-g87a8202" → alpha 102 g 87 a 8202.
func preParts(s string) []string {
	var out []string
	start := -1
	digit := func(c byte) bool { return c >= '0' && c <= '9' }
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '.' || s[i] == '-' {
			if start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
			continue
		}
		if start >= 0 && digit(s[i]) != digit(s[i-1]) {
			out = append(out, s[start:i])
			start = i
		}
		if start < 0 {
			start = i
		}
	}
	return out
}

func splitVersion(v string) ([3]int, string) {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	pre := ""
	if i := strings.IndexByte(v, '-'); i >= 0 {
		v, pre = v[:i], v[i+1:]
	}
	var out [3]int
	for i, f := range strings.SplitN(v, ".", 3) {
		out[i], _ = strconv.Atoi(f)
	}
	return out, pre
}
