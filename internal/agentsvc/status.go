package agentsvc

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"drool/internal/release"
	"drool/internal/store/db"
	"drool/internal/version"
)

// UpdateStatus is the Shepherd update overview (GET /agents/updates/status):
// what DROOL keeps Shepherds on, whether that can work, how the fleet
// stands, and what needs a person. Every role that can see Shepherds can
// read it.
type UpdateStatus struct {
	Policy UpdatePolicy `json:"policy"`
	// PolicySaved: someone saved the policy; otherwise it is the default.
	PolicySaved bool `json:"policy_saved"`
	// Channels the policy can choose.
	Channels []string `json:"channels"`
	// State: none (no Shepherds), current, available, updating, attention
	// (a paused rollout, a failed or rolled-back update, a release that
	// failed verification) or blocked (behind and nothing can update them).
	State   string `json:"state"`
	Summary string `json:"summary"`
	// Target is the version automatic updates keep Shepherds on ("": none
	// is available), and whether it is an official release.
	Target         string         `json:"target"`
	TargetOfficial bool           `json:"target_official"`
	Controller     ControllerInfo `json:"controller"`
	Official       OfficialStatus `json:"official"`
	Check          CheckStatus    `json:"check"`
	// Latest is a newer DROOL than this controller, when the release source
	// has one and it verified.
	Latest   *LatestRelease    `json:"latest,omitempty"`
	Counts   UpdateCounts      `json:"counts"`
	Blocked  []BlockedShepherd `json:"blocked"`
	Problems []UpdateProblem   `json:"problems"`
	Active   []RolloutSummary  `json:"active"`
	History  []RolloutSummary  `json:"history"`
}

// ControllerInfo is this DROOL's version, as far as Shepherd updates go.
type ControllerInfo struct {
	Version string `json:"version"`
	Channel string `json:"channel"`
	// Official: a stable or prerelease build, which has official Shepherd
	// releases (a development build has none).
	Official bool `json:"official"`
}

// OfficialStatus says whether official Shepherd releases can work here.
type OfficialStatus struct {
	State  string   `json:"state"` // see the Official* constants
	Detail string   `json:"detail"`
	Keys   []string `json:"keys"` // the project's key ids built into this DROOL
}

// CheckStatus is the official release check.
type CheckStatus struct {
	Enabled     bool    `json:"enabled"`
	Source      string  `json:"source"`
	Checking    bool    `json:"checking"`
	LastSuccess *string `json:"last_success"`
	LastAttempt *string `json:"last_attempt"`
	LastError   string  `json:"last_error,omitempty"`
	LastErrorAt *string `json:"last_error_at"`
	NextCheck   *string `json:"next_check"`
}

// LatestRelease is a newer official DROOL release.
type LatestRelease struct {
	Version  string `json:"version"`
	IssuedAt string `json:"issued_at"`
	URL      string `json:"url"`
}

// UpdateCounts summarize the fleet.
type UpdateCounts struct {
	Total    int `json:"total"`
	Current  int `json:"current"`
	Behind   int `json:"behind"` // can be updated
	Updating int `json:"updating"`
	// Waiting: in a running update, offline; they update when they reconnect.
	Waiting int `json:"waiting"`
	Blocked int `json:"blocked"`
	Offline int `json:"offline"`
}

// BlockedShepherd is a Shepherd behind the target (or DROOL) that can't be
// updated now, and why.
type BlockedShepherd struct {
	HostID   string `json:"host_id"`
	HostName string `json:"host_name"`
	Version  string `json:"version"`
	Reason   string `json:"reason"`
	// RetryVersion: the update failed here; a person can try it again.
	RetryVersion string `json:"retry_version,omitempty"`
}

// UpdateProblem is something about updates that needs a person.
type UpdateProblem struct {
	Kind      string `json:"kind"` // paused | failed | rolled_back | verification
	Title     string `json:"title"`
	Detail    string `json:"detail"`
	RolloutID string `json:"rollout_id,omitempty"`
	HostID    string `json:"host_id,omitempty"`
	HostName  string `json:"host_name,omitempty"`
	Version   string `json:"version,omitempty"`
	At        string `json:"at"`
}

// RolloutSummary is one update rollout, counted.
type RolloutSummary struct {
	ID        string `json:"id"`
	Version   string `json:"version"`
	Status    string `json:"status"`
	Reason    string `json:"reason"`
	Automatic bool   `json:"automatic"`
	Retry     bool   `json:"retry"`
	Hosts     int    `json:"hosts"`
	Healthy   int    `json:"healthy"`
	Failed    int    `json:"failed"`
	Skipped   int    `json:"skipped"`
	Waiting   int    `json:"waiting"` // pending, offline
	InFlight  int    `json:"in_flight"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func msString(ms int64) string { return time.UnixMilli(ms).UTC().Format(time.RFC3339) }

func timeString(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

// UpdateStatus computes the overview.
func (s *Service) UpdateStatus(ctx context.Context) (*UpdateStatus, error) {
	p := s.GetUpdatePolicy(ctx)
	out := &UpdateStatus{Policy: p, Channels: Channels, Blocked: []BlockedShepherd{}, Problems: []UpdateProblem{},
		Active: []RolloutSummary{}, History: []RolloutSummary{}}
	if _, err := s.st.R.GetSetting(ctx, "agent_updates"); err == nil {
		out.PolicySaved = true
	}
	b := version.Get()
	out.Controller = ControllerInfo{Version: b.Version, Channel: b.Channel, Official: b.Channel == version.Stable || b.Channel == version.Prerelease}

	// The official release check.
	c := s.checker()
	st := s.releaseCheck(ctx)
	out.Official = OfficialStatus{State: st.OwnState, Detail: st.OwnDetail, Keys: release.OfficialKeyIDs()}
	switch {
	case c.opt.Source == "off":
		out.Official.State, out.Official.Detail = OfficialOff, "Checking for official releases is turned off (agent.update_source: off)."
	case len(release.OfficialKeys()) == 0:
		out.Official.State, out.Official.Detail = OfficialNoKey, "This DROOL build has no DROOL release key built in, so it can't verify official Shepherd releases."
	case !out.Controller.Official:
		out.Official.State, out.Official.Detail = OfficialDevelopment, fmt.Sprintf("This DROOL (%s) is a development build, not an official release: there are no official Shepherd releases for it.", b.Version)
	case st.OwnVersion != b.Version || st.OwnState == "":
		out.Official.State, out.Official.Detail = OfficialPending, "DROOL hasn't checked for its official Shepherd release yet."
	}
	c.mu.Lock()
	out.Check = CheckStatus{Enabled: c.opt.Source != "off" && len(release.OfficialKeys()) > 0, Source: c.opt.Source, Checking: c.checking,
		NextCheck: timeString(c.next)}
	c.mu.Unlock()
	out.Check.LastSuccess, out.Check.LastAttempt, out.Check.LastErrorAt = timeString(st.LastSuccess), timeString(st.LastAttempt), timeString(st.LastErrorAt)
	out.Check.LastError = st.LastError
	if st.LatestVersion != "" && release.CompareVersions(st.LatestVersion, b.Version) > 0 && out.Check.Enabled {
		out.Latest = &LatestRelease{Version: st.LatestVersion, IssuedAt: st.LatestIssuedAt.UTC().Format(time.RFC3339), URL: st.LatestURL}
	}

	// What automatic updates keep Shepherds on.
	out.Target = s.AutoTarget(ctx, p)
	rels := map[string]*db.AgentRelease{}
	if out.Target != "" {
		rels = s.platformReleases(ctx, out.Target)
		for _, r := range rels {
			out.TargetOfficial = out.TargetOfficial || release.IsOfficialKey(r.KeyID)
		}
	}
	// Up to date means the target, or DROOL's own version when that is newer:
	// Shepherds follow DROOL, so one behind it is behind even when no
	// release is there to update it (which the reason then says).
	reference := b.Version
	if out.Target != "" && release.CompareVersions(out.Target, reference) > 0 {
		reference = out.Target
	}

	// Rollouts.
	names := s.hostNames(ctx)
	rolls, _ := s.st.R.ListRollouts(ctx)
	inFlight := map[string]bool{}
	type hostResult struct {
		state, detail, rollout string
		at                     int64
	}
	lastAtTarget := map[string]hostResult{} // newest rollout result per host, for the target
	for _, r := range rolls {
		if r.Kind != "update" {
			continue
		}
		hosts, _ := s.st.R.ListRolloutHosts(ctx, r.ID)
		sum := RolloutSummary{ID: r.ID, Version: r.Version, Status: r.Status, Reason: r.PausedReason, Automatic: r.Automatic != 0,
			Retry: r.Retry != 0, Hosts: len(hosts), CreatedAt: msString(r.CreatedAt), UpdatedAt: msString(r.UpdatedAt)}
		for _, h := range hosts {
			switch h.State {
			case "healthy":
				sum.Healthy++
			case "failed", "rolled_back":
				sum.Failed++
			case "skipped":
				sum.Skipped++
			case "pending":
				sum.Waiting++
			default:
				sum.InFlight++
			}
			if r.Status == "running" && !terminal[h.State] {
				inFlight[h.HostID] = true
			}
			if r.Version == out.Target {
				if prev, ok := lastAtTarget[h.HostID]; !ok || h.UpdatedAt > prev.at {
					lastAtTarget[h.HostID] = hostResult{state: h.State, detail: h.Detail, rollout: r.ID, at: h.UpdatedAt}
				}
			}
		}
		if r.Status == "running" {
			out.Active = append(out.Active, sum)
		} else if len(out.History) < 8 {
			out.History = append(out.History, sum)
		}
		if r.Status == "paused" {
			out.Problems = append(out.Problems, UpdateProblem{Kind: "paused", RolloutID: r.ID, Version: r.Version, At: msString(r.UpdatedAt),
				Title:  fmt.Sprintf("The update to %s is paused", r.Version),
				Detail: r.PausedReason})
		}
	}
	if out.Official.State == OfficialRejected {
		out.Problems = append(out.Problems, UpdateProblem{Kind: "verification", Version: b.Version, Title: fmt.Sprintf("DROOL %s's Shepherd release failed verification", b.Version),
			Detail: out.Official.Detail + " Nothing was installed.", At: deref(timeString(st.LastAttempt))})
	}

	// The fleet.
	idents, err := s.st.R.ListLiveIdentities(ctx)
	if err != nil {
		return nil, err
	}
	behindVersions := map[string]int{}
	for _, id := range idents {
		if id.Status != "active" {
			continue
		}
		out.Counts.Total++
		_, live := s.SessionFor(id.HostID)
		if !live {
			out.Counts.Offline++
		}
		if inFlight[id.HostID] {
			if live {
				out.Counts.Updating++
			} else {
				out.Counts.Waiting++
			}
			continue
		}
		if release.CompareVersions(id.AgentVersion, reference) >= 0 {
			out.Counts.Current++
			continue
		}
		behindVersions[id.AgentVersion]++
		res, tried := lastAtTarget[id.HostID]
		if tried && (res.state == "failed" || res.state == "rolled_back") || failedHere(id, out.Target) {
			kind, verb := "failed", "failed"
			if res.state == "rolled_back" || id.UpdateState == "rolled_back" {
				kind, verb = "rolled_back", "was rolled back"
			}
			detail := res.detail
			if detail == "" {
				detail = id.UpdateDetail
			}
			out.Problems = append(out.Problems, UpdateProblem{Kind: kind, HostID: id.HostID, HostName: names[id.HostID], Version: out.Target,
				RolloutID: res.rollout, At: msString(max(res.at, id.UpdateAt.Int64)),
				Title:  fmt.Sprintf("The update of %s to %s %s", names[id.HostID], out.Target, verb),
				Detail: detail})
			out.Counts.Blocked++
			out.Blocked = append(out.Blocked, BlockedShepherd{HostID: id.HostID, HostName: names[id.HostID], Version: id.AgentVersion,
				Reason: fmt.Sprintf("the update to %s %s here: %s", out.Target, verb, detail), RetryVersion: out.Target})
			continue
		}
		reason := ""
		switch {
		case out.Target == "" || release.CompareVersions(id.AgentVersion, out.Target) >= 0:
			reason = "no release newer than this Shepherd is available to DROOL: " + noReleaseReason(out.Official)
			if out.Official.State == OfficialVerified && !ChannelIncludes(p.Channel, b.Channel) {
				reason = fmt.Sprintf("this DROOL (%s) is a %s build, and Shepherds update from the %s channel: choose %s to update them to it",
					b.Version, channelWord(b.Channel), p.Channel, channelChoice(b.Channel))
			}
		case tried && res.state == "skipped":
			reason = res.detail
		default:
			reason = Blocker(id, IdentityTrust(id), out.Target, rels[id.Os+"/"+id.Arch], false)
		}
		if reason != "" {
			out.Counts.Blocked++
			out.Blocked = append(out.Blocked, BlockedShepherd{HostID: id.HostID, HostName: names[id.HostID], Version: id.AgentVersion, Reason: reason})
			continue
		}
		out.Counts.Behind++
	}
	sort.Slice(out.Blocked, func(i, j int) bool { return out.Blocked[i].HostName < out.Blocked[j].HostName })

	// The state and its one-line summary.
	switch {
	case out.Counts.Total == 0:
		out.State, out.Summary = "none", "Nothing to update."
	case len(out.Problems) > 0:
		out.State, out.Summary = "attention", attentionSummary(out.Problems)
	case out.Counts.Updating > 0:
		out.State, out.Summary = "updating", fmt.Sprintf("Updating %s to %s.", plural(out.Counts.Updating, "Shepherd"), activeVersion(out.Active, out.Target))
		if out.Counts.Waiting > 0 {
			out.Summary += fmt.Sprintf(" %s will update when %s.", plural(out.Counts.Waiting, "offline Shepherd"), reconnects(out.Counts.Waiting))
		}
	case out.Counts.Waiting > 0:
		out.State, out.Summary = "updating", fmt.Sprintf("%s run %s; %s will update when %s.", plural(out.Counts.Current, "Shepherd"),
			activeVersion(out.Active, out.Target), plural(out.Counts.Waiting, "offline Shepherd"), reconnects(out.Counts.Waiting))
	case out.Counts.Behind > 0 && p.Policy == "automatic":
		out.State, out.Summary = "available", fmt.Sprintf("%s will update to %s automatically.", plural(out.Counts.Behind, "Shepherd"), out.Target)
	case out.Counts.Behind > 0:
		out.State, out.Summary = "available", fmt.Sprintf("An update to %s is available for %s.", out.Target, plural(out.Counts.Behind, "Shepherd"))
	case out.Counts.Blocked > 0:
		out.State, out.Summary = "blocked", fmt.Sprintf("%s can't be updated: %s.", plural(out.Counts.Blocked, "Shepherd"), firstReason(out.Blocked))
	default:
		out.State = "current"
		if out.Counts.Total == out.Counts.Current && len(behindVersions) == 0 {
			out.Summary = fmt.Sprintf("All %s are up to date (%s).", plural(out.Counts.Total, "Shepherd"), reference)
		}
	}
	return out, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// attentionSummary is the most important problem in one sentence.
func attentionSummary(ps []UpdateProblem) string {
	failed := map[string][]string{} // version → hosts
	for _, p := range ps {
		if (p.Kind == "failed" || p.Kind == "rolled_back") && p.HostName != "" {
			failed[p.Version] = append(failed[p.Version], p.HostName)
		}
	}
	for _, p := range ps {
		if p.Kind == "paused" {
			if hs := failed[p.Version]; len(hs) > 0 {
				return fmt.Sprintf("The update to %s is paused: it failed on %s.", p.Version, strings.Join(hs, ", "))
			}
			return fmt.Sprintf("The update to %s is paused.", p.Version)
		}
	}
	for v, hs := range failed {
		return fmt.Sprintf("The update to %s failed on %s.", v, strings.Join(hs, ", "))
	}
	return ps[0].Title + "."
}

func reconnects(n int) string {
	if n == 1 {
		return "it reconnects"
	}
	return "they reconnect"
}

func activeVersion(active []RolloutSummary, target string) string {
	if len(active) > 0 {
		return active[0].Version
	}
	return target
}

func firstReason(b []BlockedShepherd) string {
	if len(b) == 0 {
		return ""
	}
	return strings.TrimSuffix(b[0].Reason, ".")
}

func channelWord(c string) string {
	if c == version.Prerelease {
		return "release candidate"
	}
	return c
}

func channelChoice(c string) string {
	if c == version.Prerelease {
		return "the Release candidates channel"
	}
	return "its channel"
}

// noReleaseReason says, from the official check, why there is no release
// to update to.
func noReleaseReason(o OfficialStatus) string {
	switch o.State {
	case OfficialVerified:
		return "they already run this DROOL's Shepherd version or a newer one"
	case "", OfficialPending:
		return "DROOL hasn't checked for its official Shepherd release yet"
	}
	return strings.TrimSuffix(lowerFirst(o.Detail), ".")
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	if strings.HasPrefix(s, "DROOL") || strings.HasPrefix(s, "This DROOL") {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}
