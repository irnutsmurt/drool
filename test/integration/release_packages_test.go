//go:build integration

package integration

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// packageTarget is a distribution the native packages are qualified on.
type packageTarget struct {
	Name, Image, Kind string // Kind: deb | rpm
}

// The official package support matrix (README "Supported platforms").
var packageTargets = []packageTarget{
	{"debian-12", "debian:12", "deb"},
	{"debian-13", "debian:13", "deb"},
	{"ubuntu-22.04", "ubuntu:22.04", "deb"},
	{"ubuntu-24.04", "ubuntu:24.04", "deb"},
	{"ubuntu-26.04", "ubuntu:26.04", "deb"},
	{"rockylinux-9", "rockylinux:9", "rpm"},
	{"rockylinux-10", "rockylinux/rockylinux:10", "rpm"},
	{"almalinux-9", "almalinux:9", "rpm"},
	{"almalinux-10", "almalinux:10", "rpm"},
	{"fedora-44", "fedora:44", "rpm"},
}

// TestReleasePackages installs, reinstalls (the upgrade path of the
// maintainer scripts), removes and purges the release's .deb and .rpm on
// every supported distribution, in ordinary unprivileged containers
// (without systemd: the scripts must cope, and do nothing else). The
// controller runs from the installed layout as the drool user; the package
// never touches the data, configuration or the account it created once
// they hold something.
func TestReleasePackages(t *testing.T) {
	r := needRelease(t)
	for _, pt := range packageTargets {
		t.Run(pt.Name, func(t *testing.T) {
			t.Parallel()
			pkg := r.artifact(t, pt.Kind, "amd64")
			name := "drool-it-" + runID + "-pkg-" + pt.Name
			port := freePort()
			mustDocker(t, "run", "-d", "--name", name, "--memory", "1g", "-p", fmt.Sprintf("127.0.0.1:%d:8080", port),
				"-v", r.Dir+":/release:ro", pt.Image, "sleep", "infinity")
			t.Cleanup(func() {
				if !*flagKeep {
					docker("rm", "-f", name)
				}
			})
			sh := func(script string) (string, error) { return docker("exec", name, "sh", "-c", script) }
			must := func(script string) string {
				t.Helper()
				out, err := sh(script)
				if err != nil {
					t.Fatalf("%s: %v\n%s", script, err, out)
				}
				return out
			}
			in := "/release/" + filepath.Base(pkg)
			install, remove, purge := "", "", ""
			if pt.Kind == "deb" {
				must("apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq procps >/dev/null")
				install = "DEBIAN_FRONTEND=noninteractive apt-get install -y -q " + in
				remove, purge = "apt-get remove -y -q drool", "apt-get purge -y -q drool"
			} else {
				must("command -v runuser >/dev/null && command -v pkill >/dev/null || dnf install -y -q util-linux procps-ng >/dev/null")
				// Not quiet: dnf -q hides what the package's scripts print.
				install = "dnf install -y " + in
				remove = "dnf remove -y drool"
			}
			retry := func(script string) string {
				t.Helper()
				var out string
				var err error
				for i := 0; i < 3; i++ { // mirrors come and go
					if out, err = sh(script); err == nil {
						return out
					}
					time.Sleep(10 * time.Second)
				}
				t.Fatalf("%s: %v\n%s", script, err, out)
				return ""
			}

			// Install.
			out := retry(install + " 2>&1")
			expect(t, strings.Contains(out, "DROOL is installed. Start it:") && strings.Contains(out, "systemctl enable --now drool"),
				"no first-start instructions:\n%s", out)
			files := must(`stat -c '%n %a %U:%G' /usr/bin/drool /usr/lib/drool /usr/lib/drool/drool-agent-linux-amd64 \
				/usr/lib/drool/drool-agent-linux-arm64 /usr/lib/systemd/system/drool.service /var/lib/drool /etc/drool \
				/usr/share/drool/THIRD_PARTY_NOTICES.txt /usr/share/drool/config.example.yaml`)
			for _, want := range []string{"/usr/bin/drool 755 root:root", "/usr/lib/drool 755 root:root",
				"/usr/lib/drool/drool-agent-linux-amd64 755 root:root", "/usr/lib/drool/drool-agent-linux-arm64 755 root:root",
				"/usr/lib/systemd/system/drool.service 644 root:root", "/var/lib/drool 750 drool:drool", "/etc/drool 755 root:root",
				"/usr/share/drool/THIRD_PARTY_NOTICES.txt 644 root:root", "/usr/share/drool/config.example.yaml 644 root:root"} {
				expect(t, strings.Contains(files, want), "%s: want %q in\n%s", pt.Name, want, files)
			}
			// The documentation, unless the system leaves /usr/share/doc out
			// (Ubuntu's minimized container images do).
			if _, err := sh("grep -qs 'path-exclude=/usr/share/doc/' /etc/dpkg/dpkg.cfg.d/*"); err != nil {
				must("test -s /usr/share/doc/drool/README.md && test -s /usr/share/doc/drool/PACKAGE.md")
			}
			acct := strings.Split(strings.TrimSpace(must("getent passwd drool")), ":")
			uid := -1
			if len(acct) == 7 {
				uid, _ = strconv.Atoi(acct[2])
			}
			expect(t, uid > 0 && uid < 1000 && acct[5] == "/var/lib/drool" && strings.HasSuffix(acct[6], "nologin"),
				"%s: the drool account isn't a system account without a shell: %v", pt.Name, acct)
			v := must("drool version --json")
			expect(t, strings.Contains(v, `"version": "`+r.Version+`"`) && strings.Contains(v, `"modified": false`), "drool version: %s", v)
			unit := must("cat /usr/lib/systemd/system/drool.service")
			expect(t, strings.Contains(unit, "ExecStart=/usr/bin/drool serve") && strings.Contains(unit, "User=drool"), "unit:\n%s", unit)

			// DROOL's commands run as the drool user; root is told so.
			out, err := sh("drool migrate 2>&1")
			expect(t, err != nil && strings.Contains(out, "sudo -u drool drool migrate"), "migrate as root: %v\n%s", err, out)
			must("runuser -u drool -- drool migrate")
			// The controller runs from the package's layout and hands out the
			// Shepherd binaries in /usr/lib/drool.
			must("runuser -u drool -- env DROOL_SETUP_SECRET=pkg-test-setup-code-1 DROOL_LOGGING_OUTPUT=stdout DROOL_NETWORK_GEOIP_DOWNLOAD=false drool serve >/tmp/serve.log 2>&1 &")
			c := &controller{port: port, setupKey: "pkg-test-setup-code-1", client: insecureClient()}
			waitFor(t, time.Minute, pt.Name+" controller", func() bool {
				_, err := sh("runuser -u drool -- drool healthcheck >/dev/null 2>&1")
				return err == nil
			})
			if err := c.claim(); err != nil {
				t.Fatalf("setup on %s: %v\n%s", pt.Name, err, must("cat /tmp/serve.log"))
			}
			resp, err := c.client.Get(c.base() + "/agent/install.sh")
			if err != nil {
				t.Fatal(err)
			}
			script := readAll(resp)
			for _, arch := range []string{"amd64", "arm64"} {
				sum := strings.Fields(must("sha256sum /usr/lib/drool/drool-agent-linux-" + arch))[0]
				expect(t, strings.Contains(script, sum), "%s: install.sh lacks the packaged %s Shepherd", pt.Name, arch)
			}
			must("pkill -x drool; sleep 1")

			// Operator files the package must never replace.
			must("printf 'server:\\n  web_port: 8443\\n' > /etc/drool/config.yaml && chmod 0640 /etc/drool/config.yaml && chgrp drool /etc/drool/config.yaml")
			before := must("sha256sum /etc/drool/config.yaml /var/lib/drool/master.key /var/lib/drool/drool.db")

			// Reinstalling runs the scripts' upgrade path.
			reinstall := "DEBIAN_FRONTEND=noninteractive apt-get install -y -q --reinstall " + in
			if pt.Kind == "rpm" {
				reinstall = "dnf reinstall -y -q " + in
			}
			out = retry(reinstall + " 2>&1")
			expect(t, !strings.Contains(out, "DROOL is installed. Start it:"), "%s: an upgrade printed the first-install message:\n%s", pt.Name, out)
			expect(t, must("sha256sum /etc/drool/config.yaml /var/lib/drool/master.key /var/lib/drool/drool.db") == before,
				"%s: the upgrade changed operator files", pt.Name)

			// Remove: the program goes, the data, configuration and account stay.
			out = must(remove + " 2>&1")
			expect(t, strings.Contains(out, "Its data was kept"), "%s remove:\n%s", pt.Name, out)
			left, _ := sh("ls -d /usr/bin/drool /usr/lib/drool /usr/lib/systemd/system/drool.service /usr/share/drool /usr/share/doc/drool 2>/dev/null")
			expect(t, strings.TrimSpace(left) == "", "%s: left after removal: %s", pt.Name, left)
			expect(t, must("sha256sum /etc/drool/config.yaml /var/lib/drool/master.key /var/lib/drool/drool.db") == before,
				"%s: removing the package changed operator files", pt.Name)
			must("getent passwd drool")
			if purge != "" {
				must("dpkg -i " + in + " >/dev/null 2>&1 && " + purge)
				expect(t, must("sha256sum /etc/drool/config.yaml /var/lib/drool/master.key /var/lib/drool/drool.db") == before,
					"%s: purging the package deleted or changed operator files", pt.Name)
			}
			// Installing again picks the data up.
			retry(install + " >/dev/null 2>&1")
			out = must("runuser -u drool -- drool migrate 2>&1")
			expect(t, strings.Contains(out, "up to date"), "%s: after reinstalling: %s", pt.Name, out)
		})
	}
}
