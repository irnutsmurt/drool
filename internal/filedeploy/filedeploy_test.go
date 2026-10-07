package filedeploy_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"drool/internal/artifacts"
	"drool/internal/audit"
	"drool/internal/driver"
	"drool/internal/driver/sshdriver"
	"drool/internal/filedeploy"
	"drool/internal/files"
	"drool/internal/logging"
	"drool/internal/model"
	"drool/internal/sshx"
	"drool/internal/sshx/sshtest"
	"drool/internal/store"
)

type env struct {
	t     *testing.T
	st    *store.Store
	arts  *artifacts.Store
	rec   *filedeploy.StoreRecorder
	fsys  files.FS
	dir   string
	owner string
	group string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	data := t.TempDir()
	st, err := store.Open(filepath.Join(data, store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO users (id, username, password_hash, role, created_at, updated_at, password_changed_at) VALUES ('usr_1','alice','x','admin',0,0,0)`,
		`INSERT INTO hosts (id, name, connect_address, ssh_user, created_at, updated_at) VALUES ('h1','web1','x','x',0,0)`,
	} {
		if _, err := st.Writer.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	arts, err := artifacts.Open(data, st)
	if err != nil {
		t.Fatal(err)
	}
	hostKey := sshtest.NewEd25519Signer(t)
	srv := sshtest.Start(t, hostKey)
	client := sshtest.NewEd25519Signer(t)
	srv.Authorize(client.PublicKey())
	srv.SetExec(sshtest.ExecLocal)
	h, p := srv.Host()
	pool := sshx.NewPool(&sshx.Dialer{DialTimeout: time.Second})
	t.Cleanup(pool.Close)
	tg := &sshx.Target{HostID: "h1", Name: "web1", Address: h, Port: p, User: "deploy", Signer: client, PinnedKey: hostKey.PublicKey()}
	fsys, err := sshdriver.OpenFS(ctx, pool, tg, sshdriver.Become{Method: "none"}, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fsys.Close() })
	u, _ := user.Current()
	g, _ := user.LookupGroupId(u.Gid)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	return &env{t: t, st: st, arts: arts, fsys: fsys, dir: dir, owner: u.Username, group: g.Name,
		rec: &filedeploy.StoreRecorder{Store: st, Audit: audit.NewWriter(st, logging.Discard().Audit)}}
}

func (e *env) put(content string) string {
	e.t.Helper()
	a, err := e.arts.Put(context.Background(), strings.NewReader(content), "app.conf", "usr_1")
	if err != nil {
		e.t.Fatal(err)
	}
	return a.SHA256
}

func (e *env) op(path, artifact string) *model.FileDeployOp {
	op := &model.FileDeployOp{Path: path, Artifact: artifact}
	o := model.Operation{Type: model.OpFileDeploy, FileDeploy: op}
	if err := o.Normalize(); err != nil {
		e.t.Fatal(err)
	}
	return op
}

func (e *env) run(op *model.FileDeployOp, svc filedeploy.ServiceFunc) (*filedeploy.Result, string, error) {
	var out bytes.Buffer
	res, err := filedeploy.Run(context.Background(), filedeploy.Deps{FS: e.fsys, Source: e.arts, Recorder: e.rec, Service: svc,
		Notes: &out, Output: &out, Step: driver.StepInfo{HostID: "h1", UserID: "usr_1"}}, op)
	return res, out.String(), err
}

func (e *env) read(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func TestDeployLifecycle(t *testing.T) {
	e := newEnv(t)
	p := filepath.Join(e.dir, "app.conf")
	v1, v2 := e.put("port=80\n"), e.put("port=8080\n")

	// Create.
	res, _, err := e.run(e.op(p, v1), nil)
	if err != nil || res.Outcome != filedeploy.Installed || !res.Changed || res.Before.Exists || e.read(p) != "port=80\n" {
		t.Fatalf("create: %+v %v", res, err)
	}
	// Deploying the same content again is a successful no-op.
	res, notes, err := e.run(e.op(p, v1), nil)
	if err != nil || res.Outcome != filedeploy.Unchanged || res.Changed || !strings.Contains(notes, "already has this content") {
		t.Fatalf("no-op: %+v %v", res, err)
	}

	// Replace, keeping the previous version for rollback.
	os.Chmod(p, 0o640)
	res, _, err = e.run(e.op(p, v2), nil)
	if err != nil || res.Outcome != filedeploy.Installed || !res.Before.Captured || res.Before.SHA256 != v1 || res.After.Mode != "0640" {
		t.Fatalf("replace: %+v %v", res, err)
	}
	deployID := res.DeploymentID

	// Roll back: content and metadata come back.
	row, err := e.st.R.GetFileDeployment(context.Background(), deployID)
	if err != nil || row.BeforeArtifact.String != v1 || row.AfterArtifact.String != v2 {
		t.Fatalf("history row: %+v %v", row, err)
	}
	rb := e.op(p, v1)
	rb.RollbackOf, rb.ExpectCurrent, rb.Mode = deployID, v2, "0640"
	res, _, err = e.run(rb, nil)
	if err != nil || res.Outcome != filedeploy.Installed || e.read(p) != "port=80\n" {
		t.Fatalf("rollback: %+v %v", res, err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o640 {
		t.Fatalf("rollback mode %v", fi.Mode())
	}

	// A rollback refuses when the file changed on the host meanwhile.
	os.WriteFile(p, []byte("hand edited\n"), 0o640)
	rb2 := e.op(p, v2)
	rb2.RollbackOf, rb2.ExpectCurrent = deployID, v1
	if _, _, err := e.run(rb2, nil); err == nil || e.read(p) != "hand edited\n" {
		t.Fatalf("rollback over a hand edit: %v %q", err, e.read(p))
	}

	// Audit: hashes and metadata, never content.
	var details string
	e.st.Reader.QueryRow(`SELECT group_concat(details_json, ' ') FROM audit_events WHERE action LIKE 'file.%'`).Scan(&details)
	if !strings.Contains(details, v1) || !strings.Contains(details, `"mode":"0640"`) {
		t.Fatalf("audit lacks hashes/metadata: %s", details)
	}
	for _, secret := range []string{"port=80", "port=8080", "hand edited"} {
		if strings.Contains(details, secret) {
			t.Fatalf("file content in the audit log: %s", details)
		}
	}
	var n int
	e.st.Reader.QueryRow(`SELECT count(*) FROM audit_events WHERE action = 'file.rollback'`).Scan(&n)
	if n != 2 {
		t.Fatalf("rollbacks audited: %d", n)
	}
}

func TestValidationFailureLeavesLiveFile(t *testing.T) {
	e := newEnv(t)
	p := filepath.Join(e.dir, "app.conf")
	os.WriteFile(p, []byte("port=80\n"), 0o644)
	op := e.op(p, e.put("garbage\n"))
	op.Validate = `grep -q '^port=' {path} || { echo 'missing port setting'; exit 2; }`
	res, out, err := e.run(op, nil)
	if err == nil || res.Outcome != filedeploy.ValidationFailed || res.Validation.ExitCode != 2 || !strings.Contains(out, "missing port setting") {
		t.Fatalf("validation failure: %+v %v %q", res, err, out)
	}
	if e.read(p) != "port=80\n" {
		t.Fatalf("live file changed: %q", e.read(p))
	}
	op2 := e.op(p, e.put("port=9090\n"))
	op2.Validate = `grep -q '^port=' {path}`
	if res, _, err := e.run(op2, nil); err != nil || !res.Validation.OK || e.read(p) != "port=9090\n" {
		t.Fatalf("validation success: %+v %v", res, err)
	}
}

func TestServiceFailureReportedSeparately(t *testing.T) {
	e := newEnv(t)
	p := filepath.Join(e.dir, "app.conf")
	op := e.op(p, e.put("port=80\n"))
	op.Service = &model.FileServiceAction{Name: "app", Action: "reload"}
	res, _, err := e.run(op, func(context.Context, string, string) error {
		return &model.OpError{Class: model.ClassService, Message: "app.service does not support reload"}
	})
	if err == nil || model.ClassOf(err) != model.ClassService || !strings.Contains(err.Error(), "file was installed") {
		t.Fatalf("service failure: %v", err)
	}
	if res.Outcome != filedeploy.Installed || res.Service.OK || e.read(p) != "port=80\n" {
		t.Fatalf("the file must be reported (and be) installed: %+v", res)
	}
	op2 := e.op(p, e.put("port=81\n"))
	op2.Service = &model.FileServiceAction{Name: "app", Action: "restart"}
	var got string
	res, _, err = e.run(op2, func(_ context.Context, unit, action string) error { got = action + " " + unit; return nil })
	if err != nil || !res.Service.OK || got != "restart app.service" {
		t.Fatalf("service success: %+v %v %q", res, err, got)
	}
}

func TestRollbackOfCreatedFileRemovesIt(t *testing.T) {
	e := newEnv(t)
	p := filepath.Join(e.dir, "new.conf")
	v := e.put("x=1\n")
	res, _, _ := e.run(e.op(p, v), nil)
	rb := &model.FileDeployOp{Path: p, Remove: true, RollbackOf: res.DeploymentID, ExpectCurrent: v}
	o := model.Operation{Type: model.OpFileDeploy, FileDeploy: rb}
	if err := o.Normalize(); err != nil {
		t.Fatal(err)
	}
	res, _, err := e.run(rb, nil)
	if err != nil || res.Outcome != filedeploy.Removed || e.read(p) != "<missing>" {
		t.Fatalf("rollback to absent: %+v %v", res, err)
	}
}

func TestPreviewStates(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := filepath.Join(e.dir, "app.conf")
	v1 := e.put("a\nb\nc\n")
	pr, err := filedeploy.Preview(ctx, e.fsys, e.arts, e.op(p, v1))
	if err != nil || pr.State != filedeploy.StateCreate || !strings.Contains(pr.Diff, "+a\n+b\n+c\n") {
		t.Fatalf("create: %+v %v", pr, err)
	}
	os.WriteFile(p, []byte("a\nB\nc\n"), 0o644)
	pr, _ = filedeploy.Preview(ctx, e.fsys, e.arts, e.op(p, v1))
	if pr.State != filedeploy.StateReplace || !strings.Contains(pr.Diff, "-B\n+b\n") || pr.Current.Mode != "0644" {
		t.Fatalf("replace: %+v", pr)
	}
	os.WriteFile(p, []byte("a\nb\nc\n"), 0o644)
	pr, _ = filedeploy.Preview(ctx, e.fsys, e.arts, e.op(p, v1))
	if pr.State != filedeploy.StateIdentical {
		t.Fatalf("identical: %+v", pr)
	}
	op := e.op(p, v1)
	op.Mode = "0600"
	if pr, _ = filedeploy.Preview(ctx, e.fsys, e.arts, op); pr.State != filedeploy.StateMetadata || pr.Desired.Mode != "0600" {
		t.Fatalf("metadata: %+v", pr)
	}
	op = e.op(p, e.put("other\n"))
	op.Overwrite = "create_only"
	if pr, _ = filedeploy.Preview(ctx, e.fsys, e.arts, op); pr.State != filedeploy.StateKeep {
		t.Fatalf("keep: %+v", pr)
	}
	os.Symlink(p, filepath.Join(e.dir, "link"))
	_, err = filedeploy.Preview(ctx, e.fsys, e.arts, e.op(filepath.Join(e.dir, "link"), v1))
	var fe *files.Error
	if !errors.As(err, &fe) || fe.Code != files.CodeSymlink {
		t.Fatalf("symlink: %v", err)
	}
	bin := e.put("\x00\x01")
	if pr, _ = filedeploy.Preview(ctx, e.fsys, e.arts, e.op(p, bin)); pr.Diff != "" || !strings.Contains(pr.DiffNote, "binary") {
		t.Fatalf("binary: %+v", pr)
	}
}
