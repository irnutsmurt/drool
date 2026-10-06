package discovery

import (
	"os"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Fixtures were captured from real distro containers (sshd only, no
// systemd), with host-inherited values sanitized.
func TestParseDiscoverDistros(t *testing.T) {
	cases := []struct {
		file, family, distro, version, pkg, kernel string
	}{
		{"debian-12", "debian", "debian", "12", "apt", "6.1.0-28-amd64"},
		{"ubuntu-24-04", "debian", "ubuntu", "24.04", "apt", "6.8.0-49-generic"},
		{"rockylinux-9", "redhat", "rocky", "9", "dnf", "5.14.0-503.11.1.el9_5.x86_64"},
		{"fedora-41", "redhat", "fedora", "41", "dnf", "6.11.10-300.fc41.x86_64"},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			f, complete := ParseDiscover(fixture(t, c.file+".discover.txt"))
			if !complete {
				t.Fatal("end marker missing")
			}
			if f.OSFamily != c.family || f.Distro != c.distro || !strings.HasPrefix(f.DistroVersion, c.version) {
				t.Fatalf("os: %s/%s/%s", f.OSFamily, f.Distro, f.DistroVersion)
			}
			if f.PkgManager != c.pkg || f.Kernel != c.kernel || f.Arch != "x86_64" {
				t.Fatalf("pkg=%s kernel=%s arch=%s", f.PkgManager, f.Kernel, f.Arch)
			}
			if f.CPUCount == 0 || f.MemBytes == 0 || f.BootID == "" || f.Hostname != "m3-"+c.file {
				t.Fatalf("cpu=%d mem=%d boot=%q host=%q", f.CPUCount, f.MemBytes, f.BootID, f.Hostname)
			}
			if len(f.Filesystems) == 0 || f.Filesystems[0].SizeBytes == 0 {
				t.Fatalf("filesystems %+v", f.Filesystems)
			}
			var eth *Interface
			for i := range f.Interfaces {
				if f.Interfaces[i].Name == "eth0" {
					eth = &f.Interfaces[i]
				}
			}
			if eth == nil || eth.MAC == "" || eth.MTU != 1500 || len(eth.Addresses) == 0 || eth.Addresses[0].Family != 4 {
				t.Fatalf("eth0 %+v", eth)
			}
			if len(f.Ports) != 2 || f.Ports[0].Port != 22 {
				t.Fatalf("ports %+v", f.Ports)
			}
			// No systemd in these containers: degraded, not failed.
			if f.SystemdRunning || len(f.Services) != 0 {
				t.Fatal("systemd facts reported without systemd")
			}
			if !hasWarning(f.Warnings, "systemd is not running") {
				t.Fatalf("missing systemd warning: %v", f.Warnings)
			}
			if f.Virtualization != "docker" {
				t.Fatalf("virt %q", f.Virtualization)
			}
		})
	}
}

func hasWarning(ws []string, sub string) bool {
	for _, w := range ws {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}

func TestParseDiscoverSystemdSections(t *testing.T) {
	out := fixture(t, "debian-12.discover.txt")
	// Replace the no-systemd sections with real-format systemd output.
	out = out[:strings.Index(out, "@@DROOL:init@@")] + fixture(t, "systemd-sections.txt")
	f, _ := ParseDiscover(out)
	if !f.SystemdRunning || f.InitSystem != "systemd" {
		t.Fatalf("init %q running=%v", f.InitSystem, f.SystemdRunning)
	}
	if len(f.Services) != 4 || f.Services[1].Name != "nginx" || !strings.HasPrefix(f.Services[1].Description, "A high performance") {
		t.Fatalf("services %+v", f.Services)
	}
	if len(f.FailedUnits) != 2 || f.FailedUnits[1] != "certbot.timer" {
		t.Fatalf("failed %v", f.FailedUnits)
	}
	var dns, web *Port
	for i := range f.Ports {
		switch {
		case f.Ports[i].Port == 53 && f.Ports[i].Proto == "tcp":
			dns = &f.Ports[i]
		case f.Ports[i].Port == 80 && f.Ports[i].Address == "::":
			web = &f.Ports[i]
		}
	}
	if dns == nil || dns.Address != "127.0.0.53" || dns.Process != "systemd-resolve" {
		t.Fatalf("dns port %+v", dns)
	}
	if web == nil || web.Process != "nginx" {
		t.Fatalf("ipv6 web port %+v", web)
	}
}

func TestParseDiscoverDegradesGracefully(t *testing.T) {
	// Missing os-release, truncated output, garbage lines.
	f, complete := ParseDiscover("motd noise\n@@DROOL:uname@@\nLinux\n@@DROOL:df@@\nnot a df table\n")
	if complete {
		t.Fatal("truncated output reported complete")
	}
	if f.OSFamily != "unknown" || f.Distro != "" {
		t.Fatalf("os %q %q", f.OSFamily, f.Distro)
	}
	for _, w := range []string{"os-release", "uname", "incomplete"} {
		if !hasWarning(f.Warnings, w) {
			t.Errorf("no warning about %s: %v", w, f.Warnings)
		}
	}
	if f.Filesystems == nil || f.Services == nil || f.Ports == nil {
		t.Fatal("slices must be empty, not nil (stable JSON)")
	}
}

func TestOSFamily(t *testing.T) {
	cases := map[[2]string]string{
		{"almalinux", "rhel centos fedora"}: "redhat",
		{"linuxmint", "ubuntu debian"}:      "debian",
		{"pop", "ubuntu debian"}:            "debian",
		{"opensuse-leap", "suse opensuse"}:  "suse",
		{"weird", ""}:                       "unknown",
	}
	for in, want := range cases {
		if got := OSFamily(in[0], in[1]); got != want {
			t.Errorf("OSFamily(%v) = %s, want %s", in, got, want)
		}
	}
}

func TestParseStatus(t *testing.T) {
	st := ParseStatus(fixture(t, "systemd.status.txt"))
	if st.UptimeS == nil || *st.UptimeS != 93784 || st.Load1 == nil || *st.Load1 != 0.42 {
		t.Fatalf("uptime/load %+v", st)
	}
	if st.MemUsedPct == nil || *st.MemUsedPct != 75 {
		t.Fatalf("mem %v", st.MemUsedPct)
	}
	if st.MaxFSUsedPct == nil || *st.MaxFSUsedPct != 90 || st.MaxFSMount != "/srv/data store" {
		t.Fatalf("fs %v %q", st.MaxFSUsedPct, st.MaxFSMount)
	}
	if st.FailedUnits == nil || *st.FailedUnits != 1 {
		t.Fatalf("failed %v", st.FailedUnits)
	}
	if st.RebootRequired == nil || !*st.RebootRequired || !strings.Contains(st.RebootReason, "503.11.1") {
		t.Fatalf("reboot %v %q", st.RebootRequired, st.RebootReason)
	}
	// Real container output: no systemd, single module dir absent → unknowns, not zeros.
	st = ParseStatus(fixture(t, "debian-12.status.txt"))
	if st.FailedUnits != nil {
		t.Fatal("failed units reported without systemd")
	}
	if st.BootID == "" || st.UptimeS == nil {
		t.Fatalf("container status %+v", st)
	}
}

func TestParseUpdates(t *testing.T) {
	u := ParseUpdatesAPT(fixture(t, "debian-12.updates.txt"))
	if u.Available == nil || *u.Available != 1 || *u.Security != 1 {
		t.Fatalf("debian (oldstable-security): %+v", u)
	}
	u = ParseUpdatesAPT(fixture(t, "ubuntu-24-04.updates.txt"))
	if *u.Available != 3 || *u.Security != 1 || !u.Packages[2].Security || u.Packages[0].Security {
		t.Fatalf("ubuntu: %+v", u)
	}
	u = ParseUpdatesDNF(fixture(t, "rockylinux-9.updates.txt"))
	if u.Available == nil || *u.Available < 10 || u.Security == nil || *u.Security == 0 || *u.Security > *u.Available {
		t.Fatalf("rocky: available=%v security=%v", *u.Available, *u.Security)
	}
	for _, p := range u.Packages {
		if p.Name == "binutils-gold" && !p.Security {
			t.Fatal("binutils-gold should be a security update")
		}
		if p.Name == "basesystem" && p.Security {
			t.Fatal("basesystem is not a security update")
		}
	}
	if u.RebootRequired == nil || *u.RebootRequired {
		t.Fatalf("rocky needs-restarting: %v", u.RebootRequired)
	}
	u = ParseUpdatesDNF(fixture(t, "fedora-41.updates.txt"))
	if *u.Available != 2 || u.Packages[0].Name != "xz" || u.Security == nil || *u.Security != 0 {
		t.Fatalf("fedora: %+v", u)
	}
	// No package cache yet: unknown, not zero.
	u = ParseUpdatesDNF("@@DROOL:dnf-check@@\n@@rc=1\n@@DROOL:end@@\n")
	if u.Available != nil {
		t.Fatal("failed check reported a count")
	}
}

func TestNEVRAName(t *testing.T) {
	cases := map[string]string{
		"binutils-gold-2.35.2-67.el9_7.1.x86_64": "binutils-gold",
		"openssl-1:3.0.7-28.el9_4.x86_64":        "openssl",
		"bash-5.1.8-9.el9.x86_64":                "bash",
		"python3-libs-3.9.18-3.el9.noarch":       "python3-libs",
	}
	for in, want := range cases {
		if got := nevraName(in); got != want {
			t.Errorf("nevraName(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"5.14.0-503.11.1.el9_5.x86_64", "5.14.0-427.13.1.el9_4.x86_64", 1},
		{"6.1.0-28-amd64", "6.1.0-9-amd64", 1},
		{"6.8.0-49-generic", "6.8.0-49-generic", 0},
		{"6.11.10-300.fc41.x86_64", "6.11.9-300.fc41.x86_64", 1},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
