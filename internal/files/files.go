// Package files is DROOL's transport-neutral view of a managed host's
// filesystem (ARCHITECTURE-ALPHA §10.6). The file manager API and file
// deployments use only this package; the SSH driver implements it today
// (SFTP for the login user, explicit sudo commands for elevated access) and
// the agent is meant to implement the same interface later.
package files

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

// Kind is the type of a directory entry.
type Kind string

const (
	KindFile    Kind = "file"
	KindDir     Kind = "dir"
	KindSymlink Kind = "symlink"
	KindOther   Kind = "other" // device, socket, fifo
)

// Info describes one filesystem entry. Stat never follows symlinks.
type Info struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	Kind    Kind      `json:"kind"`
	Size    int64     `json:"size"`
	Mode    uint32    `json:"mode"` // permission bits incl. setuid/setgid/sticky (07777)
	UID     int       `json:"uid"`
	GID     int       `json:"gid"`
	Owner   string    `json:"owner"` // name, or the number when unknown
	Group   string    `json:"group"`
	ModTime time.Time `json:"mtime"`
	Target  string    `json:"target,omitempty"` // symlink target as stored in the link
}

// Caps describes what a filesystem session can do. For SSH hosts the
// ceiling is the login account (and its sudo rules); a future agent reports
// its locally configured allowed paths, which the controller can only
// narrow, never widen.
type Caps struct {
	Transport string `json:"transport"` // "sftp", "ssh-sudo", later "agent"
	Elevated  bool   `json:"elevated"`
	// AllowedPaths limits where the session may read and write; nil means
	// unrestricted (bounded only by the account's own permissions).
	AllowedPaths []string `json:"allowed_paths"`
	// RootEquivalent is true when the session can write anywhere as root.
	RootEquivalent bool `json:"root_equivalent"`
}

// StageOptions controls how new content is prepared.
type StageOptions struct {
	// Mode, Owner and Group of the installed file. Unset values keep those of
	// the file being replaced, or use defaults (0644, the session's user)
	// for a new file.
	Mode  *uint32
	Owner string
	Group string
	// SHA256 of the content, when known in advance; staging fails if the
	// bytes that arrive differ.
	SHA256 string
}

// Precondition guards Install against concurrent changes.
type Precondition struct {
	// MustNotExist refuses to replace an existing file.
	MustNotExist bool
	// ExpectSHA256 refuses unless the current file has exactly this content.
	ExpectSHA256 string
}

// Staged is complete new content sitting in a private temporary file on the
// host, next to its destination. The live file is untouched until Install.
type Staged interface {
	// Path is the staged file's path on the host, for validation commands.
	Path() string
	SHA256() string
	Size() int64
	// Validate runs cmd on the host with {path} replaced by the staged path
	// (as the session's user, i.e. as root for elevated sessions). Output
	// goes to out. A non-zero exit is reported in the result, not as error.
	Validate(ctx context.Context, cmd string, out io.Writer) (ValidationResult, error)
	// Install atomically replaces the destination and verifies the result.
	Install(ctx context.Context, pre Precondition) (*Installed, error)
	// Discard removes the staged file. Safe to call after Install.
	Discard(ctx context.Context)
}

// ValidationResult is the outcome of a validation command.
type ValidationResult struct {
	Command  string `json:"command"`
	ExitCode int    `json:"exit_code"`
	OK       bool   `json:"ok"`
}

// Installed describes a completed install.
type Installed struct {
	Info   Info   `json:"info"`
	SHA256 string `json:"sha256"`
}

// FS is one session on one host's filesystem. Paths are absolute and clean
// (see CleanPath). Implementations must never follow a symlink in the final
// path component when changing something.
type FS interface {
	Caps() Caps
	// Home is a sensible starting directory (the login user's home).
	Home(ctx context.Context) (string, error)
	// RealPath resolves symlinks in a directory path.
	RealPath(ctx context.Context, p string) (string, error)
	Stat(ctx context.Context, p string) (Info, error)
	List(ctx context.Context, dir string) ([]Info, error)
	// Open streams a regular file's content. It refuses symlinks.
	Open(ctx context.Context, p string) (io.ReadCloser, Info, error)
	// Hash returns the SHA-256 of a regular file's content.
	Hash(ctx context.Context, p string) (string, error)
	// Stage streams content into a private temporary file next to p.
	Stage(ctx context.Context, p string, r io.Reader, opt StageOptions) (Staged, error)
	Mkdir(ctx context.Context, p string, mode uint32) error
	// Rename moves from to to; it never replaces an existing entry.
	Rename(ctx context.Context, from, to string) error
	// Remove deletes a file, a symlink (not its target) or an empty
	// directory; recursive removes a directory tree.
	Remove(ctx context.Context, p string, recursive bool) error
	Chmod(ctx context.Context, p string, mode uint32) error
	Chown(ctx context.Context, p string, owner, group string) error
	Close() error
}

// Opener opens filesystem sessions on hosts.
type Opener interface {
	OpenFS(ctx context.Context, hostID string, elevated bool) (FS, error)
}

// Error codes.
const (
	CodeNotFound     = "not_found"
	CodePermission   = "permission_denied"
	CodeExists       = "exists"
	CodeSymlink      = "symlink"
	CodeNotRegular   = "not_regular"
	CodeNotDir       = "not_directory"
	CodeNotEmpty     = "not_empty"
	CodeConflict     = "changed"
	CodeUnsafeDir    = "unsafe_directory"
	CodeIntegrity    = "integrity"
	CodeInvalid      = "invalid"
	CodeTooLarge     = "too_large"
	CodeUnsupported  = "unsupported"
	CodeVerification = "verification_failed"
)

// Error is a filesystem error users can act on.
type Error struct {
	Code    string
	Message string
	Hint    string
	Err     error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Err }

// Errorf builds an Error.
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// WithHint returns e with a hint.
func (e *Error) WithHint(h string) *Error { e.Hint = h; return e }

// CleanPath validates a remote path from a request: absolute, no NUL or
// newline, cleaned. The controller's own filesystem is never involved.
func CleanPath(p string) (string, error) {
	if p == "" || !strings.HasPrefix(p, "/") {
		return "", Errorf(CodeInvalid, "path must be absolute (start with /)")
	}
	if strings.ContainsAny(p, "\x00\n\r") {
		return "", Errorf(CodeInvalid, "path contains a NUL or newline character")
	}
	if len(p) > 4096 {
		return "", Errorf(CodeInvalid, "path is too long")
	}
	return path.Clean(p), nil
}

// CleanName validates a single entry name (no slashes).
func CleanName(n string) (string, error) {
	if n == "" || n == "." || n == ".." || strings.ContainsAny(n, "/\x00\n\r") || len(n) > 255 {
		return "", Errorf(CodeInvalid, "invalid name %q", n)
	}
	return n, nil
}

// ParseMode parses an octal mode such as "644" or "0755".
func ParseMode(s string) (uint32, error) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 5 {
		return 0, Errorf(CodeInvalid, "mode must be octal, like 644 or 0755")
	}
	var m uint32
	for _, c := range s {
		if c < '0' || c > '7' {
			return 0, Errorf(CodeInvalid, "mode must be octal, like 644 or 0755")
		}
		m = m*8 + uint32(c-'0')
	}
	if m > 07777 {
		return 0, Errorf(CodeInvalid, "mode out of range")
	}
	return m, nil
}

// FormatMode renders a mode as four octal digits.
func FormatMode(m uint32) string { return fmt.Sprintf("%04o", m&07777) }

// MaxTextBytes is the largest file viewed or edited as text; larger or
// binary files are downloaded instead.
const MaxTextBytes = 1 << 20

// IsText reports whether b is editable text: valid UTF-8 without NUL bytes.
func IsText(b []byte) bool {
	if len(b) > MaxTextBytes {
		return false
	}
	for _, c := range b {
		if c == 0 {
			return false
		}
	}
	return utf8.Valid(b)
}
