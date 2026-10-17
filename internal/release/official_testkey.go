//go:build drooltestofficial

package release

// Test builds only (-tags drooltestofficial): the integration tests make
// controller and Shepherd binaries that also trust a key they generate for
// the run, its base64 public key passed with
// -ldflags "-X drool/internal/release.testOfficialKey=…". Release builds
// never have this file: without the tag the variable doesn't exist, and
// the linker ignores the -X.
var testOfficialKey string

func init() {
	if testOfficialKey == "" {
		return
	}
	k, err := ParsePublicKey(pubPrefix + " " + testOfficialKey + " DROOL test project key")
	if err != nil {
		panic("drooltestofficial: " + err.Error())
	}
	extraOfficial = append(extraOfficial, k)
}
