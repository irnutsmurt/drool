package agentsvc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"drool/internal/agentcaps"
	"drool/internal/audit"
	"drool/internal/ids"
	"drool/internal/inventory"
	"drool/internal/store/db"
	"drool/internal/version"
)

// Bulk SSH → agent conversion (M11) is a rollout of kind "install": the
// same canary-then-batches machinery as agent updates, where offering a
// host means running the existing single-host InstallViaSSH. One host's
// failure leaves that host on SSH and counts toward the rollout's
// failure limit; it never touches the others.

// ConversionHost classifies one host for conversion.
type ConversionHost struct {
	HostID string `json:"host_id"`
	Name   string `json:"name"`
	Status string `json:"status"` // compatible | agent | unsupported | unreachable
	Reason string `json:"reason,omitempty"`
	Arch   string `json:"arch,omitempty"`
}

// ConversionPreview summarizes a selection.
type ConversionPreview struct {
	Selected int               `json:"selected"`
	Counts   map[string]int    `json:"counts"`
	Hosts    []ConversionHost  `json:"hosts"`
	Binary   map[string]string `json:"binary"` // arch → what would be installed
}

var archOf = map[string]string{"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}

// PreviewConversion classifies hosts without touching them.
func (s *Service) PreviewConversion(ctx context.Context, hostIDs []string) (*ConversionPreview, error) {
	if len(hostIDs) > 500 {
		return nil, errors.New("at most 500 hosts at once")
	}
	p := &ConversionPreview{Counts: map[string]int{}, Hosts: []ConversionHost{}, Binary: map[string]string{}}
	for _, id := range hostIDs {
		h, err := s.st.R.GetHost(ctx, id)
		if err != nil {
			continue
		}
		p.Selected++
		c := ConversionHost{HostID: id, Name: h.Name, Status: "compatible"}
		f, ferr := s.st.R.GetHostFacts(ctx, id)
		if ferr == nil {
			c.Arch = archOf[f.Arch]
		}
		_, idErr := s.st.R.GetLiveIdentityForHost(ctx, id)
		switch {
		case idErr == nil:
			c.Status, c.Reason = "agent", "already has an agent identity"
		case h.SshConfigured == 0 || h.JumpOnly != 0:
			c.Status, c.Reason = "unsupported", "no SSH access to install over"
		case !inventory.CanBecomeRoot(h):
			c.Status, c.Reason = "unsupported", "installing needs root: log in as root or allow sudo for this host"
		case h.Status != inventory.StatusReady && h.Status != inventory.StatusUnsupported: // no systemd: the agent supervises itself
			c.Status, c.Reason = "unreachable", "the host isn't reachable over SSH right now ("+h.Status+")"
		case ferr != nil:
			c.Status, c.Reason = "unreachable", "no inventory yet: connect to the host first"
		case c.Arch == "":
			c.Status, c.Reason = "unsupported", fmt.Sprintf("architecture %q has no agent build (amd64 and arm64 are supported)", f.Arch)
		default:
			if _, ok := p.Binary[c.Arch]; !ok {
				if img, err := s.InstallBinary(ctx, c.Arch); err == nil {
					p.Binary[c.Arch] = img.Version + ", " + img.Source
				} else {
					p.Binary[c.Arch] = ""
				}
			}
			if p.Binary[c.Arch] == "" {
				c.Status, c.Reason = "unsupported", "the controller has no agent binary for linux/"+c.Arch
			}
		}
		p.Counts[c.Status]++
		p.Hosts = append(p.Hosts, c)
	}
	sort.SliceStable(p.Hosts, func(i, j int) bool {
		rank := map[string]int{"compatible": 0, "unreachable": 1, "unsupported": 2, "agent": 3}
		return rank[p.Hosts[i].Status] < rank[p.Hosts[j].Status]
	})
	return p, nil
}

// ConversionOptions configure a bulk conversion.
type ConversionOptions struct {
	HostIDs     []string
	Preset      string
	Canary      int // default 1
	BatchSize   int // default 5
	MaxFailures int // default 1: stop at the first failure
	UserID      string
	// WebHost is the host name the administrator uses for the web UI: each
	// host is given the endpoint the controller resolves for it, as when
	// installing one host from its page (TokenOptions.WebHost).
	WebHost string
}

// StartConversion starts a conversion rollout over the compatible hosts.
func (s *Service) StartConversion(ctx context.Context, o ConversionOptions) (string, *ConversionPreview, error) {
	if _, err := agentcaps.Preset(o.Preset); err != nil {
		return "", nil, err
	}
	p, err := s.PreviewConversion(ctx, o.HostIDs)
	if err != nil {
		return "", nil, err
	}
	var targets []string
	for _, h := range p.Hosts {
		if h.Status == "compatible" {
			targets = append(targets, h.HostID)
		}
	}
	if len(targets) == 0 {
		return "", p, errors.New("none of the selected hosts can be converted")
	}
	if o.Canary <= 0 {
		o.Canary = 1
	}
	if o.BatchSize <= 0 {
		o.BatchSize = 5
	}
	if o.MaxFailures <= 0 {
		o.MaxFailures = 1
	}
	if o.Canary > 50 || o.BatchSize > 50 || o.MaxFailures > 100 {
		return "", p, errors.New("canary and batch size must be at most 50, failures at most 100")
	}
	id := ids.New(ids.AgentRollout)
	now := s.now().UnixMilli()
	err = s.st.InTx(ctx, func(q *db.Queries) error {
		if err := q.InsertRollout(ctx, db.InsertRolloutParams{ID: id, Version: version.Version, Canary: int64(o.Canary), BatchSize: int64(o.BatchSize),
			MaxFailures: int64(o.MaxFailures), HealthWindowS: 300, CreatedBy: sql.NullString{String: o.UserID, Valid: o.UserID != ""},
			CreatedAt: now, UpdatedAt: now}); err != nil {
			return err
		}
		if err := q.SetRolloutKind(ctx, db.SetRolloutKindParams{Kind: "install", Preset: o.Preset, ID: id}); err != nil {
			return err
		}
		if err := q.SetRolloutWebHost(ctx, db.SetRolloutWebHostParams{WebHost: o.WebHost, ID: id}); err != nil {
			return err
		}
		for i, h := range targets {
			wave := 0
			if i >= o.Canary {
				wave = 1 + (i-o.Canary)/o.BatchSize
			}
			if err := q.InsertRolloutHost(ctx, db.InsertRolloutHostParams{RolloutID: id, HostID: h, Wave: int64(wave), UpdatedAt: now}); err != nil {
				return err
			}
		}
		_, err := s.audit.RecordTx(ctx, q, audit.Event{Action: "agent.convert.start", TargetType: "agent_rollout", TargetID: id,
			Details: map[string]any{"hosts": len(targets), "preset": o.Preset, "canary": o.Canary, "batch_size": o.BatchSize, "max_failures": o.MaxFailures}})
		return err
	})
	if err != nil {
		return "", p, err
	}
	go s.advanceRollouts(context.Background())
	return id, p, nil
}

// Locker serializes an installation with jobs on the same host (the
// engine's host locks); set by the controller.
type Locker func(hostID string) (release func(), ok bool)

// SetLocker installs the host locker.
func (s *Service) SetLocker(l Locker) {
	s.mu.Lock()
	s.locker = l
	s.mu.Unlock()
}

var convertingMu sync.Mutex
var convertingSet = map[string]bool{}

func (s *Service) converting(rollout, host string) bool {
	convertingMu.Lock()
	defer convertingMu.Unlock()
	return convertingSet[rollout+"|"+host]
}

// convertHost installs the agent on one host of a conversion rollout.
func (s *Service) convertHost(ctx context.Context, r db.AgentRollout, hostID string) {
	key := r.ID + "|" + hostID
	convertingMu.Lock()
	if convertingSet[key] {
		convertingMu.Unlock()
		return
	}
	convertingSet[key] = true
	convertingMu.Unlock()
	s.setHostState(ctx, r.ID, hostID, "offered", "installing over SSH")
	go func() {
		defer func() {
			convertingMu.Lock()
			delete(convertingSet, key)
			convertingMu.Unlock()
			s.advanceRollouts(context.Background())
		}()
		s.mu.Lock()
		lock := s.locker
		s.mu.Unlock()
		if lock != nil {
			release, ok := lock(hostID)
			for i := 0; !ok && i < 30; i++ { // a job holds the host: wait a little
				time.Sleep(10 * time.Second)
				release, ok = lock(hostID)
			}
			if !ok {
				s.setHostState(context.Background(), r.ID, hostID, "failed", "a job kept the host busy; try again later")
				return
			}
			defer release()
		}
		ictx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		var out strings.Builder
		err := s.InstallViaSSH(ictx, InstallOptions{HostID: hostID, Preset: r.Preset, UserID: r.CreatedBy.String, WebHost: r.WebHost}, &out)
		if err != nil {
			s.setHostState(context.Background(), r.ID, hostID, "failed", trunc(err.Error(), 400))
			s.audit.Record(context.Background(), audit.Event{Action: "agent.install", TargetType: "host", TargetID: hostID, Result: audit.Failure,
				Details: map[string]any{"rollout_id": r.ID, "preset": r.Preset, "error": err.Error()}})
			return
		}
		s.setHostState(context.Background(), r.ID, hostID, "healthy", "the agent connected over mTLS")
	}()
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
