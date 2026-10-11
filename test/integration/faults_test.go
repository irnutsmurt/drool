//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"
)

// TestFaults injects the realistic failures that could leave a host or the
// controller in an ambiguous state, and checks each is reported clearly.
func TestFaults(t *testing.T) {
	freshController(t) // a controller of its own: nothing from other tests (M17)
	d := selected()[0]
	c := startHost(t, image(t, d), "faults", true, netOuter)
	h := addHost(t, "faults", c, hostOpts{})

	t.Run("connection fails before the operation starts", func(t *testing.T) {
		mustDocker(t, "stop", "-t", "1", c.Name)
		jh := run(t, h.ID, cmd("touch /tmp/ran", false))
		mustDocker(t, "start", c.Name)
		waitFor(t, 30*time.Second, "sshd back", func() bool { return probe(t, h.ID).OK || getHost(t, h.ID).Status != "unreachable" })
		expect(t, jh.State == "failed" && jh.ErrorClass == "ConnectionError", "%v", jh)
		expect(t, strings.Contains(jh.ErrorMessage, "refused"), "message should say what happened: %v", jh)
		_, err := docker("exec", c.Name, "test", "-e", "/tmp/ran")
		expect(t, err != nil, "command ran on a host that was down")
	})

	t.Run("connection dies during the operation", func(t *testing.T) {
		id := submit(t, []string{h.ID}, cmd("echo started; sleep 30; echo finished", false))
		waitFor(t, time.Minute, "command running", func() bool {
			_, err := docker("exec", c.Name, "pgrep", "-x", "sleep")
			return err == nil
		})
		mustDocker(t, "kill", c.Name) // the host vanishes mid-command
		waitJob(t, id, 3*time.Minute)
		jh := getJobHost(t, id, h.ID)
		mustDocker(t, "start", c.Name)
		expect(t, jh.State == "failed" && jh.ErrorClass == "ConnectionError", "%v", jh)
		expect(t, strings.Contains(strings.ToLower(jh.ErrorMessage+" "+jh.ErrorHint), "unknown") ||
			strings.Contains(strings.ToLower(jh.ErrorMessage+" "+jh.ErrorHint), "may have"),
			"a lost connection after the command started must say the outcome is unknown: %v", jh)
		expect(t, len(jh.Steps) == 1 && jh.Steps[0].Attempt == 1, "a started command must not be retried: %+v", jh.Steps)
		waitFor(t, time.Minute, "host back", func() bool { return probe(t, h.ID).OK || getHost(t, h.ID).Status == "unsupported" })
	})

	t.Run("cancellation kills root processes", func(t *testing.T) {
		id := submit(t, []string{h.ID}, cmd("sleep 300", true))
		waitFor(t, time.Minute, "sleep running", func() bool {
			_, err := docker("exec", c.Name, "pgrep", "-x", "sleep")
			return err == nil
		})
		ctl.must(t, "POST", "/api/v1/jobs/"+id+"/cancel", nil, nil)
		expect(t, waitJob(t, id, time.Minute) == "cancelled", "job not cancelled")
		jh := getJobHost(t, id, h.ID)
		expect(t, jh.State == "cancelled" && jh.ErrorClass == "CancelledError", "%v", jh)
		waitFor(t, 20*time.Second, "root sleep killed", func() bool {
			_, err := docker("exec", c.Name, "pgrep", "-x", "sleep")
			return err != nil
		})
	})

	t.Run("output beyond the cap", func(t *testing.T) {
		id := submit(t, []string{h.ID}, cmd("yes drool-output-line | head -c 3000000; echo done", false))
		waitJob(t, id, 2*time.Minute)
		jh := getJobHost(t, id, h.ID)
		out := output(t, id, h.ID)
		expect(t, jh.State == "succeeded", "%v", jh)
		expect(t, len(jh.Steps) == 1 && jh.Steps[0].Truncated, "truncated flag not set")
		expect(t, len(out) <= 1100000 && strings.Contains(out, "output limit"), "stored %d bytes; marker present: %v", len(out), strings.Contains(out, "output limit"))
	})

	t.Run("controller crashes during a job", func(t *testing.T) {
		id := submit(t, []string{h.ID}, cmd("sleep 15; touch /tmp/finished-after-crash", false))
		waitFor(t, time.Minute, "command running", func() bool {
			_, err := docker("exec", c.Name, "pgrep", "-x", "sleep")
			return err == nil
		})
		ctl.crash()
		if err := ctl.start(); err != nil {
			t.Fatal(err)
		}
		if err := ctl.login(); err != nil {
			t.Fatal(err)
		}
		expect(t, waitJob(t, id, time.Minute) == "interrupted", "job not marked interrupted")
		jh := getJobHost(t, id, h.ID)
		expect(t, jh.State == "interrupted" && strings.Contains(strings.ToLower(jh.ErrorMessage+jh.ErrorHint), "unknown"), "%v", jh)
		// Documented behavior: the remote command is not killed by a controller
		// crash, which is exactly why the outcome is reported as unknown.
		waitFor(t, 30*time.Second, "orphaned command to finish", func() bool {
			_, err := docker("exec", c.Name, "test", "-e", "/tmp/finished-after-crash")
			return err == nil
		})
	})

	t.Run("authentication failure", func(t *testing.T) {
		var k struct {
			ID string `json:"id"`
		}
		ctl.must(t, "POST", "/api/v1/secrets", map[string]any{"name": "unauthorized-key", "kind": "ssh_private_key", "generate": true}, &k)
		ha := addHost(t, "faults-badkey", c, hostOpts{CredentialID: k.ID})
		expect(t, ha.Status == "auth_failed", "status %q (%s)", ha.Status, ha.Detail)
		jh := run(t, ha.ID, cmd("true", false))
		expect(t, jh.State == "failed" && jh.ErrorClass == "AuthenticationError", "%v", jh)
	})

	// Last: replaces the container.
	t.Run("host key changes", func(t *testing.T) {
		c.recreate(t)
		p := probe(t, h.ID)
		st, _, class := p.step("host key")
		expect(t, st == "failed" && class == "HostKeyError", "probe after key change: %s %s", st, class)
		expect(t, getHost(t, h.ID).Status == "hostkey_mismatch", "status %q", getHost(t, h.ID).Status)
		jh := run(t, h.ID, cmd("touch /tmp/ran-with-new-key", false))
		expect(t, jh.State == "failed" && jh.ErrorClass == "HostKeyError", "job after key change: %v", jh)
		_, err := docker("exec", c.Name, "test", "-e", "/tmp/ran-with-new-key")
		expect(t, err != nil, "command ran on a host with an unapproved key")

		// Approving the new key (after checking it out of band) restores service.
		ctl.must(t, "POST", "/api/v1/hosts/"+h.ID+"/hostkeys/scan", nil, nil)
		ctl.must(t, "POST", "/api/v1/hosts/"+h.ID+"/hostkeys/approve", map[string]string{"fingerprint": c.fingerprint(t)}, nil)
		jh = run(t, h.ID, cmd("true", false))
		expect(t, jh.State == "succeeded", "after approving the new key: %v", jh)
	})
}
