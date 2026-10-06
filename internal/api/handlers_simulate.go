package api

import (
	"context"
	"net/http"
	"sync"
	"time"

	"drool/internal/engine"
	"drool/internal/model"
	"drool/internal/ops/pkg"
)

type simulateRequest struct {
	Targets model.TargetSpec `json:"targets"`
	Step    model.Operation  `json:"step"`
}

type simErrorJSON struct {
	Class   string `json:"class"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

type simHostJSON struct {
	HostID         string        `json:"host_id"`
	HostName       string        `json:"host_name"`
	Skipped        string        `json:"skipped,omitempty"` // reason the step wouldn't run here
	Error          *simErrorJSON `json:"error,omitempty"`
	PackageManager string        `json:"package_manager,omitempty"`
	Plan           *pkg.Plan     `json:"plan,omitempty"`
	ExtraRemovals  []pkg.Change  `json:"extra_removals"`
}

type simulateResponse struct {
	Hosts []simHostJSON `json:"hosts"`
}

// handleSimulateJob dry-runs a package or system-update step on each target
// using the hosts' current package metadata. Nothing is changed. Hosts that
// a job holds are reported as busy rather than waited for.
func (s *Server) handleSimulateJob(w http.ResponseWriter, r *http.Request) {
	var req simulateRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	if req.Step.Type != model.OpPackage && req.Step.Type != model.OpSystemUpdate {
		writeProblem(w, r, errValidation("only package and system_update steps can be simulated"))
		return
	}
	if err := req.Step.Normalize(); err != nil {
		writeProblem(w, r, errValidation(err.Error()))
		return
	}
	ctx := r.Context()
	targets, _, err := engine.ResolveTargets(ctx, s.d.Store.R, req.Targets)
	if err != nil {
		writeProblem(w, r, s.jobError(err))
		return
	}
	if len(targets) > 100 {
		writeProblem(w, r, errValidation("simulate at most 100 hosts at once"))
		return
	}
	out := make([]simHostJSON, len(targets))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, t := range targets {
		out[i] = simHostJSON{HostID: t.ID, HostName: t.Name, ExtraRemovals: []pkg.Change{}}
		wg.Add(1)
		go func(h *simHostJSON) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s.simulateHost(ctx, h, req.Step)
		}(&out[i])
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, simulateResponse{Hosts: out})
}

func (s *Server) simulateHost(ctx context.Context, h *simHostJSON, step model.Operation) {
	fail := func(err error) {
		oe := model.AsOpError(err)
		h.Error = &simErrorJSON{Class: string(oe.Class), Message: errMsgOf(oe), Hint: oe.Hint}
	}
	osInfo, err := s.d.Inventory.HostOS(ctx, h.HostID)
	if err != nil {
		fail(err)
		return
	}
	if _, ok, why := step.ForHost(osInfo); !ok {
		h.Skipped = why
		return
	}
	release, ok := s.d.Engine.Locks().TryAcquire(h.HostID, "simulate", model.LockShared)
	if !ok {
		h.Error = &simErrorJSON{Class: "Busy", Message: "a job is changing this host right now"}
		return
	}
	defer release()
	conn, err := s.d.Inventory.Resolve(ctx, h.HostID, true)
	if err != nil {
		fail(err)
		return
	}
	sctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	res, err := s.d.Simulator.Simulate(sctx, conn.Target, conn.Become, step)
	if err != nil {
		fail(err)
		return
	}
	h.PackageManager, h.Plan, h.ExtraRemovals = res.PackageManager, &res.Plan, res.ExtraRemovals
}

func errMsgOf(oe *model.OpError) string {
	if oe.Message != "" {
		return oe.Message
	}
	return oe.Error()
}
