package desired

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"drool/internal/agentcaps"
	"drool/internal/audit"
	"drool/internal/auth"
	"drool/internal/engine"
	"drool/internal/ids"
	"drool/internal/model"
	"drool/internal/store/db"
)

// Selection chooses what Restore managed state corrects: drift and failed
// corrections, optionally limited to hosts, resources (keys) and the
// resources a baseline decides. Empty means everything that differs.
type Selection struct {
	HostIDs    []string `json:"host_ids,omitempty"`
	Keys       []string `json:"keys,omitempty"`
	BaselineID string   `json:"baseline_id,omitempty"`
}

// RestoreOptions are the operator's choices for a correction.
type RestoreOptions struct {
	// Strategy is the rollout (canary, batches, concurrency, failure
	// threshold); nil: the recommended rollout for several hosts.
	Strategy *model.Strategy `json:"strategy,omitempty"`
	// AllowRemovals lets removing a prohibited package also remove the
	// packages that depend on it. Off by default: then such a removal
	// fails safely on that host.
	AllowRemovals bool `json:"allow_removals,omitempty"`
}

// PlanItem is one correction on one host.
type PlanItem struct {
	HostID      string       `json:"host_id"`
	HostName    string       `json:"host_name"`
	Key         string       `json:"key"`
	Kind        string       `json:"kind"`
	Want        string       `json:"want"`
	Current     string       `json:"current"`
	Diffs       []Diff       `json:"diffs"`
	Job         int          `json:"job"`
	Step        int          `json:"step"`
	SourceLabel string       `json:"source_label"`
	Baseline    *BaselineRef `json:"baseline,omitempty"`
	Desired     Resource     `json:"desired"`
}

// Excluded is something that differs but isn't part of the plan.
type Excluded struct {
	HostID   string `json:"host_id"`
	HostName string `json:"host_name"`
	Key      string `json:"key"`
	Status   string `json:"status"`
	Reason   string `json:"reason"`
}

// PlanWarning is something to know before confirming.
type PlanWarning struct {
	HostID   string `json:"host_id,omitempty"`
	HostName string `json:"host_name,omitempty"`
	Key      string `json:"key,omitempty"`
	Message  string `json:"message"`
}

// PlanHost is a host in a corrective job.
type PlanHost struct {
	HostID   string `json:"host_id"`
	HostName string `json:"host_name"`
	Steps    []int  `json:"steps"`
	Wave     int    `json:"wave"`
}

// PlanWave is a rollout wave.
type PlanWave struct {
	Index  int      `json:"index"`
	Canary bool     `json:"canary"`
	Hosts  []string `json:"hosts"`
}

// PlanJob is one ordinary job of a correction: a step per resource that
// differs somewhere among its hosts; each host runs only its own steps.
type PlanJob struct {
	Name      string            `json:"name"`
	Steps     []model.Operation `json:"steps"`
	StepText  []string          `json:"step_text"`
	StepHosts []int             `json:"step_hosts"`
	Hosts     []PlanHost        `json:"hosts"`
	Waves     []PlanWave        `json:"waves"`
	Strategy  model.Strategy    `json:"strategy"`
	WaveNote  string            `json:"wave_note,omitempty"`
}

// RemovalCheck is what removing a prohibited package would also remove.
type RemovalCheck struct {
	HostID   string   `json:"host_id"`
	HostName string   `json:"host_name"`
	Package  string   `json:"package"`
	Also     []string `json:"also"`
	Error    string   `json:"error,omitempty"`
}

// Plan is a correction, computed from fresh observations.
type Plan struct {
	Items    []PlanItem      `json:"items"`
	Excluded []Excluded      `json:"excluded"`
	Jobs     []PlanJob       `json:"jobs"`
	Warnings []PlanWarning   `json:"warnings"`
	Removals []RemovalCheck  `json:"removals"`
	Options  RestoreOptions  `json:"options"`
	Hosts    int             `json:"hosts"`
	Hash     string          `json:"hash"`
	At       int64           `json:"at"`
	Needs    []string        `json:"needs"` // permissions the corrections need beyond desired.restore
	needs    map[string]bool // (internal)
	byHost   map[string][]PlanItem
}

// RecommendedRollout is used for corrections of several hosts unless the
// operator chooses another: one canary, then batches of five, pausing
// after two failures (as M13 recommends for fleet changes).
func RecommendedRollout() model.Strategy {
	two := 2
	return model.Strategy{Concurrency: 5, CanaryCount: 1, BatchSize: 5, FailureThreshold: &two}
}

// ErrPlanChanged: what would be corrected changed since the preview.
var ErrPlanChanged = errors.New("the hosts' state changed since the preview; review the correction again")

// ErrNothingToRestore: nothing in the selection differs (no job is created).
var ErrNothingToRestore = errors.New("nothing to restore: the selected resources match their desired state (or can't be restored right now)")

// planCtx is what a plan is for.
type planCtx struct {
	refresh bool   // check hosts first (previews)
	auto    string // automatic restore of this baseline ("" manual)
	now     int64
}

// PreviewRestore checks the selected hosts now (when their observations
// are older than a minute) and plans the correction. Nothing changes.
func (s *Service) PreviewRestore(ctx context.Context, sel Selection, opt RestoreOptions) (*Plan, error) {
	return s.plan(ctx, sel, opt, planCtx{refresh: true})
}

// PreviewRestoreNoCheck plans from what DROOL knows now, without checking
// hosts first (to see what a restore would need).
func (s *Service) PreviewRestoreNoCheck(ctx context.Context, sel Selection, opt RestoreOptions) (*Plan, error) {
	return s.plan(ctx, sel, opt, planCtx{})
}

func inList(xs []string, x string) bool {
	if len(xs) == 0 {
		return true
	}
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// plan computes a correction.
func (s *Service) plan(ctx context.Context, sel Selection, opt RestoreOptions, pc planCtx) (*Plan, error) {
	s.fresh(ctx)
	s.mu.Lock()
	snap := s.snap
	eff := s.eff
	s.mu.Unlock()
	if snap == nil {
		return nil, errors.New("desired state is starting")
	}
	for _, id := range sel.HostIDs {
		if _, ok := snap.Hosts[id]; !ok {
			return nil, &ValidationError{Msg: "host " + id + " not found"}
		}
	}
	if sel.BaselineID != "" {
		if _, ok := snap.Baselines[sel.BaselineID]; !ok {
			return nil, &ValidationError{Msg: "baseline " + sel.BaselineID + " not found"}
		}
	}
	inScope := func(hostID, key string, e Effective) bool {
		if !inList(sel.HostIDs, hostID) || !inList(sel.Keys, key) {
			return false
		}
		if sel.BaselineID != "" && !hasBaseline(e.Source.Baselines, sel.BaselineID) {
			return false
		}
		return true
	}
	// Hosts with something to correct (or that DROOL can't see well
	// enough to tell), in scope.
	candidates := func() (map[string]map[string]*resRow, error) {
		rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT host_id, `+resCols+` FROM ds_resource_state WHERE status <> 'compliant'`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[string]map[string]*resRow{}
		for rows.Next() {
			var hid string
			var r resRow
			if err := rows.Scan(&hid, &r.Key, &r.Kind, &r.Status, &r.Desired, &r.Source, &r.Observed, &r.ObservedAt, &r.Diffs, &r.Sig,
				&r.LastKnown, &r.Note, &r.Since, &r.ReconcileID, &r.JobID, &r.LastJobID, &r.AutoAttempts, &r.AutoNextAt, &r.AutoPaused,
				&r.RestoredAt, &r.Redrifts, &r.RedriftSince); err != nil {
				return nil, err
			}
			hs, ok := eff[hid]
			if !ok {
				continue
			}
			e, ok := hs.Resources[r.Key]
			if !ok || !inScope(hid, r.Key, e) {
				continue
			}
			if out[hid] == nil {
				out[hid] = map[string]*resRow{}
			}
			out[hid][r.Key] = &r
		}
		return out, rows.Err()
	}
	cands, err := candidates()
	if err != nil {
		return nil, err
	}
	if pc.refresh && len(cands) > 0 {
		var stale []string
		for hid := range cands {
			if s.observedWithin(ctx, hid, time.Minute) {
				continue
			}
			stale = append(stale, hid)
		}
		sort.Strings(stale)
		if len(stale) > 0 {
			s.checkHosts(ctx, stale, true, 0)
			s.mu.Lock()
			snap, eff = s.snap, s.eff
			s.mu.Unlock()
			if cands, err = candidates(); err != nil {
				return nil, err
			}
		}
	}

	p := &Plan{Items: []PlanItem{}, Excluded: []Excluded{}, Jobs: []PlanJob{}, Warnings: []PlanWarning{}, Removals: []RemovalCheck{},
		Options: opt, At: s.nowMs(), needs: map[string]bool{}, byHost: map[string][]PlanItem{}}
	hostIDs := make([]string, 0, len(cands))
	for hid := range cands {
		hostIDs = append(hostIDs, hid)
	}
	sort.Slice(hostIDs, func(i, j int) bool {
		a, b := snap.Hosts[hostIDs[i]], snap.Hosts[hostIDs[j]]
		if a != b {
			return strings.ToLower(a) < strings.ToLower(b)
		}
		return hostIDs[i] < hostIDs[j]
	})
	for _, hid := range hostIDs {
		name := snap.Hosts[hid]
		hs := eff[hid]
		h, err := s.d.Store.R.GetHost(ctx, hid)
		if err != nil {
			continue
		}
		hostBlock, filesBlock, caps := s.hostBlockers(ctx, h)
		keys := make([]string, 0, len(cands[hid]))
		for k := range cands[hid] {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			r := cands[hid][key]
			e := hs.Resources[key]
			ex := func(reason string) {
				p.Excluded = append(p.Excluded, Excluded{HostID: hid, HostName: name, Key: key, Status: r.Status, Reason: reason})
			}
			switch r.Status {
			case StatusConflict:
				ex(e.Source.Label + " — resolve the conflict first (a group priority, a host-specific setting, or a baseline change)")
				continue
			case StatusUnknown:
				ex("DROOL can't currently verify it: " + r.Note)
				continue
			case StatusReconciling:
				ex("a correction is already in progress (job " + r.JobID.String + ")")
				continue
			}
			if e.Conflict || e.Resource == nil {
				continue
			}
			var obs Observation
			if json.Unmarshal([]byte(r.Observed), &obs) != nil {
				ex("not observed yet")
				continue
			}
			j := Compare(*e.Resource, obs)
			if j.Compliant() {
				continue
			}
			if j.Blocker != "" {
				ex(j.Blocker)
				continue
			}
			if hostBlock != "" {
				ex(hostBlock)
				continue
			}
			if e.Kind == KindFile && filesBlock != "" {
				ex(filesBlock)
				continue
			}
			if caps != nil {
				if why := capsDenial(*caps, *e.Resource); why != "" {
					ex("the agent's capability ceiling doesn't allow this: " + why)
					continue
				}
			}
			if pc.auto != "" {
				if r.AutoPaused != "" {
					ex("automatic restore is paused for it: " + r.AutoPaused)
					continue
				}
				if r.AutoNextAt.Valid && r.AutoNextAt.Int64 > pc.now {
					ex("waiting before the next automatic attempt (after " + clock(r.AutoNextAt.Int64) + ")")
					continue
				}
			}
			it := PlanItem{HostID: hid, HostName: name, Key: key, Kind: e.Kind, Want: e.Resource.Want(), Current: CurrentText(e.Kind, obs),
				Diffs: j.Diffs, SourceLabel: e.Source.Label, Baseline: e.SourceBaseline(), Desired: *e.Resource}
			p.Items = append(p.Items, it)
			p.byHost[hid] = append(p.byHost[hid], it)
			if e.Kind == KindFile {
				p.needs[string(auth.PermFilesDeploy)] = true
				if e.Resource.Elevated {
					p.needs[string(auth.PermFilesElevate)] = true
				}
			}
		}
	}
	p.Hosts = len(p.byHost)
	for k := range p.needs {
		p.Needs = append(p.Needs, k)
	}
	sort.Strings(p.Needs)
	if len(p.Items) == 0 {
		p.Hash = planHash(p)
		return p, nil
	}
	if err := s.buildJobs(p, snap, opt, pc); err != nil {
		return nil, err
	}
	s.planWarnings(ctx, p, pc)
	p.Hash = planHash(p)
	return p, nil
}

// observedWithin reports whether a host's packages, files and services
// were all observed within d (no need to check again for a preview).
func (s *Service) observedWithin(ctx context.Context, hostID string, d time.Duration) bool {
	hs, ok := s.effective(hostID)
	if !ok {
		return true
	}
	cut := s.nowMs() - d.Milliseconds()
	h, err := loadHost(ctx, s.d.Store.Reader, hostID)
	if err != nil || !h.exists {
		return false
	}
	for _, e := range hs.Resources {
		switch e.Kind {
		case KindPackage:
			if !h.PackagesAt.Valid || h.PackagesAt.Int64 < cut {
				return false
			}
		case KindFile:
			if !h.FilesAt.Valid || h.FilesAt.Int64 < cut {
				return false
			}
		case KindService:
			if s.servicesStamp(ctx, hostID) < cut {
				return false
			}
		}
	}
	return true
}

// hostBlockers says why a host can't be corrected at all (an identity an
// administrator must decide on; an agent of another controller epoch),
// why its files can't (its file state is ahead of this controller, e.g.
// after restoring an old backup), and returns the agent's effective
// capabilities for agent-managed hosts.
func (s *Service) hostBlockers(ctx context.Context, h db.Host) (string, string, *agentcaps.Caps) {
	hostBlock, filesBlock := "", ""
	if s.d.Path != nil {
		pi := s.d.Path(ctx, h)
		switch {
		case pi.Blocked:
			hostBlock = "DROOL can't change this host: " + pi.Reason
		case pi.AgentState == "epoch_mismatch":
			hostBlock = "the host's Shepherd belongs to another controller epoch (a restored or different controller); reconcile it on the Shepherds page first"
		}
	}
	var st string
	if err := s.d.Store.Reader.QueryRowContext(ctx, `SELECT status FROM state_versions WHERE host_id = ? AND domain = 'files'`, h.ID).Scan(&st); err == nil && st != "ok" {
		filesBlock = "the host's managed-file state is ahead of this controller (" + st + "); reconcile it first — DROOL never overwrites newer state"
	}
	if h.ManagementMethod != "agent" {
		return hostBlock, filesBlock, nil
	}
	var eff string
	if err := s.d.Store.Reader.QueryRowContext(ctx, `SELECT effective_json FROM agent_identities WHERE host_id = ? AND status <> 'revoked'
		ORDER BY enrolled_at DESC LIMIT 1`, h.ID).Scan(&eff); err != nil || eff == "" || eff == "{}" {
		return hostBlock, filesBlock, nil
	}
	var c agentcaps.Caps
	if json.Unmarshal([]byte(eff), &c) != nil {
		return hostBlock, filesBlock, nil
	}
	return hostBlock, filesBlock, &c
}

// capsDenial checks a correction against an agent's effective
// capabilities (the agent enforces them anyway; this explains it up front).
func capsDenial(c agentcaps.Caps, r Resource) string {
	var err error
	switch r.Kind {
	case KindPackage:
		err = c.AllowPackages([]string{r.Name})
	case KindService:
		err = c.AllowService(r.Name)
	case KindFile:
		if err = c.AllowWrite(r.Path); err == nil && r.Validate != "" {
			err = c.AllowValidation(r.Validate)
		}
		if err == nil && r.Mode != "" {
			if m, perr := model.ParseOctalMode(r.Mode); perr == nil {
				err = c.AllowMode(m)
			}
		}
		if err == nil && r.Service != nil {
			err = c.AllowService(r.Service.Name)
		}
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

// buildJobs turns items into ordinary jobs: one step per distinct
// correction (in a safe order), each host running only its own steps;
// split only when a job would exceed model.MaxCorrectiveSteps.
func (s *Service) buildJobs(p *Plan, snap *Snapshot, opt RestoreOptions, pc planCtx) error {
	type corr struct {
		key string
		res Resource
		op  model.Operation
		id  string // canonical identity
	}
	var hostOrder []string
	for _, it := range p.Items {
		if len(hostOrder) == 0 || hostOrder[len(hostOrder)-1] != it.HostID {
			hostOrder = append(hostOrder, it.HostID)
		}
	}
	type jobAcc struct {
		corrs []corr
		index map[string]int
		hosts []string
	}
	var jobs []*jobAcc
	cur := &jobAcc{index: map[string]int{}}
	for _, hid := range hostOrder {
		items := p.byHost[hid]
		var mine []corr
		for _, it := range items {
			op := it.Desired.Corrective(opt.AllowRemovals)
			b, _ := json.Marshal(op)
			mine = append(mine, corr{key: it.Key, res: it.Desired, op: op, id: string(b)})
		}
		extra := 0
		for _, c := range mine {
			if _, ok := cur.index[c.id]; !ok {
				extra++
			}
		}
		if len(cur.corrs)+extra > model.MaxCorrectiveSteps && len(cur.hosts) > 0 {
			jobs = append(jobs, cur)
			cur = &jobAcc{index: map[string]int{}}
		}
		if len(mine) > model.MaxCorrectiveSteps {
			return &ValidationError{Msg: fmt.Sprintf("%s needs %d corrections; at most %d fit in one correction (restore it in parts, e.g. one baseline at a time)",
				snap.Hosts[hid], len(mine), model.MaxCorrectiveSteps)}
		}
		for _, c := range mine {
			if _, ok := cur.index[c.id]; !ok {
				cur.index[c.id] = len(cur.corrs)
				cur.corrs = append(cur.corrs, c)
			}
		}
		cur.hosts = append(cur.hosts, hid)
	}
	if len(cur.hosts) > 0 {
		jobs = append(jobs, cur)
	}
	for ji, ja := range jobs {
		// Safe order: installs, files, services, removals; then by key.
		order := make([]int, len(ja.corrs))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(a, b int) bool {
			ca, cb := ja.corrs[order[a]], ja.corrs[order[b]]
			if stepOrder(ca.res) != stepOrder(cb.res) {
				return stepOrder(ca.res) < stepOrder(cb.res)
			}
			return ca.key < cb.key
		})
		pos := map[string]int{}
		pj := PlanJob{Steps: make([]model.Operation, len(order)), StepText: make([]string, len(order)), StepHosts: make([]int, len(order))}
		for i, o := range order {
			c := ja.corrs[o]
			op := c.op
			if err := op.Normalize(); err != nil {
				return fmt.Errorf("correction for %s: %w", Describe(c.key), err)
			}
			pj.Steps[i] = op
			pj.StepText[i] = op.Summary()
			pos[c.id] = i
		}
		for _, hid := range ja.hosts {
			ph := PlanHost{HostID: hid, HostName: snap.Hosts[hid]}
			for k, it := range p.byHost[hid] {
				op := it.Desired.Corrective(opt.AllowRemovals)
				b, _ := json.Marshal(op)
				i := pos[string(b)]
				ph.Steps = append(ph.Steps, i)
				pj.StepHosts[i]++
				p.byHost[hid][k].Job, p.byHost[hid][k].Step = ji, i
			}
			sort.Ints(ph.Steps)
			pj.Hosts = append(pj.Hosts, ph)
		}
		st := model.Strategy{}
		switch {
		case opt.Strategy != nil:
			st = *opt.Strategy
		case len(ja.hosts) > 1:
			st = RecommendedRollout()
		}
		if err := model.NormalizeStrategy(&st); err != nil {
			return &ValidationError{Msg: err.Error()}
		}
		pj.Strategy = st
		hostIDs := make([]string, len(pj.Hosts))
		for i, h := range pj.Hosts {
			hostIDs[i] = h.HostID
		}
		plan := model.PlanWaves(hostIDs, st)
		pj.WaveNote = plan.Note
		for wi, w := range plan.Waves {
			pw := PlanWave{Index: wi, Canary: plan.Canary && wi == 0}
			for _, x := range w {
				pw.Hosts = append(pw.Hosts, pj.Hosts[x].HostName)
				pj.Hosts[x].Wave = wi
			}
			pj.Waves = append(pj.Waves, pw)
		}
		nRes := map[string]bool{}
		for _, hid := range ja.hosts {
			for _, it := range p.byHost[hid] {
				nRes[it.Key] = true
			}
		}
		prefix := "Restore managed state"
		if pc.auto != "" {
			prefix = "Automatic restore (" + snap.Baselines[pc.auto].Name + ")"
		}
		if len(ja.hosts) == 1 {
			pj.Name = fmt.Sprintf("%s on %s", prefix, snap.Hosts[ja.hosts[0]])
		} else {
			pj.Name = fmt.Sprintf("%s: %d hosts, %s", prefix, len(ja.hosts), plural(len(nRes), "resource", "resources"))
		}
		if len(jobs) > 1 {
			pj.Name += fmt.Sprintf(" (part %d of %d)", ji+1, len(jobs))
		}
		p.Jobs = append(p.Jobs, pj)
	}
	// Items in host order with their job and step.
	p.Items = p.Items[:0]
	for _, hid := range hostOrder {
		p.Items = append(p.Items, p.byHost[hid]...)
	}
	return nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// planWarnings adds what an operator should know before confirming: hosts
// in maintenance, services that aren't installed, and (previews only)
// what removing a prohibited package would also remove.
func (s *Service) planWarnings(ctx context.Context, p *Plan, pc planCtx) {
	if s.d.Alerts != nil {
		if maint, err := s.d.Alerts.HostsInMaintenance(ctx); err == nil {
			for hid := range p.byHost {
				if m, ok := maint[hid]; ok {
					msg := "in maintenance"
					if m.Reason != "" {
						msg += " (" + m.Reason + ")"
					}
					p.Warnings = append(p.Warnings, PlanWarning{HostID: hid, HostName: p.byHost[hid][0].HostName,
						Message: msg + ": restoring is a deliberate change during maintenance"})
				}
			}
		}
	}
	for hid, items := range p.byHost {
		installs := map[string]bool{}
		for _, it := range items {
			if it.Kind == KindPackage && it.Desired.State == StateInstalled {
				installs[it.Desired.Name] = true
			}
		}
		for _, it := range items {
			if it.Kind == KindService && strings.Contains(it.Current, "not installed") && len(installs) == 0 &&
				(it.Desired.State == StateRunning || it.Desired.Enabled != nil && *it.Desired.Enabled) {
				p.Warnings = append(p.Warnings, PlanWarning{HostID: hid, HostName: it.HostName, Key: it.Key,
					Message: it.Desired.Name + " isn't installed on " + it.HostName + "; starting or enabling it will fail unless something installs it first"})
			}
		}
	}
	if !pc.refresh || s.d.Removals == nil {
		return
	}
	type rq struct {
		hostID, host, pkg string
	}
	var qs []rq
	for hid, items := range p.byHost {
		for _, it := range items {
			if it.Kind == KindPackage && it.Desired.State == StateAbsent {
				qs = append(qs, rq{hid, it.HostName, it.Desired.Name})
			}
		}
	}
	sort.Slice(qs, func(i, j int) bool { return qs[i].host+qs[i].pkg < qs[j].host+qs[j].pkg })
	if len(qs) > 50 {
		qs = qs[:50]
	}
	out := make([]RemovalCheck, len(qs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, q := range qs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, q rq) {
			defer func() { <-sem; wg.Done() }()
			rc := RemovalCheck{HostID: q.hostID, HostName: q.host, Package: q.pkg, Also: []string{}}
			sctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			also, err := s.d.Removals(sctx, q.hostID, q.pkg)
			cancel()
			if err != nil {
				rc.Error = errText(err)
			} else {
				rc.Also = append(rc.Also, also...)
			}
			out[i] = rc
		}(i, q)
	}
	wg.Wait()
	for _, rc := range out {
		p.Removals = append(p.Removals, rc)
		if len(rc.Also) > 0 && !p.Options.AllowRemovals {
			p.Warnings = append(p.Warnings, PlanWarning{HostID: rc.HostID, HostName: rc.HostName, Key: KindPackage + ":" + rc.Package,
				Message: fmt.Sprintf("removing %s on %s would also remove %s; without “allow removing dependent packages” that removal fails safely there (nothing changes)",
					rc.Package, rc.HostName, strings.Join(rc.Also, ", "))})
		}
	}
}

// planHash identifies exactly what a plan would submit.
func planHash(p *Plan) string {
	h := sha256.New()
	fmt.Fprintf(h, "removals=%v;", p.Options.AllowRemovals)
	for _, j := range p.Jobs {
		b, _ := json.Marshal(j.Steps)
		h.Write(b)
		sb, _ := json.Marshal(j.Strategy)
		h.Write(sb)
		for _, ho := range j.Hosts {
			fmt.Fprintf(h, "|%s:%v", ho.HostID, ho.Steps)
		}
		h.Write([]byte{'\n'})
	}
	for _, it := range p.Items {
		fmt.Fprintf(h, "%s|%s|%s;", it.HostID, it.Key, diffSig(it.Diffs))
	}
	return hex.EncodeToString(h.Sum(nil))[:24]
}

// Actor is who asks for a change.
type Actor struct {
	UserID string
	Name   string
}

// RestoreResult lists the jobs a correction created.
type RestoreResult struct {
	Jobs []string `json:"jobs"`
	Plan *Plan    `json:"plan"`
}

// Restore submits the correction a preview showed. The plan is computed
// again from what DROOL knows now; if it differs from the preview (hash),
// nothing is submitted.
func (s *Service) Restore(ctx context.Context, sel Selection, opt RestoreOptions, hash string, by Actor) (*RestoreResult, error) {
	p, err := s.plan(ctx, sel, opt, planCtx{})
	if err != nil {
		return nil, err
	}
	if len(p.Items) == 0 {
		return nil, ErrNothingToRestore
	}
	if hash != "" && hash != p.Hash {
		return nil, ErrPlanChanged
	}
	jobs, err := s.submit(ctx, p, "manual", "", by)
	if err != nil {
		return nil, err
	}
	return &RestoreResult{Jobs: jobs, Plan: p}, nil
}

// submit creates a plan's jobs: in one transaction per job, the
// correction record, the ordinary job (engine.Submit) and its resources
// marked as reconciling — so a resource never gets a second corrective
// job for the same drift — then launches them.
func (s *Service) submit(ctx context.Context, p *Plan, kind, baselineID string, by Actor) ([]string, error) {
	var jobIDs []string
	for ji, pj := range p.Jobs {
		rid := ids.New(ids.Reconcile)
		type item struct {
			HostID   string       `json:"host_id"`
			HostName string       `json:"host_name"`
			Key      string       `json:"key"`
			Step     int          `json:"step"`
			Desired  Resource     `json:"desired"`
			Baseline *BaselineRef `json:"baseline,omitempty"`
			Diffs    []Diff       `json:"diffs"`
		}
		var items []item
		hostSteps := map[string][]int{}
		var targets []string
		for _, h := range pj.Hosts {
			hostSteps[h.HostID] = h.Steps
			targets = append(targets, h.HostID)
		}
		for _, it := range p.Items {
			if it.Job == ji {
				items = append(items, item{HostID: it.HostID, HostName: it.HostName, Key: it.Key, Step: it.Step, Desired: it.Desired,
					Baseline: it.Baseline, Diffs: it.Diffs})
			}
		}
		spec := model.JobSpec{Name: pj.Name, Steps: pj.Steps, Targets: model.TargetSpec{Hosts: targets}, Strategy: pj.Strategy}
		var sub *engine.Submission
		var emits []func()
		now := s.nowMs()
		err := s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
			// Nothing may already be correcting these resources.
			for _, it := range items {
				var st string
				var job sql.NullString
				err := tx.QueryRowContext(ctx, `SELECT status, job_id FROM ds_resource_state WHERE host_id = ? AND key = ?`, it.HostID, it.Key).Scan(&st, &job)
				if err != nil {
					return ErrPlanChanged
				}
				if st == StatusReconciling && job.Valid {
					return ErrPlanChanged
				}
			}
			summary := fmt.Sprintf("%s, %s", plural(len(pj.Hosts), "host", "hosts"), plural(len(items), "correction", "corrections"))
			if _, err := tx.ExecContext(ctx, `INSERT INTO ds_reconciles (id, kind, baseline_id, requested_by, job_id, status, items_json, summary, created_at)
				VALUES (?, ?, ?, ?, '', 'active', ?, ?, ?)`, rid, kind, baselineID, nullStr(by.UserID), mustJSON(items), summary, now); err != nil {
				return err
			}
			q := db.New(tx)
			var err error
			sub, err = s.d.Engine.Submit(ctx, q, spec, engine.SubmitOptions{UserID: by.UserID, Origin: engine.Origin{ReconcileID: rid},
				HostSteps: hostSteps, MaxSteps: model.MaxCorrectiveSteps})
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE ds_reconciles SET job_id = ? WHERE id = ?`, sub.Job.ID, rid); err != nil {
				return err
			}
			for _, it := range items {
				if _, err := tx.ExecContext(ctx, `UPDATE ds_resource_state SET status = 'reconciling', reconcile_id = ?, job_id = ?, since = ?
					WHERE host_id = ? AND key = ?`, rid, sub.Job.ID, now, it.HostID, it.Key); err != nil {
					return err
				}
				detail := "restore requested"
				if kind == "auto" {
					detail = "automatic restore started"
				}
				he := historyEntry{HostID: it.HostID, HostName: it.HostName, Key: it.Key, Kind: "restore_requested", Status: StatusReconciling,
					PrevStatus: StatusDrift, Detail: detail + ": " + DiffText(it.Diffs), Desired: mustJSON(it.Desired), Diffs: mustJSON(it.Diffs),
					JobID: sub.Job.ID, ActorID: by.UserID, ActorName: by.Name}
				if it.Baseline != nil {
					he.BaselineID, he.BaselineRevision = it.Baseline.ID, it.Baseline.Revision
				}
				if err := writeHistory(ctx, tx, now, he); err != nil {
					return err
				}
			}
			if s.d.Audit != nil {
				summaryItems := make([]map[string]any, 0, min(len(items), 50))
				for _, it := range items[:min(len(items), 50)] {
					summaryItems = append(summaryItems, map[string]any{"host": it.HostName, "resource": it.Key, "want": it.Desired.Want()})
				}
				details := map[string]any{"reconcile_id": rid, "job_id": sub.Job.ID, "kind": kind, "hosts": len(pj.Hosts),
					"corrections": len(items), "items": summaryItems, "plan_hash": p.Hash, "allow_removals": p.Options.AllowRemovals}
				if kind == "auto" {
					details["baseline_id"] = baselineID
					details["summary"] = fmt.Sprintf("system execution via automatic restore of baseline %q, owned by %s", s.baselineName(baselineID), by.Name)
				}
				ev := audit.Event{Action: "desired.restore", TargetType: "job", TargetID: sub.Job.ID, Details: details}
				jobEv := audit.Event{Action: "job.create", TargetType: "job", TargetID: sub.Job.ID,
					Details: map[string]any{"name": pj.Name, "hosts": len(pj.Hosts), "via": "desired_state", "reconcile_id": rid}}
				if kind == "auto" {
					sys := audit.System
					ev.Actor, jobEv.Actor = &sys, &sys
					jobEv.Details["summary"] = details["summary"]
				}
				for _, e := range []audit.Event{ev, jobEv} {
					emit, err := s.d.Audit.RecordTx(ctx, q, e)
					if err != nil {
						return err
					}
					emits = append(emits, emit)
				}
			}
			return nil
		})
		if err != nil {
			if errors.Is(err, engine.ErrNothingToRun) {
				continue
			}
			var ve *engine.ValidationError
			if errors.As(err, &ve) {
				return jobIDs, &ValidationError{Msg: ve.Msg}
			}
			return jobIDs, err
		}
		for _, e := range emits {
			e()
		}
		s.d.Engine.Launch(sub.Job.ID)
		jobIDs = append(jobIDs, sub.Job.ID)
		hosts := make([]string, 0, len(pj.Hosts))
		for _, h := range pj.Hosts {
			hosts = append(hosts, h.HostID)
		}
		s.markDirty(hosts...)
	}
	return jobIDs, nil
}

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func (s *Service) baselineName(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snap != nil {
		if b, ok := s.snap.Baselines[id]; ok {
			return b.Name
		}
	}
	return id
}

// ValidationError is a user-correctable problem.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }
