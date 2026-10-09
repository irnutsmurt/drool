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

	"drool/internal/monitor/agentmon"
)

// evalInput is what an evaluation has besides stored state.
type evalInput struct {
	check *checkResult // a check that just completed
	// since: observations must be at least this recent (proof after a
	// correction); older data is "unknown", never "compliant".
	since   int64
	outcome *jobOutcome // a finished correction on this host
	// accepted: an operator accepted the current state of these keys
	// (Accept current state); the resolution is "configuration".
	accepted *acceptance
}

type acceptance struct {
	keys               map[string]bool
	actorID, actorName string
	note               string
}

// jobOutcome is a finished correction as one host saw it.
type jobOutcome struct {
	reconcileID, jobID string
	kind               string // manual | auto
	actorID, actorName string
	items              map[string]itemOutcome // key → this host's step for it
}

type itemOutcome struct {
	ran     bool   // the step ran and succeeded
	message string // why not
}

var errStale = errors.New("configuration changed during the evaluation")

// Bounded retries of automatic restore: after a failed attempt wait 15
// minutes, then an hour; the third failure pauses automatic restore for
// the resource. Drifting again three times within a day of automatic
// restores also pauses it (DROOL never fights an operator or another
// tool on the host).
var autoBackoff = []time.Duration{15 * time.Minute, time.Hour}

const (
	autoMaxAttempts = 3
	redriftLimit    = 3
	redriftWindow   = 24 * time.Hour
)

// monCache caches monitoring settings briefly (read on every evaluation).
type monCache struct {
	mu      sync.Mutex
	at      time.Time
	enabled bool
	agentS  int
	sshS    int
}

func (s *Service) monWindows(ctx context.Context) (enabled bool, agent, ssh time.Duration) {
	mons := &s.mons
	mons.mu.Lock()
	defer mons.mu.Unlock()
	if time.Since(mons.at) > 10*time.Second {
		mons.at = time.Now()
		if s.d.Monitor == nil {
			mons.enabled = false
		} else {
			st := s.d.Monitor.Settings(ctx)
			mons.enabled, mons.agentS, mons.sshS = st.Enabled, st.ServicesS, st.SSHPollS
		}
	}
	agent = agentGrace(mons.agentS)
	ssh = 3*time.Duration(mons.sshS)*time.Second + time.Minute
	if mons.sshS <= 0 {
		ssh = 16 * time.Minute
	}
	return mons.enabled, agent, ssh
}

// agentGrace is how long an agent's service data counts without a
// connected agent (it reports every change within its interval).
func agentGrace(servicesS int) time.Duration {
	return max(5*time.Minute, 4*time.Duration(servicesS)*time.Second)
}

// agentCurrent is how long a connected agent's unchanged service list
// stays current: the agent re-sends it at least every
// agentmon.ServicesResend and sends any change at once (a stopped unit's
// boot state within 10 minutes, at once when asked).
const agentCurrent = agentmon.ServicesResend + 5*time.Minute

// svcData is a host's service data from monitoring.
type svcData struct {
	present   bool
	source    string
	at        int64
	supported bool
	note      string
	units     map[string]svcUnit
	// The host's agent (agent-sourced data): connected now, or when it
	// was last seen (controller time).
	agentConnected bool
	agentSeen      int64
}

type svcUnit struct {
	load, active, sub, fileState string
	known                        bool
}

func loadServices(ctx context.Context, q queryer, hostID string, units []string) (svcData, error) {
	var d svcData
	var sup int
	err := q.QueryRowContext(ctx, `SELECT source, services_at, services_supported, services_note FROM mon_current WHERE host_id = ?`, hostID).
		Scan(&d.source, &d.at, &sup, &d.note)
	if errors.Is(err, sql.ErrNoRows) {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	d.present, d.supported = true, sup == 1
	if d.source != "ssh" {
		var conn int
		var seen sql.NullInt64
		if err := q.QueryRowContext(ctx, `SELECT connected, last_seen_at FROM agent_identities WHERE host_id = ? AND status = 'active'`, hostID).
			Scan(&conn, &seen); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return d, err
		}
		d.agentConnected, d.agentSeen = conn == 1, seen.Int64
	}
	d.units = map[string]svcUnit{}
	for len(units) > 0 {
		n := min(len(units), 200)
		args := []any{hostID}
		for _, u := range units[:n] {
			args = append(args, u)
		}
		rows, err := q.QueryContext(ctx, `SELECT unit, load_state, active_state, sub_state, file_state, known FROM mon_services
			WHERE host_id = ? AND unit IN (?`+strings.Repeat(",?", n-1)+`)`, args...)
		if err != nil {
			return d, err
		}
		for rows.Next() {
			var name string
			var u svcUnit
			var known int
			if err := rows.Scan(&name, &u.load, &u.active, &u.sub, &u.fileState, &known); err != nil {
				rows.Close()
				return d, err
			}
			u.known = known == 1
			d.units[name] = u
		}
		rows.Close()
		units = units[n:]
	}
	return d, nil
}

// observeService turns monitoring data into an observation, or says why
// it can't be trusted (hostWide: the reason applies to the whole host).
func (s *Service) observeService(ctx context.Context, d svcData, unit string, now, since int64) (Observation, bool, string, bool) {
	enabled, agentWin, sshWin := s.monWindows(ctx)
	switch {
	case !enabled:
		return Observation{}, false, "monitoring is turned off (Settings → Monitoring); desired service state is read from monitoring data", true
	case !d.present || d.at == 0:
		return Observation{}, false, "no service data from this host yet", true
	case !d.supported:
		why := d.note
		if why == "" {
			why = "this host reports no service inventory"
		}
		return Observation{}, false, why, true
	}
	if since > 0 && d.at < since {
		return Observation{}, false, "no fresh service data after the correction", true
	}
	age := now - d.at
	switch {
	case d.source == "ssh":
		if age > sshWin.Milliseconds() {
			return Observation{}, false, "no fresh service data since " + clock(d.at) + " (the host may be unreachable)", true
		}
	case age <= agentWin.Milliseconds():
	case age <= agentCurrent.Milliseconds() && (d.agentConnected || now-d.agentSeen <= agentWin.Milliseconds()):
		// A connected agent's list is current until it says otherwise.
	case !d.agentConnected && d.agentSeen > 0:
		return Observation{}, false, "the agent is not connected (last seen " + clock(d.agentSeen) + ")", true
	default:
		return Observation{}, false, "no fresh service data since " + clock(d.at) + " (the host may be unreachable)", true
	}
	u, ok := d.units[unit]
	if ok && !u.known {
		return Observation{}, false, "the unit's state is unknown (monitoring lost contact with the host)", true
	}
	o := Observation{Source: "monitoring", At: d.at, Service: &ServiceObs{Found: ok}}
	if ok {
		o.Service.Load, o.Service.Active, o.Service.Sub, o.Service.FileState = u.load, u.active, u.sub, u.fileState
	}
	return o, true, "", false
}

func clock(ms int64) string { return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04 UTC") }

// diffSig identifies a set of differences.
func diffSig(diffs []Diff) string {
	if len(diffs) == 0 {
		return ""
	}
	h := sha256.New()
	for _, d := range diffs {
		fmt.Fprintf(h, "%s=%s|%s;", d.Field, d.Desired, d.Current)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// DiffText summarizes differences for one line: "state: running → failed".
func DiffText(diffs []Diff) string {
	parts := make([]string, 0, len(diffs))
	for _, d := range diffs {
		des, cur := d.Desired, d.Current
		if d.Field == "content" {
			des, cur = shortSHA(des), shortSHA(cur)
		}
		parts = append(parts, d.Field+": "+des+" → "+cur)
	}
	return strings.Join(parts, "; ")
}

func shortSHA(s string) string {
	if strings.HasPrefix(s, "sha256 ") {
		return "sha256 " + short(strings.TrimPrefix(s, "sha256 "))
	}
	return s
}

// hostEvents collects what happened on hosts for the event timeline.
type eventBatch struct {
	mu        sync.Mutex
	drift     map[string][]string // host name → keys newly drifted
	conflicts map[string][]string
	paused    []string // automatic restore paused (titles)
}

func (b *eventBatch) addDrift(host, key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.drift == nil {
		b.drift = map[string][]string{}
	}
	b.drift[host] = append(b.drift[host], key)
}

func (b *eventBatch) addConflict(host, key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conflicts == nil {
		b.conflicts = map[string][]string{}
	}
	b.conflicts[host] = append(b.conflicts[host], key)
}

func (s *Service) hostName(hostID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snap != nil {
		if n, ok := s.snap.Hosts[hostID]; ok {
			return n
		}
	}
	return hostID
}

// evaluate compares a host's effective desired state with what DROOL
// observed and records what changed. It runs inside one write
// transaction, against the configuration generation it was computed
// from (a newer configuration is loaded first, never written over).
func (s *Service) evaluate(ctx context.Context, hostID string, in evalInput) error {
	err := s.evaluateOnce(ctx, hostID, in)
	if errors.Is(err, errStale) {
		if err := s.reload(ctx, true); err != nil {
			return err
		}
		err = s.evaluateOnce(ctx, hostID, in)
		if errors.Is(err, errStale) {
			s.markDirty(hostID)
			return nil
		}
	}
	return err
}

func (s *Service) evaluateOnce(ctx context.Context, hostID string, in evalInput) error {
	s.mu.Lock()
	snap := s.snap
	hs, managed := s.eff[hostID]
	s.mu.Unlock()
	if snap == nil {
		return nil
	}
	hostName := s.hostName(hostID)
	now := s.nowMs()
	var newDrift, newConflict []string
	err := s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
		if g, err := generation(ctx, tx); err != nil {
			return err
		} else if g != snap.Gen {
			return errStale
		}
		rows, err := loadRows(ctx, tx, hostID)
		if err != nil {
			return err
		}
		host, err := loadHost(ctx, tx, hostID)
		if err != nil {
			return err
		}
		if !managed || len(hs.Resources) == 0 {
			for key, r := range rows {
				if r.Status != StatusCompliant && r.Status != StatusUnknown {
					if err := writeHistory(ctx, tx, now, historyEntry{HostID: hostID, HostName: hostName, Key: key, Kind: "no_longer_managed",
						PrevStatus: r.Status, Resolution: "configuration", Detail: "no longer managed on this host", Desired: r.Desired}); err != nil {
						return err
					}
				}
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM ds_resource_state WHERE host_id = ?`, hostID); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `DELETE FROM ds_host_state WHERE host_id = ?`, hostID)
			return err
		}
		prevHost := host
		if c := in.check; c != nil {
			if c.pkgChecked {
				if c.pkgNote == "" {
					host.PackagesAt, host.PackagesNote = nullInt(now), ""
				} else {
					host.PackagesNote = c.pkgNote
				}
			}
			if c.filesChecked {
				if c.filesNote == "" {
					host.FilesAt, host.FilesNote = nullInt(now), ""
				} else {
					host.FilesNote = c.filesNote
				}
			}
		}
		var units []string
		for k, e := range hs.Resources {
			if e.Kind == KindService {
				units = append(units, KeyName(k))
			}
		}
		sort.Strings(units)
		var svc svcData
		if len(units) > 0 {
			if svc, err = loadServices(ctx, tx, hostID, units); err != nil {
				return err
			}
		}
		checkWin := (2*s.cfg.CheckEvery + 5*time.Minute).Milliseconds()
		var c counts
		hostWideUnknown := ""
		keys := make([]string, 0, len(hs.Resources))
		for k := range hs.Resources {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			e := hs.Resources[key]
			row := rows[key]
			if row == nil {
				row = &resRow{Key: key, Kind: e.Kind, Since: now}
			}
			prev := *row
			desired := ""
			if !e.Conflict {
				desired = mustJSON(e.Resource)
			}
			row.Kind, row.Desired, row.Source = e.Kind, desired, mustJSON(e.Source)

			// What DROOL knows about this resource now.
			var obs Observation
			have, why, hostWide := false, "", false
			if e.Kind == KindService {
				obs, have, why, hostWide = s.observeService(ctx, svc, KeyName(key), now, in.since)
				if have {
					if b := mustJSON(obs.Service); b != observedPart(row.Observed, "service") {
						row.Observed, row.ObservedAt = mustJSON(obs), nullInt(obs.At)
					}
				}
			} else {
				if c := in.check; c != nil {
					if o, ok := c.obs[key]; ok && !sameObservation(row.Observed, o) {
						row.Observed, row.ObservedAt = mustJSON(o), nullInt(o.At)
					}
				}
				at, note := host.PackagesAt, host.PackagesNote
				if e.Kind == KindFile {
					at, note = host.FilesAt, host.FilesNote
				}
				if row.Observed != "" && json.Unmarshal([]byte(row.Observed), &obs) == nil {
					have = true
				}
				switch {
				case have && obs.Error != "":
					have, why = false, "couldn't check: "+obs.Error
				case !have && note != "":
					why, hostWide = "couldn't check: "+note, true
				case !have:
					why = "not checked yet"
				case in.since > 0 && (!at.Valid || at.Int64 < in.since):
					have, why, hostWide = false, "couldn't check the host after the correction", true
					if note != "" {
						why += ": " + note
					}
				case !at.Valid || now-at.Int64 > checkWin:
					have, hostWide = false, true
					why = "not checked since " + clock(row.ObservedAt.Int64)
					if at.Valid {
						why = "not checked since " + clock(at.Int64)
					}
					if note != "" {
						why += ": " + note
					}
				}
			}

			// Its status.
			var diffs []Diff
			status := ""
			blocker := ""
			reconciling := prev.Status == StatusReconciling && prev.JobID.Valid
			switch {
			case e.Conflict:
				status = StatusConflict
			case !have:
				status = StatusUnknown
			default:
				j := Compare(*e.Resource, obs)
				diffs, blocker = j.Diffs, j.Blocker
				status = StatusDrift
				if j.Compliant() {
					status = StatusCompliant
				}
			}
			out, isOutcome := itemOutcome{}, false
			if in.outcome != nil {
				out, isOutcome = in.outcome.items[key]
			}
			if reconciling && !isOutcome {
				status = StatusReconciling // until its job has finished and the host was checked
			}
			if status == StatusDrift && prev.Status == StatusFailed && diffSig(diffs) == prev.Sig {
				status = StatusFailed // the failed correction's drift, unchanged
			}
			row.Diffs = mustJSON(nonNilDiffs(diffs))
			row.Note = why
			switch {
			case blocker != "":
				row.Note = blocker
			case status == StatusFailed && !isOutcome:
				row.Note = prev.Note // why the correction failed, until something changes
			}

			kind, resolution, detail := "", "", ""
			actorID, actorName, jobID := "", "", ""
			prevKnown := prev.Status
			if prevKnown == StatusUnknown {
				prevKnown = prev.LastKnown
			}
			desiredChanged := prev.exists && prev.Desired != desired
			switch {
			case isOutcome:
				jobID = in.outcome.jobID
				actorID, actorName = in.outcome.actorID, in.outcome.actorName
				row.ReconcileID, row.JobID, row.LastJobID = sql.NullString{}, sql.NullString{}, in.outcome.jobID
				switch status {
				case StatusCompliant:
					switch {
					case out.ran:
						kind, resolution, detail = "restored", "restored", "restored by a corrective job; a fresh check after the job proved it"
						row.RestoredAt = nullInt(now)
						row.AutoAttempts, row.AutoNextAt = 0, sql.NullInt64{}
					case out.message != "":
						// Compliant now, but DROOL can't say the job did it.
						kind, resolution, detail = "compliant", "observed", "compliant when checked after the correction, whose own outcome isn't known: "+out.message
					default:
						kind, resolution, detail = "compliant", "observed", "compliant (the correction didn't change it)"
					}
				case StatusUnknown:
					kind, detail = "restore_unverified", "the correction finished, but DROOL couldn't check the host afterwards: "+why
					row.LastKnown = StatusDrift
				case StatusConflict:
					kind, detail = "conflict_detected", "the desired state changed into a conflict during the correction"
				default:
					status = StatusFailed
					msg := out.message
					if msg == "" {
						msg = "the correction ran, but the host still differs: " + DiffText(diffs)
					}
					kind, detail = "restore_failed", msg
					row.Note = msg
					if in.outcome.kind == "auto" {
						row.AutoAttempts++
						if row.AutoAttempts >= autoMaxAttempts {
							row.AutoPaused = fmt.Sprintf("automatic restore failed %d times; paused — needs attention", row.AutoAttempts)
							row.AutoNextAt = sql.NullInt64{}
							s.events.mu.Lock()
							s.events.paused = append(s.events.paused, fmt.Sprintf("Automatic restore paused for %s on %s: it failed %d times", Describe(key), hostName, row.AutoAttempts))
							s.events.mu.Unlock()
						} else {
							row.AutoNextAt = nullInt(now + autoBackoff[min(row.AutoAttempts-1, len(autoBackoff)-1)].Milliseconds())
						}
					}
				}
			case status == StatusReconciling:
			case status == StatusUnknown:
				if prev.Status != StatusUnknown && prev.Status != "" {
					row.LastKnown = prev.Status
				}
				switch {
				case hostWide:
					if hostWideUnknown == "" {
						hostWideUnknown = why
					}
				case prev.Status == "" || prev.Status == StatusUnknown && prev.LastKnown == "":
					// Not checked yet: not a transition worth recording.
				case prev.Status != StatusUnknown || prev.Note != why:
					kind, detail = "unknown", why
				}
			default:
				from := prevKnown
				sameAsBefore := from == status && (status == StatusCompliant || status == StatusConflict || diffSig(diffs) == prev.Sig)
				switch {
				case sameAsBefore && !desiredChanged:
				case in.accepted != nil && in.accepted.keys[key] && status == StatusCompliant:
					kind, resolution = "accepted", "configuration"
					detail = "current state accepted as this host's desired state (not a restore: the host didn't change)"
					if in.accepted.note != "" {
						detail += ": " + in.accepted.note
					}
					actorID, actorName = in.accepted.actorID, in.accepted.actorName
				case status == StatusCompliant && (from == StatusDrift || from == StatusFailed || from == StatusConflict || from == StatusReconciling):
					switch {
					case desiredChanged || from == StatusConflict:
						kind, resolution, detail = "resolved_by_configuration", "configuration", "the desired state changed; the host didn't"
					default:
						kind, resolution, detail = "compliant", "observed", "matches its desired state again (changed on the host, not by a DROOL restore)"
					}
				case status == StatusCompliant:
				case status == StatusConflict:
					kind, detail = "conflict_detected", e.Source.Label
					newConflict = append(newConflict, key)
				case from == StatusConflict:
					kind, detail = "conflict_resolved", "the conflict was resolved; the host differs: "+DiffText(diffs)
				case (status == StatusDrift || status == StatusFailed) && (from == StatusDrift || from == StatusFailed):
					if diffSig(diffs) != prev.Sig || desiredChanged {
						kind, detail = "drift_changed", DiffText(diffs)
					}
				default:
					kind, detail = "drift_detected", DiffText(diffs)
					newDrift = append(newDrift, key)
					// Drifting again soon after an automatic restore: count it,
					// and stop fighting when it keeps happening.
					if e.Source.Auto && prev.RestoredAt.Valid && now-prev.RestoredAt.Int64 < redriftWindow.Milliseconds() {
						if !row.RedriftSince.Valid || now-row.RedriftSince.Int64 > redriftWindow.Milliseconds() {
							row.Redrifts, row.RedriftSince = 0, nullInt(now)
						}
						row.Redrifts++
						if row.Redrifts >= redriftLimit && row.AutoPaused == "" {
							row.AutoPaused = fmt.Sprintf("drifted again %d times within a day of being restored; something on the host keeps changing it — paused, needs attention", row.Redrifts)
							s.events.mu.Lock()
							s.events.paused = append(s.events.paused, fmt.Sprintf("Automatic restore paused for %s on %s: it keeps drifting back", Describe(key), hostName))
							s.events.mu.Unlock()
						}
					}
				}
			}
			if status != StatusUnknown {
				row.LastKnown = ""
			}
			if status == StatusDrift || status == StatusFailed {
				row.Sig = diffSig(diffs)
			} else if status != StatusReconciling {
				row.Sig = ""
			}
			if status != prev.Status {
				row.Since = now
			}
			row.Status = status
			c.add(status, row.AutoPaused != "")
			if kind != "" {
				ref := e.SourceBaseline()
				if ref == nil && len(e.Source.Baselines) > 0 {
					ref = &e.Source.Baselines[0] // a conflict: the first baseline in it
				}
				he := historyEntry{HostID: hostID, HostName: hostName, Key: key, Kind: kind, Status: status, PrevStatus: prev.Status,
					Resolution: resolution, Detail: detail, Desired: desired, Observed: row.Observed, Diffs: row.Diffs,
					SourceLabel: e.Source.Label, JobID: jobID, ActorID: actorID, ActorName: actorName}
				if ref != nil {
					he.BaselineID, he.BaselineRevision = ref.ID, ref.Revision
				}
				if err := writeHistory(ctx, tx, now, he); err != nil {
					return err
				}
			}
			if !prev.exists || rowChanged(prev, *row) {
				if err := saveRow(ctx, tx, hostID, row); err != nil {
					return err
				}
			}
		}
		for key, r := range rows {
			if _, ok := hs.Resources[key]; ok {
				continue
			}
			if r.Status == StatusDrift || r.Status == StatusFailed || r.Status == StatusConflict {
				if err := writeHistory(ctx, tx, now, historyEntry{HostID: hostID, HostName: hostName, Key: key, Kind: "no_longer_managed",
					PrevStatus: r.Status, Resolution: "configuration", Detail: "no longer managed on this host (resolved by configuration)",
					Desired: r.Desired}); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM ds_resource_state WHERE host_id = ? AND key = ?`, hostID, key); err != nil {
				return err
			}
		}
		// Host level: unknown because the host couldn't be observed is one
		// entry for the host, not one per resource.
		switch {
		case hostWideUnknown != "" && !host.UnknownSince.Valid:
			host.UnknownSince = nullInt(now)
			if err := writeHistory(ctx, tx, now, historyEntry{HostID: hostID, HostName: hostName, Kind: "unknown", Status: StatusUnknown,
				Detail: hostWideUnknown}); err != nil {
				return err
			}
		case hostWideUnknown == "" && host.UnknownSince.Valid:
			host.UnknownSince = sql.NullInt64{}
			if err := writeHistory(ctx, tx, now, historyEntry{HostID: hostID, HostName: hostName, Kind: "observed",
				Detail: "DROOL can observe this host's desired state again"}); err != nil {
				return err
			}
		}
		host.EffHash, host.C, host.Status = hs.Hash, c, hostStatus(c)
		if !prevHost.exists || host.Status != prevHost.Status || host.C != prevHost.C || host.EffHash != prevHost.EffHash {
			host.ChangedAt = now
		}
		if !prevHost.exists || host != prevHost {
			return saveHost(ctx, tx, hostID, host)
		}
		return nil
	})
	if err == nil {
		for _, k := range newDrift {
			s.events.addDrift(hostName, k)
		}
		for _, k := range newConflict {
			s.events.addConflict(hostName, k)
		}
	}
	return err
}

func nonNilDiffs(d []Diff) []Diff {
	if d == nil {
		return []Diff{}
	}
	return d
}

// observedPart extracts one kind's observation from stored JSON (to avoid
// rewriting a row when only the timestamp changed).
func observedPart(stored, kind string) string {
	if stored == "" {
		return ""
	}
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(stored), &m) != nil {
		return ""
	}
	return string(m[kind])
}

// sameObservation compares a new observation with the stored one,
// ignoring when it was made.
func sameObservation(stored string, o Observation) bool {
	if stored == "" {
		return false
	}
	var old Observation
	if json.Unmarshal([]byte(stored), &old) != nil {
		return false
	}
	old.At, o.At = 0, 0
	return mustJSON(old) == mustJSON(o)
}

func rowChanged(a, b resRow) bool {
	a.exists, b.exists = false, false
	return a != b
}
