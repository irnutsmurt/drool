package sshdriver

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"

	"drool/internal/files"
	"drool/internal/model"
	"drool/internal/sshx"
)

// FileSystem implements files.FS over SSH (ARCHITECTURE-ALPHA §10.6).
//
// As the login user it uses SFTP. Elevated sessions never run an SFTP
// server as root: every privileged step is an explicit, small POSIX command
// run through the host's configured privilege escalation, and file content
// moves over SFTP through a private temporary directory owned by the login
// user, so content and a sudo password never share a stream.
type FileSystem struct {
	c        *sshx.Client
	release  func()
	become   Become // escalation for elevated steps (Method "none" when logged in as root)
	user     string
	elevated bool
	sftp     *sftp.Client
	upDir    string // private upload directory (elevated staging), created on demand
}

// OpenFS opens a filesystem session. elevated sessions need root login or
// sudo; they are refused otherwise.
func OpenFS(ctx context.Context, pool *sshx.Pool, t *sshx.Target, b Become, elevated bool) (*FileSystem, error) {
	if elevated {
		var err error
		if b, err = rootBecome(t, b); err != nil {
			return nil, err
		}
	}
	sess, err := pool.Get(ctx, t)
	if err != nil {
		return nil, err
	}
	fsys := &FileSystem{c: sess.Client, release: sess.Release, become: b, user: t.User, elevated: elevated || t.User == "root"}
	sc, err := sftp.NewClient(sess.Client.Client, sftp.UseConcurrentWrites(true))
	if err != nil {
		sess.Release()
		return nil, &model.OpError{Class: model.ClassConfiguration, Err: err,
			Message: "the host does not offer SFTP: " + err.Error(),
			Hint:    "Enable the sftp subsystem in sshd_config (Subsystem sftp ...); most distributions have it by default."}
	}
	fsys.sftp = sc
	return fsys, nil
}

// Close ends the session and removes the private upload directory.
func (f *FileSystem) Close() error {
	if f.upDir != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		f.exec(ctx, false, `rm -rf -- "$1"`, f.upDir)
		cancel()
	}
	f.sftp.Close()
	f.release()
	return nil
}

func (f *FileSystem) Caps() files.Caps {
	tr := "sftp"
	if f.elevated && f.user != "root" {
		tr = "ssh-sudo"
	}
	// SSH accounts are bounded by their own permissions; root (directly or
	// through sudo) can write anywhere.
	return files.Caps{Transport: tr, Elevated: f.elevated, RootEquivalent: f.elevated}
}

// --- remote commands ---

// execResult is the output of a short remote command.
type execResult struct {
	out  string
	err  string
	exit int
}

// exec runs a POSIX script with positional args, as root when asRoot.
func (f *FileSystem) exec(ctx context.Context, asRoot bool, script string, args ...string) (*execResult, error) {
	return f.execT(ctx, asRoot, 2*time.Minute, script, args...)
}

// longOp bounds steps that read or copy whole files.
const longOp = 30 * time.Minute

func (f *FileSystem) execT(ctx context.Context, asRoot bool, timeout time.Duration, script string, args ...string) (*execResult, error) {
	cmd := "sh -c " + shQuote(script) + " sh"
	for _, a := range args {
		cmd += " " + shQuote(a)
	}
	b := Become{Method: "none"}
	if asRoot {
		b = f.become
	}
	r, err := run(ctx, f.c, b, asRoot, cmd, timeout)
	if err != nil {
		return nil, err
	}
	res := &execResult{out: strings.ReplaceAll(string(r.Stdout), "\r", ""), err: string(r.Stderr), exit: r.ExitCode}
	if asRoot && b.Method == "sudo" && r.ExitCode == 1 && looksLikeSudoFailure(res.err+res.out) {
		detail, hint := classifySudo(res.err + res.out)
		return nil, &model.OpError{Class: model.ClassAuthorization, Message: detail, Hint: hint}
	}
	return res, nil
}

// scriptErr turns "@@ERR code message" lines into files errors.
func scriptErr(r *execResult) error {
	for _, l := range strings.Split(r.out, "\n") {
		if rest, ok := strings.CutPrefix(l, "@@ERR "); ok {
			code, msg, _ := strings.Cut(rest, " ")
			e := files.Errorf(code, "%s", msg)
			switch code {
			case files.CodeUnsafeDir:
				e.Hint = "Root writes into a directory that other users can modify are refused (they could redirect the write with a symlink). Use the login account without administrator privileges if it owns the directory."
			case files.CodeSymlink:
				e.Hint = "DROOL doesn't follow symlinks when changing files. Use the real path."
			case files.CodeConflict:
				e.Hint = "Reload it, or explicitly overwrite the newer version."
			}
			return e
		}
	}
	if r.exit != 0 {
		return classifyStderr(r.err)
	}
	return nil
}

func classifyStderr(stderr string) error {
	s := strings.TrimSpace(stderr)
	low := strings.ToLower(s)
	switch {
	case strings.Contains(low, "permission denied"), strings.Contains(low, "operation not permitted"):
		return permissionErr(s)
	case strings.Contains(low, "no such file"):
		return files.Errorf(files.CodeNotFound, "%s", firstLine(s))
	case strings.Contains(low, "read-only file system"):
		return files.Errorf(files.CodePermission, "%s", firstLine(s))
	}
	if s == "" {
		s = "the command failed"
	}
	return files.Errorf("failed", "%s", firstLine(s))
}

func permissionErr(detail string) error {
	return files.Errorf(files.CodePermission, "permission denied: %s", firstLine(detail)).
		WithHint("The login account can't do this. Turn on \"Use administrator privileges\" if it may use sudo.")
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return l
}

// sftpErr maps SFTP errors.
func sftpErr(p string, err error) error {
	if err == nil {
		return nil
	}
	var fe *files.Error
	if errors.As(err, &fe) {
		return err
	}
	var se *sftp.StatusError
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return files.Errorf(files.CodeNotFound, "%s does not exist", p)
	case errors.Is(err, fs.ErrPermission):
		return permissionErr(p)
	case errors.As(err, &se) && se.FxCode() == sftp.ErrSSHFxPermissionDenied:
		return permissionErr(p)
	case errors.As(err, &se) && se.FxCode() == sftp.ErrSSHFxNoSuchFile:
		return files.Errorf(files.CodeNotFound, "%s does not exist", p)
	}
	var oe *model.OpError
	if errors.As(err, &oe) {
		return err
	}
	return &model.OpError{Class: model.ClassConnection, Err: err, Message: "file transfer failed: " + err.Error()}
}

// --- metadata ---

func (f *FileSystem) Home(ctx context.Context) (string, error) {
	wd, err := f.sftp.Getwd()
	if err != nil || wd == "" {
		return "/", nil
	}
	return wd, nil
}

func (f *FileSystem) RealPath(ctx context.Context, p string) (string, error) {
	if f.elevated {
		r, err := f.exec(ctx, true, `cd -P -- "$1" 2>/dev/null || { [ -e "$1" ] && echo "@@ERR not_directory $1 is not a directory" || echo "@@ERR not_found $1 does not exist"; exit 3; }; pwd -P`, p)
		if err != nil {
			return "", err
		}
		if err := scriptErr(r); err != nil {
			return "", err
		}
		return strings.TrimSpace(r.out), nil
	}
	rp, err := f.sftp.RealPath(p)
	return rp, sftpErr(p, err)
}

// findFormat prints one entry as 10 NUL-terminated fields.
const findFormat = `%y\0%s\0%m\0%U\0%G\0%u\0%g\0%T@\0%l\0%p\0`

func parseFind(out string) []files.Info {
	fields := strings.Split(out, "\x00")
	var res []files.Info
	for i := 0; i+10 <= len(fields); i += 10 {
		f := fields[i : i+10]
		info := files.Info{Path: f[9], Name: path.Base(f[9]), Owner: f[5], Group: f[6], Target: f[8]}
		switch f[0] {
		case "f":
			info.Kind = files.KindFile
		case "d":
			info.Kind = files.KindDir
		case "l":
			info.Kind = files.KindSymlink
		default:
			info.Kind = files.KindOther
		}
		info.Size, _ = strconv.ParseInt(f[1], 10, 64)
		m, _ := strconv.ParseUint(f[2], 8, 32)
		info.Mode = uint32(m)
		info.UID, _ = strconv.Atoi(f[3])
		info.GID, _ = strconv.Atoi(f[4])
		if sec, err := strconv.ParseFloat(f[7], 64); err == nil {
			info.ModTime = time.Unix(0, int64(sec*1e9)).UTC()
		}
		res = append(res, info)
	}
	return res
}

func (f *FileSystem) Stat(ctx context.Context, p string) (files.Info, error) {
	if f.elevated {
		r, err := f.exec(ctx, true, `[ -e "$1" ] || [ -L "$1" ] || { echo "@@ERR not_found $1 does not exist"; exit 3; }; find -P "$1" -maxdepth 0 -printf '`+findFormat+`'`, p)
		if err != nil {
			return files.Info{}, err
		}
		if err := scriptErr(r); err != nil {
			return files.Info{}, err
		}
		infos := parseFind(r.out)
		if len(infos) != 1 {
			return files.Info{}, files.Errorf("failed", "could not read %s", p)
		}
		return infos[0], nil
	}
	fi, err := f.sftp.Lstat(p)
	if err != nil {
		return files.Info{}, sftpErr(p, err)
	}
	info := infoFromSFTP(p, fi)
	if info.Kind == files.KindSymlink {
		info.Target, _ = f.sftp.ReadLink(p)
	}
	f.names(ctx, []*files.Info{&info})
	return info, nil
}

func infoFromSFTP(p string, fi os.FileInfo) files.Info {
	info := files.Info{Path: p, Name: path.Base(p), Size: fi.Size(), ModTime: fi.ModTime().UTC()}
	m := fi.Mode()
	switch {
	case m.IsRegular():
		info.Kind = files.KindFile
	case m.IsDir():
		info.Kind = files.KindDir
	case m&fs.ModeSymlink != 0:
		info.Kind = files.KindSymlink
	default:
		info.Kind = files.KindOther
	}
	info.Mode = uint32(m.Perm())
	if m&fs.ModeSetuid != 0 {
		info.Mode |= 04000
	}
	if m&fs.ModeSetgid != 0 {
		info.Mode |= 02000
	}
	if m&fs.ModeSticky != 0 {
		info.Mode |= 01000
	}
	if st, ok := fi.Sys().(*sftp.FileStat); ok {
		info.UID, info.GID = int(st.UID), int(st.GID)
	}
	info.Owner, info.Group = strconv.Itoa(info.UID), strconv.Itoa(info.GID)
	return info
}

// names fills owner and group names with one getent lookup.
func (f *FileSystem) names(ctx context.Context, infos []*files.Info) {
	uids, gids := map[int]bool{}, map[int]bool{}
	for _, i := range infos {
		uids[i.UID], gids[i.GID] = true, true
	}
	var args []string
	for u := range uids {
		args = append(args, "u"+strconv.Itoa(u))
	}
	for g := range gids {
		args = append(args, "g"+strconv.Itoa(g))
	}
	if len(args) == 0 {
		return
	}
	r, err := f.exec(ctx, false, `for a; do case $a in u*) e=$(getent passwd "${a#u}") && printf 'u %s %s\n' "${a#u}" "${e%%:*}";; g*) e=$(getent group "${a#g}") && printf 'g %s %s\n' "${a#g}" "${e%%:*}";; esac; done; true`, args...)
	if err != nil {
		return
	}
	users, groups := map[string]string{}, map[string]string{}
	for _, l := range strings.Split(r.out, "\n") {
		parts := strings.Fields(l)
		if len(parts) != 3 {
			continue
		}
		if parts[0] == "u" {
			users[parts[1]] = parts[2]
		} else {
			groups[parts[1]] = parts[2]
		}
	}
	for _, i := range infos {
		if n := users[strconv.Itoa(i.UID)]; n != "" {
			i.Owner = n
		}
		if n := groups[strconv.Itoa(i.GID)]; n != "" {
			i.Group = n
		}
	}
}

func (f *FileSystem) List(ctx context.Context, dir string) ([]files.Info, error) {
	if f.elevated {
		r, err := f.exec(ctx, true, `[ -d "$1" ] && [ ! -L "$1" ] || { [ -e "$1" ] && echo "@@ERR not_directory $1 is not a directory" || echo "@@ERR not_found $1 does not exist"; exit 3; }; find -P "$1" -mindepth 1 -maxdepth 1 -printf '`+findFormat+`'`, dir)
		if err != nil {
			return nil, err
		}
		if err := scriptErr(r); err != nil {
			return nil, err
		}
		return parseFind(r.out), nil
	}
	fis, err := f.sftp.ReadDir(dir)
	if err != nil {
		if fi, serr := f.sftp.Lstat(dir); serr == nil && !fi.IsDir() {
			return nil, files.Errorf(files.CodeNotDir, "%s is not a directory", dir)
		}
		return nil, sftpErr(dir, err)
	}
	infos := make([]files.Info, len(fis))
	ptrs := make([]*files.Info, len(fis))
	for i, fi := range fis {
		infos[i] = infoFromSFTP(path.Join(dir, fi.Name()), fi)
		if infos[i].Kind == files.KindSymlink {
			infos[i].Target, _ = f.sftp.ReadLink(infos[i].Path)
		}
		ptrs[i] = &infos[i]
	}
	f.names(ctx, ptrs)
	return infos, nil
}

// --- reading ---

func (f *FileSystem) Hash(ctx context.Context, p string) (string, error) {
	r, err := f.execT(ctx, f.elevated, longOp, `[ -L "$1" ] && { echo "@@ERR symlink $1 is a symlink"; exit 3; }; [ -e "$1" ] || { echo "@@ERR not_found $1 does not exist"; exit 3; }; [ -f "$1" ] || { echo "@@ERR not_regular $1 is not a regular file"; exit 3; }; sha256sum < "$1"`, p)
	if err != nil {
		return "", err
	}
	if err := scriptErr(r); err != nil {
		return "", err
	}
	h := strings.Fields(r.out)
	if len(h) == 0 || len(h[0]) != 64 {
		return "", files.Errorf("failed", "could not hash %s", p)
	}
	return h[0], nil
}

func (f *FileSystem) checkRegular(info files.Info) error {
	switch info.Kind {
	case files.KindSymlink:
		return files.Errorf(files.CodeSymlink, "%s is a symlink to %s; open the target instead", info.Path, info.Target)
	case files.KindFile:
		return nil
	case files.KindDir:
		return files.Errorf(files.CodeNotRegular, "%s is a directory", info.Path)
	}
	return files.Errorf(files.CodeNotRegular, "%s is not a regular file", info.Path)
}

func (f *FileSystem) Open(ctx context.Context, p string) (io.ReadCloser, files.Info, error) {
	info, err := f.Stat(ctx, p)
	if err != nil {
		return nil, info, err
	}
	if err := f.checkRegular(info); err != nil {
		return nil, info, err
	}
	if !f.elevated {
		fh, err := f.sftp.Open(p)
		if err != nil {
			return nil, info, sftpErr(p, err)
		}
		return fh, info, nil
	}
	if f.become.PTY {
		// A terminal would mangle binary data: copy into the private upload
		// directory as root, hand it to the login user, read it over SFTP.
		up, err := f.uploadDir(ctx)
		if err != nil {
			return nil, info, err
		}
		dst := path.Join(up, "read")
		r, err := f.exec(ctx, true, `[ -L "$1" ] && { echo "@@ERR symlink $1 is a symlink"; exit 3; }; cp -- "$1" "$2" && chown -- "$3" "$2"`, p, dst, f.user)
		if err != nil {
			return nil, info, err
		}
		if err := scriptErr(r); err != nil {
			return nil, info, err
		}
		fh, err := f.sftp.Open(dst)
		if err != nil {
			return nil, info, sftpErr(dst, err)
		}
		return fh, info, nil
	}
	pr, pw := io.Pipe()
	var stderr bytes.Buffer
	cmd, stdin := f.become.Wrap("cat -- " + shQuote(p))
	running, err := f.c.Start(cmd, stdin, pw, &stderr, false)
	if err != nil {
		return nil, info, err
	}
	go func() {
		code, err := running.ExitCode()
		switch {
		case err != nil:
			pw.CloseWithError(err)
		case code != 0:
			pw.CloseWithError(classifyStderr(stderr.String()))
		default:
			pw.Close()
		}
	}()
	return &streamReader{PipeReader: pr, stop: running.Close}, info, nil
}

type streamReader struct {
	*io.PipeReader
	stop func()
}

func (s *streamReader) Close() error {
	s.stop()
	return s.PipeReader.Close()
}

// --- writing ---

// uploadDir creates the login user's private directory for elevated
// staging (0700, on disk rather than a tmpfs).
func (f *FileSystem) uploadDir(ctx context.Context) (string, error) {
	if f.upDir != "" {
		return f.upDir, nil
	}
	r, err := f.exec(ctx, false, `find "${TMPDIR:-/var/tmp}" -maxdepth 1 -name 'drool-up.*' -user "$(id -u)" -mmin +1440 -exec rm -rf {} + 2>/dev/null
mktemp -d "${TMPDIR:-/var/tmp}/drool-up.XXXXXXXX"`)
	if err != nil {
		return "", err
	}
	d := strings.TrimSpace(r.out)
	if r.exit != 0 || !strings.HasPrefix(d, "/") {
		return "", files.Errorf("failed", "could not create a private upload directory: %s", firstLine(r.err))
	}
	f.upDir = d
	return d, nil
}

// hashingReader computes the SHA-256 and size of what passes through.
type hashingReader struct {
	r io.Reader
	h interface {
		io.Writer
		Sum([]byte) []byte
	}
	n int64
}

func newHashingReader(r io.Reader) *hashingReader { return &hashingReader{r: r, h: sha256.New()} }

func (h *hashingReader) Read(p []byte) (int, error) {
	n, err := h.r.Read(p)
	h.h.Write(p[:n])
	h.n += int64(n)
	return n, err
}

func (h *hashingReader) sum() string { return hex.EncodeToString(h.h.Sum(nil)) }

// ctxReader stops reading when ctx ends (an abandoned upload must not keep
// streaming).
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

func randomSuffix() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// staged implements files.Staged for both modes.
type staged struct {
	f      *FileSystem
	dir    string // real directory of the destination
	name   string // destination entry name
	tmp    string // staged file name inside dir
	sha    string
	size   int64
	mode   uint32
	owner  string
	group  string
	closed bool
}

func (s *staged) Path() string   { return path.Join(s.dir, s.tmp) }
func (s *staged) SHA256() string { return s.sha }
func (s *staged) Size() int64    { return s.size }

// preflight is shared by both modes: the destination's directory must be
// its own real path, and the destination must not be a symlink or a
// non-regular file. It returns the existing file's info, if any.
func (f *FileSystem) preflight(ctx context.Context, p string) (dir, name string, existing *files.Info, err error) {
	dir, name = path.Dir(p), path.Base(p)
	if _, err := files.CleanName(name); err != nil || p == "/" {
		return "", "", nil, files.Errorf(files.CodeInvalid, "%s is not a file path", p)
	}
	real, err := f.RealPath(ctx, dir)
	if err != nil {
		return "", "", nil, err
	}
	if real != dir {
		return "", "", nil, files.Errorf(files.CodeSymlink, "%s resolves to %s through a symlink; use the real path", dir, real).
			WithHint("DROOL doesn't follow symlinks when changing files.")
	}
	info, err := f.Stat(ctx, p)
	var fe *files.Error
	switch {
	case err == nil:
		if err := f.checkRegular(info); err != nil {
			if info.Kind == files.KindSymlink {
				return "", "", nil, files.Errorf(files.CodeSymlink, "%s is a symlink to %s; DROOL won't replace a symlink with a file. Use the target path.", p, info.Target)
			}
			return "", "", nil, err
		}
		return dir, name, &info, nil
	case errors.As(err, &fe) && fe.Code == files.CodeNotFound:
		return dir, name, nil, nil
	}
	return "", "", nil, err
}

func (f *FileSystem) Stage(ctx context.Context, p string, r io.Reader, opt files.StageOptions) (files.Staged, error) {
	dir, name, existing, err := f.preflight(ctx, p)
	if err != nil {
		return nil, err
	}
	st := &staged{f: f, dir: dir, name: name, owner: opt.Owner, group: opt.Group, mode: 0o644}
	if existing != nil {
		st.mode = existing.Mode
		if st.owner == "" {
			st.owner = existing.Owner
		}
		if st.group == "" {
			st.group = existing.Group
		}
	}
	if opt.Mode != nil {
		st.mode = *opt.Mode
	}
	hr := newHashingReader(ctxReader{ctx, r})
	if f.elevated {
		err = f.stageElevated(ctx, st, hr, opt.SHA256)
	} else {
		err = f.stageSFTP(ctx, st, hr, opt.SHA256)
	}
	if err != nil {
		return nil, err
	}
	return st, nil
}

func (f *FileSystem) stageSFTP(ctx context.Context, st *staged, hr *hashingReader, want string) error {
	st.tmp = ".drool-tmp-" + randomSuffix()
	tmpPath := path.Join(st.dir, st.tmp)
	fh, err := f.sftp.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return sftpErr(st.dir, err)
	}
	_, err = fh.ReadFrom(hr)
	if cerr := fh.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		st.sha, st.size = hr.sum(), hr.n
		if want != "" && st.sha != want {
			err = files.Errorf(files.CodeIntegrity, "content checksum mismatch (got %s, expected %s)", st.sha, want)
		}
	}
	if err == nil {
		if cerr := f.sftp.Chmod(tmpPath, fs.FileMode(st.mode&0o777)); cerr != nil {
			err = sftpErr(tmpPath, cerr)
		}
	}
	if err == nil && st.mode&0o7000 != 0 {
		// SFTP can't set setuid/setgid/sticky portably.
		if r, cerr := f.exec(ctx, false, `chmod -- "$1" "$2"`, files.FormatMode(st.mode), tmpPath); cerr != nil || r.exit != 0 {
			err = files.Errorf(files.CodePermission, "could not set mode %s", files.FormatMode(st.mode))
		}
	}
	if err == nil && (st.owner != "" || st.group != "") {
		err = f.chownNoFollow(ctx, false, tmpPath, st.owner, st.group)
	}
	if err != nil {
		st.Discard(context.Background())
		var fe *files.Error
		var oe *model.OpError
		if errors.As(err, &fe) || errors.As(err, &oe) {
			return err
		}
		return sftpErr(st.dir, err)
	}
	return nil
}

// stageScript copies uploaded content into a root-owned temporary file next
// to the destination. $1 src, $2 dir, $3 name, $4 sha256, $5 mode, $6 owner,
// $7 group, $8 login uid.
const stageScript = `set -u
fail() { echo "@@ERR $1 $2"; exit 3; }
cd -P -- "$2" 2>/dev/null || fail not_found "directory $2 does not exist"
[ "$(pwd -P)" = "$2" ] || fail symlink "$2 resolves to $(pwd -P) through a symlink; use the real path"
du=$(stat -c %u .) || fail failed "cannot read $2"
dm=$(stat -c %a .)
if [ "$du" != 0 ] && [ "$du" != "$8" ]; then fail unsafe_directory "$2 belongs to another user (uid $du)"; fi
if [ $((0$dm & 022)) -ne 0 ]; then fail unsafe_directory "$2 is writable by its group or by everyone (mode $dm)"; fi
[ -L "$3" ] && fail symlink "$2/$3 is a symlink to $(readlink -- "$3"); use the target path"
if [ -e "$3" ] && [ ! -f "$3" ]; then fail not_regular "$2/$3 is not a regular file"; fi
find . -maxdepth 1 -name '.drool-tmp-*' -mmin +1440 -exec rm -f {} + 2>/dev/null
t=$(mktemp ./.drool-tmp-XXXXXXXXXX) || fail failed "cannot create a temporary file in $2"
cat -- "$1" > "$t" || { rm -f -- "$t"; fail failed "copy failed"; }
s=$(sha256sum < "$t" | cut -c1-64)
[ "$s" = "$4" ] || { rm -f -- "$t"; fail integrity "staged content does not match ($s)"; }
o=$6; g=$7
if [ -e "$3" ]; then [ -n "$o" ] || o=$(stat -c %U -- "$3"); [ -n "$g" ] || g=$(stat -c %G -- "$3"); fi
[ -n "$o" ] || o=root; [ -n "$g" ] || g=root
e=$(chown -h -- "$o:$g" "$t" 2>&1) || { rm -f -- "$t"; fail invalid "cannot set owner $o:$g: $e"; }
chmod -- "$5" "$t" || { rm -f -- "$t"; fail failed "cannot set mode $5"; }
echo "@@OK ${t#./} $o $g"`

func (f *FileSystem) stageElevated(ctx context.Context, st *staged, hr *hashingReader, want string) error {
	up, err := f.uploadDir(ctx)
	if err != nil {
		return err
	}
	src := path.Join(up, "upload-"+randomSuffix())
	fh, err := f.sftp.OpenFile(src, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return sftpErr(src, err)
	}
	_, err = fh.ReadFrom(hr)
	if cerr := fh.Close(); err == nil {
		err = cerr
	}
	defer f.sftp.Remove(src)
	if err != nil {
		var fe *files.Error
		if errors.As(err, &fe) || ctx.Err() != nil {
			return err
		}
		return sftpErr(src, err)
	}
	st.sha, st.size = hr.sum(), hr.n
	if want != "" && st.sha != want {
		return files.Errorf(files.CodeIntegrity, "content checksum mismatch (got %s, expected %s)", st.sha, want)
	}
	uid := "-1"
	if r, err := f.exec(ctx, false, `id -u`); err == nil {
		uid = strings.TrimSpace(r.out)
	}
	r, err := f.execT(ctx, true, longOp, stageScript, src, st.dir, st.name, st.sha, files.FormatMode(st.mode), st.owner, st.group, uid)
	if err != nil {
		return err
	}
	if err := scriptErr(r); err != nil {
		return err
	}
	for _, l := range strings.Split(r.out, "\n") {
		if rest, ok := strings.CutPrefix(l, "@@OK "); ok {
			parts := strings.Fields(rest)
			if len(parts) == 3 {
				st.tmp, st.owner, st.group = parts[0], parts[1], parts[2]
				return nil
			}
		}
	}
	return files.Errorf("failed", "staging did not complete")
}

func (s *staged) Validate(ctx context.Context, cmd string, out io.Writer) (files.ValidationResult, error) {
	full := strings.ReplaceAll(cmd, "{path}", shQuote(s.Path()))
	res := files.ValidationResult{Command: cmd}
	b := Become{Method: "none"}
	if s.f.elevated {
		b = s.f.become
	}
	r, err := run(ctx, s.f.c, b, s.f.elevated, "sh -c "+shQuote(full), 10*time.Minute)
	if err != nil {
		return res, err
	}
	out.Write(r.Stdout)
	out.Write(r.Stderr)
	res.ExitCode, res.OK = r.ExitCode, r.ExitCode == 0
	return res, nil
}

// installScript atomically replaces the destination. $1 dir, $2 staged name,
// $3 name, $4 expected sha256 ("" = any), $5 "1" = must not exist.
const installScript = `set -u
fail() { echo "@@ERR $1 $2"; exit 3; }
cd -P -- "$1" 2>/dev/null || fail not_found "directory $1 does not exist"
[ "$(pwd -P)" = "$1" ] || fail symlink "$1 resolves to $(pwd -P) through a symlink"
[ -f "$2" ] && [ ! -L "$2" ] || fail integrity "the staged file is gone"
[ -L "$3" ] && fail symlink "$1/$3 is a symlink"
if [ -e "$3" ]; then
  [ -f "$3" ] || fail not_regular "$1/$3 is not a regular file"
  [ "$5" = 1 ] && fail exists "$1/$3 already exists"
  if [ -n "$4" ]; then c=$(sha256sum < "$3" | cut -c1-64); [ "$c" = "$4" ] || fail changed "$1/$3 changed on the host after it was read (now $c)"; fi
elif [ -n "$4" ]; then fail changed "$1/$3 was removed on the host after it was read"
fi
mv -f -- "$2" "$3" || fail failed "could not replace $1/$3"
printf '@@SHA %s\n' "$(sha256sum < "$3" | cut -c1-64)"
find -P "$3" -maxdepth 0 -printf '` + findFormat + `'`

func (s *staged) Install(ctx context.Context, pre files.Precondition) (*files.Installed, error) {
	must := "0"
	if pre.MustNotExist {
		must = "1"
	}
	r, err := s.f.execT(ctx, s.f.elevated, longOp, installScript, s.dir, s.tmp, s.name, pre.ExpectSHA256, must)
	if err != nil {
		return nil, err
	}
	if err := scriptErr(r); err != nil {
		var fe *files.Error
		if errors.As(err, &fe) && fe.Code == files.CodeConflict && fe.Hint == "" {
			fe.Hint = "Reload it, or explicitly overwrite the newer version."
		}
		return nil, err
	}
	s.closed = true
	shaLine, rest, _ := strings.Cut(r.out, "\n")
	sha := strings.TrimPrefix(shaLine, "@@SHA ")
	infos := parseFind(rest)
	if len(infos) != 1 {
		return nil, files.Errorf(files.CodeVerification, "could not verify %s after installing it", path.Join(s.dir, s.name))
	}
	inst := &files.Installed{Info: infos[0], SHA256: sha}
	inst.Info.Path, inst.Info.Name = path.Join(s.dir, s.name), s.name
	switch {
	case sha != s.sha:
		return inst, files.Errorf(files.CodeVerification, "%s has checksum %s after install, expected %s", inst.Info.Path, sha, s.sha)
	case inst.Info.Mode != s.mode&07777:
		return inst, files.Errorf(files.CodeVerification, "%s has mode %s after install, expected %s", inst.Info.Path, files.FormatMode(inst.Info.Mode), files.FormatMode(s.mode))
	case s.owner != "" && inst.Info.Owner != s.owner && strconv.Itoa(inst.Info.UID) != s.owner:
		return inst, files.Errorf(files.CodeVerification, "%s is owned by %s after install, expected %s", inst.Info.Path, inst.Info.Owner, s.owner)
	case s.group != "" && inst.Info.Group != s.group && strconv.Itoa(inst.Info.GID) != s.group:
		return inst, files.Errorf(files.CodeVerification, "%s has group %s after install, expected %s", inst.Info.Path, inst.Info.Group, s.group)
	}
	return inst, nil
}

func (s *staged) Discard(ctx context.Context) {
	if s.closed || s.tmp == "" {
		return
	}
	s.closed = true
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !s.f.elevated {
		s.f.sftp.Remove(path.Join(s.dir, s.tmp))
		return
	}
	s.f.exec(ctx, true, `cd -P -- "$1" && case $2 in .drool-tmp-*) rm -f -- "$2";; esac`, s.dir, s.tmp)
}

// --- other changes ---

// entryScript prepares a privileged change of one entry: it pins the
// parent directory (cd -P, which must be the real path) and checks that the
// entry exists. Callers append the action, operating on "$n" relative to
// the pinned directory, so swapping path components afterwards can't
// redirect it.
const entryPrelude = `set -u
fail() { echo "@@ERR $1 $2"; exit 3; }
d=$(dirname -- "$1"); n=$(basename -- "$1")
cd -P -- "$d" 2>/dev/null || fail not_found "directory $d does not exist"
[ "$(pwd -P)" = "$d" ] || fail symlink "$d resolves to $(pwd -P) through a symlink; use the real path"
[ -e "$n" ] || [ -L "$n" ] || fail not_found "$1 does not exist"
`

func (f *FileSystem) requireRealParent(ctx context.Context, p string) error {
	dir := path.Dir(p)
	real, err := f.RealPath(ctx, dir)
	if err != nil {
		return err
	}
	if real != dir {
		return files.Errorf(files.CodeSymlink, "%s resolves to %s through a symlink; use the real path", dir, real).
			WithHint("DROOL doesn't follow symlinks when changing files.")
	}
	return nil
}

func (f *FileSystem) Mkdir(ctx context.Context, p string, mode uint32) error {
	if p == "/" {
		return files.Errorf(files.CodeExists, "/ already exists")
	}
	if f.elevated {
		r, err := f.exec(ctx, true, `set -u
d=$(dirname -- "$1"); n=$(basename -- "$1")
cd -P -- "$d" 2>/dev/null || { echo "@@ERR not_found directory $d does not exist"; exit 3; }
[ "$(pwd -P)" = "$d" ] || { echo "@@ERR symlink $d resolves to $(pwd -P) through a symlink"; exit 3; }
[ -e "$n" ] || [ -L "$n" ] && { echo "@@ERR exists $1 already exists"; exit 3; }
mkdir -m "$2" -- "$n"`, p, files.FormatMode(mode))
		if err != nil {
			return err
		}
		return scriptErr(r)
	}
	if err := f.requireRealParent(ctx, p); err != nil {
		return err
	}
	if _, err := f.sftp.Lstat(p); err == nil {
		return files.Errorf(files.CodeExists, "%s already exists", p)
	}
	if err := f.sftp.Mkdir(p); err != nil {
		return sftpErr(p, err)
	}
	return sftpErr(p, f.sftp.Chmod(p, fs.FileMode(mode&0o777)))
}

func (f *FileSystem) Rename(ctx context.Context, from, to string) error {
	if from == "/" || to == "/" {
		return files.Errorf(files.CodeInvalid, "can't rename /")
	}
	if to == from || strings.HasPrefix(to, from+"/") {
		return files.Errorf(files.CodeInvalid, "can't move %s into itself", from)
	}
	if f.elevated {
		r, err := f.exec(ctx, true, entryPrelude+`
td=$(dirname -- "$2"); tn=$(basename -- "$2")
[ "$(cd -P -- "$td" 2>/dev/null && pwd -P)" = "$td" ] || fail not_found "directory $td does not exist or is reached through a symlink"
[ -e "$2" ] || [ -L "$2" ] && fail exists "$2 already exists"
mv -n -T -- "$n" "$td/$tn" && { [ -e "$td/$tn" ] || [ -L "$td/$tn" ]; } || fail failed "could not move $1"`, from, to)
		if err != nil {
			return err
		}
		return scriptErr(r)
	}
	if err := f.requireRealParent(ctx, from); err != nil {
		return err
	}
	if err := f.requireRealParent(ctx, to); err != nil {
		return err
	}
	if _, err := f.sftp.Lstat(to); err == nil {
		return files.Errorf(files.CodeExists, "%s already exists", to)
	}
	// Plain SFTP rename never replaces an existing entry.
	return sftpErr(from, f.sftp.Rename(from, to))
}

// protectedTrees can't be removed recursively, even by root.
func shallow(p string) bool { return strings.Count(strings.Trim(p, "/"), "/") < 1 }

func (f *FileSystem) Remove(ctx context.Context, p string, recursive bool) error {
	if p == "/" || (recursive && shallow(p)) {
		return files.Errorf(files.CodeInvalid, "DROOL won't recursively delete a top-level directory (%s)", p).
			WithHint("Delete its contents instead, or use a command job if you really mean it.")
	}
	if f.elevated {
		flag := "0"
		if recursive {
			flag = "1"
		}
		r, err := f.exec(ctx, true, entryPrelude+`
if [ -d "$n" ] && [ ! -L "$n" ]; then
  if [ "$2" = 1 ]; then rm -rf -- "$n" || fail failed "could not delete everything in $1"
  else rmdir -- "$n" 2>/dev/null || fail not_empty "$1 is not empty"; fi
else rm -f -- "$n" || fail failed "could not delete $1"; fi`, p, flag)
		if err != nil {
			return err
		}
		return scriptErr(r)
	}
	if err := f.requireRealParent(ctx, p); err != nil {
		return err
	}
	fi, err := f.sftp.Lstat(p)
	if err != nil {
		return sftpErr(p, err)
	}
	if fi.IsDir() {
		if recursive {
			// SFTP's RemoveAll walks with lstat and never follows symlinks.
			return sftpErr(p, f.sftp.RemoveAll(p))
		}
		if err := f.sftp.RemoveDirectory(p); err != nil {
			if list, lerr := f.sftp.ReadDir(p); lerr == nil && len(list) > 0 {
				return files.Errorf(files.CodeNotEmpty, "%s is not empty", p)
			}
			return sftpErr(p, err)
		}
		return nil
	}
	return sftpErr(p, f.sftp.Remove(p))
}

func (f *FileSystem) Chmod(ctx context.Context, p string, mode uint32) error {
	if f.elevated {
		// chmod follows symlinks, so a root chmod is only safe where nobody
		// else can swap the entry for a link.
		r, err := f.exec(ctx, true, entryPrelude+`
[ -L "$n" ] && fail symlink "$1 is a symlink; change the target instead"
du=$(stat -c %u .); dm=$(stat -c %a .)
if [ "$du" != 0 ] && [ "$du" != "$3" ]; then fail unsafe_directory "$d belongs to another user (uid $du)"; fi
if [ $((0$dm & 022)) -ne 0 ]; then fail unsafe_directory "$d is writable by its group or by everyone (mode $dm)"; fi
chmod -- "$2" "$n"`, p, files.FormatMode(mode), f.loginUID(ctx))
		if err != nil {
			return err
		}
		return scriptErr(r)
	}
	info, err := f.Stat(ctx, p)
	if err != nil {
		return err
	}
	if info.Kind == files.KindSymlink {
		return files.Errorf(files.CodeSymlink, "%s is a symlink; change the target instead", p)
	}
	if mode&0o7000 != 0 {
		r, err := f.exec(ctx, false, `chmod -- "$1" "$2"`, files.FormatMode(mode), p)
		if err != nil {
			return err
		}
		if r.exit != 0 {
			return classifyStderr(r.err)
		}
		return nil
	}
	return sftpErr(p, f.sftp.Chmod(p, fs.FileMode(mode&0o777)))
}

func (f *FileSystem) loginUID(ctx context.Context) string {
	if r, err := f.exec(ctx, false, `id -u`); err == nil {
		return strings.TrimSpace(r.out)
	}
	return "-1"
}

// chownNoFollow changes ownership without following a final symlink.
func (f *FileSystem) chownNoFollow(ctx context.Context, asRoot bool, p, owner, group string) error {
	spec := owner
	if group != "" {
		spec += ":" + group
	}
	r, err := f.exec(ctx, asRoot, entryPrelude+`chown -h -- "$2" "$n" 2>&1 >/dev/null | head -n 1 | sed 's/^/@@ERR invalid /'; true`, p, spec)
	if err != nil {
		return err
	}
	if err := scriptErr(r); err != nil {
		var fe *files.Error
		if errors.As(err, &fe) && strings.Contains(strings.ToLower(fe.Message), "not permitted") {
			return permissionErr(fe.Message)
		}
		return err
	}
	return nil
}

func (f *FileSystem) Chown(ctx context.Context, p string, owner, group string) error {
	if owner == "" && group == "" {
		return files.Errorf(files.CodeInvalid, "give an owner, a group, or both")
	}
	for _, n := range []string{owner, group} {
		if n != "" && !validAccountName(n) {
			return files.Errorf(files.CodeInvalid, "%q is not a valid user or group name", n)
		}
	}
	return f.chownNoFollow(ctx, f.elevated, p, owner, group)
}

func validAccountName(s string) bool {
	if len(s) > 64 {
		return false
	}
	for i, c := range s {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' ||
			(c == '-' && i > 0) || (c == '$' && i == len(s)-1)
		if !ok {
			return false
		}
	}
	return s != ""
}

var _ files.FS = (*FileSystem)(nil)
