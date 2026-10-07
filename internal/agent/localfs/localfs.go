// Package localfs is the agent's implementation of files.FS (ARCHITECTURE-
// ALPHA §23.8): the M6 file manager and file deployments, executed locally
// by the agent under its capability ceiling.
//
// Path safety is enforced by the kernel, not by string checks: every
// access starts from a directory file descriptor of the allowed path that
// contains it and resolves the rest with openat2(RESOLVE_BENEATH), so no
// symlink can lead outside files.allowed_paths. Changes additionally use
// RESOLVE_NO_SYMLINKS for the parent directory and act on directory file
// descriptors (no check-then-use races on path strings), and the final
// component is never followed.
package localfs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"drool/internal/agentcaps"
	"drool/internal/files"
	"drool/internal/hostexec"
)

// FS is a local filesystem session limited by caps.
type FS struct {
	caps agentcaps.Caps
	run  hostexec.Runner
	uid  int // the agent's uid (root in production)

	mu    sync.Mutex
	names map[string]string
}

// New opens a session. caps are the effective capabilities.
func New(caps agentcaps.Caps, run hostexec.Runner) *FS {
	return &FS{caps: caps, run: run, uid: os.Getuid(), names: map[string]string{}}
}

// Caps implements files.FS.
func (f *FS) Caps() files.Caps {
	paths := f.caps.Files.AllowedPaths
	unrestricted := f.caps.Files.Enabled && (len(paths) == 0 || slicesContains(paths, "/"))
	if len(paths) == 0 {
		paths = nil
	}
	return files.Caps{Transport: "agent", Elevated: f.uid == 0, AllowedPaths: paths, RootEquivalent: unrestricted && f.uid == 0}
}

func slicesContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// Close implements files.FS.
func (f *FS) Close() error { return nil }

func capErr(err error) error {
	var ce *agentcaps.Error
	if errors.As(err, &ce) {
		return &files.Error{Code: "capability", Message: ce.Message,
			Hint: "This limit is set on the host in /etc/drool/agent.yaml; the controller can't change it."}
	}
	return err
}

// Home implements files.FS: the first allowed path, or /.
func (f *FS) Home(ctx context.Context) (string, error) {
	if !f.caps.Files.Enabled {
		return "", capErr(f.caps.AllowPath("/"))
	}
	if len(f.caps.Files.AllowedPaths) > 0 {
		return f.caps.Files.AllowedPaths[0], nil
	}
	if f.uid == 0 {
		return "/root", nil
	}
	return os.UserHomeDir()
}

// --- resolution ---

// base finds the allowed path that contains p and returns an open
// directory fd for it plus p relative to it.
func (f *FS) base(p string) (int, string, error) {
	if err := f.caps.AllowPath(p); err != nil {
		return -1, "", capErr(err)
	}
	root := "/"
	for _, a := range f.caps.Files.AllowedPaths {
		if agentcaps.Within(p, a) && len(a) > len(root) {
			root = a
		}
	}
	fd, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, "", mapErr(root, err)
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(p, root), "/")
	if rel == "" {
		rel = "."
	}
	return fd, rel, nil
}

// openDir opens directory dir (absolute) for operations on its entries.
// With noSymlinks (changes), no component below the allowed root may be a
// symlink. The returned real path is the directory's actual location.
func (f *FS) openDir(dir string, noSymlinks bool) (int, string, error) {
	bfd, rel, err := f.base(dir)
	if err != nil {
		return -1, "", err
	}
	defer unix.Close(bfd)
	how := &unix.OpenHow{Flags: unix.O_DIRECTORY | unix.O_RDONLY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS}
	if noSymlinks {
		how.Resolve |= unix.RESOLVE_NO_SYMLINKS
	}
	fd, err := unix.Openat2(bfd, rel, how)
	if errors.Is(err, unix.ENOSYS) {
		return f.openDirFallback(dir, noSymlinks)
	}
	if err != nil {
		if errors.Is(err, unix.ELOOP) && noSymlinks {
			return -1, "", files.Errorf(files.CodeSymlink, "%s is (or is inside) a symlinked directory; DROOL doesn't change files through symlinks", dir).
				WithHint("Use the real path of the directory.")
		}
		if errors.Is(err, unix.EXDEV) {
			return -1, "", capErr(&agentcaps.Error{Capability: "files", Message: dir + " leads outside the agent's allowed paths through a symlink"})
		}
		return -1, "", mapErr(dir, err)
	}
	real, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
	if err != nil {
		real = dir
	}
	// The real location must itself be allowed (and never the agent's own
	// files), wherever the path pointed.
	if err := f.caps.AllowPath(real); err != nil {
		unix.Close(fd)
		return -1, "", capErr(err)
	}
	return fd, real, nil
}

func (f *FS) openDirFallback(dir string, noSymlinks bool) (int, string, error) {
	real, err := realPath(dir)
	if err != nil {
		return -1, "", mapErr(dir, err)
	}
	if noSymlinks && real != dir {
		return -1, "", files.Errorf(files.CodeSymlink, "%s is (or is inside) a symlinked directory", dir)
	}
	if err := f.caps.AllowPath(real); err != nil {
		return -1, "", capErr(err)
	}
	fd, err := unix.Open(real, unix.O_DIRECTORY|unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, "", mapErr(dir, err)
	}
	return fd, real, nil
}

func realPath(p string) (string, error) { return filepath.EvalSymlinks(p) }

// entry opens the parent of p and returns its fd and p's final name.
func (f *FS) entry(p string, change bool) (int, string, string, error) {
	p = path.Clean(p)
	if p == "/" {
		return -1, "", "", files.Errorf(files.CodeInvalid, "/ can't be changed")
	}
	if change {
		if err := f.caps.AllowWrite(p); err != nil {
			return -1, "", "", capErr(err)
		}
	}
	dfd, real, err := f.openDir(path.Dir(p), change)
	if err != nil {
		return -1, "", "", err
	}
	name := path.Base(p)
	full := path.Join(real, name)
	if err := f.caps.AllowPath(full); err != nil {
		unix.Close(dfd)
		return -1, "", "", capErr(err)
	}
	if change {
		if err := f.caps.AllowWrite(full); err != nil {
			unix.Close(dfd)
			return -1, "", "", capErr(err)
		}
		if err := f.safeDir(dfd, real); err != nil {
			unix.Close(dfd)
			return -1, "", "", err
		}
	}
	return dfd, name, full, nil
}

// safeDir refuses root writes into directories other users can modify
// (same rule as the SSH path, ARCHITECTURE-ALPHA §10.6).
func (f *FS) safeDir(dfd int, dir string) error {
	var st unix.Stat_t
	if err := unix.Fstat(dfd, &st); err != nil {
		return mapErr(dir, err)
	}
	if (int(st.Uid) != 0 && int(st.Uid) != f.uid) || st.Mode&0o022 != 0 {
		return files.Errorf(files.CodeUnsafeDir, "%s can be modified by other users, so DROOL won't write there as root", dir).
			WithHint("Use a directory owned by root that only root can write to.")
	}
	return nil
}

func mapErr(p string, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, unix.ENOENT):
		return files.Errorf(files.CodeNotFound, "%s does not exist", p)
	case errors.Is(err, unix.EACCES), errors.Is(err, unix.EPERM):
		return files.Errorf(files.CodePermission, "%s: permission denied", p)
	case errors.Is(err, unix.ENOTDIR):
		return files.Errorf(files.CodeNotDir, "%s is not a directory", p)
	case errors.Is(err, unix.EEXIST):
		return files.Errorf(files.CodeExists, "%s already exists", p)
	case errors.Is(err, unix.ENOTEMPTY):
		return files.Errorf(files.CodeNotEmpty, "%s is not empty", p)
	case errors.Is(err, unix.ELOOP):
		return files.Errorf(files.CodeSymlink, "%s is a symlink", p)
	}
	var fe *files.Error
	if errors.As(err, &fe) {
		return err
	}
	return fmt.Errorf("%s: %w", p, err)
}

// --- reading ---

func (f *FS) name(id int, group bool) string {
	key := fmt.Sprintf("%v:%d", group, id)
	f.mu.Lock()
	n, ok := f.names[key]
	f.mu.Unlock()
	if ok {
		return n
	}
	n = strconv.Itoa(id)
	if group {
		if g, err := user.LookupGroupId(n); err == nil {
			n = g.Name
		}
	} else if u, err := user.LookupId(n); err == nil {
		n = u.Username
	}
	f.mu.Lock()
	f.names[key] = n
	f.mu.Unlock()
	return n
}

func (f *FS) info(dfd int, name, full string) (files.Info, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(dfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return files.Info{}, mapErr(full, err)
	}
	in := files.Info{Name: path.Base(full), Path: full, Size: st.Size, Mode: st.Mode & 0o7777,
		UID: int(st.Uid), GID: int(st.Gid), ModTime: time.Unix(st.Mtim.Sec, st.Mtim.Nsec).UTC()}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		in.Kind = files.KindFile
	case unix.S_IFDIR:
		in.Kind, in.Size = files.KindDir, 0
	case unix.S_IFLNK:
		in.Kind = files.KindSymlink
		buf := make([]byte, 4096)
		if n, err := unix.Readlinkat(dfd, name, buf); err == nil {
			in.Target = string(buf[:n])
		}
	default:
		in.Kind = files.KindOther
	}
	in.Owner, in.Group = f.name(in.UID, false), f.name(in.GID, true)
	return in, nil
}

// RealPath implements files.FS.
func (f *FS) RealPath(ctx context.Context, p string) (string, error) {
	fd, real, err := f.openDir(path.Clean(p), false)
	if err != nil {
		return "", err
	}
	unix.Close(fd)
	return real, nil
}

// Stat implements files.FS.
func (f *FS) Stat(ctx context.Context, p string) (files.Info, error) {
	p = path.Clean(p)
	if p == "/" {
		fd, real, err := f.openDir("/", false)
		if err != nil {
			return files.Info{}, err
		}
		defer unix.Close(fd)
		in, err := f.info(fd, ".", real)
		in.Name, in.Path = "/", "/"
		return in, err
	}
	dfd, name, full, err := f.entry(p, false)
	if err != nil {
		return files.Info{}, err
	}
	defer unix.Close(dfd)
	in, err := f.info(dfd, name, full)
	in.Path = p
	return in, err
}

// List implements files.FS.
func (f *FS) List(ctx context.Context, dir string) ([]files.Info, error) {
	dir = path.Clean(dir)
	fd, real, err := f.openDir(dir, false)
	if err != nil {
		return nil, err
	}
	d := os.NewFile(uintptr(fd), real)
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return nil, mapErr(dir, err)
	}
	out := make([]files.Info, 0, len(names))
	for _, n := range names {
		full := path.Join(real, n)
		if f.caps.AllowPath(full) != nil {
			continue // e.g. the agent's own directories
		}
		in, err := f.info(fd, n, full)
		if err != nil {
			continue // vanished meanwhile
		}
		in.Path = path.Join(dir, n)
		out = append(out, in)
	}
	return out, nil
}

func (f *FS) openFile(p string) (*os.File, files.Info, error) {
	dfd, name, full, err := f.entry(p, false)
	if err != nil {
		return nil, files.Info{}, err
	}
	defer unix.Close(dfd)
	in, err := f.info(dfd, name, full)
	if err != nil {
		return nil, in, err
	}
	switch in.Kind {
	case files.KindSymlink:
		return nil, in, files.Errorf(files.CodeSymlink, "%s is a symlink to %s; open the target instead", p, in.Target)
	case files.KindFile:
	default:
		return nil, in, files.Errorf(files.CodeNotRegular, "%s is not a regular file", p)
	}
	fd, err := unix.Openat(dfd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, in, mapErr(p, err)
	}
	in.Path = p
	return os.NewFile(uintptr(fd), full), in, nil
}

// Open implements files.FS.
func (f *FS) Open(ctx context.Context, p string) (io.ReadCloser, files.Info, error) {
	fh, in, err := f.openFile(p)
	if err != nil {
		return nil, in, err
	}
	return fh, in, nil
}

// Hash implements files.FS.
func (f *FS) Hash(ctx context.Context, p string) (string, error) {
	fh, _, err := f.openFile(p)
	if err != nil {
		return "", err
	}
	defer fh.Close()
	h := sha256.New()
	if _, err := io.Copy(h, fh); err != nil {
		return "", mapErr(p, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// --- writing ---

func randName() string {
	b := make([]byte, 6)
	rand.Read(b)
	return ".drool-tmp-" + hex.EncodeToString(b)
}

type staged struct {
	f       *FS
	dfd     int
	dir     string
	name    string // destination name
	tmp     string
	sha     string
	size    int64
	once    sync.Once
	removed bool
}

func (s *staged) Path() string   { return path.Join(s.dir, s.tmp) }
func (s *staged) SHA256() string { return s.sha }
func (s *staged) Size() int64    { return s.size }

func (f *FS) lookupIDs(owner, group string, def unix.Stat_t, exists bool) (int, int, error) {
	uid, gid := f.uid, os.Getgid()
	if exists {
		uid, gid = int(def.Uid), int(def.Gid)
	}
	if owner != "" {
		u, err := user.Lookup(owner)
		if err != nil {
			if n, e := strconv.Atoi(owner); e == nil {
				uid = n
			} else {
				return 0, 0, files.Errorf(files.CodeInvalid, "unknown user %q", owner)
			}
		} else {
			uid, _ = strconv.Atoi(u.Uid)
		}
	}
	if group != "" {
		g, err := user.LookupGroup(group)
		if err != nil {
			if n, e := strconv.Atoi(group); e == nil {
				gid = n
			} else {
				return 0, 0, files.Errorf(files.CodeInvalid, "unknown group %q", group)
			}
		} else {
			gid, _ = strconv.Atoi(g.Gid)
		}
	}
	return uid, gid, nil
}

// Stage implements files.FS.
func (f *FS) Stage(ctx context.Context, p string, r io.Reader, opt files.StageOptions) (files.Staged, error) {
	dfd, name, full, err := f.entry(p, true)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			unix.Close(dfd)
		}
	}()
	var cur unix.Stat_t
	exists := false
	if err := unix.Fstatat(dfd, name, &cur, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		switch cur.Mode & unix.S_IFMT {
		case unix.S_IFREG:
			exists = true
		case unix.S_IFLNK:
			return nil, files.Errorf(files.CodeSymlink, "%s is a symlink; DROOL won't replace a symlink", p)
		default:
			return nil, files.Errorf(files.CodeNotRegular, "%s exists and is not a regular file", p)
		}
	}
	mode := uint32(0o644)
	if exists {
		mode = cur.Mode & 0o7777
	}
	if opt.Mode != nil {
		mode = *opt.Mode
	}
	if err := f.caps.AllowMode(mode); err != nil {
		return nil, capErr(err)
	}
	uid, gid, err := f.lookupIDs(opt.Owner, opt.Group, cur, exists)
	if err != nil {
		return nil, err
	}
	dir := path.Dir(full)
	tmp := randName()
	fd, err := unix.Openat(dfd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, mapErr(path.Join(dir, tmp), err)
	}
	fh := os.NewFile(uintptr(fd), path.Join(dir, tmp))
	st := &staged{f: f, dfd: dfd, dir: dir, name: name, tmp: tmp}
	fail := func(err error) (files.Staged, error) {
		fh.Close()
		unix.Unlinkat(dfd, tmp, 0)
		return nil, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(fh, h), ctxReader{ctx, r})
	if err != nil {
		return fail(err)
	}
	st.sha, st.size = hex.EncodeToString(h.Sum(nil)), n
	if opt.SHA256 != "" && opt.SHA256 != st.sha {
		return fail(files.Errorf(files.CodeIntegrity, "the received content's sha256 %s does not match the expected %s", st.sha[:12], opt.SHA256[:min(12, len(opt.SHA256))]))
	}
	if err := fh.Chown(uid, gid); err != nil {
		return fail(mapErr(p, err))
	}
	if err := fh.Chmod(os.FileMode(mode&0o777) | modeBits(mode)); err != nil {
		return fail(mapErr(p, err))
	}
	if err := fh.Sync(); err != nil {
		return fail(err)
	}
	if err := fh.Close(); err != nil {
		unix.Unlinkat(dfd, tmp, 0)
		return nil, err
	}
	keep = true
	return st, nil
}

func modeBits(m uint32) os.FileMode {
	var out os.FileMode
	if m&0o4000 != 0 {
		out |= os.ModeSetuid
	}
	if m&0o2000 != 0 {
		out |= os.ModeSetgid
	}
	if m&0o1000 != 0 {
		out |= os.ModeSticky
	}
	return out
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// Validate implements files.Staged. The command runs as root: without the
// shell capability only templates listed in the ceiling are allowed.
func (s *staged) Validate(ctx context.Context, cmd string, out io.Writer) (files.ValidationResult, error) {
	res := files.ValidationResult{Command: cmd}
	if err := s.f.caps.AllowValidation(cmd); err != nil {
		return res, capErr(err)
	}
	full := strings.ReplaceAll(cmd, "{path}", "'"+strings.ReplaceAll(s.Path(), "'", `'\''`)+"'")
	r, err := s.f.run.Run(ctx, full, hostexec.RunOptions{MaxOutput: 1 << 20})
	if err != nil {
		return res, err
	}
	out.Write(r.Stdout)
	out.Write(r.Stderr)
	res.ExitCode, res.OK = r.ExitCode, r.ExitCode == 0
	return res, nil
}

func hashAt(dfd int, name string) (string, error) {
	fd, err := unix.Openat(dfd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	fh := os.NewFile(uintptr(fd), name)
	defer fh.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return "", err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return "", files.Errorf(files.CodeNotRegular, "%s is not a regular file", name)
	}
	h := sha256.New()
	if _, err := io.Copy(h, fh); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Install implements files.Staged: check the precondition, then rename
// atomically within the directory and verify.
func (s *staged) Install(ctx context.Context, pre files.Precondition) (*files.Installed, error) {
	dest := path.Join(s.dir, s.name)
	var st unix.Stat_t
	err := unix.Fstatat(s.dfd, s.name, &st, unix.AT_SYMLINK_NOFOLLOW)
	exists := err == nil
	switch {
	case exists && pre.MustNotExist:
		return nil, files.Errorf(files.CodeExists, "%s already exists", dest)
	case exists && st.Mode&unix.S_IFMT == unix.S_IFLNK:
		return nil, files.Errorf(files.CodeSymlink, "%s became a symlink; not replacing it", dest)
	case pre.ExpectSHA256 != "":
		if !exists {
			return nil, files.Errorf(files.CodeConflict, "%s was removed since it was read", dest)
		}
		cur, err := hashAt(s.dfd, s.name)
		if err != nil {
			return nil, mapErr(dest, err)
		}
		if cur != pre.ExpectSHA256 {
			return nil, files.Errorf(files.CodeConflict, "%s changed since it was read", dest)
		}
	}
	if pre.MustNotExist {
		err = unix.Renameat2(s.dfd, s.tmp, s.dfd, s.name, unix.RENAME_NOREPLACE)
	} else {
		err = unix.Renameat(s.dfd, s.tmp, s.dfd, s.name)
	}
	if err != nil {
		return nil, mapErr(dest, err)
	}
	s.removed = true
	unix.Fsync(s.dfd)
	got, err := hashAt(s.dfd, s.name)
	if err != nil {
		return nil, mapErr(dest, err)
	}
	if got != s.sha {
		return nil, files.Errorf(files.CodeVerification, "%s does not have the installed content after the rename", dest)
	}
	in, err := s.f.info(s.dfd, s.name, dest)
	if err != nil {
		return nil, err
	}
	return &files.Installed{Info: in, SHA256: got}, nil
}

// Discard implements files.Staged.
func (s *staged) Discard(ctx context.Context) {
	s.once.Do(func() {
		if !s.removed {
			unix.Unlinkat(s.dfd, s.tmp, 0)
		}
		unix.Close(s.dfd)
	})
}

// Mkdir implements files.FS.
func (f *FS) Mkdir(ctx context.Context, p string, mode uint32) error {
	if err := f.caps.AllowMode(mode); err != nil {
		return capErr(err)
	}
	dfd, name, full, err := f.entry(p, true)
	if err != nil {
		return err
	}
	defer unix.Close(dfd)
	if err := unix.Mkdirat(dfd, name, 0o700); err != nil {
		return mapErr(full, err)
	}
	fd, err := unix.Openat(dfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return mapErr(full, err)
	}
	defer unix.Close(fd)
	return mapErr(full, unix.Fchmod(fd, mode&0o7777))
}

// Rename implements files.FS. It never replaces an existing entry.
func (f *FS) Rename(ctx context.Context, from, to string) error {
	if from == "/" || agentcaps.Within(path.Clean(to), path.Clean(from)) {
		return files.Errorf(files.CodeInvalid, "can't move %s into itself", from)
	}
	sfd, sname, sfull, err := f.entry(from, true)
	if err != nil {
		return err
	}
	defer unix.Close(sfd)
	tfd, tname, tfull, err := f.entry(to, true)
	if err != nil {
		return err
	}
	defer unix.Close(tfd)
	if err := unix.Renameat2(sfd, sname, tfd, tname, unix.RENAME_NOREPLACE); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return files.Errorf(files.CodeExists, "%s already exists", tfull)
		}
		return mapErr(sfull, err)
	}
	return nil
}

func shallow(p string) bool { return strings.Count(strings.Trim(p, "/"), "/") < 1 }

// Remove implements files.FS.
func (f *FS) Remove(ctx context.Context, p string, recursive bool) error {
	p = path.Clean(p)
	if recursive && shallow(p) {
		return files.Errorf(files.CodeInvalid, "DROOL won't recursively delete a top-level directory (%s)", p)
	}
	dfd, name, full, err := f.entry(p, true)
	if err != nil {
		return err
	}
	defer unix.Close(dfd)
	var st unix.Stat_t
	if err := unix.Fstatat(dfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return mapErr(full, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return mapErr(full, unix.Unlinkat(dfd, name, 0))
	}
	if recursive {
		if err := removeContents(ctx, dfd, name); err != nil {
			return mapErr(full, err)
		}
	}
	return mapErr(full, unix.Unlinkat(dfd, name, unix.AT_REMOVEDIR))
}

// removeContents empties a directory through file descriptors, never
// following symlinks.
func removeContents(ctx context.Context, parent int, name string) error {
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	d := os.NewFile(uintptr(fd), name)
	defer d.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		names, err := d.Readdirnames(256)
		if len(names) == 0 {
			if err == io.EOF || err == nil {
				return nil
			}
			return err
		}
		for _, n := range names {
			var st unix.Stat_t
			if err := unix.Fstatat(fd, n, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				continue
			}
			if st.Mode&unix.S_IFMT == unix.S_IFDIR {
				if err := removeContents(ctx, fd, n); err != nil {
					return err
				}
				if err := unix.Unlinkat(fd, n, unix.AT_REMOVEDIR); err != nil {
					return err
				}
			} else if err := unix.Unlinkat(fd, n, 0); err != nil {
				return err
			}
		}
	}
}

// Chmod implements files.FS (never through a symlink).
func (f *FS) Chmod(ctx context.Context, p string, mode uint32) error {
	if err := f.caps.AllowMode(mode); err != nil {
		return capErr(err)
	}
	dfd, name, full, err := f.entry(p, true)
	if err != nil {
		return err
	}
	defer unix.Close(dfd)
	fd, err := unix.Openat(dfd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return files.Errorf(files.CodeSymlink, "%s is a symlink; change the target instead", p)
		}
		return mapErr(full, err)
	}
	defer unix.Close(fd)
	return mapErr(full, unix.Fchmod(fd, mode&0o7777))
}

// Chown implements files.FS (never through a symlink).
func (f *FS) Chown(ctx context.Context, p, owner, group string) error {
	if owner == "" && group == "" {
		return files.Errorf(files.CodeInvalid, "give an owner, a group, or both")
	}
	dfd, name, full, err := f.entry(p, true)
	if err != nil {
		return err
	}
	defer unix.Close(dfd)
	var st unix.Stat_t
	if err := unix.Fstatat(dfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return mapErr(full, err)
	}
	if st.Mode&unix.S_IFMT == unix.S_IFLNK {
		return files.Errorf(files.CodeSymlink, "%s is a symlink; change the target instead", p)
	}
	uid, gid, err := f.lookupIDs(owner, group, st, true)
	if err != nil {
		return err
	}
	return mapErr(full, unix.Fchownat(dfd, name, uid, gid, unix.AT_SYMLINK_NOFOLLOW))
}
