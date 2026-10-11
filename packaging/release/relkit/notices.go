package main

import (
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// licenseFile matches the files that carry a component's license terms.
var licenseFile = regexp.MustCompile(`(?i)^(licen[cs]e|copying|notice|patents)([.-].*)?$`)

type component struct {
	Kind    string // "Go", "Go module", "npm package"
	Name    string
	Version string
	License string // declared SPDX id, when the component declares one
	In      []string
	Files   []namedText
}

type namedText struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

type npmPackage struct {
	Name    string      `json:"name"`
	Version string      `json:"version"`
	License string      `json:"license"`
	Files   []namedText `json:"files"`
}

func cmdNotices(args []string) error {
	fs := flag.NewFlagSet("notices", flag.ContinueOnError)
	npmFile := fs.String("npm", "", "web-packages.json from npm-licenses.mjs")
	out := fs.String("out", "THIRD_PARTY_NOTICES.txt", "output file")
	modCache := fs.String("modcache", "", "Go module cache (default: go env GOMODCACHE)")
	goroot := fs.String("goroot", "", "Go installation (default: go env GOROOT)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: relkit notices -npm FILE -out FILE BINARY...")
	}
	if *modCache == "" {
		*modCache = goEnv("GOMODCACHE")
	}
	if *goroot == "" {
		*goroot = goEnv("GOROOT")
	}
	comps, err := goComponents(fs.Args(), *modCache, *goroot)
	if err != nil {
		return err
	}
	if *npmFile != "" {
		b, err := os.ReadFile(*npmFile)
		if err != nil {
			return err
		}
		var pkgs []npmPackage
		if err := json.Unmarshal(b, &pkgs); err != nil {
			return fmt.Errorf("%s: %w", *npmFile, err)
		}
		for _, p := range pkgs {
			if len(p.Files) == 0 {
				return fmt.Errorf("npm package %s %s has no license file; look at it before distributing it", p.Name, p.Version)
			}
			comps = append(comps, component{Kind: "npm package", Name: p.Name, Version: p.Version, License: p.License,
				In: []string{"web UI (in drool)"}, Files: p.Files})
		}
	}
	return os.WriteFile(*out, []byte(renderNotices(comps)), 0o644)
}

func goEnv(key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	out, err := exec.Command("go", "env", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// goComponents lists the Go toolchain and every module linked into the
// binaries, with their license files.
func goComponents(binaries []string, modCache, goroot string) ([]component, error) {
	type key struct{ path, version string }
	mods := map[key]*component{}
	var goVersions []string
	for _, bin := range binaries {
		bi, err := buildinfo.ReadFile(bin)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", bin, err)
		}
		name := filepath.Base(bin)
		if !contains(goVersions, bi.GoVersion) {
			goVersions = append(goVersions, bi.GoVersion)
		}
		for _, d := range bi.Deps {
			m := d
			if d.Replace != nil {
				m = d.Replace
			}
			k := key{m.Path, m.Version}
			c := mods[k]
			if c == nil {
				dir := filepath.Join(modCache, escapeModulePath(m.Path)+"@"+m.Version)
				files, err := readLicenseFiles(dir)
				if err != nil {
					return nil, fmt.Errorf("module %s %s: %w", m.Path, m.Version, err)
				}
				if len(files) == 0 {
					return nil, fmt.Errorf("module %s %s has no license file in %s; look at it before distributing it", m.Path, m.Version, dir)
				}
				c = &component{Kind: "Go module", Name: m.Path, Version: m.Version, Files: files}
				mods[k] = c
			}
			if !contains(c.In, name) {
				c.In = append(c.In, name)
			}
		}
	}
	files, err := readLicenseFiles(goroot)
	if err != nil || len(files) == 0 {
		return nil, fmt.Errorf("the Go distribution's LICENSE in %q: %v", goroot, err)
	}
	sort.Strings(goVersions)
	out := []component{{Kind: "Go", Name: "Go standard library and runtime", Version: strings.Join(goVersions, ", "),
		License: "BSD-3-Clause", In: []string{"every binary"}, Files: files}}
	var list []*component
	for _, c := range mods {
		sort.Strings(c.In)
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Name != list[j].Name {
			return list[i].Name < list[j].Name
		}
		return list[i].Version < list[j].Version
	})
	for _, c := range list {
		out = append(out, *c)
	}
	return out, nil
}

// escapeModulePath is the module cache's case encoding: "!" + lower case
// for every upper-case letter.
func escapeModulePath(p string) string {
	var b strings.Builder
	for _, r := range p {
		if unicode.IsUpper(r) {
			b.WriteByte('!')
			b.WriteRune(unicode.ToLower(r))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func readLicenseFiles(dir string) ([]namedText, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []namedText
	for _, e := range ents {
		if e.IsDir() || !licenseFile.MatchString(e.Name()) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, namedText{Name: e.Name(), Text: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

const rule = "--------------------------------------------------------------------------------\n"

func renderNotices(comps []component) string {
	var b strings.Builder
	b.WriteString("DROOL third-party software notices\n==================================\n\n")
	b.WriteString("The DROOL controller (drool) and the DROOL agent (drool-agent) are built with Go and\n" +
		"include the Go modules listed below; the controller's web UI includes the npm packages\n" +
		"listed below. Each component's license terms follow, as its authors ship them.\n\nContents\n\n")
	for _, c := range comps {
		lic := ""
		if c.License != "" {
			lic = " (" + c.License + ")"
		}
		fmt.Fprintf(&b, "  %s %s%s — %s\n", c.Name, c.Version, lic, c.Kind)
	}
	for _, c := range comps {
		b.WriteString("\n" + rule)
		fmt.Fprintf(&b, "%s %s\n%s; in: %s\n", c.Name, c.Version, c.Kind, strings.Join(c.In, ", "))
		if c.License != "" {
			fmt.Fprintf(&b, "Declared license: %s\n", c.License)
		}
		b.WriteString(rule)
		for _, f := range c.Files {
			fmt.Fprintf(&b, "\n%s:\n\n%s", f.Name, strings.TrimRight(f.Text, "\n")+"\n")
		}
	}
	return b.String()
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
