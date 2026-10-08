// Package source is how monitoring collectors read a host: Local reads the
// machine the agent runs on; Dump answers from the output of a script run
// over SSH. Collectors only ever see this interface, so both management
// paths run the same collection code.
//
// SSH collection is two passes: the first script dumps every file the
// collectors may read and the outputs of fixed commands; the collectors
// run against it and ask for host-specific commands (a SMART query per
// disk, capacity per mount), which a second script runs. Collectors call
// Run and Statfs the same way in both modes; in the first pass the Dump
// answers ErrDeferred and records the request.
package source

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// MaxFile bounds every file read (mountinfo on a host with many
// containers is the largest; anything bigger is cut).
const MaxFile = 1 << 20

// ErrDeferred: the answer comes in the second SSH pass.
var ErrDeferred = errors.New("deferred to the second pass")

// ErrNotFound: no such file, link or command output.
var ErrNotFound = errors.New("not found")

// Statfs is a filesystem's capacity.
type Statfs struct {
	Blocks, Bfree, Bavail, Bsize uint64
	Files, Ffree                 uint64
}

// Result is a helper command's outcome.
type Result struct {
	Stdout    []byte
	Stderr    []byte
	Exit      int
	Truncated bool
	TimedOut  bool
}

// Source reads one host.
type Source interface {
	ReadFile(p string) ([]byte, error)
	// Glob matches like path.Match against existing paths.
	Glob(pattern string) []string
	// Link resolves a symlink to an absolute path ("" if it isn't one).
	Link(p string) string
	Statfs(ctx context.Context, mount string) (*Statfs, error)
	// Has reports whether a helper command is installed.
	Has(tool string) bool
	// Run runs an allowed helper (see Allowed).
	Run(ctx context.Context, tool string, args ...string) (*Result, error)
}

// Allowed are the only commands collectors may run, with the argument
// shapes they may use. Arguments are checked: no shell, no paths except
// device nodes.
var Allowed = map[string]bool{
	"systemctl":  true,
	"smartctl":   true,
	"nvme":       true,
	"nvidia-smi": true,
	"sensors":    true,
	"journalctl": true,
	"lspci":      true,
}

var argRE = regexp.MustCompile(`^[A-Za-z0-9_.,:=@*/+-]{1,200}$`)

// CheckArgs validates a helper invocation.
func CheckArgs(tool string, args []string) error {
	if !Allowed[tool] {
		return fmt.Errorf("helper %q is not allowed", tool)
	}
	if len(args) > 600 {
		return errors.New("too many arguments")
	}
	for _, a := range args {
		if tool == "journalctl" && a == JournalPattern {
			continue
		}
		if !argRE.MatchString(a) || strings.Contains(a, "..") {
			return fmt.Errorf("argument %q is not allowed", a)
		}
		if strings.HasPrefix(a, "/") && !strings.HasPrefix(a, "/dev/") {
			return fmt.Errorf("argument %q: only device paths may be passed", a)
		}
	}
	return nil
}

// JournalPattern is the one free-form argument allowed: the kernel-message
// filter (set by the storage package), passed to journalctl -g.
var JournalPattern string

// Key identifies a command for the dump.
func Key(tool string, args ...string) string {
	return tool + " " + strings.Join(args, " ")
}

// Glob helpers shared by both sources.

func matchAll(pattern string, paths []string) []string {
	var out []string
	for _, p := range paths {
		if ok, _ := path.Match(pattern, p); ok {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}
