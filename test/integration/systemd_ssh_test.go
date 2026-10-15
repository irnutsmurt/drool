//go:build integration

package integration

import (
	"encoding/base64"
	"os/exec"
	"strings"
	"testing"
	"time"

	"drool/internal/discovery"
)

// fakeRootSystemctl answers root with units kept in /var/lib/fake-systemd:
// on a real host root asks systemd through its private socket, and no
// systemd runs in an unprivileged container. Every other account gets the
// real systemctl, which needs a D-Bus system bus this host doesn't have:
// the condition of a DietPi host (systemd as PID 1, no D-Bus) over SSH as a
// non-root account.
const fakeRootSystemctl = `#!/bin/sh
[ "$(id -u)" = 0 ] || exec /usr/bin/systemctl "$@"
D=/var/lib/fake-systemd
verb="" unit="" state=""
while [ $# -gt 0 ]; do
	case "$1" in
	-p) shift 2; continue ;;
	--state=*) state=${1#--state=}; shift; continue ;;
	--) shift; unit=$1; shift; continue ;;
	-*) shift; continue ;;
	*) if [ -z "$verb" ]; then verb=$1; else unit=$1; fi; shift ;;
	esac
done
units() { for f in "$D"/*.active; do basename "$f" .active; done; }
sub() { case $1 in active) echo running ;; failed) echo failed ;; *) echo dead ;; esac; }
show1() {
	a=$(cat "$D/$1.active"); f=$(cat "$D/$1.file"); l=loaded; [ "$f" = masked ] && l=masked
	printf 'Id=%s\nDescription=%s (test unit)\nLoadState=%s\nActiveState=%s\nSubState=%s\nUnitFileState=%s\nCanReload=no\nNRestarts=0\nResult=success\nTransient=no\nActiveEnterTimestampMonotonic=0\nStateChangeTimestampMonotonic=0\n' "$1" "$1" "$l" "$a" "$(sub "$a")" "$f"
}
case "$verb" in
show)
	if [ -z "$unit" ] || [ "$unit" = "*.service" ]; then n=0; for u in $(units); do [ $n = 0 ] || echo; n=1; show1 "$u"; done
	elif [ -f "$D/$unit.active" ]; then show1 "$unit"
	else printf 'Id=%s\nLoadState=not-found\nActiveState=inactive\nSubState=dead\nUnitFileState=\n' "$unit"; fi ;;
list-units)
	for u in $(units); do a=$(cat "$D/$u.active"); case "$state" in running) [ "$a" = active ] || continue ;; failed) [ "$a" = failed ] || continue ;; esac
		echo "$u loaded $a $(sub "$a") $u (test unit)"; done ;;
list-unit-files) for u in $(units); do echo "$u $(cat "$D/$u.file") enabled"; done ;;
start|restart)
	if [ "$(cat "$D/$unit.file")" = masked ]; then echo "Failed to start $unit: Unit $unit is masked." >&2; exit 1; fi
	echo active > "$D/$unit.active" ;;
stop) echo inactive > "$D/$unit.active" ;;
enable) echo enabled > "$D/$unit.file" ;;
disable) echo disabled > "$D/$unit.file" ;;
status) echo "* $unit - $unit (test unit)"; echo "   Active: $(cat "$D/$unit.active")" ;;
*) exit 0 ;;
esac
`

// sdProbeImage is the Debian 12 test image with the systemd package (a real
// systemctl, no systemd running), sshd running as PID 1 under the name
// "systemd", systemd's runtime directory closed to other accounts, and no
// D-Bus: what a DietPi host looks like to DROOL's own SSH account.
func sdProbeImage(t *testing.T) string {
	t.Helper()
	var d12 distro
	for _, d := range allDistros {
		if d.Name == "debian-12" {
			d12 = d
		}
	}
	base := image(t, d12)
	tag := base + "-sdprobe"
	imagesMu.Lock()
	err, done := built[tag]
	imagesMu.Unlock()
	if !done {
		entry := `#!/bin/sh
set -e
ssh-keygen -A >/dev/null
for u in deploy pwuser; do touch /home/$u/.ssh/authorized_keys; chown $u:$u /home/$u/.ssh/authorized_keys; chmod 600 /home/$u/.ssh/authorized_keys; done
mkdir -p /run/systemd/system && chmod 0700 /run/systemd
exec /usr/local/sbin/systemd -D -e
`
		dockerfile := "FROM " + base + "\n" +
			"RUN apt-get update && apt-get install -y --no-install-recommends systemd && (apt-get purge -y dbus dbus-daemon >/dev/null 2>&1 || true) && rm -rf /var/lib/apt/lists/*\n" +
			"RUN cp /usr/sbin/sshd /usr/local/sbin/systemd && mkdir -p /var/lib/fake-systemd && cd /var/lib/fake-systemd && " +
			"echo active > demo.service.active && echo enabled > demo.service.file && " +
			"echo inactive > backup.service.active && echo disabled > backup.service.file && " +
			"echo active > helper.service.active && echo static > helper.service.file && " +
			"echo inactive > hidden.service.active && echo masked > hidden.service.file && " +
			"echo failed > broken.service.active && echo enabled > broken.service.file\n" +
			"RUN echo " + base64.StdEncoding.EncodeToString([]byte(fakeRootSystemctl)) + " | base64 -d > /usr/local/bin/systemctl && chmod 755 /usr/local/bin/systemctl\n" +
			"RUN echo " + base64.StdEncoding.EncodeToString([]byte(entry)) + " | base64 -d > /entrypoint-sd.sh && chmod 755 /entrypoint-sd.sh\n" +
			"CMD [\"/entrypoint-sd.sh\"]\n"
		cmd := exec.Command("docker", "build", "-q", "-t", tag, "-")
		cmd.Stdin = strings.NewReader(dockerfile)
		if out, berr := cmd.CombinedOutput(); berr != nil {
			err = berr
			t.Logf("docker build: %s", out)
		}
		imagesMu.Lock()
		built[tag] = err
		imagesMu.Unlock()
	}
	if err != nil {
		t.Fatalf("build the systemd-probe image: %v", err)
	}
	return tag
}

// TestSystemdOverUnprivilegedSSH (dogfooding 2026-10-14): a DietPi host
// managed over SSH was reported as "systemd is not running" with no service
// information, while its Shepherd (root) saw every service. DietPi runs
// systemd without D-Bus, so systemctl run by a non-root account fails with
// "Failed to connect to bus", which DROOL took for no systemd; and the old
// detection looked into /run/systemd, which an account may not be able to.
// Here the real systemctl fails for deploy exactly that way, /run/systemd
// is closed to it, and PID 1 is systemd: whether systemd runs comes from PID
// 1 now, over SSH as through a Shepherd; services are read as root where
// the host's sign-in allows it; service operations ask systemd with the
// host's sudo and verify the result. Unprivileged containers only.
func TestSystemdOverUnprivilegedSSH(t *testing.T) {
	freshController(t)
	c := startHost(t, sdProbeImage(t), "sdprobe", true, netOuter)
	// The condition is real.
	expect(t, strings.TrimSpace(c.exec(t, "cat /proc/1/comm")) == "systemd", "PID 1: %s", c.exec(t, "cat /proc/1/comm"))
	asDeploy := func(cmd string) string {
		out, _ := docker("exec", "-u", "deploy", c.Name, "sh", "-c", cmd+" 2>&1; echo rc=$?")
		return out
	}
	expect(t, strings.Contains(asDeploy("systemctl list-units --no-pager"), "Failed to connect to bus"), "deploy's systemctl: %s", asDeploy("systemctl list-units --no-pager"))
	expect(t, strings.Contains(asDeploy("[ -d /run/systemd/system ]"), "rc=1"), "the old test passes for deploy: %s", asDeploy("[ -d /run/systemd/system ]"))

	h := addHost(t, "sdprobe", c, hostOpts{})
	st, detail, _ := probe(t, h.ID).step("init system")
	expect(t, st == "ok" && detail == "systemd", "init system: %s %s", st, detail)

	// Discovery over SSH, as deploy: systemd runs, the list is limited.
	ctl.must(t, "POST", "/api/v1/hosts/"+h.ID+"/refresh", nil, nil)
	var facts struct {
		Facts *discovery.Facts `json:"facts"`
	}
	waitFor(t, 2*time.Minute, "facts", func() bool {
		ctl.must(t, "GET", "/api/v1/hosts/"+h.ID+"/facts", nil, &facts)
		return facts.Facts != nil && facts.Facts.Distro != ""
	})
	sf := facts.Facts
	expect(t, sf.SystemdRunning && sf.SystemdState == "running" && sf.InitSystem == "systemd", "SSH facts: running %v state %q init %q", sf.SystemdRunning, sf.SystemdState, sf.InitSystem)
	expect(t, sf.ServicesLimited && strings.Contains(sf.ServicesError, "Failed to connect to bus"), "SSH services: limited %v %q", sf.ServicesLimited, sf.ServicesError)
	for _, w := range sf.Warnings {
		expect(t, !strings.Contains(w, "systemd is not running"), "a false no-systemd warning: %v", sf.Warnings)
	}

	// The Shepherd runs the same script as root (internal/agent/telemetry.go):
	// the same facts.
	asRoot := exec.Command("docker", "exec", "-i", c.Name, "sh", "-s")
	asRoot.Stdin = strings.NewReader(discovery.DiscoverScript)
	rootOut, err := asRoot.Output()
	if err != nil {
		t.Fatalf("discovery as root: %v", err)
	}
	rf, _ := discovery.ParseDiscover(string(rootOut))
	expect(t, rf.SystemdRunning == sf.SystemdRunning && rf.SystemdState == sf.SystemdState && rf.InitSystem == sf.InitSystem,
		"Shepherd (root) facts differ: running %v state %q init %q", rf.SystemdRunning, rf.SystemdState, rf.InitSystem)
	expect(t, !rf.ServicesLimited && len(rf.Services) == 2 && len(rf.FailedUnits) == 1, "root lists the services: %+v failed %v", rf.Services, rf.FailedUnits)

	// Monitoring over SSH: the first poll reads the services as root.
	ctl.must(t, "POST", "/api/v1/hosts/"+h.ID+"/monitoring/refresh", nil, nil)
	var view struct {
		Supported bool   `json:"supported"`
		Note      string `json:"note"`
		Units     []struct {
			Name, State, Expected, Health string
			Actions                       []string `json:"actions"`
			ActionNote                    string   `json:"action_note"`
		} `json:"units"`
	}
	waitFor(t, 2*time.Minute, "services", func() bool {
		ctl.must(t, "GET", "/api/v1/hosts/"+h.ID+"/monitoring/services?all=true", nil, &view)
		return view.Supported
	})
	expect(t, strings.Contains(view.Note, "Read as root") && strings.Contains(view.Note, "Failed to connect to bus"), "note: %s", view.Note)
	byName := map[string]int{}
	for i, u := range view.Units {
		byName[u.Name] = i
	}
	for _, n := range []string{"demo.service", "backup.service", "helper.service", "hidden.service", "broken.service"} {
		_, ok := byName[n]
		expect(t, ok, "%s missing from %+v", n, view.Units)
	}
	if i, ok := byName["helper.service"]; ok {
		u := view.Units[i]
		expect(t, strings.Join(u.Actions, ",") == "stop,restart" && strings.Contains(u.ActionNote, "Static"), "static unit actions: %+v", u)
	}
	if i, ok := byName["hidden.service"]; ok {
		u := view.Units[i]
		expect(t, len(u.Actions) == 0 && strings.Contains(u.ActionNote, "Masked"), "masked unit actions: %+v", u)
	}

	// Service operations over SSH: systemctl through sudo, the result read
	// back the same way (as deploy, systemctl couldn't even read it).
	svc := func(name string, fields map[string]any) jobHost {
		step := map[string]any{"type": "service", "name": name}
		for k, v := range fields {
			step[k] = v
		}
		return run(t, h.ID, step)
	}
	jh := svc("demo.service", map[string]any{"state": "stopped", "enabled": false})
	expect(t, jh.State == "succeeded", "disable & stop: %v", jh)
	if len(jh.Steps) > 0 {
		after, _ := jh.Steps[0].Result["after"].(map[string]any)
		expect(t, after["active"] == "inactive" && after["unit_file"] == "disabled", "verified after disable & stop: %v", jh.Steps[0].Result)
	}
	expect(t, strings.TrimSpace(c.exec(t, "cat /var/lib/fake-systemd/demo.service.active /var/lib/fake-systemd/demo.service.file")) == "inactive\ndisabled", "on the host")
	jh = svc("backup.service", map[string]any{"state": "running", "enabled": true})
	expect(t, jh.State == "succeeded", "enable & start: %v", jh)
	jh = svc("helper.service", map[string]any{"enabled": true})
	expect(t, jh.State == "failed" && strings.Contains(jh.ErrorMessage, "cannot be enabled (unit file state: static)"), "enable a static unit: %v", jh)
	jh = svc("hidden.service", map[string]any{"state": "running"})
	expect(t, jh.State == "failed" && strings.Contains(jh.ErrorMessage, "is masked"), "start a masked unit: %v", jh)
}
