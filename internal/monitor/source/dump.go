package source

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"drool/internal/hostops"
)

// Spec lists what the collectors may read: files (globs), symlinks
// (globs) and fixed helper commands. The SSH script is generated from it,
// so the script and the collectors can't drift apart.
type Spec struct {
	Files    []string
	Links    []string
	Commands [][]string // tool, args...
	Tools    []string   // helpers whose presence matters
}

// Merge combines specs.
func Merge(specs ...Spec) Spec {
	var s Spec
	for _, x := range specs {
		s.Files = append(s.Files, x.Files...)
		s.Links = append(s.Links, x.Links...)
		s.Commands = append(s.Commands, x.Commands...)
		s.Tools = append(s.Tools, x.Tools...)
	}
	return s
}

// Script renders the first-pass script. Files are dumped with one grep
// (NUL after the name, so names may contain ':'), links with one find:
// the whole first pass costs a handful of processes on the host.
// Everything tolerates failure; root ("" or a fixture directory in
// tests) prefixes every path.
func (s Spec) Script(root string) string {
	var b strings.Builder
	b.WriteString("set +e\nexport LC_ALL=C\n" + errFile)
	fmt.Fprintf(&b, "R=%s\n", hostops.ShQuote(root))
	b.WriteString("printf '@@DROOL-MONITOR 1\\n'\n")
	b.WriteString("printf '@@FILES\\n'\n")
	// Globs are expanded by the shell; unmatched ones stay literal and
	// grep -s ignores them. Empty files print nothing (they are empty).
	var globs []string
	for _, g := range uniq(s.Files) {
		globs = append(globs, `"$R"`+globQuote(g))
	}
	fmt.Fprintf(&b, "grep -sHaZ '' %s 2>/dev/null\n", strings.Join(globs, " "))
	b.WriteString("printf '\\n@@LINKS\\n'\n")
	var links []string
	for _, g := range uniq(s.Links) {
		links = append(links, `"$R"`+globQuote(g))
	}
	fmt.Fprintf(&b, "for p in %s; do [ -L \"$p\" ] && printf '%%s\\t%%s\\n' \"$p\" \"$(readlink \"$p\")\"; done 2>/dev/null\n", strings.Join(links, " "))
	b.WriteString("printf '@@TOOLS\\n'\n")
	tools := append([]string(nil), s.Tools...)
	sort.Strings(tools)
	tools = compact(tools)
	for _, t := range tools {
		fmt.Fprintf(&b, "command -v %s >/dev/null 2>&1 && echo %s\n", t, t)
	}
	seen := map[string]bool{}
	for _, c := range s.Commands {
		if k := Key(c[0], c[1:]...); !seen[k] {
			seen[k] = true
			writeCommand(&b, c[0], c[1:])
		}
	}
	b.WriteString(errFileDone + "printf '\\n@@END\\n'\n")
	return b.String()
}

// errFile: a temporary file for each command's error output, whose first
// two lines travel as "@@ERR" (a command that failed says why, e.g. systemctl
// on a host without a D-Bus system bus); without mktemp nothing is kept.
const (
	errFile     = "E=$(mktemp 2>/dev/null) || E=/dev/null\n"
	errFileDone = "[ \"$E\" = /dev/null ] || rm -f \"$E\"\n"
)

func uniq(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range s {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// globQuote quotes a glob for sh, leaving * ? [ ] active.
func globQuote(g string) string {
	var b strings.Builder
	for _, r := range g {
		switch r {
		case '*', '?', '[', ']':
			b.WriteRune(r)
		default:
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("/_.-:", r) {
				b.WriteRune(r)
			} else {
				b.WriteString(hostops.ShQuote(string(r)))
			}
		}
	}
	return b.String()
}

func compact(s []string) []string {
	out := s[:0]
	for i, x := range s {
		if i == 0 || x != s[i-1] {
			out = append(out, x)
		}
	}
	return out
}

func writeCommand(b *strings.Builder, tool string, args []string) {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = hostops.ShQuote(a)
	}
	key := Key(tool, args...)
	fmt.Fprintf(b, "printf '\\n@@CMD %%s\\n' %s\n", hostops.ShQuote(key))
	// The helper's own exit status (smartctl's is a bit mask), not head's:
	// it travels on fd 4 into rc while the output goes through head to fd 3.
	// The first two lines of its error output follow as "@@ERR".
	fmt.Fprintf(b, "if command -v %s >/dev/null 2>&1; then { rc=$( { { timeout 60 %s %s 2>\"$E\"; echo $? >&4; } | head -c %d >&3; } 4>&1 ); } 3>&1; printf '\\n@@EXIT %%s\\n' \"$rc\"; "+
		"if [ \"$E\" != /dev/null ] && [ -s \"$E\" ]; then printf '@@ERR %%s\\n' \"$(head -n 2 \"$E\" | tr '\\n' ' ' | cut -c 1-300)\"; : > \"$E\"; fi; else printf '\\n@@EXIT 127\\n'; fi\n",
		tool, tool, strings.Join(q, " "), 1<<20)
}

// SecondPass renders the script for the commands and capacity checks
// the collectors asked for in the first pass. Nothing is run that isn't
// in Allowed with checked arguments; mounts are quoted.
func SecondPass(cmds [][]string, mounts []string, root string) (string, error) {
	var b strings.Builder
	b.WriteString("set +e\nexport LC_ALL=C\n" + errFile + "printf '@@DROOL-MONITOR 1\\n'\n")
	for _, c := range cmds {
		if err := CheckArgs(c[0], c[1:]); err != nil {
			return "", err
		}
		writeCommand(&b, c[0], c[1:])
	}
	if len(mounts) > 0 {
		q := make([]string, len(mounts))
		for i, m := range mounts {
			q[i] = hostops.ShQuote(root + m)
		}
		// One stat for all mounts; %n last so names with spaces parse.
		fmt.Fprintf(&b, "printf '\\n@@STATFS\\n'\ntimeout 20 stat -f -c '%%b %%f %%a %%S %%c %%d %%n' -- %s 2>/dev/null\n", strings.Join(q, " "))
	}
	b.WriteString(errFileDone + "printf '\\n@@END\\n'\n")
	return b.String(), nil
}

// Dump answers from script output.
type Dump struct {
	files  map[string][]byte
	links  map[string]string
	tools  map[string]bool
	cmds   map[string]*Result
	statfs map[string]*Statfs
	paths  []string
	root   string

	// Second-pass requests recorded while running the collectors.
	Wanted   [][]string
	WantedFS []string
	final    bool
}

// ParseDump reads first-pass output; root is stripped from paths.
func ParseDump(out []byte, root string) (*Dump, error) {
	d := &Dump{files: map[string][]byte{}, links: map[string]string{}, tools: map[string]bool{},
		cmds: map[string]*Result{}, statfs: map[string]*Statfs{}, root: root}
	if !bytes.HasPrefix(out, []byte("@@DROOL-MONITOR 1\n")) {
		return nil, fmt.Errorf("unexpected monitoring script output")
	}
	out = out[len("@@DROOL-MONITOR 1\n"):]
	sec, rest := cut(out, "@@FILES\n")
	_ = sec
	files, rest := cut(rest, "\n@@LINKS\n")
	d.parseFiles(files, root)
	links, rest := cut(rest, "@@TOOLS\n")
	for _, ln := range strings.Split(string(links), "\n") {
		p, t, ok := strings.Cut(ln, "\t")
		if !ok {
			continue
		}
		p = strings.TrimPrefix(p, root)
		if !path.IsAbs(t) {
			t = path.Join(path.Dir(p), t)
		} else {
			t = strings.TrimPrefix(t, root)
		}
		d.links[p] = path.Clean(t)
	}
	tools, rest := cut(rest, "\n@@")
	for _, t := range strings.Fields(string(tools)) {
		d.tools[t] = true
	}
	d.parseCommands(append([]byte("\n@@"), rest...))
	for p := range d.files {
		d.paths = append(d.paths, p)
	}
	for p := range d.links {
		d.paths = append(d.paths, p)
	}
	sort.Strings(d.paths)
	return d, nil
}

// AddPass adds a later pass's outputs.
func (d *Dump) AddPass(out []byte) error {
	if !bytes.HasPrefix(out, []byte("@@DROOL-MONITOR 1\n")) {
		return fmt.Errorf("unexpected monitoring script output")
	}
	d.parseCommands(out[len("@@DROOL-MONITOR 1"):])
	if d.root != "" {
		fixed := map[string]*Statfs{}
		for k, v := range d.statfs {
			fixed[strings.TrimPrefix(k, d.root)] = v
		}
		d.statfs = fixed
	}
	return nil
}

// Forget drops one command's output so the collectors ask for it again
// (e.g. to rerun it with privileges in a later pass).
func (d *Dump) Forget(tool string, args ...string) { delete(d.cmds, Key(tool, args...)) }

// Begin clears recorded requests before the collectors run again.
func (d *Dump) Begin() { d.Wanted, d.WantedFS = nil, nil }

// Final marks the dump complete: nothing is deferred any more.
func (d *Dump) Final() { d.final = true }

func cut(b []byte, sep string) ([]byte, []byte) {
	before, after, ok := bytes.Cut(b, []byte(sep))
	if !ok {
		return b, nil
	}
	return before, after
}

func (d *Dump) parseFiles(b []byte, root string) {
	// grep -HZ: "path\0line\n" per line of every file. A file matched
	// by two globs appears twice; only its first run of lines counts.
	last := ""
	done := map[string]bool{}
	for len(b) > 0 {
		nul := bytes.IndexByte(b, 0)
		if nul < 0 {
			break
		}
		p := strings.TrimPrefix(string(b[:nul]), root)
		b = b[nul+1:]
		nl := bytes.IndexByte(b, '\n')
		var line []byte
		if nl < 0 {
			line, b = b, nil
		} else {
			line, b = b[:nl+1], b[nl+1:]
		}
		if p != last {
			if last != "" {
				done[last] = true
			}
			last = p
		}
		if done[p] {
			continue
		}
		if cur := d.files[p]; len(cur) < MaxFile {
			d.files[p] = append(cur, line...)
		}
	}
}

func (d *Dump) parseCommands(b []byte) {
	parts := bytes.Split(b, []byte("\n@@"))
	for i := 0; i < len(parts); i++ {
		hdr, body, _ := bytes.Cut(parts[i], []byte("\n"))
		h := string(hdr)
		switch {
		case strings.HasPrefix(h, "CMD "):
			key := strings.TrimPrefix(h, "CMD ")
			r := &Result{Stdout: body, Exit: -1}
			if i+1 < len(parts) && bytes.HasPrefix(parts[i+1], []byte("EXIT ")) {
				code, _ := strconv.Atoi(strings.TrimSpace(string(parts[i+1][5:])))
				r.Exit = code
				i++
			}
			if i+1 < len(parts) && bytes.HasPrefix(parts[i+1], []byte("ERR ")) {
				r.Stderr = bytes.TrimSpace(parts[i+1][4:])
				i++
			}
			if code := r.Exit; code == 124 {
				r.TimedOut = true
			}
			d.cmds[key] = r
		case h == "STATFS":
			for _, ln := range strings.Split(string(body), "\n") {
				f := strings.SplitN(ln, " ", 7)
				if len(f) < 7 {
					continue
				}
				var v [6]uint64
				ok := true
				for j := 0; j < 6; j++ {
					n, err := strconv.ParseUint(f[j], 10, 64)
					if err != nil {
						ok = false
						break
					}
					v[j] = n
				}
				if ok {
					d.statfs[f[6]] = &Statfs{Blocks: v[0], Bfree: v[1], Bavail: v[2], Bsize: v[3], Files: v[4], Ffree: v[5]}
				}
			}
		}
	}
}

// ReadFile implements Source.
func (d *Dump) ReadFile(p string) ([]byte, error) {
	if b, ok := d.files[p]; ok {
		return b, nil
	}
	return nil, ErrNotFound
}

// Glob implements Source.
func (d *Dump) Glob(pattern string) []string { return matchAll(pattern, d.paths) }

// Link implements Source.
func (d *Dump) Link(p string) string { return d.links[p] }

// Has implements Source.
func (d *Dump) Has(tool string) bool { return d.tools[tool] }

// Statfs implements Source.
func (d *Dump) Statfs(_ context.Context, mount string) (*Statfs, error) {
	if s, ok := d.statfs[mount]; ok {
		return s, nil
	}
	if d.final {
		return nil, ErrNotFound
	}
	d.WantedFS = append(d.WantedFS, mount)
	return nil, ErrDeferred
}

// Run implements Source.
func (d *Dump) Run(_ context.Context, tool string, args ...string) (*Result, error) {
	if err := CheckArgs(tool, args); err != nil {
		return nil, err
	}
	if r, ok := d.cmds[Key(tool, args...)]; ok {
		if r.Exit == 127 {
			return nil, ErrNotFound
		}
		if r.TimedOut {
			return r, context.DeadlineExceeded
		}
		return r, nil
	}
	if d.final || !d.tools[tool] {
		return nil, ErrNotFound
	}
	d.Wanted = append(d.Wanted, append([]string{tool}, args...))
	return nil, ErrDeferred
}
