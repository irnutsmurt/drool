//go:build integration

package integration

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// VM tests need real systemd and real reboots, which containers can't give
// without privileges. Each VM is a cloud image under qemu in software
// emulation (TCG) inside an unprivileged container (see vm/Dockerfile):
// no --privileged, no /dev/kvm. They are slow (minutes per boot) and opt-in:
//
//	go test -tags integration ./test/integration -run TestVM -vm debian-12,ubuntu-24.04 -v -timeout 4h
var flagVMs = flag.String("vm", "", "comma-separated VMs to test (see vmDefs: debian-12, ubuntu-24.04, rockylinux-9, …, debian-12-arm64); empty skips TestVM")

type vmDef struct {
	Name, URL, Family string
	// CPU model for qemu's software emulation. Newer kernels (e.g. a
	// backports or HWE kernel) crash on TCG's cmpxchg16b, so Debian, Ubuntu
	// and Fedora guests run without it; RHEL 9 needs x86-64-v2 and RHEL 10
	// x86-64-v3, so those keep it.
	CPU               string
	SSHUnit, CronUnit string
	BigPkg            string // slow to install: used to cancel a live transaction
	Arch              string // "arm64": an emulated aarch64 guest; default amd64
}

// The VM matrix: every Linux distribution the managed-host support matrix
// names (README "Supported platforms"), plus an arm64 guest.
var vmDefs = []vmDef{
	{"debian-12", "https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-genericcloud-amd64.qcow2", "apt", "max,la57=off,cx16=off", "ssh", "cron", "default-jre-headless", ""},
	{"debian-13", "https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-amd64.qcow2", "apt", "max,la57=off,cx16=off", "ssh", "cron", "default-jre-headless", ""},
	{"ubuntu-22.04", "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img", "apt", "max,la57=off,cx16=off", "ssh", "cron", "default-jre-headless", ""},
	{"ubuntu-24.04", "https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img", "apt", "max,la57=off,cx16=off", "ssh", "cron", "default-jre-headless", ""},
	{"ubuntu-26.04", "https://cloud-images.ubuntu.com/resolute/current/resolute-server-cloudimg-amd64.img", "apt", "max,la57=off,cx16=off", "ssh", "cron", "default-jre-headless", ""},
	{"rockylinux-9", "https://dl.rockylinux.org/pub/rocky/9/images/x86_64/Rocky-9-GenericCloud-Base.latest.x86_64.qcow2", "dnf", "max,la57=off", "sshd", "crond", "java-17-openjdk-headless", ""},
	{"rockylinux-10", "https://dl.rockylinux.org/pub/rocky/10/images/x86_64/Rocky-10-GenericCloud-Base.latest.x86_64.qcow2", "dnf", "max,la57=off", "sshd", "crond", "java-21-openjdk-headless", ""},
	{"almalinux-9", "https://repo.almalinux.org/almalinux/9/cloud/x86_64/images/AlmaLinux-9-GenericCloud-latest.x86_64.qcow2", "dnf", "max,la57=off", "sshd", "crond", "java-17-openjdk-headless", ""},
	{"almalinux-10", "https://repo.almalinux.org/almalinux/10/cloud/x86_64/images/AlmaLinux-10-GenericCloud-latest.x86_64.qcow2", "dnf", "max,la57=off", "sshd", "crond", "java-21-openjdk-headless", ""},
	{"fedora-44", "https://download.fedoraproject.org/pub/fedora/linux/releases/44/Cloud/x86_64/images/Fedora-Cloud-Base-Generic-44-1.7.x86_64.qcow2", "dnf", "max,la57=off,cx16=off", "sshd", "crond", "java-25-openjdk-headless", ""},
	{"debian-12-arm64", "https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-genericcloud-arm64.qcow2", "apt", "max,pauth-impdef=on", "ssh", "cron", "default-jre-headless", "arm64"},
}

func vmDefNamed(t *testing.T, name string) vmDef {
	t.Helper()
	for _, d := range vmDefs {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("no VM %q", name)
	return vmDef{}
}

// wantedVMs is the -vm selection.
func wantedVMs() map[string]bool {
	want := map[string]bool{}
	for _, n := range strings.Split(*flagVMs, ",") {
		if n = strings.TrimSpace(n); n != "" {
			want[n] = true
		}
	}
	return want
}

var vmImageOnce sync.Once

type vm struct {
	def         vmDef
	c           *container
	fingerprint string
	// fwd maps guest ports forwarded with VM_FWD to their port on
	// 127.0.0.1 of this machine.
	fwd map[int]int
}

func startVM(t *testing.T, def vmDef, env ...string) *vm {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	sshPub, _ := ssh.NewPublicKey(pub)
	privPEM := strings.TrimSpace(string(pem.EncodeToMemory(block)))
	indent := func(s string) string { return "      " + strings.ReplaceAll(s, "\n", "\n      ") }
	seed := t.TempDir()
	os.Chmod(seed, 0o755)
	userData := fmt.Sprintf(`#cloud-config
ssh_deletekeys: true
ssh_genkeytypes: []
ssh_keys:
  ed25519_private: |
%s
  ed25519_public: %s
users:
  - name: deploy
    shell: /bin/bash
    sudo: "ALL=(ALL) NOPASSWD:ALL"
    ssh_authorized_keys: [%q, %q]
  - name: pwuser
    shell: /bin/bash
    lock_passwd: false
    sudo: "ALL=(ALL) ALL"
    ssh_authorized_keys: [%q]
chpasswd:
  expire: false
  users:
    - {name: pwuser, password: %s, type: text}
`, indent(privPEM), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))), ctl.controllerKey(t), testAuthorizedKey(), ctl.controllerKey(t), sudoPassword)
	os.WriteFile(filepath.Join(seed, "user-data"), []byte(userData), 0o644)
	os.WriteFile(filepath.Join(seed, "meta-data"), []byte("instance-id: drool-"+runID+"-"+def.Name+"\nlocal-hostname: "+strings.ReplaceAll(def.Name, ".", "-")+"\n"), 0o644)

	// Always build (a cached no-op when vm/ is unchanged), so changes to
	// run-vm.sh reach the image.
	vmImageOnce.Do(func() {
		mustDocker(t, "build", "-q", "-t", "drool-it-vmrunner", filepath.Join(root, "test", "integration", "vm"))
	})
	c := &container{Name: "drool-it-" + runID + "-vm-" + def.Name, Port: freePort()}
	args := []string{"run", "-d", "--name", c.Name, "--network", netOuter, "-p", fmt.Sprintf("127.0.0.1:%d:22", c.Port),
		"--cpus", "2", "--memory", "3g", "-e", "VM_CPU=" + def.CPU, "-v", "drool-vm-images:/images", "-v", seed + ":/seed:ro"}
	if def.Arch == "arm64" {
		args = append(args, "-e", "VM_ARCH=aarch64")
	}
	if def.Name == "ubuntu-26.04" || def.Name == "ubuntu-24.04" {
		// Ubuntu's 7.0 kernel (7.0.0-38: 26.04's, and the HWE kernel the
		// reboot-required test installs on 24.04) oopsed on an int3 in
		// kernel text while booting with two vCPUs under multi-threaded TCG
		// (26.04), or didn't come back from a reboot (24.04, the fourth boot
		// of that kernel): code patching on one vCPU raced execution on the
		// other, an emulator problem. One vCPU runs the same kernel and
		// systemd without that race.
		args = append(args, "-e", "VM_CPUS=1")
	}
	fwd := map[int]int{}
	for _, e := range env {
		args = append(args, "-e", e)
		if ports, ok := strings.CutPrefix(e, "VM_FWD="); ok {
			for _, p := range strings.Fields(ports) {
				var guest int
				fmt.Sscan(p, &guest)
				fwd[guest] = freePort()
				args = append(args, "-p", fmt.Sprintf("127.0.0.1:%d:%d", fwd[guest], guest))
			}
		}
	}
	mustDocker(t, append(args, "drool-it-vmrunner", def.URL)...)
	t.Cleanup(func() {
		if !*flagKeep {
			docker("rm", "-f", c.Name)
		}
	})
	v := &vm{def: def, c: c, fingerprint: ssh.FingerprintSHA256(sshPub), fwd: fwd}
	t.Logf("%s: booting under TCG (this takes a few minutes)", def.Name)
	booted := false
	defer func() {
		if !booted {
			out, _ := exec.Command("docker", "logs", "--tail", "60", c.Name).CombinedOutput()
			t.Logf("%s console (last lines):\n%s", def.Name, out)
		}
	}()
	boot := 20 * time.Minute
	if def.Arch == "arm64" {
		boot = 40 * time.Minute // a whole other architecture, emulated
	}
	waitFor(t, boot, def.Name+" sshd", func() bool { return sshBanner(c.Port) })
	booted = true
	return v
}

// sshBanner reports whether a real sshd answers (qemu accepts TCP on the
// forwarded port even while the guest is still booting).
func sshBanner(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 3*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 8)
	n, _ := conn.Read(buf)
	return n >= 4 && string(buf[:4]) == "SSH-"
}

// addVMHost registers the VM with its known host key fingerprint and waits
// until cloud-init has created the users.
func addVMHost(t *testing.T, name string, v *vm, o hostOpts) host {
	t.Helper()
	if o.User == "" {
		o.User = "deploy"
	}
	addr, port := "127.0.0.1", v.c.Port
	if o.Via != "" {
		addr, port = v.c.Name, 22
	}
	body := map[string]any{"name": name, "address": addr, "port": port, "user": o.User, "become_method": "sudo"}
	if o.Via != "" {
		body["via_host_id"] = o.Via
	}
	if o.BecomeCredID != "" {
		body["become_credential_id"] = o.BecomeCredID
	}
	var h host
	ctl.must(t, "POST", "/api/v1/hosts", body, &h)
	ctl.must(t, "POST", "/api/v1/hosts/"+h.ID+"/hostkeys/scan", nil, nil)
	ctl.must(t, "POST", "/api/v1/hosts/"+h.ID+"/hostkeys/approve", map[string]string{"fingerprint": v.fingerprint}, nil)
	waitFor(t, 10*time.Minute, name+" connection test", func() bool { return probe(t, h.ID).OK })
	jh := run(t, h.ID, cmd("cloud-init status --wait >/dev/null 2>&1; true", false))
	if jh.State != "succeeded" {
		t.Fatalf("waiting for cloud-init: %v", jh)
	}
	return getHost(t, h.ID)
}

func TestVM(t *testing.T) {
	if *flagVMs == "" {
		t.Skip("set -vm to run the VM tests")
	}
	freshController(t) // a controller of its own: nothing from other tests (M17)
	want := wantedVMs()
	var jump *container
	var jumpHost host
	jumpOnce := func(t *testing.T) (*container, host) {
		if jump == nil {
			jump = startHost(t, image(t, allDistros[0]), "vm-jump", true, netOuter)
			jumpHost = addHost(t, "vm-jump", jump, hostOpts{JumpOnly: true})
		}
		return jump, jumpHost
	}
	_, _ = jumpOnce(t)
	for _, def := range vmDefs {
		if !want[def.Name] {
			continue
		}
		t.Run(def.Name, func(t *testing.T) {
			t.Parallel()
			v := startVM(t, def)
			h := addVMHost(t, "vm-"+def.Name, v, hostOpts{})
			testVM(t, v, h, jumpHost)
		})
	}
}

func svc(name, state string, enabled *bool) map[string]any {
	m := map[string]any{"type": "service", "name": name}
	if state != "" {
		m["state"] = state
	}
	if enabled != nil {
		m["enabled"] = *enabled
	}
	return m
}

func ptr[T any](v T) *T { return &v }

func result(jh jobHost) map[string]any {
	if len(jh.Steps) == 0 {
		return nil
	}
	return jh.Steps[len(jh.Steps)-1].Result
}

func testVM(t *testing.T, v *vm, h host, jump host) {
	def := v.def
	isApt := def.Family == "apt"

	t.Run("connection test with systemd", func(t *testing.T) {
		p := probe(t, h.ID)
		st, detail, _ := p.step("init system")
		expect(t, p.OK && st == "ok" && detail == "systemd", "init system: %s %s", st, detail)
		expect(t, getHost(t, h.ID).Status == "ready", "status %q", getHost(t, h.ID).Status)
		waitFor(t, 5*time.Minute, "facts", func() bool {
			hd := getHostDetail(t, h.ID)
			return hd.Facts != nil && hd.Facts.InitSystem == "systemd"
		})
	})

	t.Run("services", func(t *testing.T) {
		setup := run(t, h.ID, map[string]any{"type": "script", "become": true, "interpreter": "sh", "body": `set -e
cat > /etc/systemd/system/drool-test.service <<'EOF'
[Unit]
Description=DROOL test service
[Service]
ExecStart=/bin/sleep infinity
[Install]
WantedBy=multi-user.target
EOF
cat > /etc/systemd/system/drool-static.service <<'EOF'
[Service]
Type=oneshot
ExecStart=/bin/true
RemainAfterExit=yes
EOF
cat > /etc/systemd/system/drool-fail.service <<'EOF'
[Service]
ExecStart=/bin/false
EOF
systemctl daemon-reload
systemctl mask drool-masked.service
`})
		if setup.State != "succeeded" {
			t.Fatalf("unit setup: %v", setup)
		}
		check := func(step map[string]any, wantState string, wantChanged bool, what string) jobHost {
			t.Helper()
			jh := run(t, h.ID, step)
			ok := jh.State == wantState && (wantState != "succeeded" || (jh.changed() != nil && *jh.changed() == wantChanged))
			expect(t, ok, "%s: %v changed=%v result=%v", what, jh, jh.changed(), result(jh))
			return jh
		}
		check(svc("drool-test", "running", nil), "succeeded", true, "start")
		check(svc("drool-test", "running", nil), "succeeded", false, "start again (idempotent)")
		jh := check(svc("drool-test", "restarted", nil), "succeeded", true, "restart (always acts)")
		if after, _ := result(jh)["after"].(map[string]any); after == nil || after["active"] != "active" {
			t.Errorf("restart did not verify the running state: %v", result(jh))
		}
		jh = check(svc("drool-test", "reloaded", nil), "failed", false, "reload without ExecReload")
		expect(t, jh.ErrorClass == "ServiceError" && strings.Contains(jh.ErrorMessage, "reload"), "reload unsupported: %v", jh)
		check(svc("drool-test", "", ptr(true)), "succeeded", true, "enable")
		check(svc("drool-test", "", ptr(true)), "succeeded", false, "enable again (idempotent)")
		check(svc("drool-test", "", ptr(false)), "succeeded", true, "disable")
		check(svc("drool-test", "stopped", nil), "succeeded", true, "stop")
		check(svc("drool-test", "stopped", nil), "succeeded", false, "stop again (idempotent)")
		check(svc("drool-test", "running", ptr(true)), "succeeded", true, "enable and start")
		check(svc(def.SSHUnit, "reloaded", nil), "succeeded", true, "reload sshd (DROOL's own connection survives)")
		check(svc("systemd-journald", "restarted", nil), "succeeded", true, "restart a system unit")

		jh = check(svc("drool-static", "", ptr(true)), "failed", false, "enable a static unit")
		expect(t, jh.ErrorClass == "ServiceError" && strings.Contains(jh.ErrorMessage, "static"), "static: %v", jh)
		jh = check(svc("drool-masked", "running", nil), "failed", false, "start a masked unit")
		expect(t, jh.ErrorClass == "ServiceError" && strings.Contains(jh.ErrorMessage, "masked"), "masked: %v", jh)
		jh = check(svc("drool-no-such-unit", "running", nil), "failed", false, "start a missing unit")
		expect(t, jh.ErrorClass == "ServiceError" && strings.Contains(jh.ErrorMessage, "not found"), "missing: %v", jh)
		id := submit(t, []string{h.ID}, svc("drool-fail", "running", nil))
		waitJob(t, id, 5*time.Minute)
		jh = getJobHost(t, id, h.ID)
		expect(t, jh.State == "failed" && jh.ErrorClass == "ServiceError", "failing unit: %v", jh)
		expect(t, strings.Contains(output(t, id, h.ID), "drool-fail.service"), "failing unit output should include systemctl status")
	})

	t.Run("file deployment with service actions", func(t *testing.T) {
		deploy := func(content string, extra map[string]any) (string, jobHost) {
			s := map[string]any{"artifact": putArtifact(t, "50-drool.conf", content), "path": "/etc/ssh/sshd_config.d/50-drool.conf",
				"elevated": true, "mode": "0600", "validate": "sshd -t -f {path}"}
			for k, v := range extra {
				s[k] = v
			}
			id := deployJob(t, map[string]any{"hosts": []string{h.ID}}, s)
			return id, getJobHost(t, id, h.ID)
		}
		sshd := map[string]any{"service": map[string]any{"name": def.SSHUnit, "action": "reload"}}
		// Validation catches a broken sshd config before sshd ever sees it.
		_, jh := deploy("NoSuchDirective yes\n", sshd)
		expect(t, jh.State == "failed" && strings.Contains(jh.ErrorMessage, "validation failed"), "invalid sshd config: %v", jh)
		_, jh = deploy("ClientAliveInterval 120\n", sshd)
		expect(t, jh.State == "succeeded", "deploy + reload sshd: %v", jh)
		if s, _ := result(jh)["service"].(map[string]any); s == nil || s["ok"] != true {
			t.Errorf("service result: %v", result(jh))
		}
		eff := submit(t, []string{h.ID}, cmd("sshd -T | grep -i '^clientaliveinterval'", true))
		waitJob(t, eff, 5*time.Minute)
		expect(t, strings.Contains(output(t, eff, h.ID), "clientaliveinterval 120"), "sshd effective config: %q", output(t, eff, h.ID))

		// Restart a unit after installing its drop-in.
		_, jh = deploy("# drop-in\n", map[string]any{"path": "/etc/systemd/system/drool-test.service.d/x.conf", "validate": "",
			"mode": "0644", "service": map[string]any{"name": "drool-test", "action": "restart"}})
		expect(t, jh.State == "failed" && jh.ErrorClass == "ExecutionError" && strings.Contains(jh.ErrorMessage, "does not exist"),
			"drop-in without its directory: %v", jh)
		run(t, h.ID, cmd("mkdir -p /etc/systemd/system/drool-test.service.d", true))
		_, jh = deploy("[Service]\nEnvironment=DROOL=1\n", map[string]any{"path": "/etc/systemd/system/drool-test.service.d/x.conf",
			"validate": "", "mode": "0644", "service": map[string]any{"name": "drool-test", "action": "restart"}})
		expect(t, jh.State == "succeeded", "deploy + restart: %v", jh)

		// The file is installed even when the service then fails, and the job says so.
		_, jh = deploy("# fails\n", map[string]any{"path": "/etc/drool-fail.conf", "validate": "", "mode": "0644",
			"service": map[string]any{"name": "drool-fail", "action": "restart"}})
		expect(t, jh.State == "failed" && jh.ErrorClass == "ServiceError" && strings.Contains(jh.ErrorMessage, "file was installed"),
			"service failure after install: %v", jh)
		chk := run(t, h.ID, cmd("cat /etc/drool-fail.conf", true))
		expect(t, chk.State == "succeeded", "file present after the service failure: %v", chk)
	})

	if def.Name == "ubuntu-24.04" {
		t.Run("unattended-upgrades holds the lock", func(t *testing.T) {
			// Start the real unattended-upgrades run, then install right away.
			run(t, h.ID, cmd("apt-get -q update >/dev/null && systemctl start --no-block apt-daily-upgrade.service", true))
			id := submit(t, []string{h.ID}, map[string]any{"type": "package", "action": "install", "names": []string{"tree"}})
			waitJob(t, id, 30*time.Minute)
			jh := getJobHost(t, id, h.ID)
			out := output(t, id, h.ID)
			t.Logf("contention: %v; waited for lock: %v; attempts: %d", jh, strings.Contains(out, "lock"), len(jh.Steps))
			// Show the overlap from the host's own logs.
			ev := submit(t, []string{h.ID}, map[string]any{"type": "command", "become": true, "read_only": true,
				"command": "grep -h -E 'Start-Date|Commandline|End-Date' /var/log/apt/history.log | tail -n 12; tail -n 5 /var/log/unattended-upgrades/unattended-upgrades.log"})
			waitJob(t, ev, 5*time.Minute)
			t.Logf("apt history and unattended-upgrades log:\n%s", output(t, ev, h.ID))
			expect(t, jh.State == "succeeded" || (jh.ErrorClass == "PackageManagerError" && strings.Contains(jh.ErrorMessage, "locked")),
				"install during unattended-upgrades: %v", jh)
		})
	}

	t.Run("system update and reboot-required", func(t *testing.T) {
		id := submit(t, []string{h.ID}, map[string]any{"type": "system_update", "mode": "full_upgrade", "allow_removals": true})
		waitJob(t, id, 90*time.Minute)
		jh := getJobHost(t, id, h.ID)
		out := output(t, id, h.ID)
		expect(t, jh.State == "succeeded", "system update: %v", jh)
		t.Logf("system update: changed=%v reboot_required=%v reason=%v", jh.changed() != nil && *jh.changed(), result(jh)["reboot_required"], result(jh)["reboot_reason"])
		if def.Name == "ubuntu-24.04" {
			t.Logf("needrestart lines: %q", grepLines(out, "needrestart", "Restarting", "restart"))
		}
		// Install a newer kernel than the running one, as a kernel update does.
		var step map[string]any
		switch {
		case strings.HasPrefix(def.Name, "debian-"):
			// Plain Debian keeps no reboot-required file: DROOL compares the
			// newest installed kernel with the running one, so a newer one
			// comes from backports.
			suite := map[string]string{"debian-13": "trixie"}[def.Name]
			if suite == "" {
				suite = "bookworm"
			}
			arch := "amd64"
			if def.Arch == "arm64" {
				arch = "arm64"
			}
			step = cmd(fmt.Sprintf("echo 'deb http://deb.debian.org/debian %[1]s-backports main' > /etc/apt/sources.list.d/drool-bp.list && "+
				"apt-get -q update >/dev/null && DEBIAN_FRONTEND=noninteractive apt-get -y -q -t %[1]s-backports install linux-image-cloud-%[2]s", suite, arch), true)
			step["timeout_s"] = 3600 // initramfs generation is slow under emulation
		case def.Name == "ubuntu-24.04":
			step = map[string]any{"type": "package", "action": "install", "names": []string{"linux-image-generic-hwe-24.04"}}
		case def.Family == "apt":
			// Ubuntu marks a reboot as required whenever a kernel is installed.
			step = cmd("DEBIAN_FRONTEND=noninteractive apt-get -y -q install --reinstall linux-image-$(uname -r)", true)
			step["timeout_s"] = 3600
		default:
			// needs-restarting counts any core package installed after boot.
			step = cmd("dnf -y -q reinstall kernel-core", true)
			step["timeout_s"] = 3600
		}
		jh = run(t, h.ID, step)
		expect(t, jh.State == "succeeded", "kernel install: %v", jh)
		jh = run(t, h.ID, map[string]any{"type": "package", "action": "install", "names": []string{"tree"}})
		rr := result(jh)["reboot_required"]
		t.Logf("after installing a kernel: reboot_required=%v reason=%v", rr, result(jh)["reboot_reason"])
		expect(t, rr == true, "reboot_required should be true after a kernel install, got %v (%v)", rr, result(jh)["reboot_reason"])

		jh = run(t, h.ID, map[string]any{"type": "system_update", "mode": "upgrade", "reboot": "if_required", "refresh": false})
		reb, _ := result(jh)["reboot"].(map[string]any)
		expect(t, jh.State == "succeeded" && reb != nil && reb["boot_id_after"] != nil && reb["boot_id_after"] != reb["boot_id_before"],
			"system update with reboot if required: %v %v", jh, result(jh))
	})

	t.Run("reboot", func(t *testing.T) {
		jh := run(t, h.ID, map[string]any{"type": "reboot"})
		reb, _ := result(jh)["reboot"].(map[string]any)
		expect(t, jh.State == "succeeded" && jh.changed() != nil && *jh.changed() && reb != nil &&
			reb["boot_id_after"] != nil && reb["boot_id_after"] != reb["boot_id_before"], "reboot: %v %v", jh, result(jh))
		t.Logf("reboot: back after %vs (unreachable %vs)", reb["total_seconds"], reb["down_seconds"])
		expect(t, probe(t, h.ID).OK, "connection test after reboot")
	})

	t.Run("reboot through the jump host", func(t *testing.T) {
		hj := addVMHost(t, "vm-"+def.Name+"-via-jump", v, hostOpts{Via: jump.ID})
		jh := run(t, hj.ID, map[string]any{"type": "reboot"})
		reb, _ := result(jh)["reboot"].(map[string]any)
		expect(t, jh.State == "succeeded" && reb != nil && reb["boot_id_after"] != reb["boot_id_before"], "reboot via jump: %v %v", jh, result(jh))
	})

	t.Run("cancel a live package transaction", func(t *testing.T) {
		id := submit(t, []string{h.ID}, map[string]any{"type": "package", "action": "install", "names": []string{def.BigPkg}})
		proc := map[bool]string{true: "dpkg", false: "rpm"}[isApt]
		// Wait until packages are being unpacked/installed, not just downloaded.
		waitFor(t, 30*time.Minute, proc+" running", func() bool {
			o := output(t, id, h.ID)
			return strings.Contains(o, "Unpacking") || strings.Contains(o, "Installing") || strings.Contains(o, "Running transaction")
		})
		ctl.must(t, "POST", "/api/v1/jobs/"+id+"/cancel", nil, nil)
		waitJob(t, id, 10*time.Minute)
		jh := getJobHost(t, id, h.ID)
		expect(t, jh.State == "cancelled", "cancel during the transaction: %v", jh)
		// What state is the package database in now? DROOL must explain it, not
		// hide it. The next operation has to reach the package manager: a
		// package that isn't installed (tree is, since the reboot-required check).
		var pkgName string
		for _, p := range []string{"jq", "telnet", "zip", "lsof"} {
			q := map[bool]string{true: "dpkg-query -W -f='${Status}' " + p + " 2>/dev/null | grep -q 'ok installed'", false: "rpm -q " + p + " >/dev/null"}[isApt]
			if out, _ := vmSSH(v, q+" && echo installed || echo absent"); out == "absent" {
				pkgName = p
				break
			}
		}
		if pkgName == "" {
			t.Fatal("no small package left that isn't installed")
		}
		next := run(t, h.ID, map[string]any{"type": "package", "action": "install", "names": []string{pkgName}})
		t.Logf("after cancelling a live %s transaction, the next package operation (install %s): %v, changed %v", proc, pkgName, next, next.changed() != nil && *next.changed())
		if isApt {
			st, _ := vmSSH(v, "echo updates: $(ls -A /var/lib/dpkg/updates | wc -l); dpkg --audit | head -3; pgrep -a 'apt|dpkg' || true")
			t.Logf("dpkg after the next operation: %s", st)
		}
		if next.State != "succeeded" {
			expect(t, next.ErrorClass == "PackageManagerError" && next.ErrorHint != "", "next operation failed without an explanation: %v", next)
			if isApt {
				fix := run(t, h.ID, cmd("dpkg --configure -a && apt-get -y -f install", true))
				expect(t, fix.State == "succeeded", "documented fix: %v", fix)
			}
			again := run(t, h.ID, map[string]any{"type": "package", "action": "install", "names": []string{pkgName}})
			expect(t, again.State == "succeeded", "install %s after the documented fix: %v", pkgName, again)
		}
		if !isApt {
			chk := run(t, h.ID, cmd("rpm -Va --nofiles --nodigest >/dev/null 2>&1; dnf -q check && echo rpmdb-ok", true))
			t.Logf("dnf check after cancel: %v", chk)
		}
	})

	if def.Name == "debian-12" {
		t.Run("host key changes across a reboot", func(t *testing.T) {
			run(t, h.ID, cmd("rm -f /etc/ssh/ssh_host_* && ssh-keygen -A >/dev/null", true))
			jh := run(t, h.ID, map[string]any{"type": "reboot"})
			expect(t, jh.State == "failed" && jh.ErrorClass == "HostKeyError", "reboot onto a new host key must not be trusted: %v", jh)
			expect(t, getHost(t, h.ID).Status == "hostkey_mismatch" || probe(t, h.ID).OK == false, "host should need key review")
			// What an operator does next: compare the key DROOL is offered with
			// the host's own (here read on the host, as from its console),
			// approve it, and the host works again.
			onHost := mustVMSSH(t, v, `for f in /etc/ssh/ssh_host_*_key.pub; do ssh-keygen -lf "$f"; done`)
			var scanned struct {
				Items []struct {
					Fingerprint string `json:"fingerprint"`
				} `json:"items"`
			}
			ctl.must(t, "POST", "/api/v1/hosts/"+h.ID+"/hostkeys/scan", nil, &scanned)
			approve := ""
			for _, k := range scanned.Items {
				if k.Fingerprint != "" && strings.Contains(onHost, k.Fingerprint) {
					approve = k.Fingerprint
				}
			}
			if approve == "" {
				t.Fatalf("none of the offered keys is the host's own:\n%s\n%+v", onHost, scanned.Items)
			}
			ctl.must(t, "POST", "/api/v1/hosts/"+h.ID+"/hostkeys/approve", map[string]string{"fingerprint": approve}, nil)
			waitFor(t, 2*time.Minute, "the host after approving its new key", func() bool { return probe(t, h.ID).OK })
		})
	}
}

func grepLines(s string, subs ...string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		for _, sub := range subs {
			if strings.Contains(l, sub) {
				out = append(out, strings.TrimSpace(l))
				break
			}
		}
	}
	if len(out) > 15 {
		out = out[:15]
	}
	return out
}
