package release

import (
	_ "embed"
	"sync"
)

// The DROOL project's Shepherd release keys (official_keys.txt). They are
// a trust domain of their own: not the OpenSSH key that signs a release's
// SHA256SUMS for the people who download it (packaging/keys), and not an
// operator's own release keys, which each host trusts locally. Shepherds
// trust them for their updates unless their host says otherwise; the
// controller only checks official releases against them, and nothing it
// does can add a key to the list or to a host.

//go:embed official_keys.txt
var officialKeysFile []byte

var (
	officialMu   sync.Mutex
	officialKeys []PublicKey
	officialErr  error
	officialSet  bool
	// extraOfficial: a key only test builds add (official_testkey.go, the
	// drooltestofficial tag the integration tests build with).
	extraOfficial []PublicKey
)

// OfficialKeys returns the project's Shepherd release keys built into this
// binary (none until the project has one).
func OfficialKeys() []PublicKey {
	officialMu.Lock()
	defer officialMu.Unlock()
	if !officialSet {
		officialKeys, officialErr = ParsePublicKeys(officialKeysFile)
		officialKeys = append(officialKeys, extraOfficial...)
		officialSet = true
	}
	if officialErr != nil {
		return nil
	}
	return append([]PublicKey(nil), officialKeys...)
}

// OfficialKeysError is a malformed official_keys.txt (a build that must
// not ship; the release tests check it).
func OfficialKeysError() error {
	OfficialKeys()
	officialMu.Lock()
	defer officialMu.Unlock()
	return officialErr
}

// IsOfficialKey reports whether keyID is one of the project's keys.
func IsOfficialKey(keyID string) bool {
	for _, k := range OfficialKeys() {
		if k.ID() == keyID {
			return true
		}
	}
	return false
}

// OfficialKeyIDs lists the built-in keys' ids.
func OfficialKeyIDs() []string {
	out := []string{}
	for _, k := range OfficialKeys() {
		out = append(out, k.ID())
	}
	return out
}

// SetOfficialKeysForTest replaces the built-in keys until the returned
// function restores them. In-process tests only.
func SetOfficialKeysForTest(keys []PublicKey) (restore func()) {
	OfficialKeys()
	officialMu.Lock()
	saved, savedErr := officialKeys, officialErr
	officialKeys, officialErr = append([]PublicKey(nil), keys...), nil
	officialMu.Unlock()
	return func() {
		officialMu.Lock()
		officialKeys, officialErr = saved, savedErr
		officialMu.Unlock()
	}
}
