package desired

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"drool/internal/filedeploy"
	"drool/internal/files"
	"drool/internal/hostops"
)

// Accept current state means "what the host has now is what I intend".
// It is policy, never a restore: it writes a host-specific declaration
// (the most specific scope) and changes no baseline. The host doesn't
// change; its history says "resolved by configuration".

// AcceptResult is what accepting recorded.
type AcceptResult struct {
	Accepted []AcceptedItem `json:"accepted"`
}

// AcceptedItem is one host-specific declaration created or updated.
type AcceptedItem struct {
	Key      string    `json:"key"`
	Was      string    `json:"was"` // the previous effective declaration, in words ("conflict" for one)
	Now      string    `json:"now"`
	Resource *Resource `json:"resource,omitempty"`
	Excluded bool      `json:"excluded,omitempty"`
}

// Accept makes the current state of keys on a host its desired state.
// The host is checked first (fresh data); unknown state can't be accepted.
func (s *Service) Accept(ctx context.Context, hostID string, keys []string, note string, by Actor) (*AcceptResult, error) {
	note = strings.TrimSpace(note)
	if len(note) > 500 {
		return nil, &ValidationError{Msg: "the note is too long"}
	}
	if len(keys) == 0 || len(keys) > 200 {
		return nil, &ValidationError{Msg: "choose 1 to 200 resources"}
	}
	s.fresh(ctx)
	hs, ok := s.effective(hostID)
	if !ok {
		return nil, ErrNotFound
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, ok := hs.Resources[k]; !ok {
			return nil, &ValidationError{Msg: Describe(k) + " is not managed on this host"}
		}
	}
	// Prove the current state first.
	s.checkAndEvaluate(ctx, hostID, true, 0)
	hs, _ = s.effective(hostID)
	rows, err := loadRows(ctx, s.d.Store.Reader, hostID)
	if err != nil {
		return nil, err
	}
	snap := s.Snapshot()
	type decl struct {
		key      string
		res      *Resource
		excluded bool
		was      string
	}
	var decls []decl
	for _, k := range keys {
		e := hs.Resources[k]
		r := rows[k]
		if r == nil || r.Status == StatusUnknown || r.Observed == "" {
			why := "not observed yet"
			if r != nil && r.Note != "" {
				why = r.Note
			}
			return nil, &ValidationError{Msg: fmt.Sprintf("%s: DROOL can't verify its current state (%s), so it can't be accepted", Describe(k), why)}
		}
		if r.Status == StatusReconciling {
			return nil, &ValidationError{Msg: Describe(k) + ": a correction is in progress; wait for it to finish"}
		}
		if r.Status == StatusCompliant {
			return nil, &ValidationError{Msg: Describe(k) + " already matches its desired state"}
		}
		var obs Observation
		if err := json.Unmarshal([]byte(r.Observed), &obs); err != nil || obs.Error != "" {
			return nil, &ValidationError{Msg: Describe(k) + ": its current state couldn't be read"}
		}
		was := "conflict (" + e.Source.Label + ")"
		var tmpl Resource // which fields are managed (and how a file is deployed)
		if e.Resource != nil {
			was, tmpl = e.Resource.Want(), *e.Resource
		} else {
			tmpl = competingTemplate(snap, e)
		}
		d := decl{key: k, was: was}
		switch e.Kind {
		case KindService:
			nr := Resource{Kind: KindService, Name: KeyName(k)}
			so := obs.Service
			if so == nil {
				return nil, &ValidationError{Msg: Describe(k) + ": no service state observed"}
			}
			missing := !so.Found || so.Load == "not-found"
			if tmpl.State != "" {
				nr.State = StateStopped
				if !missing && hostops.UnitRunning(so.Active) {
					nr.State = StateRunning
				}
			}
			if tmpl.Enabled != nil {
				en := !missing && hostops.UnitEnabled(so.FileState)
				nr.Enabled = &en
			}
			d.res = &nr
		case KindPackage:
			nr := Resource{Kind: KindPackage, Name: KeyName(k), State: StateAbsent}
			if obs.Package != nil && obs.Package.Installed {
				nr.State = StateInstalled
			}
			d.res = &nr
		case KindFile:
			fo := obs.File
			switch {
			case fo == nil:
				return nil, &ValidationError{Msg: Describe(k) + ": no file state observed"}
			case !fo.Exists:
				d.excluded = true // "this host has no such file": not managed here
			case fo.Type != string(files.KindFile):
				return nil, &ValidationError{Msg: fmt.Sprintf("%s is a %s on the host; DROOL only manages regular files, so it can't be accepted (change the baseline instead)", KeyName(k), fo.Type)}
			default:
				tmpl.Kind, tmpl.Path = KindFile, KeyName(k)
				nr, err := s.captureFile(ctx, hostID, tmpl, *fo, by)
				if err != nil {
					return nil, err
				}
				d.res = nr
			}
		}
		if d.res != nil {
			if err := d.res.Normalize(); err != nil {
				return nil, &ValidationError{Msg: Describe(k) + ": " + err.Error()}
			}
		}
		decls = append(decls, d)
	}
	res := &AcceptResult{}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		now := s.nowMs()
		for _, d := range decls {
			rj, excl, now2 := "", 0, "not managed on this host"
			if d.excluded {
				excl = 1
			} else {
				rj, now2 = mustJSON(d.res), d.res.Want()
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO ds_host_overrides (host_id, key, resource_json, excluded, origin, note, version, created_by, created_at, updated_by, updated_at)
				VALUES (?, ?, ?, ?, 'accept', ?, 1, ?, ?, ?, ?)
				ON CONFLICT(host_id, key) DO UPDATE SET resource_json = excluded.resource_json, excluded = excluded.excluded, origin = 'accept',
				note = excluded.note, version = ds_host_overrides.version + 1, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
				hostID, d.key, rj, excl, note, nullStr(by.UserID), now, nullStr(by.UserID), now); err != nil {
				return err
			}
			res.Accepted = append(res.Accepted, AcceptedItem{Key: d.key, Was: d.was, Now: now2, Resource: d.res, Excluded: d.excluded})
			det := map[string]any{"host": s.hostName(hostID), "resource": d.key, "was": d.was, "now": now2, "note": note}
			if d.res != nil && d.res.Kind == KindFile {
				det["artifact"] = d.res.Artifact
			}
			if err := s.auditTx(ctx, tx, "desired.accept", "host", hostID, det); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := s.reload(ctx, true); err != nil {
		return nil, err
	}
	acc := &acceptance{keys: map[string]bool{}, actorID: by.UserID, actorName: by.Name, note: note}
	for _, k := range keys {
		acc.keys[k] = true
	}
	if err := s.evaluate(ctx, hostID, evalInput{accepted: acc}); err != nil {
		return nil, err
	}
	return res, nil
}

// competingTemplate is what a conflict's declarations manage together
// (accepting a conflict decides every field any of them manages).
func competingTemplate(snap *Snapshot, e Effective) Resource {
	t := Resource{Kind: e.Kind}
	if snap == nil {
		return t
	}
	for _, c := range e.Source.Competing {
		if c.Baseline == nil {
			continue
		}
		b := snap.Baselines[c.Baseline.ID]
		if b == nil {
			continue
		}
		r, ok := b.Resources[e.Key]
		if !ok {
			continue
		}
		if r.State != "" {
			t.State = r.State
		}
		if r.Enabled != nil {
			t.Enabled = r.Enabled
		}
		if r.Kind == KindFile {
			if t.Owner == "" {
				t.Owner = r.Owner
			}
			if t.Group == "" {
				t.Group = r.Group
			}
			if t.Mode == "" {
				t.Mode = r.Mode
			}
			t.Elevated = t.Elevated || r.Elevated
			if t.Validate == "" {
				t.Validate = r.Validate
			}
			if t.Service == nil {
				t.Service = r.Service
			}
		}
	}
	return t
}

// captureFile copies a host's current file into the artifact store (the
// same limits as a deployment's rollback copy) and returns the
// declaration that makes it the desired content.
func (s *Service) captureFile(ctx context.Context, hostID string, tmpl Resource, fo FileObs, by Actor) (*Resource, error) {
	p := tmpl.Path
	if fo.Size > filedeploy.MaxCapture {
		return nil, &ValidationError{Msg: fmt.Sprintf("%s is %d MiB; DROOL keeps managed content up to %d MiB, so it can't be accepted (change the baseline instead)",
			p, fo.Size>>20, filedeploy.MaxCapture>>20)}
	}
	if s.d.Artifacts == nil || s.d.Prober == nil {
		return nil, &ValidationError{Msg: "artifact storage is not configured"}
	}
	fsys, err := s.d.Prober.OpenFS(ctx, hostID, tmpl.Elevated)
	if err != nil {
		return nil, &ValidationError{Msg: "couldn't read " + p + ": " + errText(err)}
	}
	defer fsys.Close()
	rc, info, err := fsys.Open(ctx, p)
	if err != nil {
		return nil, &ValidationError{Msg: "couldn't read " + p + ": " + errText(err)}
	}
	defer rc.Close()
	a, err := s.d.Artifacts.Put(ctx, io.LimitReader(rc, filedeploy.MaxCapture+1), path.Base(p), by.UserID)
	if err != nil {
		return nil, &ValidationError{Msg: "couldn't keep " + p + ": " + errText(err)}
	}
	if a.SHA256 != fo.SHA256 {
		return nil, &ValidationError{Msg: p + " changed while DROOL was reading it; check again and retry"}
	}
	r := tmpl
	r.Kind, r.Path, r.Artifact, r.ArtifactName = KindFile, p, a.SHA256, path.Base(p)+" (accepted from "+s.hostName(hostID)+")"
	// Metadata that was managed keeps being managed, as it is now.
	if tmpl.Owner != "" {
		r.Owner = info.Owner
	}
	if tmpl.Group != "" {
		r.Group = info.Group
	}
	if tmpl.Mode != "" {
		r.Mode = files.FormatMode(info.Mode)
	}
	return &r, nil
}

// SetOverride sets a host-specific declaration explicitly (for example to
// settle a conflict for one host), or excludes a resource on this host.
func (s *Service) SetOverride(ctx context.Context, hostID string, r *Resource, key string, excluded bool, note string, by Actor) error {
	note = strings.TrimSpace(note)
	if len(note) > 500 {
		return &ValidationError{Msg: "the note is too long"}
	}
	s.fresh(ctx)
	snap := s.Snapshot()
	if snap == nil {
		return errors.New("desired state is starting")
	}
	if _, ok := snap.Hosts[hostID]; !ok {
		return ErrNotFound
	}
	rj := ""
	if excluded {
		if KeyKind(key) != KindService && KeyKind(key) != KindPackage && KeyKind(key) != KindFile || KeyName(key) == "" {
			return &ValidationError{Msg: "name the resource to exclude (service:<unit>, package:<name> or file:<path>)"}
		}
	} else {
		if r == nil {
			return &ValidationError{Msg: "give the resource's desired state"}
		}
		if err := r.Normalize(); err != nil {
			return &ValidationError{Msg: err.Error()}
		}
		if r.Kind == KindFile {
			if s.d.Artifacts == nil {
				return &ValidationError{Msg: "artifact storage is not configured"}
			}
			if _, err := s.d.Artifacts.Get(ctx, r.Artifact); err != nil {
				return &ValidationError{Msg: "the file's content is not stored on the controller; upload it first"}
			}
		}
		key, rj = r.Key(), mustJSON(r)
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		now := s.nowMs()
		excl := 0
		if excluded {
			excl = 1
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO ds_host_overrides (host_id, key, resource_json, excluded, origin, note, version, created_by, created_at, updated_by, updated_at)
			VALUES (?, ?, ?, ?, 'manual', ?, 1, ?, ?, ?, ?)
			ON CONFLICT(host_id, key) DO UPDATE SET resource_json = excluded.resource_json, excluded = excluded.excluded, origin = 'manual',
			note = excluded.note, version = ds_host_overrides.version + 1, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
			hostID, key, rj, excl, note, nullStr(by.UserID), now, nullStr(by.UserID), now); err != nil {
			return err
		}
		det := map[string]any{"host": s.hostName(hostID), "resource": key, "excluded": excluded, "note": note}
		if r != nil {
			det["want"] = r.Want()
		}
		return s.auditTx(ctx, tx, "desired.override.set", "host", hostID, det)
	})
	if err != nil {
		return err
	}
	s.afterChange(ctx)
	return nil
}

// ResetOverride removes a host-specific declaration: the host inherits
// again (and may drift).
func (s *Service) ResetOverride(ctx context.Context, hostID, key string) error {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var rj string
		var excl int
		if err := tx.QueryRowContext(ctx, `SELECT resource_json, excluded FROM ds_host_overrides WHERE host_id = ? AND key = ?`, hostID, key).Scan(&rj, &excl); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM ds_host_overrides WHERE host_id = ? AND key = ?`, hostID, key); err != nil {
			return err
		}
		return s.auditTx(ctx, tx, "desired.override.remove", "host", hostID, map[string]any{"host": s.hostName(hostID), "resource": key,
			"was": json.RawMessage(nonEmptyJSON(rj)), "was_excluded": excl == 1})
	})
	if err != nil {
		return err
	}
	s.afterChange(ctx)
	return nil
}

func nonEmptyJSON(s string) string {
	if s == "" {
		return "null"
	}
	return s
}
