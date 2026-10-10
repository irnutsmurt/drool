package agent

import (
	"context"
	"testing"

	"drool/internal/agentcaps"
	"drool/internal/agentrpc"
	"drool/internal/agentrpc/pb"
)

// TestEpochTransition: the agent checks everything before changing
// anything, moves every domain or none, answers the same transition again
// with the same result, and from then on accepts only the new epoch.
func TestEpochTransition(t *testing.T) {
	full, _ := agentcaps.Preset(agentcaps.PresetFull)
	a := testAgent(t, full)
	ctx := context.Background()
	a.store.ApplyDomain(agentrpc.StateVersion{Domain: "agent-config", Epoch: "epoch-A", Revision: 2, ContentHash: "sha256:c2"})
	a.store.ApplyDomain(agentrpc.StateVersion{Domain: "files", Epoch: "epoch-A", Revision: 7, ContentHash: "sha256:f7"})
	both := []*pb.EpochDomain{
		{Domain: "agent-config", ExpectEpoch: "epoch-A", ExpectRevision: 2, ExpectHash: "sha256:c2"},
		{Domain: "files", ExpectEpoch: "epoch-A", ExpectRevision: 7, ExpectHash: "sha256:f7"},
	}
	tr := func(from string, doms ...*pb.EpochDomain) *pb.EpochTransition {
		return &pb.EpochTransition{TransitionId: "etr_1", FromEpoch: from, ToEpoch: "epoch-B", Domains: doms}
	}
	unchanged := func() {
		t.Helper()
		id, _ := a.store.Identity()
		d, _ := a.store.Domains()
		if id.ControllerEpoch != "epoch-A" || d["agent-config"].Epoch != "epoch-A" || d["files"].Epoch != "epoch-A" {
			t.Fatalf("a refused transition changed something: %s %+v", id.ControllerEpoch, d)
		}
	}

	// Refusals change nothing.
	if r := a.applyEpochTransition(ctx, tr("epoch-X", both...)); r.Ok || r.Code != "epoch" {
		t.Errorf("from another epoch: %+v", r)
	}
	unchanged()
	if r := a.applyEpochTransition(ctx, tr("epoch-A", both[0])); r.Ok || r.Code != "changed" {
		t.Errorf("a domain left out: %+v", r)
	}
	unchanged()
	stale := []*pb.EpochDomain{both[0], {Domain: "files", ExpectEpoch: "epoch-A", ExpectRevision: 6, ExpectHash: "sha256:f6"}}
	if r := a.applyEpochTransition(ctx, tr("epoch-A", stale...)); r.Ok || r.Code != "changed" {
		t.Errorf("compared against an older revision: %+v", r)
	}
	unchanged()
	if r := a.applyEpochTransition(ctx, &pb.EpochTransition{TransitionId: "etr_0", Domains: both}); r.Ok || r.Code != "invalid" {
		t.Errorf("no epochs: %+v", r)
	}
	unchanged()

	// The transition, then the same one again (a lost answer).
	for i := 0; i < 2; i++ {
		r := a.applyEpochTransition(ctx, tr("epoch-A", both...))
		if !r.Ok || r.ControllerEpoch != "epoch-B" || len(r.State.GetDomains()) != 2 {
			t.Fatalf("transition %d: %+v", i, r)
		}
	}
	id, _ := a.store.Identity()
	d, _ := a.store.Domains()
	if id.ControllerEpoch != "epoch-B" || d["agent-config"] != (agentrpc.StateVersion{Domain: "agent-config", Epoch: "epoch-B", Revision: 2, ContentHash: "sha256:c2"}) ||
		d["files"].Epoch != "epoch-B" || d["files"].Revision != 7 {
		t.Fatalf("after the transition: %s %+v", id.ControllerEpoch, d)
	}
	// Commands from the old epoch are refused now; the new one's accepted.
	old := cmdEnv("op_old")
	if r := a.admit(old); r == nil || r.Reason != agentrpc.RejectEpoch {
		t.Errorf("old epoch's command: %+v", r)
	}
	cur := cmdEnv("op_new")
	cur.Epoch = "epoch-B"
	if r := a.admit(cur); r != nil {
		t.Errorf("new epoch's command refused: %+v", r)
	}
	// A third controller can't take the agent over from here.
	if r := a.applyEpochTransition(ctx, &pb.EpochTransition{TransitionId: "etr_2", FromEpoch: "epoch-A", ToEpoch: "epoch-C", Domains: both}); r.Ok {
		t.Errorf("moved from an epoch it no longer belongs to: %+v", r)
	}
}
