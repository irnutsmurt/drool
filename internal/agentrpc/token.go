package agentrpc

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

// Bundle is what an administrator copies to a new host: the single-use
// enrollment secret plus how to find and authenticate the controller. The
// root fingerprint pins the DROOL Agent Root CA, so enrollment never
// depends on browser/web TLS trust (DESIGN §6.1).
type Bundle struct {
	Endpoints []string `json:"e"` // controller agent endpoints, host:port
	Root      string   `json:"r"` // sha256:<hex> of the Agent Root CA certificate
	Secret    string   `json:"t"` // the token itself (only its hash is stored)
}

const bundlePrefix = "drool-enroll-1."

// Encode renders the bundle as one copyable string.
func (b Bundle) Encode() string {
	j, _ := json.Marshal(b)
	return bundlePrefix + base64.RawURLEncoding.EncodeToString(j)
}

// ParseBundle parses Encode's output.
func ParseBundle(s string) (Bundle, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, bundlePrefix) {
		return Bundle{}, errors.New("not a DROOL enrollment token (it starts with " + bundlePrefix + ")")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, bundlePrefix))
	if err != nil {
		return Bundle{}, errors.New("the enrollment token is damaged (copy it again)")
	}
	var b Bundle
	if err := json.Unmarshal(raw, &b); err != nil || b.Secret == "" || b.Root == "" || len(b.Endpoints) == 0 {
		return Bundle{}, errors.New("the enrollment token is incomplete (copy it again)")
	}
	return b, nil
}

// TokenHash is how the controller stores and looks up a token.
func TokenHash(secret string) []byte {
	s := sha256.Sum256([]byte("drool-enroll-token\x00" + secret))
	return s[:]
}

// EnrollProofMessage is what the identity key signs at enrollment.
func EnrollProofMessage(secret string, pub []byte) []byte {
	h := sha256.Sum256([]byte(secret))
	return append(append([]byte("drool-enroll-v1\x00"), h[:]...), pub...)
}

// RecoverMessage is what the identity key signs for certificate recovery.
func RecoverMessage(identityID string, nonce []byte) []byte {
	return append([]byte("drool-recover-v1\x00"+identityID+"\x00"), nonce...)
}
