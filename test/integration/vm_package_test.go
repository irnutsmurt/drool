//go:build integration

package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestVMReleasePackage runs the release's native package on a VM with real
// systemd, following the README (M17): an installation made by hand with an
// older version (M16, the way its README said) moved to the package, the
// hardened unit, the documented native backup, restore and key rotation,
// the package's upgrade path under systemd, removal keeping the data; then,
// from a clean machine, a package upgrade from the tagged v0.1.0-alpha and
// the documented way back. Needs -release and -vm (debian-12,
// rockylinux-9, debian-12-arm64, …).
func TestVMReleasePackage(t *testing.T) {
	r := needRelease(t)
	if *flagVMs == "" {
		t.Skip("set -vm to run the VM tests")
	}
	want := wantedVMs()
	for _, def := range vmDefs {
		if !want[def.Name] {
			continue
		}
		t.Run(def.Name, func(t *testing.T) {
			t.Parallel()
			v := startVM(t, def, "VM_FWD=8080")
			waitFor(t, 10*time.Minute, "cloud-init", func() bool {
				out, err := vmSSH(v, "cloud-init status --wait >/dev/null 2>&1; echo ok")
				return err == nil && strings.HasSuffix(out, "ok")
			})
			p := &vmPackage{t: t, v: v, r: r, arch: map[string]string{"": "amd64", "arm64": "arm64"}[def.Arch]}
			p.kind = map[string]string{"apt": "deb", "dnf": "rpm"}[def.Family]
			vmUpload(t, v, "deploy", r.artifact(t, p.kind, p.arch), "/home/deploy/"+filepath.Base(r.artifact(t, p.kind, p.arch)))
			p.pkg = "/home/deploy/" + filepath.Base(r.artifact(t, p.kind, p.arch))
			if !t.Run("from a manual M16 installation to the package", p.fromManual) {
				return
			}
			t.Run("package upgrade from v0.1.0-alpha and back", p.upgradeAndBack)
		})
	}
}

type vmPackage struct {
	t          *testing.T
	v          *vm
	r          *releaseUnderTest
	arch, kind string
	pkg        string // the release's package on the VM
}

// sh runs a command as deploy (with sudo where it says so) on the VM.
func (p *vmPackage) sh(t *testing.T, cmd string) string {
	t.Helper()
	return mustVMAs(t, p.v, "deploy", cmd, 30*time.Minute)
}

func (p *vmPackage) install(file string) string {
	if p.kind == "deb" {
		return "sudo DEBIAN_FRONTEND=noninteractive apt-get install -y " + file
	}
	return "sudo dnf install -y " + file
}

// controller is DROOL on the VM, through the forwarded port, set up with
// the code from the journal (README: Native packages).
func (p *vmPackage) controller(t *testing.T, fresh bool) *controller {
	t.Helper()
	c := &controller{port: p.v.fwd[8080], client: insecureClient()}
	waitFor(t, 5*time.Minute, "DROOL answering on the VM", func() bool {
		resp, err := c.client.Get(c.base() + "/health")
		if err == nil {
			resp.Body.Close()
		}
		return err == nil
	})
	if !fresh {
		if err := c.login(); err != nil {
			t.Fatalf("signing in: %v", err)
		}
		return c
	}
	var code string
	waitFor(t, 3*time.Minute, "the setup code in the journal", func() bool {
		lines := strings.Split(p.sh(t, "sudo journalctl -u drool -n 60 --no-pager -o cat"), "\n")
		for i, l := range lines {
			if strings.TrimSpace(l) == "Setup code:" && i+1 < len(lines) {
				code = strings.TrimSpace(lines[i+1])
			}
		}
		return code != ""
	})
	c.setupKey = code
	if err := c.claim(); err != nil {
		t.Fatal(err)
	}
	return c
}

// selfHost lets DROOL on the VM manage the VM itself over SSH (its own
// key for deploy, the host key approved by fingerprint) and runs a job.
func (p *vmPackage) selfHost(t *testing.T, c *controller) string {
	t.Helper()
	var k struct {
		PublicKey string `json:"public_key"`
	}
	c.must(t, "GET", "/api/v1/secrets/controller-ssh-key", nil, &k)
	p.sh(t, "echo "+shq(strings.TrimSpace(k.PublicKey))+" >> ~/.ssh/authorized_keys")
	fp := p.v.fingerprint // the host key the test gave the VM (cloud-init)
	var h struct{ ID string }
	c.must(t, "POST", "/api/v1/hosts", map[string]any{"name": "self", "address": "127.0.0.1", "port": 22, "user": "deploy", "become_method": "sudo"}, &h)
	c.must(t, "POST", "/api/v1/hosts/"+h.ID+"/hostkeys/scan", nil, nil)
	c.must(t, "POST", "/api/v1/hosts/"+h.ID+"/hostkeys/approve", map[string]string{"fingerprint": fp}, nil)
	p.job(t, c, h.ID, "first job")
	return h.ID
}

func (p *vmPackage) job(t *testing.T, c *controller, hostID, what string) {
	t.Helper()
	var j struct{ ID string }
	c.must(t, "POST", "/api/v1/jobs", map[string]any{"steps": []any{cmd("systemctl is-active drool; id -un", true)},
		"targets": map[string]any{"hosts": []string{hostID}}}, &j)
	var st string
	waitFor(t, 10*time.Minute, what, func() bool {
		var job struct{ Status string }
		c.must(t, "GET", "/api/v1/jobs/"+j.ID, nil, &job)
		st = job.Status
		return st == "completed" || st == "interrupted" || st == "cancelled"
	})
	var jh jobHost
	c.must(t, "GET", "/api/v1/jobs/"+j.ID+"/hosts/"+hostID, nil, &jh)
	expect(t, jh.State == "succeeded", "%s on %s: %v", what, p.v.def.Name, jh)
}

func (p *vmPackage) fromManual(t *testing.T) {
	// M16, installed by hand as its README said.
	old := m16Build.build(t, "drool", p.arch)
	vmUpload(t, p.v, "deploy", old, "/home/deploy/drool-m16")
	unit, err := exec.Command("git", "-C", root, "show", "0bb9535:packaging/systemd/drool.service").Output()
	if err != nil {
		t.Fatal(err)
	}
	unitFile := filepath.Join(t.TempDir(), "drool.service")
	os.WriteFile(unitFile, unit, 0o644)
	vmUpload(t, p.v, "deploy", unitFile, "/home/deploy/drool.service")
	p.sh(t, `sudo install -m 0755 ~/drool-m16 /usr/local/bin/drool
sudo useradd --system --home-dir /var/lib/drool --shell /usr/sbin/nologin drool
sudo install -m 0644 ~/drool.service /etc/systemd/system/drool.service
sudo systemctl daemon-reload
sudo systemctl enable --now drool`)
	c := p.controller(t, true)
	hostID := p.selfHost(t, c)

	// To the package, following "Moving to the package from a manual installation".
	out := p.sh(t, p.install(p.pkg)+" 2>&1")
	expect(t, strings.Contains(out, "/etc/systemd/system/drool.service") && strings.Contains(out, "/usr/local/bin/drool"),
		"the package didn't point out the manual installation:\n%s", out)
	p.sh(t, `sudo systemctl stop drool
sudo rm /etc/systemd/system/drool.service /usr/local/bin/drool
`+p.install(p.pkg)+` >/dev/null
sudo systemctl daemon-reload && sudo systemctl enable --now drool`)
	c = p.controller(t, false)
	var info struct {
		Version string `json:"version"`
	}
	c.must(t, "GET", "/api/v1/system/info", nil, &info)
	expect(t, info.Version == p.r.Version, "after moving to the package: %s", info.Version)
	p.job(t, c, hostID, "a job from the packaged service")

	// The unit's sandbox, as systemd applies it.
	show := p.sh(t, "systemctl show drool -p ExecStart -p User -p NoNewPrivileges -p ProtectSystem -p CapabilityBoundingSet -p MemoryDenyWriteExecute -p SystemCallFilter --value | head -c 2000")
	for _, w := range []string{"/usr/bin/drool", "drool", "yes", "strict"} {
		expect(t, strings.Contains(show, w), "systemctl show drool lacks %q:\n%s", w, show)
	}
	sec := p.sh(t, "sudo systemd-analyze security drool --no-pager 2>&1 | tail -n 1")
	t.Logf("%s: systemd-analyze security drool: %s", p.v.def.Name, sec)
	if m := regexp.MustCompile(`exposure level for drool\.service: ([0-9.]+)`).FindStringSubmatch(sec); m != nil {
		var score float64
		fmt.Sscan(m[1], &score)
		expect(t, score < 3.0, "exposure %v: the sandbox is weaker than it should be", score)
	} else {
		t.Errorf("no exposure level: %s", sec)
	}

	// Backup and restore, the README's native commands: the same machine
	// (the installed key opens it), then with the key file kept elsewhere.
	before := createSecretOn(t, c, "before-backup")
	out = p.sh(t, "sudo -u drool drool backup 2>&1")
	m := regexp.MustCompile(`/var/lib/drool/backups/drool-backup-[0-9-]+\.tar\.gz`).FindString(out)
	if m == "" {
		t.Fatalf("backup: %s", out)
	}
	p.sh(t, "sudo install -m 0600 -o \"$USER\" "+m+" ./drool-backup.tar.gz && sudo install -m 0600 -o \"$USER\" /var/lib/drool/master.key ./master.key")
	after := createSecretOn(t, c, "after-backup")
	restored := func(what string) {
		t.Helper()
		c = p.controller(t, false)
		var s struct {
			Items []struct{ ID string } `json:"items"`
		}
		c.must(t, "GET", "/api/v1/secrets", nil, &s)
		got := map[string]bool{}
		for _, x := range s.Items {
			got[x.ID] = true
		}
		expect(t, got[before] && !got[after], "%s: the backup's credential %v, the later one %v", what, got[before], got[after])
	}
	out = p.sh(t, `sudo systemctl stop drool
sudo -u drool drool restore - < drool-backup.tar.gz
sudo systemctl start drool`)
	expect(t, strings.Contains(out, "Restored backup"), "restore on the same machine:\n%s", out)
	restored("restore on the same machine")
	createSecretOn(t, c, "after-backup-2")
	out = p.sh(t, `sudo systemctl stop drool
sudo install -m 0600 -o drool -g drool master.key /var/lib/drool/restore.key
sudo -u drool drool restore --master-key /var/lib/drool/restore.key - < drool-backup.tar.gz
sudo rm /var/lib/drool/restore.key
sudo systemctl start drool`)
	expect(t, strings.Contains(out, "Restored backup"), "restore with the key file:\n%s", out)
	restored("restore with the key file")
	// Root is refused on the service's data directory.
	if out, err := vmAs(p.v, "deploy", "sudo drool backup", nil, time.Minute); err == nil || !strings.Contains(out, "sudo -u drool drool backup") {
		t.Errorf("drool backup as root: %v\n%s", err, out)
	}
	// Master key rotation, as documented.
	out = p.sh(t, "sudo systemctl stop drool && sudo -u drool drool secrets rotate-key 2>&1 && sudo systemctl start drool")
	expect(t, strings.Contains(out, "master.key.previous-"), "rotate-key:\n%s", out)
	c = p.controller(t, false)
	p.job(t, c, hostID, "a job after rotating the master key")
	var facts struct {
		MasterKey struct {
			PreviousFiles []string `json:"previous_files"`
		} `json:"master_key"`
	}
	c.must(t, "GET", "/api/v1/system/security", nil, &facts)
	expect(t, len(facts.MasterKey.PreviousFiles) == 1, "Secure your installation doesn't see the old key file: %+v", facts)

	// The package's upgrade path under systemd restarts the service.
	pid := p.sh(t, "systemctl show -p MainPID --value drool")
	reinstall := "sudo DEBIAN_FRONTEND=noninteractive apt-get install -y --reinstall " + p.pkg
	if p.kind == "rpm" {
		reinstall = "sudo dnf reinstall -y " + p.pkg
	}
	p.sh(t, reinstall+" >/dev/null")
	waitFor(t, 2*time.Minute, "the service restarted", func() bool {
		return p.sh(t, "systemctl is-active drool; systemctl show -p MainPID --value drool") != "active\n"+pid
	})
	p.controller(t, false)

	// Removal stops and disables the service and keeps the data.
	remove := map[string]string{"deb": "sudo apt-get remove -y drool", "rpm": "sudo dnf remove -y drool"}[p.kind]
	out = p.sh(t, remove+" 2>&1")
	expect(t, strings.Contains(out, "Its data was kept"), "removal:\n%s", out)
	state, _ := vmAs(p.v, "deploy", "systemctl is-active drool; systemctl is-enabled drool; sudo test -s /var/lib/drool/drool.db && sudo test -s /var/lib/drool/master.key && echo data-kept", nil, time.Minute)
	expect(t, strings.Contains(state, "inactive") && strings.Contains(state, "data-kept"), "after removal: %s", state)
	p.sh(t, p.install(p.pkg)+" >/dev/null && sudo systemctl enable --now drool")
	c = p.controller(t, false)
	p.job(t, c, hostID, "a job after reinstalling")
}

// upgradeAndBack: a clean machine with an older DROOL package (the tagged
// alpha, packaged the way a release packages DROOL), upgraded to this one —
// which backs the database up before migrating it — and taken back the way
// "Going back" says: install the older package, restore the pre-upgrade
// backup with it.
func (p *vmPackage) upgradeAndBack(t *testing.T) {
	remove := map[string]string{"deb": "sudo apt-get purge -y drool", "rpm": "sudo dnf remove -y drool"}[p.kind]
	p.sh(t, remove+" >/dev/null && sudo rm -rf /var/lib/drool /etc/drool && sudo userdel drool")
	oldPkg := p.oldPackage(t)
	vmUpload(t, p.v, "deploy", oldPkg, "/home/deploy/"+filepath.Base(oldPkg))
	old := "/home/deploy/" + filepath.Base(oldPkg)
	p.sh(t, p.install(old)+" >/dev/null && sudo systemctl enable --now drool")
	c := p.controller(t, true)
	hostID := p.selfHost(t, c)
	createSecretOn(t, c, "on-the-alpha")

	p.sh(t, p.install(p.pkg)+" >/dev/null")
	c = p.controller(t, false)
	var info struct {
		Version string `json:"version"`
	}
	c.must(t, "GET", "/api/v1/system/info", nil, &info)
	expect(t, info.Version == p.r.Version, "after the package upgrade: %s", info.Version)
	pre := p.sh(t, "sudo ls /var/lib/drool/backups/")
	expect(t, strings.Contains(pre, "drool-pre-upgrade-") && strings.Contains(pre, "-schema5.tar.gz"), "no pre-upgrade backup: %s", pre)
	p.job(t, c, hostID, "a job after the package upgrade")
	createSecretOn(t, c, "after-the-upgrade")

	// Going back, as documented.
	file := "/var/lib/drool/backups/" + strings.TrimSpace(strings.Fields(pre)[0])
	downgrade := "sudo DEBIAN_FRONTEND=noninteractive apt-get install -y --allow-downgrades " + old
	if p.kind == "rpm" {
		downgrade = "sudo dnf downgrade -y " + old
	}
	out := p.sh(t, "sudo systemctl stop drool && "+downgrade+" >/dev/null && sudo -u drool drool restore "+file+" 2>&1 && sudo systemctl start drool")
	expect(t, strings.Contains(out, "Restored backup"), "going back:\n%s", out)
	c = p.controller(t, false)
	var s struct {
		Items []struct{ Name string } `json:"items"`
	}
	c.must(t, "GET", "/api/v1/secrets", nil, &s)
	names := ""
	for _, x := range s.Items {
		names += x.Name + " "
	}
	expect(t, strings.Contains(names, "on-the-alpha") && !strings.Contains(names, "after-the-upgrade"), "after going back: %s", names)
	p.job(t, c, hostID, "a job after going back")
}

// oldPackage builds a package of the tagged alpha's binary with this
// release's packaging (version 0.1.0): what an older DROOL package is.
func (p *vmPackage) oldPackage(t *testing.T) string {
	t.Helper()
	stage := t.TempDir()
	out := t.TempDir()
	cp := func(src, dst string, mode os.FileMode) {
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		os.MkdirAll(filepath.Dir(dst), 0o755)
		if err := os.WriteFile(dst, b, mode); err != nil {
			t.Fatal(err)
		}
	}
	cp(alphaBuild.build(t, "drool", p.arch), filepath.Join(stage, "drool"), 0o755)
	for _, a := range []string{"amd64", "arm64"} {
		cp(filepath.Join(p.r.binDir, "drool-agent-linux-"+a), filepath.Join(stage, "drool-agent-linux-"+a), 0o755)
	}
	cp(filepath.Join(root, "packaging", "systemd", "drool.service"), filepath.Join(stage, "drool.service"), 0o644)
	cp(filepath.Join(root, "packaging", "config.example.yaml"), filepath.Join(stage, "share", "config.example.yaml"), 0o644)
	cp(filepath.Join(root, "README.md"), filepath.Join(stage, "doc", "README.md"), 0o644)
	for _, s := range []string{"preinstall.sh", "postinstall.sh", "preremove.sh", "postremove.sh"} {
		cp(filepath.Join(root, "packaging", "release", "scripts", s), filepath.Join(stage, "scripts", s), 0o755)
	}
	nf, err := os.ReadFile(filepath.Join(root, "packaging", "release", "nfpm.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(stage, "nfpm.yaml"), regexp.MustCompile(`(?m)^license: .*$`).ReplaceAll(nf, []byte("license: NOASSERTION")), 0o644)
	b, _ := os.ReadFile(filepath.Join(root, "packaging", "release", "build.sh"))
	image := regexp.MustCompile(`(?m)^NFPM_IMAGE=(\S+)`).FindSubmatch(b)
	if image == nil {
		t.Fatal("no NFPM_IMAGE in build.sh")
	}
	if o, err := exec.Command("docker", "run", "--rm", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"-v", stage+":/w:ro", "-v", out+":/out", "-w", "/w", "-e", "ARCH="+p.arch, "-e", "PKG_VERSION=0.1.0",
		"-e", "MAINTAINER=DROOL tests <tests@drool.invalid>", string(image[1]), "package", "-f", "nfpm.yaml", "-p", p.kind, "-t", "/out/").CombinedOutput(); err != nil {
		t.Fatalf("nfpm: %v\n%s", err, o)
	}
	files, _ := filepath.Glob(filepath.Join(out, "*."+p.kind))
	if len(files) != 1 {
		t.Fatalf("old package: %v", files)
	}
	return files[0]
}
