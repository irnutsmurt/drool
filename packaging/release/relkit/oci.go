package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
)

// ociImage is an OCI image layout archive, as BuildKit exports it
// (--output type=oci): index.json, oci-layout, blobs/sha256/<hex>.
type ociImage struct {
	files map[string][]byte
}

type ociDescriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Platform    *struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		Variant      string `json:"variant,omitempty"`
	} `json:"platform,omitempty"`
}

type ociIndex struct {
	Manifests   []ociDescriptor   `json:"manifests"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type ociManifest struct {
	Config ociDescriptor   `json:"config"`
	Layers []ociDescriptor `json:"layers"`
}

const (
	mtIndex       = "application/vnd.oci.image.index.v1+json"
	mtDockerList  = "application/vnd.docker.distribution.manifest.list.v2+json"
	mtLayerGzip   = "application/vnd.oci.image.layer.v1.tar+gzip"
	mtDockerLayer = "application/vnd.docker.image.rootfs.diff.tar.gzip"
	mtLayerTar    = "application/vnd.oci.image.layer.v1.tar"
)

func openOCI(file string) (*ociImage, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img := &ociImage{files: map[string][]byte{}}
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		img.files[path.Clean(h.Name)] = b
	}
	if _, ok := img.files["index.json"]; !ok {
		return nil, fmt.Errorf("%s is not an OCI image layout (no index.json)", file)
	}
	return img, nil
}

// blob returns a blob after checking its digest.
func (o *ociImage) blob(digest string) ([]byte, error) {
	hexd, ok := strings.CutPrefix(digest, "sha256:")
	if !ok {
		return nil, fmt.Errorf("unsupported digest %q", digest)
	}
	b, ok := o.files["blobs/sha256/"+hexd]
	if !ok {
		return nil, fmt.Errorf("blob %s missing", digest)
	}
	if s := sha256.Sum256(b); hex.EncodeToString(s[:]) != hexd {
		return nil, fmt.Errorf("blob %s doesn't match its digest", digest)
	}
	return b, nil
}

type platformImage struct {
	Platform       string `json:"platform"`
	ManifestDigest string `json:"manifest_digest"`
	// ConfigDigest is what `docker image inspect` and `podman image
	// inspect` report as the image id for this platform.
	ConfigDigest string `json:"config_digest"`
	manifest     ociManifest
}

type imageInfo struct {
	// IndexDigest is the multi-platform image's digest: what a registry
	// reports for the tag once it is pushed with its index unchanged.
	IndexDigest string          `json:"index_digest"`
	Name        string          `json:"name,omitempty"`
	Platforms   []platformImage `json:"platforms"`
}

func (o *ociImage) info() (*imageInfo, error) {
	var top ociIndex
	if err := json.Unmarshal(o.files["index.json"], &top); err != nil {
		return nil, err
	}
	if len(top.Manifests) != 1 {
		return nil, fmt.Errorf("index.json lists %d entries, expected the image's one index", len(top.Manifests))
	}
	d := top.Manifests[0]
	info := &imageInfo{IndexDigest: d.Digest, Name: d.Annotations["io.containerd.image.name"]}
	if d.MediaType != mtIndex && d.MediaType != mtDockerList {
		return nil, fmt.Errorf("the image is %s, not a multi-platform index", d.MediaType)
	}
	b, err := o.blob(d.Digest)
	if err != nil {
		return nil, err
	}
	var idx ociIndex
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, err
	}
	for _, m := range idx.Manifests {
		if m.Platform == nil || m.Platform.OS == "unknown" { // attestations
			continue
		}
		mb, err := o.blob(m.Digest)
		if err != nil {
			return nil, err
		}
		var man ociManifest
		if err := json.Unmarshal(mb, &man); err != nil {
			return nil, err
		}
		p := m.Platform.OS + "/" + m.Platform.Architecture
		if m.Platform.Variant != "" {
			p += "/" + m.Platform.Variant
		}
		info.Platforms = append(info.Platforms, platformImage{Platform: p, ManifestDigest: m.Digest, ConfigDigest: man.Config.Digest, manifest: man})
	}
	sort.Slice(info.Platforms, func(i, j int) bool { return info.Platforms[i].Platform < info.Platforms[j].Platform })
	return info, nil
}

// fileSHA256 returns the sha256 of a regular file in one platform's image
// (the topmost layer that has it).
func (o *ociImage) fileSHA256(platform, name string) (string, error) {
	info, err := o.info()
	if err != nil {
		return "", err
	}
	name = strings.TrimPrefix(path.Clean("/"+name), "/")
	for _, p := range info.Platforms {
		if p.Platform != platform {
			continue
		}
		for i := len(p.manifest.Layers) - 1; i >= 0; i-- {
			l := p.manifest.Layers[i]
			b, err := o.blob(l.Digest)
			if err != nil {
				return "", err
			}
			var r io.Reader = bytes.NewReader(b)
			switch l.MediaType {
			case mtLayerGzip, mtDockerLayer:
				gz, err := gzip.NewReader(r)
				if err != nil {
					return "", err
				}
				r = gz
			case mtLayerTar:
			default:
				return "", fmt.Errorf("layer type %s not supported", l.MediaType)
			}
			tr := tar.NewReader(r)
			for {
				h, err := tr.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					return "", err
				}
				if strings.TrimPrefix(path.Clean("/"+h.Name), "/") != name {
					continue
				}
				if h.Typeflag != tar.TypeReg {
					return "", fmt.Errorf("%s in the %s image is not a regular file", name, platform)
				}
				s := sha256.New()
				if _, err := io.Copy(s, tr); err != nil {
					return "", err
				}
				return hex.EncodeToString(s.Sum(nil)), nil
			}
		}
		return "", fmt.Errorf("%s is not in the %s image", name, platform)
	}
	return "", fmt.Errorf("the image has no %s variant", platform)
}

func cmdOCI(args []string) error {
	fs := flag.NewFlagSet("oci", flag.ContinueOnError)
	image := fs.String("image", "", "OCI image layout archive")
	file := fs.String("file", "", "print the sha256 of this file in the image")
	platform := fs.String("platform", "", "with -file: os/arch")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *image == "" {
		return errors.New("usage: relkit oci -image FILE [-file PATH -platform OS/ARCH]")
	}
	img, err := openOCI(*image)
	if err != nil {
		return err
	}
	if *file != "" {
		sum, err := img.fileSHA256(*platform, *file)
		if err != nil {
			return err
		}
		fmt.Println(sum)
		return nil
	}
	info, err := img.info()
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(info, "", "  ")
	fmt.Println(string(b))
	return nil
}
