//go:build integration

package integration

import (
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// vmAs runs a shell command on a VM as user (no sudo) over a fresh test
// connection — a real login session, as rootless container runtimes need —
// feeding it stdin, within d.
func vmAs(v *vm, user, cmd string, stdin io.Reader, d time.Duration) (string, error) {
	testAuthorizedKey()
	cfg := &ssh.ClientConfig{User: user, Auth: []ssh.AuthMethod{ssh.PublicKeys(testSigner)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 15 * time.Second}
	nc, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", v.c.Port), 15*time.Second)
	if err != nil {
		return "", err
	}
	nc.SetDeadline(time.Now().Add(d))
	cc, chans, reqs, err := ssh.NewClientConn(nc, nc.RemoteAddr().String(), cfg)
	if err != nil {
		nc.Close()
		return "", err
	}
	c := ssh.NewClient(cc, chans, reqs)
	defer c.Close()
	s, err := c.NewSession()
	if err != nil {
		return "", err
	}
	defer s.Close()
	if stdin != nil {
		s.Stdin = stdin
	}
	out, err := s.CombinedOutput("bash -lc " + shq(cmd))
	return strings.TrimSpace(string(out)), err
}

func mustVMAs(t *testing.T, v *vm, user, cmd string, d time.Duration) string {
	t.Helper()
	out, err := vmAs(v, user, cmd, nil, d)
	if err != nil {
		t.Fatalf("%s on %s as %s: %v\n%s", cmd, v.def.Name, user, err, out)
	}
	return out
}

// vmUpload copies a local file to path on the VM, as user.
func vmUpload(t *testing.T, v *vm, user, local, path string) {
	t.Helper()
	f, err := os.Open(local)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if out, err := vmAs(v, user, "cat > "+shq(path), f, 30*time.Minute); err != nil {
		t.Fatalf("upload %s: %v\n%s", local, err, out)
	}
}

// vmRuntimeUser makes an ordinary user without sudo ("alice") who can log
// in with the test key and keeps her user services running (linger).
func vmRuntimeUser(t *testing.T, v *vm) {
	mustVMSSH(t, v, `id alice >/dev/null 2>&1 || useradd -m -s /bin/bash alice
install -d -o alice -g alice -m 0700 /home/alice/.ssh
cp /home/deploy/.ssh/authorized_keys /home/alice/.ssh/authorized_keys && chown alice:alice /home/alice/.ssh/authorized_keys
loginctl enable-linger alice`)
}

// TestVMContainerRuntimes qualifies the container image beyond ordinary
// Docker (M17): rootless Podman with the release's Quadlet unit, and on
// Debian/Ubuntu rootless Docker with the release's compose.yaml, each as an
// ordinary user without sudo, following the README — first-run setup, the
// restrictions it runs with, a restart, the documented backup, master key
// copy and restore. Needs -release and -vm (e.g. ubuntu-24.04).
func TestVMContainerRuntimes(t *testing.T) {
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
			vmRuntimeUser(t, v)
			for _, f := range []struct{ kind, name string }{{"container-image", "drool.oci.tar"}, {"compose", "compose.yaml"}, {"podman-quadlet", "drool.container"}} {
				vmUpload(t, v, "alice", r.artifact(t, f.kind, ""), "/home/alice/"+f.name)
			}
			if def.Family == "apt" {
				t.Run("rootless docker", func(t *testing.T) { testRootlessDocker(t, v, r) })
			}
			t.Run("rootless podman", func(t *testing.T) { testRootlessPodman(t, v, r) })
		})
	}
}

// runtimeController checks a controller in a container on the VM through
// the forwarded port: setup with the code from the logs, its version. diag
// tells what the runtime knows when the code doesn't show up.
func runtimeController(t *testing.T, v *vm, r *releaseUnderTest, logs, diag func() string) *controller {
	t.Helper()
	var code string
	deadline := time.Now().Add(5 * time.Minute)
	for code == "" {
		code = setupCodeIn(logs())
		if code == "" && time.Now().After(deadline) {
			t.Fatalf("no setup code in the logs after 5 minutes; the logs:\n%s\n--- the runtime ---\n%s", tail(logs(), 40), diag())
		}
		if code == "" {
			time.Sleep(5 * time.Second)
		}
	}
	c := &controller{port: v.fwd[8080], setupKey: code, client: insecureClient()}
	waitFor(t, 3*time.Minute, "the controller through the VM's port", func() bool {
		resp, err := c.client.Get(c.base() + "/health")
		if err == nil {
			resp.Body.Close()
		}
		return err == nil
	})
	if err := c.claim(); err != nil {
		t.Fatal(err)
	}
	var info struct{ Version, Platform string }
	c.must(t, "GET", "/api/v1/system/info", nil, &info)
	expect(t, info.Version == r.Version, "version %+v", info)
	t.Logf("%s: drool %s (%s) set up", v.def.Name, info.Version, info.Platform)
	return c
}

func testRootlessDocker(t *testing.T, v *vm, r *releaseUnderTest) {
	codename := mustVMSSH(t, v, ". /etc/os-release; echo $ID $VERSION_CODENAME")
	id, suite, _ := strings.Cut(codename, " ")
	arch := map[string]string{"": "amd64", "arm64": "arm64"}[v.def.Arch]
	mustVMAs(t, v, "deploy", fmt.Sprintf(`set -e
sudo apt-get update -qq
sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq ca-certificates curl uidmap dbus-user-session >/dev/null
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/%[1]s/gpg -o /etc/apt/keyrings/docker.asc
echo "deb [arch=%[3]s signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/%[1]s %[2]s stable" | sudo tee /etc/apt/sources.list.d/docker.list >/dev/null
sudo apt-get update -qq
sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-ce-rootless-extras docker-compose-plugin >/dev/null
sudo systemctl disable --now docker.service docker.socket`, id, suite, arch), 40*time.Minute)
	dock := `export XDG_RUNTIME_DIR=/run/user/$(id -u) DOCKER_HOST=unix:///run/user/$(id -u)/docker.sock; cd ~/dc && `
	mustVMAs(t, v, "alice", "dockerd-rootless-setuptool.sh install >/tmp/rootless-setup.log 2>&1 || { cat /tmp/rootless-setup.log; exit 1; }", 15*time.Minute)
	ver := mustVMAs(t, v, "alice", `export XDG_RUNTIME_DIR=/run/user/$(id -u) DOCKER_HOST=unix:///run/user/$(id -u)/docker.sock
docker version --format '{{.Server.Version}}'; docker info --format '{{.SecurityOptions}}'`, 2*time.Minute)
	expect(t, strings.Contains(ver, "rootless"), "not a rootless daemon: %s", ver)
	t.Logf("rootless Docker %s", strings.Fields(ver)[0])
	mustVMAs(t, v, "alice", `mkdir -p ~/dc && mv ~/compose.yaml ~/dc/ && export XDG_RUNTIME_DIR=/run/user/$(id -u) DOCKER_HOST=unix:///run/user/$(id -u)/docker.sock
docker load -i ~/drool.oci.tar`, 20*time.Minute)
	defer vmAs(v, "alice", dock+"docker compose down -v", nil, 5*time.Minute)
	// The README's commands, as they stand.
	mustVMAs(t, v, "alice", dock+"docker compose up -d", 10*time.Minute)
	c := runtimeController(t, v, r, func() string {
		out, _ := vmAs(v, "alice", dock+"docker compose logs --no-log-prefix drool", nil, time.Minute)
		return out
	}, func() string {
		out, _ := vmAs(v, "alice", dock+"docker compose ps -a; docker images; docker compose logs --no-log-prefix drool 2>&1 | tail -20", nil, time.Minute)
		return out
	})
	insp := mustVMAs(t, v, "alice", dock+`docker inspect -f '{{.Config.User}} {{.HostConfig.ReadonlyRootfs}} {{.HostConfig.CapDrop}} {{.HostConfig.Privileged}}' $(docker compose ps -q drool)`, time.Minute)
	expect(t, insp == "nonroot true [ALL] false", "rootless Docker container settings: %s", insp)
	runtimeBackupRestore(t, v, c, dock, "docker compose exec -T drool drool backup -o - > drool-backup.tar.gz",
		"docker compose cp drool:/data/master.key ./master.key",
		`docker compose stop drool && chmod 644 master.key && docker compose run --rm -T -v "$PWD/master.key:/tmp/master.key:ro" drool restore --master-key /tmp/master.key - < drool-backup.tar.gz && chmod 600 master.key && docker compose start drool`,
		func() bool {
			out, _ := vmAs(v, "alice", dock+`docker inspect -f '{{.State.Health.Status}}' $(docker compose ps -q drool)`, nil, time.Minute)
			return out == "healthy"
		})
}

func testRootlessPodman(t *testing.T, v *vm, r *releaseUnderTest) {
	install := "sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq podman >/dev/null"
	if v.def.Family == "dnf" {
		install = "sudo dnf install -y -q podman >/dev/null"
	}
	mustVMAs(t, v, "deploy", install, 40*time.Minute)
	pod := `export XDG_RUNTIME_DIR=/run/user/$(id -u); cd ~ && `
	ver := mustVMAs(t, v, "alice", pod+"podman version --format '{{.Version}}'; podman info --format '{{.Host.Security.Rootless}}'", 2*time.Minute)
	expect(t, strings.HasSuffix(ver, "true"), "podman isn't rootless here: %s", ver)
	t.Logf("rootless Podman %s", strings.Fields(ver)[0])
	mustVMAs(t, v, "alice", pod+"podman load -i ~/drool.oci.tar", 20*time.Minute)
	// The README's Quadlet steps.
	mustVMAs(t, v, "alice", pod+"mkdir -p ~/.config/containers/systemd && cp ~/drool.container ~/.config/containers/systemd/ && systemctl --user daemon-reload && systemctl --user start drool", 10*time.Minute)
	defer vmAs(v, "alice", pod+"systemctl --user stop drool; podman volume rm -f drool-data", nil, 5*time.Minute)
	c := runtimeController(t, v, r, func() string {
		out, _ := vmAs(v, "alice", pod+"journalctl --user -u drool --no-pager -o cat", nil, time.Minute)
		return out
	}, func() string {
		out, _ := vmAs(v, "alice", pod+"systemctl --user status drool --no-pager -l 2>&1 | head -30; podman ps -a; podman images; podman logs --tail 20 drool 2>&1; "+
			"journalctl --user --no-pager -n 30 2>&1; ls -ld /var/log/journal 2>&1; ss -ltnp 2>/dev/null | grep -E ':(8080|9443) '", nil, 2*time.Minute)
		return out
	})
	insp := mustVMAs(t, v, "alice", pod+`podman inspect -f '{{.Config.User}} {{.HostConfig.ReadonlyRootfs}} {{.HostConfig.CapDrop}} {{.HostConfig.Privileged}}' drool`, time.Minute)
	expect(t, strings.HasPrefix(insp, "nonroot true") && strings.HasSuffix(insp, "false"), "rootless Podman container settings: %s", insp)
	runtimeBackupRestore(t, v, c, pod, "podman exec drool drool backup -o - > drool-backup.tar.gz",
		"podman cp drool:/data/master.key ./master.key",
		`systemctl --user stop drool && chmod 644 master.key && podman run --rm -i --read-only --tmpfs /tmp --cap-drop ALL --security-opt no-new-privileges -v drool-data:/data -v "$PWD/master.key:/tmp/master.key:ro" docker.io/library/drool:`+r.pkgver+` restore --master-key /tmp/master.key - < drool-backup.tar.gz && chmod 600 master.key && systemctl --user start drool`,
		func() bool {
			out, _ := vmAs(v, "alice", pod+"podman healthcheck run drool && echo healthy", nil, time.Minute)
			return strings.HasSuffix(out, "healthy")
		})
}

// runtimeBackupRestore runs the documented backup, key copy and restore
// commands for a runtime and checks what the restore brought back.
func runtimeBackupRestore(t *testing.T, v *vm, c *controller, prefix, backup, keyCopy, restore string, healthy func() bool) {
	t.Helper()
	before := createSecretOn(t, c, "before-backup")
	mustVMAs(t, v, "alice", prefix+backup+" && "+keyCopy+" && ls -l drool-backup.tar.gz master.key", 10*time.Minute)
	after := createSecretOn(t, c, "after-backup")
	out := mustVMAs(t, v, "alice", prefix+restore, 15*time.Minute)
	expect(t, strings.Contains(out, "Restored backup"), "restore on %s:\n%s", v.def.Name, out)
	waitFor(t, 5*time.Minute, "healthy after the restore", healthy)
	waitFor(t, 3*time.Minute, "signing in after the restore", func() bool { return c.login() == nil })
	var secrets struct {
		Items []struct{ ID string } `json:"items"`
	}
	c.must(t, "GET", "/api/v1/secrets", nil, &secrets)
	got := map[string]bool{}
	for _, s := range secrets.Items {
		got[s.ID] = true
	}
	expect(t, got[before] && !got[after], "%s after the restore: the backup's credential %v, the later one %v", v.def.Name, got[before], got[after])
	t.Logf("%s: backup, master key copy and restore as documented", v.def.Name)
}
