package api

import (
	"bufio"
	"fmt"

	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"drool/internal/auth"
	"drool/internal/sshx/sshtest"
)

// readyHost registers an sshtest server as a ready host that executes
// commands locally, and returns its id.
func readyHost(t *testing.T, h *harness, c *client, name string) string {
	t.Helper()
	id, _ := readyHostServer(t, h, c, name)
	return id
}

// readyHostServer is readyHost that also returns the server, so a test can
// decide what the host has (SetExec) instead of inheriting it from the
// machine running the test.
func readyHostServer(t *testing.T, h *harness, c *client, name string) (string, *sshtest.Server) {
	t.Helper()
	var ck controllerKeyResponse
	c.do("GET", "/api/v1/secrets/controller-ssh-key", nil).json(t, &ck)
	key := sshtest.NewEd25519Signer(t)
	srv := sshtest.Start(t, key)
	srv.SetExec(sshtest.ExecLocal)
	authorizeCred(t, srv, ck.PublicKey)
	addr, port := srv.Host()
	var host hostJSON
	c.do("POST", "/api/v1/hosts", map[string]any{"name": name, "address": addr, "port": port, "user": "deploy"}).json(t, &host)
	c.do("POST", "/api/v1/hosts/"+host.ID+"/hostkeys/scan", nil)
	if r := c.do("POST", "/api/v1/hosts/"+host.ID+"/hostkeys/approve", map[string]string{"fingerprint": ssh.FingerprintSHA256(key.PublicKey())}); r.status != 200 {
		t.Fatalf("approve: %s", r.body)
	}
	return host.ID, srv
}

func waitJob(t *testing.T, c *client, id string) jobJSON {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		var j jobJSON
		c.do("GET", "/api/v1/jobs/"+id, nil).json(t, &j)
		if j.Status == "completed" || j.Status == "cancelled" {
			return j
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s stuck in %s", id, j.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestJobRunsCommandsOverSSH(t *testing.T) {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	c := h.login("admin")
	a := readyHost(t, h, c, "alpha")
	b := readyHost(t, h, c, "beta")

	var preview previewResponse
	c.do("POST", "/api/v1/jobs/preview", map[string]any{"targets": map[string]any{"hosts": []string{a, b}}}).json(t, &preview)
	if len(preview.Hosts) != 2 {
		t.Fatalf("preview %+v", preview)
	}

	spec := map[string]any{
		"steps": []map[string]any{{"type": "script", "interpreter": "sh",
			"body": "echo \"hello from $(basename \"$PWD\" >/dev/null; echo script)\"\necho oops >&2\nexit 3"}},
		"targets": map[string]any{"hosts": []string{a, b}},
	}
	r := c.do("POST", "/api/v1/jobs", spec)
	if r.status != 201 {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	var job jobJSON
	r.json(t, &job)
	job = waitJob(t, c, job.ID)
	if job.Counts["failed"] != 2 {
		t.Fatalf("counts %v", job.Counts)
	}
	var hosts listResponse[jobHostJSON]
	c.do("GET", "/api/v1/jobs/"+job.ID+"/hosts", nil).json(t, &hosts)
	for _, jh := range hosts.Items {
		if jh.ExitCode == nil || *jh.ExitCode != 3 || jh.ErrorClass != "ExecutionError" {
			t.Fatalf("host %+v", jh)
		}
	}
	var out outputResponse
	c.do("GET", "/api/v1/jobs/"+job.ID+"/hosts/"+a+"/output", nil).json(t, &out)
	var stdout, stderr string
	for _, s := range out.Segments {
		if s.Stream == "stdout" {
			stdout += s.Text
		} else {
			stderr += s.Text
		}
	}
	if stdout != "hello from script\n" || stderr != "oops\n" {
		t.Fatalf("stdout %q stderr %q", stdout, stderr)
	}

	// A successful command, run again with the same Idempotency-Key, is not duplicated.
	ok := map[string]any{"steps": []map[string]any{{"type": "command", "command": "true"}},
		"targets": map[string]any{"hosts": []string{a}}}
	r1 := c.doHeader("POST", "/api/v1/jobs", ok, "Idempotency-Key", "k-1")
	r2 := c.doHeader("POST", "/api/v1/jobs", ok, "Idempotency-Key", "k-1")
	var j1, j2 jobJSON
	r1.json(t, &j1)
	r2.json(t, &j2)
	if r1.status != 201 || r2.status != 200 || j1.ID != j2.ID {
		t.Fatalf("idempotency: %d %d %s %s", r1.status, r2.status, j1.ID, j2.ID)
	}
	if j := waitJob(t, c, j1.ID); j.Counts["succeeded"] != 1 {
		t.Fatalf("true: %v", j.Counts)
	}

	// Re-run failed hosts creates a new job for just those hosts.
	var rerun jobJSON
	c.do("POST", "/api/v1/jobs/"+job.ID+"/rerun", map[string]any{"hosts": "failed"}).json(t, &rerun)
	if rerun.HostCount != 2 || rerun.RerunOf == nil || !strings.HasPrefix(rerun.Name, "Re-run: ") {
		t.Fatalf("rerun %+v", rerun)
	}
	waitJob(t, c, rerun.ID)
}

func TestJobCancelKillsRemoteProcess(t *testing.T) {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	c := h.login("admin")
	a := readyHost(t, h, c, "alpha")

	// A unique sleep duration identifies the remote process; pgrep's pattern is
	// anchored so it can't match the shell that runs pgrep itself.
	secs := 3000 + time.Now().Nanosecond()%5000
	marker := fmt.Sprintf("^sleep %d$", secs)
	var job jobJSON
	c.do("POST", "/api/v1/jobs", map[string]any{
		"steps":   []map[string]any{{"type": "command", "command": fmt.Sprintf("sleep %d", secs)}},
		"targets": map[string]any{"hosts": []string{a}},
	}).json(t, &job)

	// Watch the live event stream until the host is running.
	req, _ := http.NewRequest("GET", h.ts.URL+"/api/v1/jobs/"+job.ID+"/events", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: c.cookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	sc := bufio.NewScanner(resp.Body)
	running := false
	for !running && sc.Scan() {
		running = strings.Contains(sc.Text(), `"state":"running"`)
	}
	if !running {
		t.Fatal("never saw the host running")
	}
	// "running" is published when the remote session starts; the process
	// appears a moment later.
	for i := 0; !processExists(marker); i++ {
		if i > 100 {
			t.Fatal("remote process not found")
		}
		time.Sleep(20 * time.Millisecond)
	}
	start := time.Now()
	if r := c.do("POST", "/api/v1/jobs/"+job.ID+"/cancel", nil); r.status != 202 {
		t.Fatalf("cancel: %d %s", r.status, r.body)
	}
	j := waitJob(t, c, job.ID)
	if j.Status != "cancelled" || j.Counts["cancelled"] != 1 {
		t.Fatalf("after cancel: %+v", j)
	}
	if time.Since(start) > 15*time.Second {
		t.Fatalf("cancel took %s", time.Since(start))
	}
	deadline := time.Now().Add(5 * time.Second)
	for processExists(marker) {
		if time.Now().After(deadline) {
			t.Fatal("remote process survived cancellation")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// The event stream ends after the job does.
	sawEnd := false
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "event: end") {
			sawEnd = true
		}
	}
	if !sawEnd {
		t.Fatal("event stream did not send end")
	}
}

func processExists(marker string) bool {
	out, _, _ := sshtest.ExecLocal("pgrep -f '"+marker+"' >/dev/null && echo yes", nil)
	return strings.TrimSpace(out) == "yes"
}
