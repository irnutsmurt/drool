package monitor

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"

	"drool/internal/monitor/source"
)

// FSSpec is what the filesystem collector reads.
var FSSpec = source.Spec{Files: []string{"/proc/self/mountinfo"}}

// Mount is one /proc/self/mountinfo entry.
type Mount struct {
	Point, Source, Type, DevNum, Root string
	ReadOnly                          bool
}

// localFS are the filesystem types reported. Network filesystems are not
// checked: a dead server would hang the check.
var localFS = map[string]bool{"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true, "vfat": true,
	"exfat": true, "f2fs": true, "zfs": true, "ntfs": true, "ntfs3": true, "fuseblk": true, "jfs": true,
	"reiserfs": true, "bcachefs": true, "nilfs2": true, "hfsplus": true, "msdos": true}

// ParseMountinfo returns the real local filesystems, one entry per device
// (bind mounts and repeated mounts of one filesystem collapse to the
// shortest path).
func ParseMountinfo(b []byte) []Mount {
	byDev := map[string]Mount{}
	for _, ln := range strings.Split(string(b), "\n") {
		pre, post, ok := strings.Cut(ln, " - ")
		if !ok {
			continue
		}
		f, g := strings.Fields(pre), strings.Fields(post)
		if len(f) < 6 || len(g) < 3 {
			continue
		}
		if !localFS[g[0]] {
			continue
		}
		m := Mount{DevNum: f[2], Root: unescape(f[3]), Point: unescape(f[4]), Type: g[0], Source: unescape(g[1])}
		m.ReadOnly = hasOpt(f[5], "ro") || hasOpt(g[2], "ro")
		if strings.HasPrefix(m.Point, "/snap/") || strings.HasPrefix(m.Point, "/var/lib/docker/") ||
			strings.HasPrefix(m.Point, "/var/lib/containers/") || containerFileMount[m.Point] {
			continue
		}
		if cur, ok := byDev[m.DevNum]; ok && !betterMount(m, cur) {
			continue
		}
		byDev[m.DevNum] = m
	}
	out := make([]Mount, 0, len(byDev))
	for _, m := range byDev {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Point < out[j].Point })
	return out
}

// betterMount prefers the filesystem's root over a bind mount of a
// subdirectory, then the shorter path.
func betterMount(m, cur Mount) bool {
	if (m.Root == "/") != (cur.Root == "/") {
		return m.Root == "/"
	}
	return len(m.Point) < len(cur.Point)
}

func hasOpt(opts, o string) bool {
	for _, x := range strings.Split(opts, ",") {
		if x == o {
			return true
		}
	}
	return false
}

// unescape decodes mountinfo's octal escapes (\040 for a space).
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// containerFileMount are the files container runtimes bind-mount from
// the host's filesystem: not the container's storage.
var containerFileMount = map[string]bool{"/etc/hosts": true, "/etc/hostname": true, "/etc/resolv.conf": true}

// MaxFilesystems bounds the filesystems reported.
const MaxFilesystems = 64

// Filesystems reads capacity for every local filesystem. A filesystem
// whose check doesn't return is reported unresponsive (prev keeps its
// last values); in the SSH first pass capacity is deferred.
func Filesystems(ctx context.Context, src source.Source, prev []FS) ([]FS, error) {
	b, err := src.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	old := map[string]FS{}
	for _, f := range prev {
		old[f.Mount] = f
	}
	var out []FS
	var deferred error
	for _, m := range ParseMountinfo(b) {
		if len(out) >= MaxFilesystems {
			break
		}
		fs := FS{Mount: m.Point, Device: m.Source, DevNum: m.DevNum, Type: m.Type, ReadOnly: m.ReadOnly}
		st, err := src.Statfs(ctx, m.Point)
		switch {
		case errors.Is(err, source.ErrDeferred):
			deferred = err
			continue
		case errors.Is(err, source.ErrUnresponsive):
			if o, ok := old[m.Point]; ok {
				fs = o
			}
			fs.Unresponsive = true
			out = append(out, fs)
			continue
		case err != nil || st.Blocks == 0:
			continue
		}
		bs := int64(st.Bsize)
		fs.TotalBytes = int64(st.Blocks) * bs
		fs.AvailBytes = int64(st.Bavail) * bs
		fs.UsedBytes = int64(st.Blocks-st.Bfree) * bs
		// As df: used / (used + available to unprivileged users).
		if d := fs.UsedBytes + fs.AvailBytes; d > 0 {
			fs.UsedPercent = clamp(100 * float64(fs.UsedBytes) / float64(d))
		}
		if st.Files > 0 && st.Ffree <= st.Files {
			v := clamp(100 * float64(st.Files-st.Ffree) / float64(st.Files))
			fs.InodesPercent = &v
		}
		out = append(out, fs)
	}
	return out, deferred
}
