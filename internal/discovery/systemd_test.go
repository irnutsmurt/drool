package discovery

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The real DiscoverScript and StatusScript, run here against a stand-in
// host: PID 1's name, systemd's runtime directory and systemctl are fixture
// files and a fake binary (the scripts' paths are pointed at them), so each
// case is the host state it names. Over SSH DROOL runs these scripts as the
// login user, through a Shepherd as root; whether systemd runs must come out
// the same either way (dogfooding 2026-10-14: a DietPi host, systemd as PID 1
// but no D-Bus, was reported as "systemd is not running" over SSH while its
// Shepherd saw every service).

type hostState struct {
	pid1      string // PID 1's name; "" when unreadable (/proc with hidepid)
	runSD     string // /run/systemd: "dir" (with system/), "hidden" (not searchable for this account), "" absent
	systemctl string // "" not installed; "ok" answers; "bus": no D-Bus system bus for this account; "denied"; "notbooted": can't see /run/systemd
}

const fakeSystemctl = `#!/bin/sh
echo "$*" >> "$CALLS"
case "$MODE" in
ok)
	case "$*" in
	*--state=running*) echo "ssh.service loaded active running OpenBSD Secure Shell server"; echo "cron.service loaded active running Regular background program processing daemon" ;;
	*--state=failed*) echo "backup.service loaded failed failed Nightly backup" ;;
	esac ;;
bus) echo "Failed to connect to bus: No such file or directory" >&2; exit 1 ;;
denied) echo "Failed to list units: Access denied" >&2; exit 1 ;;
notbooted) echo "System has not been booted with systemd as init system (PID 1). Can't operate." >&2; echo "Failed to connect to bus: Host is down" >&2; exit 1 ;;
esac
`

// run runs script on the stand-in host and returns its output and what
// systemctl was asked.
func (h hostState) run(t *testing.T, script string) (string, string) {
	t.Helper()
	// A plain path: it goes into the script unquoted (test names have
	// quotes and parentheses).
	dir, err := os.MkdirTemp("", "drool-sd-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(dir, "run-systemd"), 0o755); os.RemoveAll(dir) })
	bin := filepath.Join(dir, "bin")
	os.MkdirAll(bin, 0o755)
	// No reading this machine's sockets, links or virtualization.
	for _, f := range []string{"ss", "ip", "systemd-detect-virt"} {
		os.WriteFile(filepath.Join(bin, f), []byte("#!/bin/sh\nexit 0\n"), 0o755)
	}
	if h.systemctl != "" {
		os.WriteFile(filepath.Join(bin, "systemctl"), []byte(fakeSystemctl), 0o755)
	}
	comm := filepath.Join(dir, "proc1-comm")
	if h.pid1 != "" {
		os.WriteFile(comm, []byte(h.pid1+"\n"), 0o644)
	}
	runSD := filepath.Join(dir, "run-systemd")
	switch h.runSD {
	case "dir", "hidden":
		os.MkdirAll(filepath.Join(runSD, "system"), 0o755)
	}
	if h.runSD == "hidden" {
		os.Chmod(runSD, 0o000)
	}
	s := strings.ReplaceAll(script, "/proc/1/comm", comm)
	s = strings.ReplaceAll(s, "/run/systemd", runSD)
	calls := filepath.Join(dir, "calls")
	cmd := exec.Command("sh", "-s")
	cmd.Stdin = strings.NewReader(s)
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "MODE=" + h.systemctl, "CALLS=" + calls, "LC_ALL=C"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("script: %v", err)
	}
	asked, _ := os.ReadFile(calls)
	return string(out), string(asked)
}

func TestSystemdDetection(t *testing.T) {
	root := os.Geteuid() == 0
	for _, c := range []struct {
		name      string
		host      hostState
		state     string // facts' SystemdState
		running   bool
		services  int  // listed services (-1: not listed)
		limited   bool // the list couldn't be read by this account
		asked     bool // systemctl was asked at all
		warning   string
		rootSkips bool // root can search any directory: the "hidden" case doesn't exist for it
	}{
		{name: "PID 1 is systemd, /run/systemd/system visible", host: hostState{pid1: "systemd", runSD: "dir", systemctl: "ok"},
			state: "running", running: true, services: 2, asked: true},
		{name: "PID 1 is systemd, /run/systemd not searchable for the account", host: hostState{pid1: "systemd", runSD: "hidden", systemctl: "ok"},
			state: "running", running: true, services: 2, asked: true},
		{name: "DietPi: PID 1 is systemd, no D-Bus for the account", host: hostState{pid1: "systemd", runSD: "dir", systemctl: "bus"},
			state: "running", running: true, services: -1, limited: true, asked: true, warning: "can't ask it which services there are (Failed to connect to bus"},
		{name: "systemd, but the account is denied", host: hostState{pid1: "systemd", runSD: "dir", systemctl: "denied"},
			state: "running", running: true, services: -1, limited: true, asked: true, warning: "Access denied"},
		// The account can't see /run/systemd, so systemctl itself claims no
		// systemd: PID 1 says otherwise.
		{name: "PID 1 is systemd, systemctl claims it wasn't booted with systemd", host: hostState{pid1: "systemd", runSD: "hidden", systemctl: "notbooted"},
			state: "running", running: true, services: -1, limited: true, asked: true, warning: "not been booted with systemd"},
		{name: "systemctl installed, PID 1 isn't systemd (a container)", host: hostState{pid1: "sshd", runSD: "", systemctl: "ok"},
			state: "none", running: false, services: -1, asked: false, warning: "PID 1 is sshd, not systemd"},
		{name: "systemctl installed and a stray /run/systemd/system, PID 1 isn't systemd", host: hostState{pid1: "sh", runSD: "dir", systemctl: "ok"},
			state: "none", running: false, services: -1, asked: false, warning: "PID 1 is sh, not systemd"},
		{name: "a host without systemd at all", host: hostState{pid1: "init"},
			state: "none", running: false, services: -1, asked: false, warning: "PID 1 is init, not systemd"},
		{name: "PID 1 hidden (hidepid), /run/systemd/system visible", host: hostState{runSD: "dir", systemctl: "ok"},
			state: "running", running: true, services: 2, asked: true},
		{name: "PID 1 hidden, /run/systemd hidden, systemctl answers", host: hostState{runSD: "hidden", systemctl: "ok"},
			state: "unknown", running: true, services: 2, asked: true, rootSkips: true},
		{name: "PID 1 hidden, /run/systemd hidden, no D-Bus", host: hostState{runSD: "hidden", systemctl: "bus"},
			state: "unknown", running: false, services: -1, limited: true, asked: true, warning: "whether systemd runs couldn't be read", rootSkips: true},
		{name: "PID 1 hidden, no /run/systemd", host: hostState{systemctl: "ok"},
			state: "none", running: false, services: -1, asked: false, warning: "systemd is not running"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.rootSkips && root {
				t.Skip("root searches any directory")
			}
			out, asked := c.host.run(t, DiscoverScript)
			f, complete := ParseDiscover(out)
			if !complete {
				t.Fatalf("incomplete:\n%s", out)
			}
			if f.SystemdState != c.state || f.SystemdRunning != c.running || f.ServicesLimited != c.limited {
				t.Fatalf("state %q running %v limited %v (error %q), want %q %v %v\n%s", f.SystemdState, f.SystemdRunning, f.ServicesLimited,
					f.ServicesError, c.state, c.running, c.limited, out)
			}
			if c.services >= 0 && len(f.Services) != c.services || c.services < 0 && len(f.Services) != 0 {
				t.Fatalf("services %+v", f.Services)
			}
			if (asked != "") != c.asked {
				t.Fatalf("systemctl asked: %q", asked)
			}
			if c.warning != "" && !hasWarning(f.Warnings, c.warning) {
				t.Fatalf("warnings %v, want %q", f.Warnings, c.warning)
			}
			if c.running && f.InitSystem != "systemd" {
				t.Fatalf("init %q", f.InitSystem)
			}
			if c.running && !c.limited && (len(f.FailedUnits) != 1 || f.FailedUnits[0] != "backup.service") {
				t.Fatalf("failed units %v", f.FailedUnits)
			}

			// The status check decides the same way, from the same evidence.
			sout, _ := c.host.run(t, StatusScript)
			st := ParseStatus(sout)
			if st.SystemdState != f.SystemdState || st.FailedLimited != c.limited {
				t.Fatalf("status: state %q limited %v, discovery %q %v", st.SystemdState, st.FailedLimited, f.SystemdState, c.limited)
			}
			switch {
			case c.running && !c.limited:
				if st.FailedUnits == nil || *st.FailedUnits != 1 {
					t.Fatalf("status failed units %v", st.FailedUnits)
				}
			default:
				if st.FailedUnits != nil {
					t.Fatalf("a failed-unit count (%d) from a list that couldn't be read", *st.FailedUnits)
				}
			}
		})
	}
}

// The same host over SSH (the login user: no D-Bus, /run/systemd not
// searchable) and through its Shepherd (root: systemd's private socket):
// the same init system, the same "systemd runs", both attempt to list
// services; only the account's permission differs, and says so.
func TestSystemdSameOverSSHAndShepherd(t *testing.T) {
	shepherd := hostState{pid1: "systemd", runSD: "dir", systemctl: "ok"}
	ssh := hostState{pid1: "systemd", runSD: "hidden", systemctl: "bus"}
	so, sAsked := shepherd.run(t, DiscoverScript)
	ho, hAsked := ssh.run(t, DiscoverScript)
	sf, _ := ParseDiscover(so)
	hf, _ := ParseDiscover(ho)
	if sf.InitSystem != hf.InitSystem || sf.SystemdRunning != hf.SystemdRunning || sf.SystemdState != hf.SystemdState || !sf.SystemdRunning {
		t.Fatalf("Shepherd: %q %v %q; SSH: %q %v %q", sf.InitSystem, sf.SystemdRunning, sf.SystemdState, hf.InitSystem, hf.SystemdRunning, hf.SystemdState)
	}
	if sAsked == "" || hAsked == "" {
		t.Fatalf("both paths ask systemctl: Shepherd %q, SSH %q", sAsked, hAsked)
	}
	if len(sf.Services) != 2 || sf.ServicesLimited || !hf.ServicesLimited || len(hf.Services) != 0 {
		t.Fatalf("Shepherd services %v (limited %v), SSH %v (limited %v)", sf.Services, sf.ServicesLimited, hf.Services, hf.ServicesLimited)
	}
	st, ht := ParseStatus(func() string { o, _ := shepherd.run(t, StatusScript); return o }()), ParseStatus(func() string { o, _ := ssh.run(t, StatusScript); return o }())
	if st.SystemdState != ht.SystemdState || st.FailedUnits == nil || ht.FailedUnits != nil || !ht.FailedLimited {
		t.Fatalf("status: Shepherd %q %v, SSH %q %v limited %v", st.SystemdState, st.FailedUnits, ht.SystemdState, ht.FailedUnits, ht.FailedLimited)
	}
}

// Output from a Shepherd built before SystemdProbe ("systemd-running", no
// error lines) still parses the way it used to.
func TestSystemdOldScriptOutput(t *testing.T) {
	out := "\n@@DROOL:init@@\nsystemd\nsystemd-running\n@@DROOL:services@@\nssh.service loaded active running SSH\n@@DROOL:failed@@\n@@DROOL:end@@\n"
	f, _ := ParseDiscover(out)
	if !f.SystemdRunning || f.SystemdState != SystemdRunning || len(f.Services) != 1 || len(f.FailedUnits) != 0 {
		t.Fatalf("%+v", f)
	}
	f, _ = ParseDiscover("\n@@DROOL:init@@\nsshd\n@@DROOL:services@@\n@@DROOL:failed@@\n@@DROOL:end@@\n")
	if f.SystemdRunning || f.SystemdState != SystemdNone {
		t.Fatalf("%+v", f)
	}
	st := ParseStatus("\n@@DROOL:failed@@\nsystemd-running\nbackup.service loaded failed failed Backup\n")
	if st.FailedUnits == nil || *st.FailedUnits != 1 || st.SystemdState != SystemdRunning {
		t.Fatalf("%+v", st)
	}
}
