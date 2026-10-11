package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"drool/internal/agentrpc"
	"drool/internal/agentsvc"
	"drool/internal/store"
)

// cmdSBOMNormalize makes SPDX documents reproducible: the creation time
// becomes the source's commit time and the namespace a stable URI (syft
// writes the current time and a random UUID).
func cmdSBOMNormalize(args []string) error {
	fs := flag.NewFlagSet("sbom-normalize", flag.ContinueOnError)
	epoch := fs.Int64("epoch", -1, "SOURCE_DATE_EPOCH")
	ns := fs.String("namespace", "", "documentNamespace prefix; the file name is appended")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *epoch < 0 || *ns == "" || fs.NArg() == 0 {
		return errors.New("usage: relkit sbom-normalize -epoch N -namespace URI FILE...")
	}
	for _, f := range fs.Args() {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		out, err := normalizeSPDX(b, time.Unix(*epoch, 0).UTC(), strings.TrimSuffix(*ns, "/")+"/"+filepath.Base(f))
		if err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		if err := os.WriteFile(f, out, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func normalizeSPDX(b []byte, created time.Time, namespace string) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	if v, _ := doc["spdxVersion"].(string); !strings.HasPrefix(v, "SPDX-2.") {
		return nil, fmt.Errorf("not an SPDX 2.x JSON document (%q)", v)
	}
	ci, ok := doc["creationInfo"].(map[string]any)
	if !ok {
		return nil, errors.New("no creationInfo")
	}
	ci["created"] = created.Format(time.RFC3339)
	doc["documentNamespace"] = namespace
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// SumsFile is the checksum list the release manager signs.
const SumsFile = "SHA256SUMS"

// cmdChecksums writes SHA256SUMS for every file in a directory, in the
// format sha256sum -c reads.
func cmdChecksums(args []string) error {
	fs := flag.NewFlagSet("checksums", flag.ContinueOnError)
	dir := fs.String("dir", "", "release directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return errors.New("usage: relkit checksums -dir DIR")
	}
	files, err := releaseFiles(*dir, true)
	if err != nil {
		return err
	}
	var b strings.Builder
	for _, f := range files {
		fmt.Fprintf(&b, "%s  %s\n", f.SHA256, f.Name)
	}
	return os.WriteFile(filepath.Join(*dir, SumsFile), []byte(b.String()), 0o644)
}

type releaseFile struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Kind   string `json:"kind"`
}

// releaseFiles lists the artifacts: every regular file except the
// checksum list and its signature, and release.json only when asked
// (the checksum list covers it; it can't list itself).
func releaseFiles(dir string, withManifest bool) ([]releaseFile, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []releaseFile
	for _, e := range ents {
		n := e.Name()
		if !e.Type().IsRegular() || strings.HasPrefix(n, SumsFile) || n == "release.json" && !withManifest || strings.HasPrefix(n, ".") {
			continue
		}
		f, err := os.Open(filepath.Join(dir, n))
		if err != nil {
			return nil, err
		}
		h := sha256.New()
		size, err := io.Copy(h, f)
		f.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, releaseFile{Name: n, Size: size, SHA256: hex.EncodeToString(h.Sum(nil)), Kind: kindOf(n)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func kindOf(name string) string {
	switch {
	case strings.HasSuffix(name, ".spdx.json"):
		return "sbom"
	case strings.HasSuffix(name, ".deb"):
		return "deb"
	case strings.HasSuffix(name, ".rpm"):
		return "rpm"
	case strings.HasSuffix(name, ".oci.tar"):
		return "container-image"
	case strings.HasSuffix(name, ".tar.gz") && strings.HasPrefix(name, "drool-agent_"):
		return "shepherd-archive"
	case strings.HasSuffix(name, ".tar.gz"):
		return "controller-archive"
	case strings.HasPrefix(name, "drool-agent-linux-"):
		return "shepherd-binary"
	case name == "compose.yaml":
		return "compose"
	case name == "drool.container":
		return "podman-quadlet"
	case name == "release.json":
		return "release-manifest"
	case strings.HasPrefix(name, "THIRD_PARTY_NOTICES"):
		return "notices"
	case strings.HasPrefix(name, "LICENSE"):
		return "license"
	}
	return "other"
}

// manifest is release.json: what was built, from what, and with what.
type manifest struct {
	Format   string `json:"format"`
	Product  string `json:"product"`
	Version  string `json:"version"`
	Channel  string `json:"channel"`
	Commit   string `json:"commit"`
	SourceAt string `json:"source_date"`
	License  string `json:"license"`
	// Signing says how a release is signed: a build is never signed; the
	// release manager adds SHA256SUMS.sig offline (RELEASING.md).
	Signing       string            `json:"signing"`
	SchemaVersion int               `json:"schema_version"`
	AgentProtocol map[string]uint32 `json:"agent_protocol"`
	AgentFeatures []string          `json:"agent_features"`
	Platforms     []string          `json:"platforms"`
	Toolchain     map[string]string `json:"toolchain"`
	Image         *imageInfo        `json:"image,omitempty"`
	Files         []releaseFile     `json:"files"`
}

func cmdManifest(args []string) error {
	fs := flag.NewFlagSet("manifest", flag.ContinueOnError)
	dir := fs.String("dir", "", "release directory")
	image := fs.String("image", "", "the OCI image archive in it")
	m := manifest{Format: "drool-release-v1", Product: "drool", AgentProtocol: map[string]uint32{}, Toolchain: map[string]string{},
		Signing: "SHA256SUMS.sig, added offline by the release manager: ssh-keygen -Y verify -f allowed_signers -I release@drool -n drool-release " +
			"-s SHA256SUMS.sig < SHA256SUMS; without a valid one this is not an official release"}
	fs.StringVar(&m.Version, "version", "", "")
	fs.StringVar(&m.Channel, "channel", "", "")
	fs.StringVar(&m.Commit, "commit", "", "")
	fs.StringVar(&m.License, "license", "NOASSERTION", "SPDX license id of DROOL itself")
	epoch := fs.Int64("epoch", -1, "SOURCE_DATE_EPOCH")
	platforms := fs.String("platforms", "linux/amd64,linux/arm64", "")
	tools := fs.String("toolchain", "", "name=reference,... of the pinned build images")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" || m.Version == "" || m.Commit == "" || *epoch < 0 {
		return errors.New("usage: relkit manifest -dir DIR -version V -channel C -commit SHA -epoch N [-image FILE] [-toolchain k=v,...]")
	}
	m.SourceAt = time.Unix(*epoch, 0).UTC().Format(time.RFC3339)
	m.SchemaVersion = store.LatestVersion()
	r := agentrpc.Local()
	m.AgentProtocol["min"], m.AgentProtocol["max"] = r.Min, r.Max
	for _, f := range agentsvc.KnownFeatures {
		m.AgentFeatures = append(m.AgentFeatures, f.Name)
	}
	m.Platforms = strings.Split(*platforms, ",")
	for _, kv := range strings.Split(*tools, ",") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m.Toolchain[k] = v
		}
	}
	if *image != "" {
		img, err := openOCI(*image)
		if err != nil {
			return err
		}
		if m.Image, err = img.info(); err != nil {
			return err
		}
	}
	var err error
	if m.Files, err = releaseFiles(*dir, false); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(filepath.Join(*dir, "release.json"), append(b, '\n'), 0o644)
}
