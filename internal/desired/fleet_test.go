package desired

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"drool/internal/alerts"
	"drool/internal/artifacts"
	"drool/internal/audit"
	"drool/internal/driver"
	"drool/internal/engine"
	"drool/internal/filedeploy"
	"drool/internal/files"
	"drool/internal/logging"
	"drool/internal/model"
	"drool/internal/monitor/monsvc"
	"drool/internal/monitor/services"
	"drool/internal/ops/pkg"
	"drool/internal/store"
	"drool/internal/store/db"
)

// The desired-state service is tested against an in-memory fleet: each
// fake host has systemd units, packages and files. Observation goes
// through the same paths as production (monitoring's service ingestion,
// a package query, files.FS); corrections are ordinary jobs run by the
// real engine, whose fake executor applies the typed operations to the
// fleet (file deployments run the real filedeploy sequence on an
// in-memory filesystem). Nothing touches the workstation.

type unit struct {
	active, sub, fileState, load string
}

type fakeFile struct {
	kind                 string // file | symlink | dir
	content              []byte
	owner, group         string
	uid, gid             int
	mode                 uint32
	target               string
	unreadableUnlessRoot bool
}

type fakeHost struct {
	units       map[string]*unit
	packages    map[string]string // name → version
	files       map[string]*fakeFile
	manager     string // apt (default), dnf, or another: unsupported
	unreachable bool
	dependents  map[string][]string // removing the key also removes these
	failService map[string]string   // unit → error on change
	ignoreStart map[string]bool     // "start" succeeds but the unit stays down
	validateOK  func(cmd string, content []byte) bool
}

type fleet struct {
	mu    sync.Mutex
	hosts map[string]*fakeHost
}

func (f *fleet) host(id string) *fakeHost {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.hosts[id]
	if h == nil {
		h = &fakeHost{units: map[string]*unit{}, packages: map[string]string{"dpkg": "1.21", "rpm": "4.18"}, files: map[string]*fakeFile{},
			manager: "apt", dependents: map[string][]string{}, failService: map[string]string{}, ignoreStart: map[string]bool{}}
		f.hosts[id] = h
	}
	return h
}

func (f *fleet) do(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func connErr(id string) error {
	return &model.OpError{Class: model.ClassConnection, Message: "host " + id + " is unreachable"}
}

// --- Prober ---

type prober struct{ f *fleet }

func (p prober) PackageVersions(_ context.Context, hostID string, names []string) (string, map[string]string, error) {
	h := p.f.host(hostID)
	p.f.mu.Lock()
	defer p.f.mu.Unlock()
	if h.unreachable {
		return "", nil, connErr(hostID)
	}
	if h.manager != "apt" && h.manager != "dnf" {
		_, err := pkg.ForManager(h.manager)
		return "", nil, err
	}
	out := map[string]string{}
	for _, n := range names {
		out[n] = h.packages[n]
	}
	return h.manager, out, nil
}

func (p prober) OpenFS(_ context.Context, hostID string, elevated bool) (files.FS, error) {
	h := p.f.host(hostID)
	p.f.mu.Lock()
	defer p.f.mu.Unlock()
	if h.unreachable {
		return nil, connErr(hostID)
	}
	return &memFS{f: p.f, h: h, root: elevated}, nil
}

// memFS is a files.FS on a fake host.
type memFS struct {
	f    *fleet
	h    *fakeHost
	root bool
}

func (m *memFS) Caps() files.Caps                                     { return files.Caps{Transport: "mem", Elevated: m.root} }
func (m *memFS) Home(context.Context) (string, error)                 { return "/root", nil }
func (m *memFS) RealPath(_ context.Context, p string) (string, error) { return p, nil }
func (m *memFS) Close() error                                         { return nil }

func (m *memFS) info(p string, ff *fakeFile) files.Info {
	k := files.KindFile
	switch ff.kind {
	case "symlink":
		k = files.KindSymlink
	case "dir":
		k = files.KindDir
	}
	return files.Info{Name: path.Base(p), Path: p, Kind: k, Size: int64(len(ff.content)), Mode: ff.mode, UID: ff.uid, GID: ff.gid,
		Owner: ff.owner, Group: ff.group, Target: ff.target, ModTime: time.Unix(0, 0)}
}

func (m *memFS) Stat(_ context.Context, p string) (files.Info, error) {
	m.f.mu.Lock()
	defer m.f.mu.Unlock()
	if ff, ok := m.h.files[p]; ok {
		return m.info(p, ff), nil
	}
	// Directories exist implicitly.
	for q := range m.h.files {
		if strings.HasPrefix(q, p+"/") {
			return files.Info{Name: path.Base(p), Path: p, Kind: files.KindDir, Mode: 0o755}, nil
		}
	}
	if p == "/" || p == "/etc" || p == "/srv" || p == "/opt" {
		return files.Info{Name: path.Base(p), Path: p, Kind: files.KindDir, Mode: 0o755}, nil
	}
	return files.Info{}, files.Errorf(files.CodeNotFound, "%s: no such file or directory", p)
}

func (m *memFS) List(context.Context, string) ([]files.Info, error) {
	return nil, files.Errorf(files.CodeUnsupported, "unsupported")
}

func (m *memFS) read(p string) ([]byte, files.Info, error) {
	m.f.mu.Lock()
	defer m.f.mu.Unlock()
	ff, ok := m.h.files[p]
	if !ok {
		return nil, files.Info{}, files.Errorf(files.CodeNotFound, "%s: no such file or directory", p)
	}
	if ff.kind == "symlink" {
		return nil, files.Info{}, files.Errorf(files.CodeSymlink, "%s is a symlink", p)
	}
	if ff.unreadableUnlessRoot && !m.root {
		return nil, files.Info{}, files.Errorf(files.CodePermission, "%s: permission denied", p)
	}
	return append([]byte(nil), ff.content...), m.info(p, ff), nil
}

func (m *memFS) Open(_ context.Context, p string) (io.ReadCloser, files.Info, error) {
	b, info, err := m.read(p)
	if err != nil {
		return nil, info, err
	}
	return io.NopCloser(bytes.NewReader(b)), info, nil
}

func (m *memFS) Hash(_ context.Context, p string) (string, error) {
	b, _, err := m.read(p)
	if err != nil {
		return "", err
	}
	return shaOf(b), nil
}

func shaOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

type memStaged struct {
	m       *memFS
	dest    string
	content []byte
	opt     files.StageOptions
}

func (m *memFS) Stage(_ context.Context, p string, r io.Reader, opt files.StageOptions) (files.Staged, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if opt.SHA256 != "" && shaOf(b) != opt.SHA256 {
		return nil, files.Errorf(files.CodeIntegrity, "staged content differs")
	}
	return &memStaged{m: m, dest: p, content: b, opt: opt}, nil
}

func (s *memStaged) Path() string            { return s.dest + ".drool-staged" }
func (s *memStaged) SHA256() string          { return shaOf(s.content) }
func (s *memStaged) Size() int64             { return int64(len(s.content)) }
func (s *memStaged) Discard(context.Context) {}

func (s *memStaged) Validate(_ context.Context, cmd string, out io.Writer) (files.ValidationResult, error) {
	ok := true
	if v := s.m.h.validateOK; v != nil {
		ok = v(cmd, s.content)
	}
	code := 0
	if !ok {
		code = 1
		fmt.Fprintln(out, "validation failed")
	}
	return files.ValidationResult{Command: cmd, ExitCode: code, OK: ok}, nil
}

func (s *memStaged) Install(_ context.Context, pre files.Precondition) (*files.Installed, error) {
	m := s.m
	m.f.mu.Lock()
	defer m.f.mu.Unlock()
	cur, exists := m.h.files[s.dest]
	if pre.MustNotExist && exists {
		return nil, files.Errorf(files.CodeConflict, "%s appeared", s.dest)
	}
	if pre.ExpectSHA256 != "" && (!exists || shaOf(cur.content) != pre.ExpectSHA256) {
		return nil, files.Errorf(files.CodeConflict, "%s changed", s.dest)
	}
	nf := &fakeFile{kind: "file", content: s.content, owner: "root", group: "root", mode: 0o644}
	if exists {
		nf.owner, nf.group, nf.uid, nf.gid, nf.mode = cur.owner, cur.group, cur.uid, cur.gid, cur.mode
	}
	if s.opt.Owner != "" {
		nf.owner = s.opt.Owner
	}
	if s.opt.Group != "" {
		nf.group = s.opt.Group
	}
	if s.opt.Mode != nil {
		nf.mode = *s.opt.Mode
	}
	m.h.files[s.dest] = nf
	return &files.Installed{Info: m.info(s.dest, nf), SHA256: shaOf(nf.content)}, nil
}

func (m *memFS) Mkdir(context.Context, string, uint32) error {
	return files.Errorf(files.CodeUnsupported, "unsupported")
}
func (m *memFS) Rename(context.Context, string, string) error {
	return files.Errorf(files.CodeUnsupported, "unsupported")
}
func (m *memFS) Remove(_ context.Context, p string, _ bool) error {
	m.f.mu.Lock()
	defer m.f.mu.Unlock()
	delete(m.h.files, p)
	return nil
}
func (m *memFS) Chmod(context.Context, string, uint32) error {
	return files.Errorf(files.CodeUnsupported, "unsupported")
}
func (m *memFS) Chown(context.Context, string, string, string) error {
	return files.Errorf(files.CodeUnsupported, "unsupported")
}

// --- the engine's executor: typed operations applied to the fleet ---

type fleetExec struct {
	f      *fleet
	arts   *artifacts.Store
	mu     sync.Mutex
	ran    map[string][]string // host → summaries of operations run
	hold   chan struct{}       // when set, every operation waits for it
	failAt map[string]string   // host → error for every operation
}

type fleetPath struct {
	x    *fleetExec
	host string
}

func (x *fleetExec) Prepare(_ context.Context, id string) (engine.Path, error) {
	return fleetPath{x, id}, nil
}

func (p fleetPath) Transport() (string, string) { return "ssh", "" }
func (p fleetPath) JumpHostID() string          { return "" }
func (p fleetPath) Secrets() []string           { return nil }

func (p fleetPath) Execute(ctx context.Context, op model.Operation, out driver.OutputSink, started func()) (*driver.OpResult, error) {
	x := p.x
	x.mu.Lock()
	x.ran[p.host] = append(x.ran[p.host], op.Summary())
	hold, fail := x.hold, x.failAt[p.host]
	x.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return nil, &model.OpError{Class: model.ClassCancelled, Message: "cancelled"}
		}
	}
	started()
	if fail != "" {
		return nil, &model.OpError{Class: model.ClassExecution, Message: fail}
	}
	h := x.f.host(p.host)
	zero := 0
	ok := &driver.OpResult{ExitCode: &zero}
	switch op.Type {
	case model.OpService:
		var err error
		x.f.do(func() {
			name := op.Service.UnitName()
			if msg := h.failService[name]; msg != "" {
				err = &model.OpError{Class: model.ClassService, Message: msg}
				return
			}
			u := h.units[name]
			if u == nil {
				err = &model.OpError{Class: model.ClassService, Message: "service " + name + " not found"}
				return
			}
			switch op.Service.State {
			case "running":
				if !h.ignoreStart[name] {
					u.active, u.sub = "active", "running"
				}
			case "stopped":
				u.active, u.sub = "inactive", "dead"
			}
			if op.Service.Enabled != nil {
				u.fileState = map[bool]string{true: "enabled", false: "disabled"}[*op.Service.Enabled]
			}
		})
		return ok, err
	case model.OpPackage:
		var err error
		x.f.do(func() {
			for _, n := range op.Package.Names {
				switch op.Package.Action {
				case "install":
					h.packages[n] = "1.0-1"
				case "remove":
					if deps := h.dependents[n]; len(deps) > 0 && !op.Package.AllowRemovals {
						var cs []pkg.Change
						for _, d := range deps {
							cs = append(cs, pkg.Change{Name: d})
						}
						err = pkg.RemovalError(cs)
						return
					}
					delete(h.packages, n)
					for _, d := range h.dependents[n] {
						delete(h.packages, d)
					}
				}
			}
		})
		return ok, err
	case model.OpFileDeploy:
		fsys, err := prober{x.f}.OpenFS(ctx, p.host, op.FileDeploy.Elevated)
		if err != nil {
			return nil, err
		}
		res, err := filedeploy.Run(ctx, filedeploy.Deps{FS: fsys, Source: x.arts, Notes: out.Stderr(), Output: out.Stdout(),
			Service: func(context.Context, string, string) error { return nil }}, op.FileDeploy)
		r := &driver.OpResult{ExitCode: &zero}
		if res != nil {
			r.Data = map[string]any{"file": res}
		}
		return r, err
	}
	return nil, &model.OpError{Class: model.ClassValidation, Message: "unsupported in the fake fleet: " + op.Type}
}

func (x *fleetExec) ops(host string) []string {
	x.mu.Lock()
	defer x.mu.Unlock()
	return append([]string(nil), x.ran[host]...)
}

type nopResolver struct{}

func (nopResolver) HostOS(context.Context, string) (model.HostOS, error) { return model.HostOS{}, nil }

// --- the test environment ---

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type env struct {
	t    *testing.T
	ctx  context.Context
	st   *store.Store
	eng  *engine.Engine
	exec *fleetExec
	f    *fleet
	mon  *monsvc.Service
	al   *alerts.Service
	arts *artifacts.Store
	s    *Service
	clk  *testClock
	aw   *audit.Writer
	data string

	pmu   sync.Mutex
	paths map[string]PathInfo // host id → management path state (default: SSH, fine)
}

var t0 = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func newEnv(t *testing.T, hosts ...string) *env {
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
	for _, u := range []struct{ id, name, role string }{{"usr_admin", "admin", "admin"}, {"usr_op", "olga", "operator"},
		{"usr_view", "vera", "viewer"}, {"usr_admin2", "ada", "admin"}} {
		if _, err := st.Writer.Exec(`INSERT INTO users (id, username, password_hash, role, created_at, updated_at, password_changed_at) VALUES (?, ?, 'x', ?, 0, 0, 0)`,
			u.id, u.name, u.role); err != nil {
			t.Fatal(err)
		}
	}
	if len(hosts) == 0 {
		hosts = []string{"web-1", "web-2", "lab-box"}
	}
	for _, name := range hosts {
		if _, err := st.Writer.Exec(`INSERT INTO hosts (id, name, connect_address, ssh_user, status, created_at, updated_at) VALUES (?, ?, '10.0.0.1', 'x', 'ready', 0, 0)`,
			"h_"+name, name); err != nil {
			t.Fatal(err)
		}
	}
	logs := logging.Discard()
	aw := audit.NewWriter(st, logs.Audit)
	arts, err := artifacts.Open(data, st)
	if err != nil {
		t.Fatal(err)
	}
	f := &fleet{hosts: map[string]*fakeHost{}}
	x := &fleetExec{f: f, arts: arts, ran: map[string][]string{}, failAt: map[string]string{}}
	eng := engine.New(st, nopResolver{}, x, aw, logs.App, engine.Config{RetryDelay: time.Millisecond, RetryAttempts: 1})
	if err := eng.Start(ctx); err != nil {
		t.Fatal(err)
	}
	clk := &testClock{t: t0}
	mon := monsvc.New(monsvc.Deps{Store: st, Log: logs.App})
	mon.SetClock(clk.now)
	al := alerts.New(alerts.Deps{Store: st, Audit: aw, Log: logs.App, Config: alerts.Config{DisableWorkers: true}})
	al.SetClock(clk.now)
	e := &env{t: t, ctx: ctx, st: st, eng: eng, exec: x, f: f, mon: mon, al: al, arts: arts, clk: clk, aw: aw, data: data,
		paths: map[string]PathInfo{}}
	e.s = e.newService()
	return e
}

func (e *env) newService() *Service {
	s := New(Deps{Store: e.st, Engine: e.eng, Monitor: e.mon, Alerts: e.al, Prober: prober{e.f}, Artifacts: e.arts, Audit: e.aw,
		Log: logging.Discard().App,
		Path: func(_ context.Context, h db.Host) PathInfo {
			e.pmu.Lock()
			defer e.pmu.Unlock()
			return e.paths[h.ID]
		},
		PollSSH: func(ctx context.Context, h db.Host) error {
			if e.f.host(h.ID).unreachable {
				return connErr(h.ID)
			}
			e.reportServices(h.ID)
			return nil
		},
		Removals: func(_ context.Context, hostID, p string) ([]string, error) {
			h := e.f.host(hostID)
			e.f.mu.Lock()
			defer e.f.mu.Unlock()
			return append([]string(nil), h.dependents[p]...), nil
		},
		Tuning: Tuning{ProofWait: 200 * time.Millisecond}})
	s.SetClock(e.clk.now)
	e.eng.OnJobDone(s.OnJobDone)
	e.mon.SetServicesHook(s.OnServices)
	if err := s.Start(e.ctx); err != nil {
		e.t.Fatal(err)
	}
	return s
}

// reportServices is monitoring receiving the host's service list (M11).
func (e *env) reportServices(hostID string) {
	h := e.f.host(hostID)
	var rep services.Report
	e.f.do(func() {
		rep = services.Report{Supported: true, Units: []services.Unit{}}
		for n, u := range h.units {
			load := u.load
			if load == "" {
				load = "loaded"
			}
			rep.Units = append(rep.Units, services.Unit{Name: n, Load: load, Active: u.active, Sub: u.sub, FileState: u.fileState})
		}
	})
	sort.Slice(rep.Units, func(i, j int) bool { return rep.Units[i].Name < rep.Units[j].Name })
	if err := e.mon.IngestServices(e.ctx, hostID, &rep, true); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) reportAll() {
	for id := range e.s.Snapshot().Hosts {
		e.reportServices(id)
	}
}

// settle runs the service until nothing is pending: evaluations, due
// checks (run synchronously here), and finished corrections.
func (e *env) step() {
	e.t.Helper()
	e.s.Step(e.ctx)
	// Step may have started a pass of automatic restore in the background:
	// let it finish, so each step is deterministic.
	for e.s.autoRunning.Load() {
		time.Sleep(5 * time.Millisecond)
	}
	// Run the due checks right here instead of in workers.
	for {
		select {
		case r := <-e.s.checks:
			e.s.checkAndEvaluate(e.ctx, r.hostID, r.proof, 0)
			e.s.mu.Lock()
			delete(e.s.inflight, r.hostID)
			e.s.mu.Unlock()
			continue
		default:
		}
		break
	}
	e.s.evaluateDirty(e.ctx)
}

// checkAll checks every managed host now and evaluates it.
func (e *env) checkAll() {
	e.t.Helper()
	snap := e.s.Snapshot()
	var ids []string
	for id := range snap.Hosts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		e.s.checkAndEvaluate(e.ctx, id, false, 0)
	}
	e.s.evaluateDirty(e.ctx)
}

// waitJobs waits for every job to finish, then settles corrections.
func (e *env) waitJobs() {
	e.t.Helper()
	e.eng.Wait()
	if err := e.s.SettleNow(e.ctx); err != nil {
		e.t.Fatal(err)
	}
	e.s.evaluateDirty(e.ctx)
}

var admin = Actor{UserID: "usr_admin", Name: "admin"}
var operator = Actor{UserID: "usr_op", Name: "olga"}

func (e *env) baseline(name string, rs []Resource, as ...Assignment) *BaselineView {
	e.t.Helper()
	v, err := e.s.CreateBaseline(e.ctx, BaselineInput{Name: name, Resources: rs, Assignments: as}, admin)
	if err != nil {
		e.t.Fatal(err)
	}
	return v
}

func (e *env) group(id, name string, hosts ...string) {
	e.t.Helper()
	if _, err := e.st.Writer.Exec(`INSERT INTO groups (id, name, created_at, updated_at) VALUES (?, ?, 0, 0)`, id, name); err != nil {
		e.t.Fatal(err)
	}
	for _, h := range hosts {
		if _, err := e.st.Writer.Exec(`INSERT INTO group_members (group_id, host_id) VALUES (?, ?)`, id, "h_"+h); err != nil {
			e.t.Fatal(err)
		}
	}
}

func (e *env) upload(name, content string) string {
	e.t.Helper()
	a, err := e.arts.Put(e.ctx, strings.NewReader(content), name, "usr_admin")
	if err != nil {
		e.t.Fatal(err)
	}
	return a.SHA256
}

func (e *env) status(host, key string) string {
	e.t.Helper()
	var st string
	if err := e.st.Reader.QueryRow(`SELECT status FROM ds_resource_state WHERE host_id = ? AND key = ?`, "h_"+host, key).Scan(&st); err != nil {
		return "(none)"
	}
	return st
}

func (e *env) resource(host, key string) ResourceView {
	e.t.Helper()
	v, _, err := e.s.Resource(e.ctx, "h_"+host, key)
	if err != nil {
		e.t.Fatalf("resource %s %s: %v", host, key, err)
	}
	return *v
}

func (e *env) history(host, key string) []HistoryEntry {
	e.t.Helper()
	hs, err := e.s.History(e.ctx, HistoryFilter{HostID: "h_" + host, Key: key, Limit: 100})
	if err != nil {
		e.t.Fatal(err)
	}
	return hs
}

func kinds(hs []HistoryEntry) string {
	var out []string
	for i := len(hs) - 1; i >= 0; i-- {
		k := hs[i].Kind
		if hs[i].Resolution != "" {
			k += "(" + hs[i].Resolution + ")"
		}
		out = append(out, k)
	}
	return strings.Join(out, ",")
}

func svc(name, state string, enabled *bool) Resource {
	return Resource{Kind: KindService, Name: name, State: state, Enabled: enabled}
}

func pkgR(name, state string) Resource { return Resource{Kind: KindPackage, Name: name, State: state} }

func on() *bool  { b := true; return &b }
func off() *bool { b := false; return &b }

var everywhere = Assignment{Scope: "global"}

func inGroup(id string) Assignment { return Assignment{Scope: "group", GroupID: id} }
func onHost(h string) Assignment   { return Assignment{Scope: "host", HostID: "h_" + h} }

// setPath sets how DROOL reaches a host (an agent epoch mismatch, a
// blocked identity).
func (e *env) setPath(host string, pi PathInfo) {
	e.pmu.Lock()
	e.paths["h_"+host] = pi
	e.pmu.Unlock()
}

// sql runs a statement against the database (fixtures).
func (e *env) sql(q string, args ...any) {
	e.t.Helper()
	if _, err := e.st.Writer.Exec(q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

func (e *env) setUnit(host, name, active, fileState string) {
	h := e.f.host("h_" + host)
	e.f.do(func() {
		sub := map[string]string{"active": "running", "inactive": "dead", "failed": "failed"}[active]
		h.units[name] = &unit{active: active, sub: sub, fileState: fileState}
	})
}
