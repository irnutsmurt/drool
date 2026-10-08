package storage

import (
	"regexp"
	"strconv"
	"strings"

	"drool/internal/monitor/source"
)

// Kernel event kinds: the storage evidence DROOL takes from kernel
// messages. Only lines matching these patterns are ever looked at; the
// rest of the kernel log is never stored or sent.
const (
	KernIOError    = "io_error"     // block layer IO error
	KernMedium     = "medium_error" // unrecoverable read / medium error
	KernTimeout    = "timeout"      // command timeouts, link resets
	KernUSBReset   = "usb_reset"    // USB device reset
	KernUSBGone    = "usb_disconnect"
	KernMMC        = "mmc_error" // SD/eMMC controller errors
	KernFSError    = "fs_error"  // filesystem reported corruption/errors
	KernFSReadOnly = "fs_readonly"
	KernCRC        = "link_crc" // interface CRC errors (cables)
)

type kernPattern struct {
	kind string
	re   *regexp.Regexp
}

// The first capture group is the device (sda, sda1, mmcblk0, mmc0,
// nvme0, ata3, a USB port like 1-1.2, or a filesystem's device).
var kernPatterns = []kernPattern{
	{KernMedium, regexp.MustCompile(`critical medium error, dev (\w+)`)},
	{KernMedium, regexp.MustCompile(`\[(sd\w+)\] .*(?:Unrecovered read error|Medium Error)`)},
	{KernIOError, regexp.MustCompile(`I/O error, dev (\w+), sector`)},
	{KernIOError, regexp.MustCompile(`Buffer I/O error on dev(?:ice)? (\w+),`)},
	{KernIOError, regexp.MustCompile(`EXT[234]-fs warning \(device (\w+)\): \w+:\d+: I/O error`)},
	{KernFSReadOnly, regexp.MustCompile(`\((\w+)\): [Rr]emounting filesystem read-only`)},
	{KernFSReadOnly, regexp.MustCompile(`BTRFS .* \(device (\w+)\).*forced readonly`)},
	{KernFSError, regexp.MustCompile(`EXT[234]-fs error \(device (\w+)\)`)},
	{KernFSError, regexp.MustCompile(`XFS \((\w+)\): .*(?:[Cc]orruption|metadata I/O error|log I/O error)`)},
	{KernFSError, regexp.MustCompile(`BTRFS (?:error|critical) \(device (\w+)`)},
	{KernFSError, regexp.MustCompile(`FAT-fs \((\w+)\): error`)},
	{KernUSBReset, regexp.MustCompile(`usb (\d+-[\d.]+): reset (?:high|super|full|low|SuperSpeed|wireless)[\w+ -]*(?:speed )?USB device`)},
	{KernUSBGone, regexp.MustCompile(`usb (\d+-[\d.]+): USB disconnect`)},
	{KernTimeout, regexp.MustCompile(`nvme (nvme\d+): I/O \d+ (?:\(\w+\) )?QID \d+ timeout`)},
	{KernTimeout, regexp.MustCompile(`(ata\d+(?:\.\d+)?): (?:hard resetting link|exception Emask|failed command)`)},
	{KernTimeout, regexp.MustCompile(`\[(sd\w+)\] tag#\d+ .*(?:timed out|timing out command)`)},
	{KernTimeout, regexp.MustCompile(`(mmc\d+): Timeout waiting for hardware`)},
	{KernMMC, regexp.MustCompile(`(mmcblk\d+): error -\d+`)},
	{KernMMC, regexp.MustCompile(`(mmc\d+): (?:Card stuck|card stuck|error \d+ whilst|data error|cmd error|Controller never released inhibit)`)},
	{KernCRC, regexp.MustCompile(`(ata\d+(?:\.\d+)?): .*(?:BadCRC|ICRC)`)},
}

// KernelEvent is one matched kernel message.
type KernelEvent struct {
	Kind   string `json:"kind"`
	Device string `json:"device"` // as the kernel named it
	Disk   string `json:"disk,omitempty"`
	MonoUs int64  `json:"mono_us"` // kernel timestamp (µs since boot): the dedup key with the boot id
	Text   string `json:"text"`    // the line, cut to 200 characters
}

// MatchKernel classifies one kernel message; ok is false for everything
// DROOL doesn't care about.
func MatchKernel(msg string) (kind, dev string, ok bool) {
	// Cheap pre-filter: nearly every line fails here.
	if !strings.Contains(msg, "error") && !strings.Contains(msg, "Error") && !strings.Contains(msg, "reset") &&
		!strings.Contains(msg, "timeout") && !strings.Contains(msg, "timed out") && !strings.Contains(msg, "timing out") &&
		!strings.Contains(msg, "disconnect") && !strings.Contains(msg, "read-only") && !strings.Contains(msg, "readonly") &&
		!strings.Contains(msg, "stuck") && !strings.Contains(msg, "Corruption") && !strings.Contains(msg, "corruption") &&
		!strings.Contains(msg, "CRC") && !strings.Contains(msg, "inhibit") && !strings.Contains(msg, "Timeout") &&
		!strings.Contains(msg, "exception") && !strings.Contains(msg, "failed command") {
		return "", "", false
	}
	for _, p := range kernPatterns {
		if m := p.re.FindStringSubmatch(msg); m != nil {
			return p.kind, m[1], true
		}
	}
	return "", "", false
}

// ParseKmsg parses one /dev/kmsg record ("pri,seq,usec,flags;message").
func ParseKmsg(rec string) (monoUs int64, msg string, ok bool) {
	hdr, msg, ok := strings.Cut(rec, ";")
	if !ok {
		return 0, "", false
	}
	f := strings.Split(hdr, ",")
	if len(f) < 3 {
		return 0, "", false
	}
	us, err := strconv.ParseInt(f[2], 10, 64)
	if err != nil {
		return 0, "", false
	}
	msg, _, _ = strings.Cut(msg, "\n") // continuation lines follow
	return us, msg, true
}

// ParseJournalLine parses `journalctl -k -o short-monotonic` output:
// "[  123.456789] host kernel: message".
func ParseJournalLine(ln string) (monoUs int64, msg string, ok bool) {
	ln = strings.TrimSpace(ln)
	if !strings.HasPrefix(ln, "[") {
		return 0, "", false
	}
	ts, rest, ok := strings.Cut(ln[1:], "]")
	if !ok {
		return 0, "", false
	}
	sec, frac, _ := strings.Cut(strings.TrimSpace(ts), ".")
	s, err := strconv.ParseInt(sec, 10, 64)
	if err != nil {
		return 0, "", false
	}
	for len(frac) < 6 {
		frac += "0"
	}
	us, _ := strconv.ParseInt(frac[:6], 10, 64)
	if _, m, ok := strings.Cut(rest, "kernel: "); ok {
		rest = m
	}
	return s*1_000_000 + us, strings.TrimSpace(rest), true
}

// Event builds a KernelEvent when msg matters.
func Event(monoUs int64, msg string) (KernelEvent, bool) {
	kind, dev, ok := MatchKernel(msg)
	if !ok {
		return KernelEvent{}, false
	}
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return KernelEvent{Kind: kind, Device: dev, MonoUs: monoUs, Text: msg}, true
}

// JournalGrep is the extended regex the SSH path uses to pre-filter
// `journalctl -k` on the host, so only candidate lines travel.
const JournalGrep = `I/O error|medium error|Medium Error|Unrecovered read|read-only|readonly|fs error|XFS .*(orruption|I/O error)|BTRFS (error|critical)|FAT-fs .*error|reset .*USB device|USB disconnect|QID [0-9]+ timeout|hard resetting link|exception Emask|failed command|timed out|timing out|Timeout waiting|mmcblk[0-9]+: error|Card stuck|card stuck|data error|cmd error|never released inhibit|BadCRC|ICRC`

var partSuffix = regexp.MustCompile(`^((?:nvme\d+n\d+|mmcblk\d+))p\d+$|^(sd[a-z]+|vd[a-z]+|xvd[a-z]+|hd[a-z]+)\d+$`)

// DiskFor maps a kernel device name to one of the inventoried disks:
// partitions to their disk, nvme0 to its namespace, mmc0/ata3/USB ports
// through the disks' sysfs paths. "" when nothing matches.
func DiskFor(dev string, disks []Device, link func(string) string) string {
	if m := partSuffix.FindStringSubmatch(dev); m != nil {
		dev = m[1] + m[2]
	}
	for _, d := range disks {
		if d.Name == dev {
			return d.Name
		}
	}
	if strings.HasPrefix(dev, "nvme") && !strings.Contains(dev, "n1") {
		for _, d := range disks {
			if strings.HasPrefix(d.Name, dev+"n") {
				return d.Name
			}
		}
	}
	for _, d := range disks {
		if t := link("/sys/block/" + d.Name); t != "" && strings.Contains(t, "/"+dev+"/") {
			return d.Name
		}
	}
	return ""
}

func init() { source.JournalPattern = JournalGrep }

// JournalCommand reads recent kernel storage messages on SSH hosts
// (filtered on the host; it needs root or the systemd-journal group).
var JournalCommand = []string{"journalctl", "-k", "-q", "--no-pager", "-o", "short-monotonic", "--since=-15min", "-g", JournalGrep}
