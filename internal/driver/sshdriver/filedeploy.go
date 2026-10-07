package sshdriver

import (
	"context"
	"encoding/json"

	"drool/internal/driver"
	"drool/internal/filedeploy"
	"drool/internal/model"
	"drool/internal/sshx"
)

// executeFileDeploy runs a managed file deployment (ARCHITECTURE-ALPHA
// §10.7) through the transport-neutral filedeploy sequence.
func (x *Executor) executeFileDeploy(ctx context.Context, t *sshx.Target, b Become, op *model.FileDeployOp,
	out driver.OutputSink, started func()) (*driver.OpResult, error) {
	if x.Files == nil {
		return nil, &model.OpError{Class: model.ClassInternal, Message: "file deployment is not configured on this controller"}
	}
	if op.Service != nil {
		// Service actions always need root, even for a login-user deployment.
		if _, err := rootBecome(t, b); err != nil {
			return nil, err
		}
	}
	fsys, err := OpenFS(ctx, x.Pool, t, b, op.Elevated)
	if err != nil {
		if model.ClassOf(err) == model.ClassConnection {
			return nil, &driver.NotStartedError{Err: err}
		}
		return nil, err
	}
	defer fsys.Close()
	started()
	svc := func(ctx context.Context, unit, action string) error {
		rb, err := rootBecome(t, b)
		if err != nil {
			return err
		}
		state := map[string]string{"reload": "reloaded", "restart": "restarted"}[action]
		_, err = x.host(fsys.c, t, rb).Service(ctx, &model.ServiceOp{Name: unit, State: state, TimeoutS: 120}, out, func() {})
		return err
	}
	res, err := filedeploy.Run(ctx, filedeploy.Deps{
		FS: fsys, Source: x.Files.Source, Recorder: x.Files.Recorder, Service: svc,
		Notes: out.Stderr(), Output: out.Stdout(), Step: driver.StepInfoFrom(ctx),
	}, op)
	zero := 0
	changed := res.Changed
	var data map[string]any
	b2, _ := json.Marshal(res)
	json.Unmarshal(b2, &data)
	return &driver.OpResult{ExitCode: &zero, Changed: &changed, Data: data}, err
}
