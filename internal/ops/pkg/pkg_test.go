package pkg

import (
	"os"
	"strings"
	"testing"

	"drool/internal/model"
)

func fx(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func names(cs []Change) string {
	var n []string
	for _, c := range cs {
		n = append(n, c.Name)
	}
	return strings.Join(n, ",")
}

// All fixtures were captured from real Debian 12, Rocky 9 (dnf4) and
// Fedora 41 (dnf5) containers.
func TestAPTPlans(t *testing.T) {
	b := apt{}
	p, err := b.ParsePlan(fx(t, "apt-sim-remove-dependents.txt"), 0)
	if err != nil || names(p.Remove) != "curl,libcurl4" || p.Remove[0].From != "7.88.1-10+deb12u15" {
		t.Fatalf("remove plan %+v %v", p, err)
	}
	if extra := p.ExtraRemovals([]string{"libcurl4"}); len(extra) != 1 || extra[0].Name != "curl" {
		t.Fatalf("extra removals %+v", extra)
	}
	p, _ = b.ParsePlan(fx(t, "apt-sim-full-upgrade.txt"), 0)
	if len(p.Upgrade) != 1 || p.Upgrade[0].Name != "perl-base" || p.Upgrade[0].From != "5.36.0-7+deb12u3" ||
		p.Upgrade[0].To != "5.36.0-7+deb12u4" || len(p.Remove) != 0 {
		t.Fatalf("full-upgrade plan %+v", p)
	}
	p, _ = b.ParsePlan(fx(t, "apt-err-dpkg-interrupted.txt"), 0) // simulation part of this capture is a normal install
	_ = p
	p, _ = b.ParsePlan(fx(t, "apt-sim-install.txt"), 0)
	if !strings.Contains(names(p.Install), "htop") || len(p.Remove) != 0 {
		t.Fatalf("install plan %+v", p)
	}
}

func TestAPTErrors(t *testing.T) {
	b := apt{}
	if _, err := b.ParsePlan(fx(t, "apt-err-not-found.txt"), 100); err == nil ||
		model.ClassOf(err) != model.ClassValidation || !strings.Contains(err.Error(), "no-such-package-xyz") {
		t.Fatalf("not found: %v", err)
	}
	e := b.Classify(fx(t, "apt-err-locked.txt"), 100)
	if !e.Retryable || e.Class != model.ClassPackageManager || !strings.Contains(e.Message, "apt-get (pid 3082)") {
		t.Fatalf("lock: %+v", e)
	}
	e = b.Classify(fx(t, "apt-err-dpkg-interrupted.txt"), 100)
	if e.Retryable || !strings.Contains(e.Hint, "dpkg --configure -a") {
		t.Fatalf("interrupted: %+v", e)
	}
	if _, err := b.Exec("system-security", nil); model.ClassOf(err) != model.ClassValidation {
		t.Fatalf("apt security-only must be refused: %v", err)
	}
}

func TestAPTVersionsAndReboot(t *testing.T) {
	v := apt{}.ParseVersions(fx(t, "dpkg-query.txt"), []string{"curl", "htop", "no-such-pkg"})
	if v["curl"] != "7.88.1-10+deb12u15" || v["htop"] != "" || v["no-such-pkg"] != "" {
		t.Fatalf("versions %v", v)
	}
	r, why := apt{}.ParseRebootRequired("required\nlinux-image-6.1.0-28-amd64\n")
	if r == nil || !*r || !strings.Contains(why, "linux-image") {
		t.Fatalf("reboot %v %q", r, why)
	}
	// Plain Debian has no reboot-required file: compare kernels.
	if r, why := (apt{}).ParseRebootRequired("not-required\n@@kernels\n6.1.0-40-cloud-amd64\n6.1.0-40-cloud-amd64\n6.12.48+deb12-cloud-amd64\n"); r == nil || !*r || !strings.Contains(why, "6.12.48") {
		t.Fatalf("newer kernel: %v %q", r, why)
	}
	if r, _ := (apt{}).ParseRebootRequired("not-required\n@@kernels\n6.1.0-40-cloud-amd64\n6.1.0-40-cloud-amd64\n6.1.0-40-amd64\n"); r == nil || *r {
		t.Fatal("another flavor of the same kernel is not a pending update")
	}
	if r, _ := (apt{}).ParseRebootRequired("not-required\n"); r == nil || *r {
		t.Fatal("not-required misparsed")
	}
}

func TestDNFPlans(t *testing.T) {
	b := dnf{}
	// dnf4 remove with dependents and unused dependencies.
	p, err := b.ParsePlan(fx(t, "rockylinux-9-sim-remove.txt"), 1)
	if err != nil || names(p.Remove) != "libpsl,wget,publicsuffix-list-dafsa" {
		t.Fatalf("rocky remove %+v %v", p, err)
	}
	// dnf5 remove with dependents.
	p, err = b.ParsePlan(fx(t, "fedora-41-sim-remove-dependents.txt"), 1)
	if err != nil || names(p.Remove) != "libnl3,htop,hwloc-libs" {
		t.Fatalf("fedora remove %+v %v", p, err)
	}
	// dnf5 install with dependencies.
	p, _ = b.ParsePlan(fx(t, "fedora-41-sim-install.txt"), 1)
	if names(p.Install) != "htop,nano,hwloc-libs,libnl3" {
		t.Fatalf("fedora install %+v", p)
	}
	// dnf5 upgrade: same-name "replacing" rows are not removals.
	p, _ = b.ParsePlan(fx(t, "fedora-41-sim-upgrade.txt"), 1)
	if names(p.Upgrade) != "xz,xz-libs" || len(p.Remove) != 0 || p.Upgrade[0].To != "1:5.8.1-4.fc41" {
		t.Fatalf("fedora upgrade %+v", p)
	}
	// dnf4 system upgrade: 106 upgrades, weak deps installed.
	p, _ = b.ParsePlan(fx(t, "rockylinux-9-sim-upgrade.txt"), 1)
	if len(p.Upgrade) != 106 || len(p.Install) != 6 || len(p.Remove) != 0 {
		t.Fatalf("rocky upgrade: %d upgrades %d installs %d removals", len(p.Upgrade), len(p.Install), len(p.Remove))
	}
}

func TestDNFObsoletesAreRemovals(t *testing.T) {
	out := "Upgrading:\n newpkg x86_64 2.0-1 baseos 10 k\n     replacing  oldpkg.x86_64 1.0-1\n" +
		" samepkg x86_64 2.0-1 baseos 1 k\n   replacing samepkg x86_64 1.0-1 repo 1 k\n\nTransaction Summary\nOperation aborted.\n"
	p, err := dnf{}.ParsePlan(out, 1)
	if err != nil || names(p.Remove) != "oldpkg" {
		t.Fatalf("obsoletes %+v %v", p, err)
	}
}

func TestDNFErrors(t *testing.T) {
	b := dnf{}
	for _, f := range []string{"rockylinux-9-err-not-found.txt", "fedora-41-err-not-found.txt", "rockylinux-9-sim-install.txt"} {
		if _, err := b.ParsePlan(fx(t, f), 1); model.ClassOf(err) != model.ClassValidation {
			t.Errorf("%s: %v", f, err)
		}
	}
	// dnf5 protecting its own dependencies.
	_, err := b.ParsePlan(fx(t, "fedora-41-sim-remove.txt"), 1)
	oe := model.AsOpError(err)
	if oe.Class != model.ClassPackageManager || oe.Retryable || !strings.Contains(oe.Message, "librepo") {
		t.Fatalf("protected: %+v", oe)
	}
}

func TestDNFVersions(t *testing.T) {
	v := dnf{}.ParseVersions(fx(t, "rockylinux-9-rpm-q.txt"), []string{"wget", "htop"})
	if v["wget"] != "1.21.1-11.el9_8" || v["htop"] != "" {
		t.Fatalf("versions %v", v)
	}
	if r, _ := (dnf{}).ParseRebootRequired("rc=1"); r == nil || !*r {
		t.Fatal("rc=1 should require reboot")
	}
	if r, _ := (dnf{}).ParseRebootRequired("unknown"); r != nil {
		t.Fatal("unknown should be nil")
	}
}
