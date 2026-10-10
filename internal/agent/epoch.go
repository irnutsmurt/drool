package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"drool/internal/agentrpc"
	"drool/internal/agentrpc/pb"
	"drool/internal/firewall/hostfw"
	"drool/internal/hostops"
	"drool/internal/model"
)

// The epoch transition after a controller restore (DESIGN §10.3,
// ARCHITECTURE-ALPHA §33.1). The agent belongs to the controller epoch it
// was enrolled in; a restored controller has a new one, and until the agent
// moves to it every command is refused ("never applied automatically").
// The controller sends EpochTransition only after comparing the agent's
// report with what it knows. The agent checks everything again before
// changing anything, applies the change in an order that can be resumed
// (firewall tool, domains, then the identity's epoch as the commit point),
// and answers the same request again with the same result.

var transitionMu sync.Mutex // one transition at a time

// stateReport is the agent's applied domains.
func (a *Agent) stateReport() *pb.StateReport {
	r := &pb.StateReport{}
	doms, _ := a.store.Domains()
	names := make([]string, 0, len(doms))
	for n := range doms {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		d := doms[n]
		r.Domains = append(r.Domains, &pb.DomainState{Domain: d.Domain, Epoch: d.Epoch, Revision: d.Revision, ContentHash: d.ContentHash})
	}
	return r
}

func (a *Agent) applyEpochTransition(ctx context.Context, t *pb.EpochTransition) *pb.EpochTransitionResult {
	transitionMu.Lock()
	defer transitionMu.Unlock()
	res := &pb.EpochTransitionResult{TransitionId: t.TransitionId}
	finish := func(code string, err error) *pb.EpochTransitionResult {
		if err != nil {
			res.Code, res.Error = code, err.Error()
			a.log.Warn("controller epoch transition refused", "code", code, "err", err)
		} else {
			res.Ok = true
		}
		res.State = a.stateReport()
		if id, e := a.store.Identity(); e == nil {
			res.ControllerEpoch = id.ControllerEpoch
		}
		return res
	}
	id, err := a.store.Identity()
	if err != nil {
		return finish("state", err)
	}
	if t.ToEpoch == "" || t.FromEpoch == "" {
		return finish("invalid", errors.New("the transition names no epochs"))
	}
	if id.ControllerEpoch != t.FromEpoch && id.ControllerEpoch != t.ToEpoch {
		return finish("epoch", fmt.Errorf("this agent belongs to controller epoch %s, not %s", short(id.ControllerEpoch), short(t.FromEpoch)))
	}

	// 1. Check everything before changing anything.
	doms, err := a.store.Domains()
	if err != nil {
		return finish("state", err)
	}
	listed := map[string]bool{}
	var fw *pb.EpochDomain
	for _, d := range t.Domains {
		listed[d.Domain] = true
		if d.Domain == "firewall" {
			fw = d
			continue
		}
		cur, ok := doms[d.Domain]
		switch {
		case !ok:
			return finish("changed", fmt.Errorf("this agent has no %s state", d.Domain))
		case cur.Revision != d.ExpectRevision || cur.ContentHash != d.ExpectHash:
			return finish("changed", fmt.Errorf("%s changed since it was compared (revision %d)", d.Domain, cur.Revision))
		case cur.Epoch != d.ExpectEpoch && cur.Epoch != t.ToEpoch:
			return finish("changed", fmt.Errorf("%s belongs to epoch %s", d.Domain, short(cur.Epoch)))
		}
	}
	for n := range doms {
		if !listed[n] {
			// Every domain moves, or none: one left behind would refuse its
			// next change for ever.
			return finish("changed", fmt.Errorf("the controller didn't compare this agent's %s state", n))
		}
	}
	if fw != nil {
		st, err := hostfw.Run(ctx, a.run, hostops.Become{Method: "none"}, a.fwPaths(), hostfw.Action{Kind: hostfw.Status})
		if err != nil {
			return finish("firewall", err)
		}
		crev, _ := strconv.ParseUint(st.Get("committed_revision"), 10, 64)
		switch {
		case st.Get("journal_state") == "pending":
			return finish("pending", errors.New("a firewall transaction is pending"))
		case crev != fw.ExpectRevision || st.Get("committed_hash") != fw.ExpectHash:
			return finish("changed", fmt.Errorf("the firewall's committed policy changed since it was compared (revision %d)", crev))
		case st.Get("committed_epoch") != fw.ExpectEpoch && st.Get("committed_epoch") != t.ToEpoch:
			return finish("changed", fmt.Errorf("the firewall's committed policy belongs to epoch %s", short(st.Get("committed_epoch"))))
		}
	}

	// 2. Apply: the firewall tool, the domains, then the identity's epoch
	// (the commit point: until it is written, the agent still answers to
	// the old epoch and the controller sends the transition again).
	if fw != nil {
		r, err := a.runFirewall(ctx, &model.FirewallOp{Kind: hostfw.Rebase, FromEpoch: fw.ExpectEpoch, Epoch: t.ToEpoch,
			Revision: int64(fw.ExpectRevision), Hash: fw.ExpectHash})
		if err != nil {
			return finish("firewall", err)
		}
		if code, _ := r.Data["err_code"].(string); code != "" {
			msg, _ := r.Data["err_msg"].(string)
			return finish(code, errors.New("firewall: "+msg))
		}
	}
	for _, d := range t.Domains {
		if d.Domain == "firewall" {
			continue
		}
		if err := a.store.ApplyDomain(agentrpc.StateVersion{Domain: d.Domain, Epoch: t.ToEpoch, Revision: d.ExpectRevision, ContentHash: d.ExpectHash}); err != nil {
			return finish("state", err)
		}
	}
	if id.ControllerEpoch != t.ToEpoch {
		if err := a.store.SetControllerEpoch(t.ToEpoch); err != nil {
			return finish("state", err)
		}
		a.log.Info("moved to the controller's new epoch after a controller restore", "from", short(t.FromEpoch), "to", short(t.ToEpoch))
		a.oplog.Write(OpEvent{Event: "epoch", Summary: "controller epoch " + short(t.FromEpoch) + " → " + short(t.ToEpoch) + " (restored controller, reconciled)",
			Time: time.Now().UTC()})
	}
	// A revocation this agent reported was seen by the controller that
	// moved it: it only moves a host that reported one after an
	// administrator accepted it.
	a.store.ClearRevocation()
	return finish("", nil)
}
