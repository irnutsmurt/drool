//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"
)

type hostDetail struct {
	host
	Facts *struct {
		OSFamily   string `json:"os_family"`
		Distro     string `json:"distro"`
		PkgManager string `json:"pkg_manager"`
		InitSystem string `json:"init_system"`
	} `json:"facts"`
	Health *struct {
		Reachable        bool `json:"reachable"`
		UpdatesAvailable *int `json:"updates_available"`
	} `json:"health"`
}

func getHostDetail(t *testing.T, id string) hostDetail {
	t.Helper()
	var h hostDetail
	ctl.must(t, "GET", "/api/v1/hosts/"+id, nil, &h)
	return h
}

// TestMatrix runs the core workflow on every distribution: connection test,
// discovery, commands, sudo (passwordless, password, wrong password), and
// package operations including their error paths.
func TestMatrix(t *testing.T) {
	for _, d := range selected() {
		t.Run(d.Name, func(t *testing.T) {
			t.Parallel()
			img := image(t, d)
			c := startHost(t, img, d.Name, true, netOuter)
			h := addHost(t, "m-"+d.Name, c, hostOpts{})

			t.Run("connection test", func(t *testing.T) {
				p := probe(t, h.ID)
				for _, s := range []string{"reachable", "host key", "authentication", "privilege escalation", "operating system"} {
					st, detail, _ := p.step(s)
					expect(t, st == "ok", "%s: %s %s", s, st, detail)
				}
				// Containers have no systemd: usable, flagged, and explained.
				st, detail, class := p.step("init system")
				expect(t, st == "failed" && class == "ConfigurationError" && strings.Contains(detail, "systemd"), "init system: %s %s %s", st, class, detail)
			})

			t.Run("discovery", func(t *testing.T) {
				var hd hostDetail
				waitFor(t, 4*time.Minute, "facts for "+d.Name, func() bool {
					hd = getHostDetail(t, h.ID)
					return hd.Facts != nil && hd.Health != nil
				})
				expect(t, hd.Facts.OSFamily == d.OSFamily, "os_family %q, want %q", hd.Facts.OSFamily, d.OSFamily)
				expect(t, hd.Facts.Distro == d.ID, "distro %q, want %q", hd.Facts.Distro, d.ID)
				expect(t, hd.Facts.PkgManager == d.PkgManager, "pkg_manager %q, want %q", hd.Facts.PkgManager, d.PkgManager)
				expect(t, hd.Health.Reachable, "not reachable")
			})

			t.Run("commands", func(t *testing.T) {
				id := submit(t, []string{h.ID}, cmd("id -un; echo to-stderr >&2", false))
				waitJob(t, id, time.Minute)
				jh := getJobHost(t, id, h.ID)
				out := output(t, id, h.ID)
				expect(t, jh.State == "succeeded" && strings.Contains(out, "deploy") && strings.Contains(out, "to-stderr"), "%v %q", jh, out)

				id = submit(t, []string{h.ID}, cmd("id -u", true))
				waitJob(t, id, time.Minute)
				expect(t, strings.TrimSpace(output(t, id, h.ID)) == "0", "become: %q", output(t, id, h.ID))

				id = submit(t, []string{h.ID}, map[string]any{"type": "script", "interpreter": "sh",
					"body": "#!/bin/sh\nset -e\n. /etc/os-release\necho \"ID=$ID\"\nexit 3\n"})
				waitJob(t, id, time.Minute)
				jh = getJobHost(t, id, h.ID)
				expect(t, jh.State == "failed" && deref(jh.ExitCode) == 3 && jh.ErrorClass == "ExecutionError" &&
					strings.Contains(output(t, id, h.ID), "ID="+d.ID), "script exit: %v", jh)
			})

			t.Run("password sudo", func(t *testing.T) {
				good := createSecret(t, "sudo-"+d.Name, "sudo_password", sudoPassword)
				bad := createSecret(t, "badsudo-"+d.Name, "sudo_password", "wrong-password")
				hp := addHost(t, "pw-"+d.Name, c, hostOpts{User: "pwuser", BecomeCredID: good})
				st, detail, _ := probe(t, hp.ID).step("privilege escalation")
				expect(t, st == "ok" && strings.Contains(detail, "password"), "privilege escalation: %s %s", st, detail)
				jh := run(t, hp.ID, cmd("id -u", true))
				expect(t, jh.State == "succeeded", "sudo with password: %v", jh)
				jh = run(t, hp.ID, map[string]any{"type": "package", "action": "install", "names": []string{"tree"}})
				expect(t, jh.State == "succeeded", "typed op with password sudo: %v", jh)

				hb := addHost(t, "badpw-"+d.Name, c, hostOpts{User: "pwuser", BecomeCredID: bad})
				expect(t, hb.Status == "auth_failed", "wrong sudo password status %q (%s)", hb.Status, hb.Detail)
				jh = run(t, hb.ID, cmd("id -u", true))
				expect(t, jh.State == "failed" && jh.ErrorClass == "AuthorizationError", "wrong sudo password job: %v", jh)
				expect(t, !strings.Contains(jh.ErrorMessage+jh.ErrorHint, "wrong-password"), "password leaked in error: %v", jh)
			})

			t.Run("packages", func(t *testing.T) {
				pkg := func(action string, names []string, allow bool) jobHost {
					return run(t, h.ID, map[string]any{"type": "package", "action": action, "names": names, "allow_removals": allow})
				}
				// No package metadata at all, as on a freshly installed host:
				// DROOL refreshes it once instead of reporting "not found".
				if d.Family == "apt" {
					c.exec(t, "rm -rf /var/lib/apt/lists/* && mkdir -p /var/lib/apt/lists/partial")
				} else {
					c.exec(t, "dnf -q clean all")
				}
				jh := pkg("install", []string{"tree"}, false)
				expect(t, jh.State == "succeeded", "install with an empty metadata cache: %v", jh)
				jh = pkg("install", []string{"tree"}, false)
				expect(t, jh.State == "succeeded" && jh.changed() != nil && !*jh.changed(), "reinstall should be unchanged: %v", jh)

				jh = pkg("install", []string{"drool-no-such-package"}, false)
				expect(t, jh.State == "failed" && jh.ErrorClass == "ValidationError" && strings.Contains(jh.ErrorMessage, "not found"), "not found: %v", jh)

				jh = pkg("upgrade", []string{"nano-not-installed-xyz"}, false)
				expect(t, jh.State == "failed", "upgrade of a missing package: %v", jh)

				jh = pkg("install", []string{d.Pkg}, false)
				expect(t, jh.State == "succeeded", "install %s: %v", d.Pkg, jh)
				jh = pkg("remove", []string{d.Dep}, false)
				expect(t, jh.State == "failed" && jh.ErrorClass == "ValidationError" && strings.Contains(jh.ErrorMessage, d.Pkg),
					"removing %s must require approval because it removes %s: %v", d.Dep, d.Pkg, jh)
				jh = pkg("remove", []string{d.Dep}, true)
				expect(t, jh.State == "succeeded", "approved removal: %v", jh)

				jh = pkg("remove", []string{"tree"}, false)
				expect(t, jh.State == "succeeded" && jh.changed() != nil && *jh.changed(), "remove: %v", jh)
			})

			t.Run("system update", func(t *testing.T) {
				var sim struct {
					Hosts []struct {
						Error *struct {
							Message string `json:"message"`
						} `json:"error"`
						PackageManager string `json:"package_manager"`
					} `json:"hosts"`
				}
				ctl.must(t, "POST", "/api/v1/jobs/simulate", map[string]any{"targets": map[string]any{"hosts": []string{h.ID}},
					"step": map[string]any{"type": "system_update", "mode": "full_upgrade"}}, &sim)
				expect(t, len(sim.Hosts) == 1 && sim.Hosts[0].Error == nil && sim.Hosts[0].PackageManager == d.PkgManager, "simulate: %+v", sim)

				jh := run(t, h.ID, map[string]any{"type": "system_update", "mode": "security"})
				if d.Family == "apt" {
					expect(t, jh.State == "failed" && jh.ErrorClass == "ValidationError", "security-only on APT must be refused: %v", jh)
				} else {
					expect(t, jh.State == "succeeded", "security-only on DNF: %v", jh)
				}
				jh = run(t, h.ID, map[string]any{"type": "system_update", "mode": "upgrade"})
				expect(t, jh.State == "succeeded", "system update: %v", jh)
				jh = run(t, h.ID, map[string]any{"type": "system_update", "mode": "upgrade", "refresh": false})
				expect(t, jh.State == "succeeded" && jh.changed() != nil && !*jh.changed(), "second system update should be unchanged: %v", jh)
			})

			if d.Family == "apt" {
				t.Run("dpkg lock and interrupted dpkg", func(t *testing.T) {
					// Another process holds the dpkg frontend lock for 20 s: apt waits and succeeds.
					c.exec(t, `(perl -MFcntl -e 'open(my $f, ">>", "/var/lib/dpkg/lock-frontend") or die; `+
						`my $s = pack("ssx4qqix4", F_WRLCK, 0, 0, 0, 0); fcntl($f, F_SETLKW, $s) or die; sleep 20' >/dev/null 2>&1 &); sleep 1`)
					start := time.Now()
					jh := run(t, h.ID, map[string]any{"type": "package", "action": "install", "names": []string{"tree"}})
					expect(t, jh.State == "succeeded" && time.Since(start) > 15*time.Second, "install under lock: %v after %s", jh, time.Since(start))

					// An interrupted dpkg run is reported with the manual fix, not repaired.
					c.exec(t, `touch /var/lib/dpkg/updates/0001`)
					jh = run(t, h.ID, map[string]any{"type": "package", "action": "install", "names": []string{"cowsay"}})
					expect(t, jh.State == "failed" && jh.ErrorClass == "PackageManagerError" && strings.Contains(jh.ErrorHint, "dpkg --configure -a"),
						"interrupted dpkg: %v", jh)
					_, err := docker("exec", c.Name, "test", "-e", "/var/lib/dpkg/updates/0001")
					expect(t, err == nil, "DROOL must not repair dpkg by itself")
					c.exec(t, `dpkg --configure -a`)
				})
			}

			t.Run("service and reboot without systemd", func(t *testing.T) {
				jh := run(t, h.ID, map[string]any{"type": "service", "name": "ssh", "state": "restarted"})
				expect(t, jh.State == "failed" && jh.ErrorClass == "ConfigurationError" && strings.Contains(jh.ErrorMessage, "systemd"), "service: %v", jh)
				jh = run(t, h.ID, map[string]any{"type": "reboot", "timeout_s": 15})
				expect(t, jh.State == "timed_out" && strings.Contains(jh.ErrorMessage, "never went away") &&
					jh.changed() != nil && !*jh.changed(), "reboot that cannot happen: %v", jh)
			})
		})
	}
}

// TestJumpHost covers hosts reached through a jump host: key approval via
// the jump, discovery, a typed operation, cancellation, and a jump host
// whose key changes.
func TestJumpHost(t *testing.T) {
	ds := selected()
	jumpD, targetD := ds[0], ds[len(ds)-1]
	jump := startHost(t, image(t, jumpD), "jump", true, netOuter, netInner)
	target := startHost(t, image(t, targetD), "behind-jump", false, netInner)

	j := addHost(t, "jump", jump, hostOpts{JumpOnly: true})
	expect(t, j.Status == "ready", "jump host status %q (%s)", j.Status, j.Detail)
	h := addHost(t, "behind-jump", target, hostOpts{Via: j.ID})
	p := probe(t, h.ID)
	st, detail, _ := p.step("authentication")
	expect(t, st == "ok", "through jump: %s %s", st, detail)

	waitFor(t, 2*time.Minute, "facts through jump", func() bool { return getHostDetail(t, h.ID).Facts != nil })

	jh := run(t, h.ID, map[string]any{"type": "package", "action": "install", "names": []string{"tree"}})
	expect(t, jh.State == "succeeded", "package via jump: %v", jh)

	// Cancellation reaches the remote process through the jump host.
	id := submit(t, []string{h.ID}, cmd("sleep 300", false))
	waitFor(t, time.Minute, "sleep running", func() bool {
		_, err := docker("exec", target.Name, "pgrep", "-x", "sleep")
		return err == nil
	})
	ctl.must(t, "POST", "/api/v1/jobs/"+id+"/cancel", nil, nil)
	waitJob(t, id, time.Minute)
	expect(t, getJobHost(t, id, h.ID).State == "cancelled", "cancel via jump: %v", getJobHost(t, id, h.ID))
	waitFor(t, 20*time.Second, "remote sleep killed", func() bool {
		_, err := docker("exec", target.Name, "pgrep", "-x", "sleep")
		return err != nil
	})

	// The jump host is replaced: the target fails with a host-key error that
	// names the jump host, and nothing runs.
	jump.recreate(t)
	jh = run(t, h.ID, cmd("touch /tmp/should-not-run", false))
	expect(t, jh.State == "failed" && jh.ErrorClass == "HostKeyError", "changed jump key: %v", jh)
	_, err := docker("exec", target.Name, "test", "-e", "/tmp/should-not-run")
	expect(t, err != nil, "command ran despite the changed jump host key")
}
