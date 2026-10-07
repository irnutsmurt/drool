package filedeploy

import (
	"context"
	"database/sql"
	"encoding/json"

	"drool/internal/audit"
	"drool/internal/ids"
	"drool/internal/model"
	"drool/internal/store"
	"drool/internal/store/db"
)

// StoreRecorder keeps deployment history in SQLite and audits each outcome
// in the same transaction. Audit records carry paths, hashes and
// ownership/mode, never file content.
type StoreRecorder struct {
	Store *store.Store
	Audit *audit.Writer
}

func js(v any) string {
	if v == nil {
		return "{}"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func (r *StoreRecorder) Record(ctx context.Context, rec *Record) error {
	res := rec.Result
	id := ids.New(ids.FileDeployment)
	res.DeploymentID = id
	kind := "deploy"
	if rec.Op.RollbackOf != "" {
		kind = "rollback"
	}
	errText := ""
	if rec.Err != nil {
		errText = model.AsOpError(rec.Err).Error()
	}
	var after *FileState
	afterSHA, afterArtifact := "", ""
	if res.After != nil {
		after = res.After
		afterSHA = res.After.SHA256
		if res.Outcome == Installed {
			afterArtifact = rec.Op.Artifact // the deployed content is in the store
		}
	}
	beforeArtifact := ""
	if res.Before.Captured {
		beforeArtifact = res.Before.SHA256
	}
	var emit func()
	err := r.Store.InTx(ctx, func(q *db.Queries) error {
		if err := q.InsertFileDeployment(ctx, db.InsertFileDeploymentParams{
			ID: id, JobID: nullStr(rec.Step.JobID), HostID: rec.Step.HostID, Path: res.Path, Kind: kind,
			RollbackOf: nullStr(rec.Op.RollbackOf), Outcome: res.Outcome, BeforeExists: boolInt(res.Before.Exists),
			BeforeSha256: res.Before.SHA256, BeforeArtifact: nullStr(beforeArtifact), BeforeMeta: js(res.Before.Meta),
			AfterSha256: afterSHA, AfterArtifact: nullStr(afterArtifact), AfterMeta: js(metaOrNil(after)),
			Validation: js(res.Validation), Service: js(res.Service), Error: errText, OpJson: js(rec.Op),
			CreatedBy: nullStr(rec.Step.UserID), CreatedAt: store.Now(),
		}); err != nil {
			return err
		}
		actor := audit.System
		if rec.Step.UserID != "" {
			actor = audit.Actor{Type: audit.ActorUser, ID: rec.Step.UserID}
			if u, err := q.GetUser(ctx, rec.Step.UserID); err == nil {
				actor.Name = u.Username
			}
		}
		result := audit.Success
		if rec.Err != nil {
			result = audit.Failure
		}
		action := "file.deploy"
		if kind == "rollback" {
			action = "file.rollback"
		}
		details := map[string]any{
			"path": res.Path, "outcome": res.Outcome, "job_id": rec.Step.JobID, "deployment_id": id,
			"before_exists": res.Before.Exists, "before_sha256": res.Before.SHA256, "before": res.Before.Meta,
			"artifact": rec.Op.Artifact, "elevated": rec.Op.Elevated,
		}
		if after != nil {
			details["after_sha256"], details["after"] = after.SHA256, after.Meta
		}
		if res.Validation != nil {
			details["validation_exit"] = res.Validation.ExitCode
		}
		if res.Service != nil {
			details["service"] = res.Service
		}
		if rec.Op.RollbackOf != "" {
			details["rollback_of"] = rec.Op.RollbackOf
		}
		if errText != "" {
			details["error"] = errText
		}
		var err error
		emit, err = r.Audit.RecordTx(ctx, q, audit.Event{Action: action, Actor: &actor, Result: result,
			TargetType: "host", TargetID: rec.Step.HostID, Details: details})
		return err
	})
	if err == nil {
		emit()
	}
	return err
}

func metaOrNil(f *FileState) any {
	if f == nil {
		return nil
	}
	return f.Meta
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
