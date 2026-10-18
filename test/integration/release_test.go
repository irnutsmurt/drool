//go:build integration

package integration

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"drool/internal/release"
)

// releaseUnderTest is a release directory made by packaging/release/build.sh
// (-release): what the qualification tests install, run and upgrade to.
type releaseUnderTest struct {
	Dir     string
	Version string `json:"version"` // v1.0.0
	Channel string `json:"channel"`
	Commit  string `json:"commit"`
	License string `json:"license"`
	Schema  int    `json:"schema_version"`
	// The DROOL project's Shepherd release keys built into the release.
	OfficialKeys []string `json:"official_release_keys"`
	Image        struct {
		IndexDigest string `json:"index_digest"`
		Name        string `json:"name"`
		Platforms   []struct {
			Platform     string `json:"platform"`
			ConfigDigest string `json:"config_digest"`
		} `json:"platforms"`
	} `json:"image"`
	Files []struct {
		Name   string `json:"name"`
		SHA256 string `json:"sha256"`
		Kind   string `json:"kind"`
	} `json:"files"`
	pkgver string
	drool  string // the controller binary for this machine, extracted
	binDir string // with the Shepherd binaries next to it
}

var rel *releaseUnderTest

// openRelease checks a release directory against its SHA256SUMS and
// release.json and extracts this machine's controller archive.
func openRelease(dir, extract string) (*releaseUnderTest, error) {
	r := &releaseUnderTest{Dir: dir}
	b, err := os.ReadFile(filepath.Join(dir, "release.json"))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, r); err != nil {
		return nil, fmt.Errorf("release.json: %w", err)
	}
	r.pkgver = strings.TrimPrefix(r.Version, "v")
	sums, err := readSums(filepath.Join(dir, "SHA256SUMS"))
	if err != nil {
		return nil, err
	}
	if len(sums) != len(r.Files)+1 {
		return nil, fmt.Errorf("SHA256SUMS lists %d files, release.json %d (+ itself)", len(sums), len(r.Files))
	}
	for name, want := range sums {
		if got, err := fileSHA256(filepath.Join(dir, name)); err != nil || got != want {
			return nil, fmt.Errorf("%s doesn't match SHA256SUMS (%v)", name, err)
		}
	}
	for _, f := range r.Files {
		if sums[f.Name] != f.SHA256 {
			return nil, fmt.Errorf("release.json and SHA256SUMS disagree about %s", f.Name)
		}
	}
	name := fmt.Sprintf("drool_%s_linux_%s", r.pkgver, runtime.GOARCH)
	if err := untar(filepath.Join(dir, name+".tar.gz"), extract); err != nil {
		return nil, err
	}
	r.binDir = filepath.Join(extract, name)
	r.drool = filepath.Join(r.binDir, "drool")
	return r, nil
}

// artifact returns a release file by kind and, for per-architecture files,
// architecture ("" for the others); it skips the test without -release.
func (r *releaseUnderTest) artifact(t *testing.T, kind, arch string) string {
	t.Helper()
	for _, f := range r.Files {
		if f.Kind != kind {
			continue
		}
		if arch == "" || strings.Contains(f.Name, "_"+arch+".") || strings.Contains(f.Name, "_linux_"+arch+".") ||
			strings.Contains(f.Name, "."+rpmArch(arch)+".") {
			return filepath.Join(r.Dir, f.Name)
		}
	}
	t.Fatalf("the release has no %s artifact for %q", kind, arch)
	return ""
}

func rpmArch(arch string) string {
	return map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[arch]
}

func needRelease(t *testing.T) *releaseUnderTest {
	t.Helper()
	if rel == nil {
		t.Skip("qualifies release artifacts: run with -release dist/<version> (make release)")
	}
	return rel
}

func readSums(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		sum, name, ok := strings.Cut(sc.Text(), "  ")
		if !ok || len(sum) != 64 {
			return nil, fmt.Errorf("SHA256SUMS: bad line %q", sc.Text())
		}
		out[name] = sum
	}
	return out, sc.Err()
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func untar(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		p := filepath.Join(dest, filepath.Clean("/"+h.Name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode)&0o777)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, tr)
			out.Close()
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s: unexpected entry %s", archive, h.Name)
		}
	}
}

// tarList lists an archive's entries as "name mode uid/gid".
func tarList(t *testing.T, archive string) []string {
	t.Helper()
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var out []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s %o %d/%d", h.Name, h.Mode, h.Uid, h.Gid))
	}
	return out
}

// TestReleaseArtifacts checks what a release consists of, before anything
// installs it: every artifact is listed and checksummed, the binaries say
// which release and commit they are, the archives hold exactly what the
// documentation says, the SBOMs are SPDX and name what was built, the
// controller hands out the Shepherd binaries of the same release, and the
// Shepherd archive installs by hand the way its INSTALL.md says.
func TestReleaseArtifacts(t *testing.T) {
	r := needRelease(t)
	kinds := map[string]int{}
	for _, f := range r.Files {
		kinds[f.Kind]++
	}
	want := map[string]int{"controller-archive": 2, "shepherd-archive": 2, "deb": 2, "rpm": 2, "container-image": 1,
		"sbom": 4, "notices": 1, "compose": 1, "podman-quadlet": 1}
	for k, n := range want {
		expect(t, kinds[k] == n, "the release has %d %s artifacts, want %d (%v)", kinds[k], k, n, kinds)
	}
	// The binaries describe themselves as this release.
	out, err := exec.Command(r.drool, "version", "--json").Output()
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Version, Channel, Commit string
		Modified                 bool
		Schema                   int      `json:"schema_version"`
		OfficialKeys             []string `json:"official_release_keys"`
	}
	json.Unmarshal(out, &v)
	expect(t, v.Version == r.Version && v.Channel == r.Channel && v.Commit == r.Commit && !v.Modified && v.Schema == r.Schema,
		"drool version --json %+v; release.json says %s %s %s schema %d", v, r.Version, r.Channel, r.Commit, r.Schema)
	agentOut, err := exec.Command(filepath.Join(r.binDir, "drool-agent-linux-"+runtime.GOARCH), "version").Output()
	expect(t, err == nil && strings.TrimSpace(string(agentOut)) == r.Version, "drool-agent version: %q %v", agentOut, err)
	// The Shepherd release keys built in: what release.json says, in the
	// controller and in the Shepherds; a release has at least one (build.sh
	// refuses a stable or candidate build without), or its Shepherds could
	// never take an official update.
	agentJSON, _ := exec.Command(filepath.Join(r.binDir, "drool-agent-linux-"+runtime.GOARCH), "version", "--json").Output()
	var av struct {
		OfficialKeys []string `json:"official_release_keys"`
	}
	json.Unmarshal(agentJSON, &av)
	expect(t, strings.Join(v.OfficialKeys, ",") == strings.Join(r.OfficialKeys, ",") && strings.Join(av.OfficialKeys, ",") == strings.Join(r.OfficialKeys, ","),
		"official Shepherd release keys: drool %v, drool-agent %v, release.json %v", v.OfficialKeys, av.OfficialKeys, r.OfficialKeys)
	if r.Channel == "stable" || r.Channel == "prerelease" {
		expect(t, len(r.OfficialKeys) > 0, "a %s release without a Shepherd release key", r.Channel)
	}

	for _, arch := range []string{"amd64", "arm64"} {
		name := fmt.Sprintf("drool_%s_linux_%s/", r.pkgver, arch)
		got := tarList(t, r.artifact(t, "controller-archive", arch))
		sort.Strings(got)
		wantEntries := []string{name + " 755 0/0", name + "INSTALL.md 644 0/0", name + "README.md 644 0/0",
			name + "THIRD_PARTY_NOTICES.txt 644 0/0", name + "config.example.yaml 644 0/0", name + "drool 755 0/0",
			name + "drool-agent-linux-amd64 755 0/0", name + "drool-agent-linux-arm64 755 0/0", name + "drool.service 644 0/0"}
		if r.License != "NOASSERTION" {
			wantEntries = append(wantEntries, name+"LICENSE 644 0/0")
		}
		sort.Strings(wantEntries)
		expect(t, strings.Join(got, ",") == strings.Join(wantEntries, ","), "controller archive %s:\n%v\nwant\n%v", arch, got, wantEntries)
		agent := tarList(t, r.artifact(t, "shepherd-archive", arch))
		expect(t, len(agent) >= 4 && strings.Contains(strings.Join(agent, ","), "/drool-agent 755 0/0"), "Shepherd archive %s: %v", arch, agent)
	}
	unit, _ := os.ReadFile(filepath.Join(r.binDir, "drool.service"))
	expect(t, strings.Contains(string(unit), "ExecStart=/usr/local/bin/drool serve") && strings.Contains(string(unit), "User=drool"),
		"the archive's unit:\n%s", unit)

	// SBOMs: SPDX 2.3, describing this version and the Go modules and npm
	// packages that went into it, with license information.
	for _, f := range r.Files {
		if f.Kind != "sbom" {
			continue
		}
		var doc struct {
			SPDXVersion string `json:"spdxVersion"`
			Name        string `json:"name"`
			Packages    []struct {
				Name             string `json:"name"`
				Version          string `json:"versionInfo"`
				LicenseConcluded string `json:"licenseConcluded"`
				LicenseDeclared  string `json:"licenseDeclared"`
			} `json:"packages"`
		}
		b, _ := os.ReadFile(filepath.Join(r.Dir, f.Name))
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		found := map[string]string{}
		for _, p := range doc.Packages {
			lic := p.LicenseDeclared
			if lic == "NOASSERTION" {
				lic = p.LicenseConcluded
			}
			found[p.Name] = lic
		}
		expect(t, doc.SPDXVersion == "SPDX-2.3", "%s is %s", f.Name, doc.SPDXVersion)
		// DROOL's own packages carry DROOL's license (relkit sbom-normalize).
		for _, p := range doc.Packages {
			if p.Name == "drool" || p.Name == "drool-web" {
				expect(t, p.LicenseDeclared == r.License, "%s: DROOL's own package %s declares %q, the release's license is %q",
					f.Name, p.Name, p.LicenseDeclared, r.License)
			}
		}
		expect(t, found["golang.org/x/crypto"] == "BSD-3-Clause" && found["google.golang.org/grpc"] != "" &&
			found["google.golang.org/grpc"] != "NOASSERTION", "%s: Go module licenses %q %q", f.Name, found["golang.org/x/crypto"], found["google.golang.org/grpc"])
		if strings.Contains(f.Name, "_image_") {
			expect(t, found["base-files"] != "" && found["ca-certificates"] != "", "%s lacks the base image's packages", f.Name)
		} else {
			expect(t, found["react"] == "MIT" && found["vite"] == "", "%s: the web UI's npm packages (react %q; development-only vite %q)",
				f.Name, found["react"], found["vite"])
		}
	}
	notices, _ := os.ReadFile(r.artifact(t, "notices", ""))
	for _, w := range []string{"Go standard library and runtime", "golang.org/x/crypto", "modernc.org/sqlite", "react 19", "@xyflow/react"} {
		expect(t, strings.Contains(string(notices), w), "THIRD_PARTY_NOTICES.txt lacks %s", w)
	}
	compose, _ := os.ReadFile(r.artifact(t, "compose", ""))
	expect(t, strings.Contains(string(compose), "image: ${DROOL_IMAGE:-drool:"+r.pkgver+"}"), "compose.yaml doesn't pin %s:\n%s", r.pkgver, compose)

	// The controller hands new hosts the release's own Shepherds.
	c := freshController(t)
	resp, err := c.client.Get(c.base() + "/agent/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	script, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	for _, arch := range []string{"amd64", "arm64"} {
		sum, _ := fileSHA256(filepath.Join(r.binDir, "drool-agent-linux-"+arch))
		expect(t, strings.Contains(string(script), sum), "install.sh doesn't offer the release's %s Shepherd (%s)", arch, sum[:12])
	}

	// The Shepherd archive, installed by hand the way its INSTALL.md says:
	// the binary copied to the host, `install --preset managed --token-file -`
	// with a token from Shepherds → Enrollment.
	dir := t.TempDir()
	if err := untar(r.artifact(t, "shepherd-archive", runtime.GOARCH), dir); err != nil {
		t.Fatal(err)
	}
	bins, _ := filepath.Glob(filepath.Join(dir, "*", "drool-agent"))
	if len(bins) != 1 {
		t.Fatalf("the Shepherd archive: %v", bins)
	}
	doc, _ := os.ReadFile(filepath.Join(filepath.Dir(bins[0]), "INSTALL.md"))
	expect(t, strings.Contains(string(doc), "sudo ./drool-agent install --preset managed --token-file -"), "the Shepherd archive's INSTALL.md changed:\n%s", doc)
	ct := startHost(t, image(t, selected()[0]), "rel-manual", true, netOuter)
	h := addHost(t, "rel-manual", ct, hostOpts{})
	var tok struct {
		Token string `json:"token"`
	}
	ctl.must(t, "POST", "/api/v1/agents/tokens", map[string]any{"preset": "managed", "description": "release archive", "host_id": h.ID}, &tok)
	mustDocker(t, "cp", bins[0], ct.Name+":/root/drool-agent")
	enrolled, err := dockerStdin(ct.Name, tok.Token+"\n", "sh", "-c", "cd /root && ./drool-agent install --preset managed --token-file -")
	if err != nil || !strings.Contains(enrolled, "Enrolled as host") {
		t.Fatalf("installing the Shepherd from the release archive: %v\n%s", err, enrolled)
	}
	waitAgent(t, h.ID, 2*time.Minute, "the Shepherd from the release archive", func(a hostAgent) bool {
		return a.Management.Active == "agent" && a.Identity != nil && a.Identity.Connected && a.Identity.AgentVersion == r.Version
	})
	// The preset is the ceiling: monitoring comes through, commands don't.
	waitFor(t, 2*time.Minute, "monitoring from the Shepherd from the release archive", func() bool {
		var m struct {
			HasData bool   `json:"has_data"`
			Source  string `json:"source"`
		}
		st, _ := ctl.do("GET", "/api/v1/hosts/"+h.ID+"/monitoring", nil, &m)
		return st == 200 && m.HasData && m.Source == "agent"
	})
	jh, _ := runT(t, h.ID, cmd("echo beyond-the-managed-preset", false))
	expect(t, jh.State == "failed" && jh.ErrorClass == "CapabilityError" && jh.Transport == "agent",
		"a command beyond the managed preset: %+v", jh)
}

// TestReleaseSigning runs the offline signing workflow (RELEASING.md) on a
// copy of the release with a throwaway key — never a real one: sign.sh
// refuses while no key is listed and when a file doesn't match SHA256SUMS,
// then signs; the user guide's verification commands (docs/wiki/Installing-DROOL.md) accept the result and
// reject a changed SHA256SUMS or another key's signature.
func TestReleaseSigning(t *testing.T) {
	r := needRelease(t)
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("needs OpenSSH's ssh-keygen")
	}
	dir := t.TempDir()
	for _, n := range append([]string{"SHA256SUMS", "release.json"}, func() (names []string) {
		for _, f := range r.Files {
			names = append(names, f.Name)
		}
		return
	}()...) {
		b, err := os.ReadFile(filepath.Join(r.Dir, n))
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, n), b, 0o644)
	}
	keys := t.TempDir()
	keygen := func(name string) string {
		if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "release@drool", "-f", filepath.Join(keys, name)).CombinedOutput(); err != nil {
			t.Fatalf("ssh-keygen: %v\n%s", err, out)
		}
		pub, _ := os.ReadFile(filepath.Join(keys, name+".pub"))
		return strings.Join(strings.Fields(string(pub))[:2], " ")
	}
	pub := keygen("release")
	keygen("other")
	allowed := filepath.Join(keys, "allowed_signers")
	os.WriteFile(allowed, []byte(`release@drool namespaces="drool-release" `+pub+"\n"), 0o644)
	// The Shepherd release key, made the documented way. This release's
	// binaries don't have it built in (only the project's), so signing is
	// told to accept it (OFFICIAL_RELEASE_KEYS) except where the refusal is
	// what's tested.
	shepherdKey := filepath.Join(keys, "shepherd")
	if out, err := exec.Command(r.drool, "release", "keygen", "-o", shepherdKey).CombinedOutput(); err != nil {
		t.Fatalf("drool release keygen: %v\n%s", err, out)
	}
	shepherdPub, _ := release.ParsePublicKeys([]byte(mustRead(t, shepherdKey+".pub")))
	signWith := func(allowedFile, key string, env ...string) (string, error) {
		cmd := exec.Command(filepath.Join(root, "packaging", "release", "sign.sh"), dir, filepath.Join(keys, key), shepherdKey+".key")
		cmd.Env = append(append(os.Environ(), "ALLOWED_SIGNERS="+allowedFile), env...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	sign := func(allowedFile, key string) (string, error) {
		return signWith(allowedFile, key, "OFFICIAL_RELEASE_KEYS="+shepherdPub[0].ID())
	}
	// A Shepherd key this release doesn't have built in: refused for a
	// release (no Shepherd would accept it), a warning for a development build.
	out, err := signWith(allowed, "release")
	if r.Channel == "development" {
		expect(t, err == nil && strings.Contains(out, "isn't built into this development build"), "a development build, another Shepherd key: %v\n%s", err, out)
		for _, f := range []string{"SHA256SUMS.sig", "drool-agent_" + r.pkgver + "_linux_amd64.release.json", "drool-agent_" + r.pkgver + "_linux_arm64.release.json"} {
			os.Remove(filepath.Join(dir, f))
		}
	} else {
		expect(t, err != nil && strings.Contains(out, "isn't built into this release"), "a release, a Shepherd key it doesn't have: %v\n%s", err, out)
		_, statErr := os.Stat(filepath.Join(dir, "SHA256SUMS.sig"))
		expect(t, os.IsNotExist(statErr), "SHA256SUMS signed although the Shepherds' signatures were refused")
	}
	verify := func() (string, error) {
		cmd := exec.Command("sh", "-c", "sha256sum --check --ignore-missing --quiet SHA256SUMS && "+
			"ssh-keygen -Y verify -f "+shq(allowed)+" -I release@drool -n drool-release -s SHA256SUMS.sig < SHA256SUMS")
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	out, err = sign(filepath.Join(root, "packaging", "keys", "allowed_signers"), "release")
	if strings.Contains(mustRead(t, filepath.Join(root, "packaging", "keys", "allowed_signers")), "ssh-ed25519") {
		expect(t, err != nil, "signed with a key that isn't the listed release key:\n%s", out)
	} else {
		expect(t, err != nil && strings.Contains(out, "lists no key"), "signing without a listed key: %v\n%s", err, out)
	}
	compose := filepath.Join(dir, "compose.yaml")
	orig := mustRead(t, compose)
	os.WriteFile(compose, []byte(orig+"# changed\n"), 0o644)
	out, err = sign(allowed, "release")
	expect(t, err != nil && strings.Contains(out, "don't match SHA256SUMS"), "signing a changed release: %v\n%s", err, out)
	os.WriteFile(compose, []byte(orig), 0o644)
	out, err = sign(allowed, "other")
	expect(t, err != nil && strings.Contains(out, "doesn't verify"), "signing with another key: %v\n%s", err, out)
	_, statErr := os.Stat(filepath.Join(dir, "SHA256SUMS.sig"))
	expect(t, os.IsNotExist(statErr), "a signature from the wrong key was left behind")
	if out, err := sign(allowed, "release"); err != nil {
		t.Fatalf("sign: %v\n%s", err, out)
	}
	if out, err := verify(); err != nil || !strings.Contains(out, `Good "drool-release" signature for release@drool`) {
		t.Fatalf("the documented verification: %v\n%s", err, out)
	}
	// Each Shepherd's signed description, for this release's version and
	// channel, verifies against the Shepherd key and its binary.
	for _, arch := range []string{"amd64", "arm64"} {
		name := "drool-agent_" + r.pkgver + "_linux_" + arch
		x := t.TempDir()
		untar(filepath.Join(dir, name+".tar.gz"), x)
		vout, err := exec.Command(r.drool, "release", "verify", "--key", shepherdKey+".pub", filepath.Join(dir, name+".release.json"),
			filepath.Join(x, name, "drool-agent")).CombinedOutput()
		expect(t, err == nil && strings.Contains(string(vout), "OK: drool-agent "+r.Version+" (linux/"+arch), "%s: %v\n%s", name, err, vout)
		var signed release.Signed
		json.Unmarshal([]byte(mustRead(t, filepath.Join(dir, name+".release.json"))), &signed)
		var m release.Manifest
		json.Unmarshal(signed.Payload, &m)
		expect(t, m.Channel == r.Channel && m.ProtocolMin == 1, "%s describes channel %q, protocol %d–%d", name, m.Channel, m.ProtocolMin, m.ProtocolMax)
	}
	// The signature covers SHA256SUMS: change one hash in it (as someone
	// replacing a file would) and the signature no longer verifies.
	sums := mustRead(t, filepath.Join(dir, "SHA256SUMS"))
	os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(strings.Replace(sums, sums[:8], "00000000", 1)), 0o644)
	sig := exec.Command("sh", "-c", "ssh-keygen -Y verify -f "+shq(allowed)+" -I release@drool -n drool-release -s SHA256SUMS.sig < SHA256SUMS")
	sig.Dir = dir
	out2, err := sig.CombinedOutput()
	expect(t, err != nil, "a changed SHA256SUMS verified:\n%s", out2)
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
