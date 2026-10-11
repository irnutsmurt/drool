// Command relkit is DROOL's release kit: the deterministic steps of the
// release pipeline (packaging/release/build.sh) other than compiling —
// third-party notices, reproducible archives, SBOM normalization, reading
// the container image, the checksum list and the release manifest. Every
// output depends only on its inputs (no clock, no randomness), so two
// builds of the same commit produce the same bytes.
//
//	relkit notices -npm web-packages.json -out THIRD_PARTY_NOTICES.txt BINARY...
//	relkit tar -out X.tar.gz -prefix DIR/ -epoch N SRC[=NAME]...
//	relkit sbom-normalize -epoch N -namespace URI FILE...
//	relkit oci -image IMAGE.tar [-file PATH -platform OS/ARCH]
//	relkit checksums -dir DIR
//	relkit manifest -dir DIR -image IMAGE.tar ... > release.json
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: relkit notices|tar|sbom-normalize|oci|checksums|manifest ...")
		os.Exit(2)
	}
	var err error
	args := os.Args[2:]
	switch os.Args[1] {
	case "notices":
		err = cmdNotices(args)
	case "tar":
		err = cmdTar(args)
	case "sbom-normalize":
		err = cmdSBOMNormalize(args)
	case "oci":
		err = cmdOCI(args)
	case "checksums":
		err = cmdChecksums(args)
	case "manifest":
		err = cmdManifest(args)
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "relkit:", err)
		os.Exit(1)
	}
}
