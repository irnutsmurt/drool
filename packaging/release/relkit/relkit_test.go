package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTarIsReproducible(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "drool"), []byte("binary"), 0o700)
	os.WriteFile(filepath.Join(dir, "README"), []byte("text"), 0o600)
	build := func() []byte {
		out := filepath.Join(t.TempDir(), "x.tar.gz")
		if err := cmdTar([]string{"-out", out, "-prefix", "drool_1.0.0_linux_amd64", "-epoch", "1760000000",
			filepath.Join(dir, "drool"), filepath.Join(dir, "README") + "=INSTALL.md"}); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(out)
		return b
	}
	a := build()
	// Different times and permissions on disk change nothing.
	os.Chtimes(filepath.Join(dir, "drool"), time.Now(), time.Now())
	os.Chmod(filepath.Join(dir, "drool"), 0o755)
	if b := build(); !bytes.Equal(a, b) {
		t.Fatal("two archives of the same files differ")
	}
	gz, err := gzip.NewReader(bytes.NewReader(a))
	if err != nil {
		t.Fatal(err)
	}
	if !gz.ModTime.IsZero() || gz.Name != "" {
		t.Errorf("gzip header carries %v %q", gz.ModTime, gz.Name)
	}
	tr := tar.NewReader(gz)
	var got []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.ModTime.Unix() != 1760000000 || h.Uid != 0 || h.Gid != 0 || h.Uname != "root" {
			t.Errorf("%s: %v %d/%d %s", h.Name, h.ModTime, h.Uid, h.Gid, h.Uname)
		}
		got = append(got, h.Name+" "+modeString(h.Mode))
	}
	want := "drool_1.0.0_linux_amd64/ 755,drool_1.0.0_linux_amd64/INSTALL.md 644,drool_1.0.0_linux_amd64/drool 755"
	if strings.Join(got, ",") != want {
		t.Errorf("entries %v, want %s", got, want)
	}
}

func modeString(m int64) string { return fmt.Sprintf("%o", m) }

func TestNormalizeSPDX(t *testing.T) {
	in := []byte(`{"spdxVersion":"SPDX-2.3","documentNamespace":"https://anchore.com/syft/dir/x-5f1b2a","creationInfo":{"created":"2026-10-10T12:34:56Z","creators":["Tool: syft-1.54.1"]},"packages":[{"name":"a<b"}]}`)
	at := time.Unix(1760000000, 0).UTC()
	a, err := normalizeSPDX(in, at, "https://drool.invalid/spdx/x.spdx.json")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := normalizeSPDX(bytes.Replace(in, []byte("5f1b2a"), []byte("99aa00"), 1), at, "https://drool.invalid/spdx/x.spdx.json")
	if !bytes.Equal(a, b) {
		t.Errorf("namespace not replaced:\n%s\n%s", a, b)
	}
	var doc struct {
		Namespace    string `json:"documentNamespace"`
		CreationInfo struct {
			Created string `json:"created"`
		} `json:"creationInfo"`
	}
	json.Unmarshal(a, &doc)
	if doc.Namespace != "https://drool.invalid/spdx/x.spdx.json" || doc.CreationInfo.Created != "2025-10-09T08:53:20Z" {
		t.Errorf("normalized: %+v", doc)
	}
	if !bytes.Contains(a, []byte(`"a<b"`)) {
		t.Errorf("HTML-escaped: %s", a)
	}
	if _, err := normalizeSPDX([]byte(`{"bomFormat":"CycloneDX"}`), at, "x"); err == nil {
		t.Error("a CycloneDX document accepted")
	}
}

func TestLicenseDiscovery(t *testing.T) {
	if got := escapeModulePath("github.com/BurntSushi/toml"); got != "github.com/!burnt!sushi/toml" {
		t.Errorf("escape: %s", got)
	}
	dir := t.TempDir()
	for _, n := range []string{"LICENSE", "LICENSE-APACHE", "COPYING.txt", "NOTICE", "PATENTS", "license_test.go", "licenses.go", "README.md"} {
		os.WriteFile(filepath.Join(dir, n), []byte(n), 0o644)
	}
	files, err := readLicenseFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != "COPYING.txt,LICENSE,LICENSE-APACHE,NOTICE,PATENTS" {
		t.Errorf("license files: %v", names)
	}
}

func TestNoticesOfThisTestBinary(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	modCache, goroot := goEnv("GOMODCACHE"), goEnv("GOROOT")
	if modCache == "" || goroot == "" {
		t.Skip("no Go toolchain")
	}
	comps, err := goComponents([]string{exe}, modCache, goroot)
	if err != nil {
		t.Fatal(err)
	}
	if comps[0].Kind != "Go" || len(comps[0].Files) == 0 {
		t.Errorf("first component %+v", comps[0])
	}
	text := renderNotices(append(comps, component{Kind: "npm package", Name: "react", Version: "19.3.0", License: "MIT",
		In: []string{"web UI (in drool)"}, Files: []namedText{{"LICENSE", "MIT License\n"}}}))
	for _, want := range []string{"Go standard library and runtime", "react 19.3.0 (MIT) — npm package", "Declared license: MIT"} {
		if !strings.Contains(text, want) {
			t.Errorf("notices lack %q", want)
		}
	}
}

// ociLayout builds a minimal two-platform OCI image layout archive.
func ociLayout(t *testing.T, files map[string]string) []byte {
	t.Helper()
	blobs := map[string][]byte{}
	put := func(b []byte) string {
		s := sha256.Sum256(b)
		d := "sha256:" + hex.EncodeToString(s[:])
		blobs[d] = b
		return d
	}
	desc := func(mt string, b []byte) map[string]any {
		return map[string]any{"mediaType": mt, "digest": put(b), "size": len(b)}
	}
	var platforms []map[string]any
	for _, arch := range []string{"amd64", "arm64"} {
		var layer bytes.Buffer
		gz := gzip.NewWriter(&layer)
		tw := tar.NewWriter(gz)
		for name, content := range files {
			c := strings.ReplaceAll(content, "ARCH", arch)
			tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(c)), Typeflag: tar.TypeReg})
			tw.Write([]byte(c))
		}
		tw.Close()
		gz.Close()
		cfg, _ := json.Marshal(map[string]any{"architecture": arch, "os": "linux"})
		man, _ := json.Marshal(map[string]any{"schemaVersion": 2, "config": desc("application/vnd.oci.image.config.v1+json", cfg),
			"layers": []any{desc(mtLayerGzip, layer.Bytes())}})
		d := desc("application/vnd.oci.image.manifest.v1+json", man)
		d["platform"] = map[string]string{"os": "linux", "architecture": arch}
		platforms = append(platforms, d)
	}
	att, _ := json.Marshal(map[string]any{"schemaVersion": 2})
	a := desc("application/vnd.oci.image.manifest.v1+json", att)
	a["platform"] = map[string]string{"os": "unknown", "architecture": "unknown"}
	idx, _ := json.Marshal(map[string]any{"schemaVersion": 2, "manifests": append(platforms, a)})
	top := desc(mtIndex, idx)
	top["annotations"] = map[string]string{"io.containerd.image.name": "docker.io/library/drool:1.0.0"}
	topIdx, _ := json.Marshal(map[string]any{"schemaVersion": 2, "manifests": []any{top}})
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	add := func(name string, b []byte) {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(b)), Typeflag: tar.TypeReg})
		tw.Write(b)
	}
	add("oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`))
	add("index.json", topIdx)
	for d, b := range blobs {
		add("blobs/sha256/"+strings.TrimPrefix(d, "sha256:"), b)
	}
	tw.Close()
	return out.Bytes()
}

func TestOCI(t *testing.T) {
	p := filepath.Join(t.TempDir(), "image.oci.tar")
	os.WriteFile(p, ociLayout(t, map[string]string{"usr/local/bin/drool": "drool for ARCH"}), 0o644)
	img, err := openOCI(p)
	if err != nil {
		t.Fatal(err)
	}
	info, err := img.info()
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "docker.io/library/drool:1.0.0" || len(info.Platforms) != 2 || info.Platforms[0].Platform != "linux/amd64" ||
		info.Platforms[1].Platform != "linux/arm64" || !strings.HasPrefix(info.IndexDigest, "sha256:") {
		t.Fatalf("info %+v", info)
	}
	sum, err := img.fileSHA256("linux/arm64", "/usr/local/bin/drool")
	want := sha256.Sum256([]byte("drool for arm64"))
	if err != nil || sum != hex.EncodeToString(want[:]) {
		t.Errorf("file sha256 %s %v", sum, err)
	}
	if _, err := img.fileSHA256("linux/arm64", "usr/local/bin/missing"); err == nil {
		t.Error("a missing file found")
	}
	if _, err := img.fileSHA256("linux/riscv64", "usr/local/bin/drool"); err == nil {
		t.Error("a missing platform found")
	}
	// A damaged blob is refused.
	for k, b := range img.files {
		if strings.HasPrefix(k, "blobs/") && len(b) > 0 {
			img.files[k] = append([]byte{b[0] ^ 1}, b[1:]...)
		}
	}
	if _, err := img.info(); err == nil {
		t.Error("damaged blobs accepted")
	}
}

func TestChecksumsAndManifest(t *testing.T) {
	dir := t.TempDir()
	for n, c := range map[string]string{"drool_1.0.0_linux_amd64.tar.gz": "a", "drool_1.0.0_amd64.deb": "b", "drool_1.0.0_linux_amd64.spdx.json": "c"} {
		os.WriteFile(filepath.Join(dir, n), []byte(c), 0o644)
	}
	if err := cmdManifest([]string{"-dir", dir, "-version", "v1.0.0", "-channel", "stable", "-commit", "abc", "-epoch", "1760000000"}); err != nil {
		t.Fatal(err)
	}
	var m manifest
	b, _ := os.ReadFile(filepath.Join(dir, "release.json"))
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 3 || m.SchemaVersion == 0 || m.AgentProtocol["min"] == 0 || m.License != "NOASSERTION" || !strings.Contains(m.Signing, "SHA256SUMS.sig") {
		t.Errorf("manifest %+v", m)
	}
	if err := cmdChecksums([]string{"-dir", dir}); err != nil {
		t.Fatal(err)
	}
	sums, _ := os.ReadFile(filepath.Join(dir, SumsFile))
	lines := strings.Split(strings.TrimSpace(string(sums)), "\n")
	if len(lines) != 4 || !strings.HasSuffix(lines[3], "  release.json") {
		t.Errorf("SHA256SUMS:\n%s", sums)
	}
	a := sha256.Sum256([]byte("b"))
	if lines[0] != hex.EncodeToString(a[:])+"  drool_1.0.0_amd64.deb" {
		t.Errorf("first line %q", lines[0])
	}
	for n, k := range map[string]string{"drool_1.0.0_amd64.deb": "deb", "drool-1.0.0-1.x86_64.rpm": "rpm", "drool_1.0.0_image.oci.tar": "container-image",
		"drool-agent_1.0.0_linux_arm64.tar.gz": "shepherd-archive", "drool_1.0.0_linux_arm64.tar.gz": "controller-archive"} {
		if got := kindOf(n); got != k {
			t.Errorf("kindOf(%s) = %s, want %s", n, got, k)
		}
	}
}
