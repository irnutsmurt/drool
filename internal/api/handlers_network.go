package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"drool/internal/audit"
	"drool/internal/netmon"
	"drool/internal/netmon/geoip"
	"drool/internal/netmon/netsvc"
	"drool/internal/store/db"
)

// Network connectivity monitoring (M10, ARCHITECTURE-ALPHA §26): observed
// flows, correlated with the firewall policy and enriched locally.

func (s *Server) networkEnabled(w http.ResponseWriter, r *http.Request) bool {
	if s.d.Network == nil {
		writeProblem(w, r, problem(http.StatusNotImplemented, "unavailable", "network monitoring is not configured"))
		return false
	}
	return true
}

// parseSince reads unix seconds, RFC 3339, or a duration back from now
// ("24h", "7d").
func parseSince(v string, now time.Time) (int64, bool) {
	if v == "" {
		return 0, true
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
		return n, true
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.Unix(), true
	}
	if strings.HasSuffix(v, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(v, "d")); err == nil && n > 0 && n <= 3650 {
			return now.Add(-time.Duration(n) * 24 * time.Hour).Unix(), true
		}
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return now.Add(-d).Unix(), true
	}
	return 0, false
}

func networkFilter(r *http.Request) (netsvc.Filter, *Problem) {
	q := r.URL.Query()
	f := netsvc.Filter{Hosts: q["host"], Group: q.Get("group"), Direction: q.Get("direction"), Scope: q.Get("scope"),
		Protocol: strings.ToLower(q.Get("protocol")), Process: q.Get("process"), Service: q.Get("service"), Country: q.Get("country"),
		Org: q.Get("org"), Peer: q.Get("peer"), Policy: q.Get("policy"), Source: q.Get("source"), NewDest: q.Get("new"), Q: q.Get("q"),
		Sort: q.Get("sort"), Cursor: q.Get("cursor")}
	ints := map[string]*int{"port": &f.Port, "local_port": &f.LocalPort, "remote_port": &f.RemotePort, "limit": &f.Limit}
	for k, dst := range ints {
		if v := q.Get(k); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return f, errValidation(k + " must be a number")
			}
			*dst = n
		}
	}
	if v := q.Get("asn"); v != "" {
		n, err := strconv.ParseUint(strings.TrimPrefix(strings.ToUpper(v), "AS"), 10, 32)
		if err != nil {
			return f, errValidation("asn must be a number (15169 or AS15169)")
		}
		f.ASN = uint32(n)
	}
	now := time.Now()
	for k, dst := range map[string]*int64{"first_seen_after": &f.FirstAfter, "last_seen_after": &f.LastAfter, "since": &f.NewSince} {
		n, ok := parseSince(q.Get(k), now)
		if !ok {
			return f, errValidation(k + " must be unix seconds, an RFC 3339 time or a duration such as 24h or 7d")
		}
		*dst = n
	}
	if f.NewDest != "" && f.NewSince == 0 {
		f.NewSince = now.Add(-24 * time.Hour).Unix()
	}
	return f, nil
}

func (s *Server) handleNetworkFlows(w http.ResponseWriter, r *http.Request) {
	if !s.networkEnabled(w, r) {
		return
	}
	f, p := networkFilter(r)
	if p != nil {
		writeProblem(w, r, p)
		return
	}
	page, err := s.d.Network.List(r.Context(), f)
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleNetworkFlow(w http.ResponseWriter, r *http.Request) {
	if !s.networkEnabled(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeProblem(w, r, errNotFound("flow"))
		return
	}
	d, err := s.d.Network.Get(r.Context(), id)
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleNetworkSummary(w http.ResponseWriter, r *http.Request) {
	if !s.networkEnabled(w, r) {
		return
	}
	f, p := networkFilter(r)
	if p != nil {
		writeProblem(w, r, p)
		return
	}
	sum, err := s.d.Network.FleetSummary(r.Context(), f)
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

func (s *Server) handleNetworkFacets(w http.ResponseWriter, r *http.Request) {
	if !s.networkEnabled(w, r) {
		return
	}
	fc, err := s.d.Network.Facets(r.Context(), r.URL.Query()["host"])
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	writeJSON(w, http.StatusOK, fc)
}

func (s *Server) handleNetworkTopology(w http.ResponseWriter, r *http.Request) {
	if !s.networkEnabled(w, r) {
		return
	}
	f, p := networkFilter(r)
	if p != nil {
		writeProblem(w, r, p)
		return
	}
	o := netsvc.TopoOptions{Filter: f, Expand: map[string]bool{}}
	for _, e := range r.URL.Query()["expand"] {
		for _, k := range strings.Split(e, ",") {
			if k = strings.TrimSpace(k); k != "" {
				o.Expand[k] = true
			}
		}
	}
	o.MaxExternal = -1
	if v := r.URL.Query().Get("max_external"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 200 {
			writeProblem(w, r, errValidation("max_external must be between 0 (one box for all) and 200"))
			return
		}
		o.MaxExternal = n
	}
	t, err := s.d.Network.Topology(r.Context(), o)
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleHostNetwork(w http.ResponseWriter, r *http.Request) {
	if !s.networkEnabled(w, r) {
		return
	}
	hn, err := s.d.Network.Host(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	writeJSON(w, http.StatusOK, hn)
}

func (s *Server) handleHostNetworkSnapshot(w http.ResponseWriter, r *http.Request) {
	if !s.networkEnabled(w, r) || !s.firewallEnabled(w, r) {
		return
	}
	n, err := s.d.Firewall.Snapshot(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"flows": n})
}

// NetworkSettings is the settings view: the settings plus what enrichment
// can do right now.
type networkSettingsView struct {
	netmon.Settings
	Enrichment geoip.Status `json:"enrichment_status"`
}

func (s *Server) handleNetworkSettings(w http.ResponseWriter, r *http.Request) {
	if !s.networkEnabled(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, networkSettingsView{Settings: s.d.Network.Settings(r.Context()), Enrichment: s.d.Network.Enricher().Status()})
}

func (s *Server) handleSetNetworkSettings(w http.ResponseWriter, r *http.Request) {
	if !s.networkEnabled(w, r) {
		return
	}
	var in netmon.Settings
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	before := s.d.Network.Settings(ctx)
	err := s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		if err := s.d.Network.SaveSettingsTx(ctx, q, in, principalFrom(ctx).UserID); err != nil {
			return audit.Event{}, err
		}
		return audit.Event{Action: "network.settings.update", TargetType: "settings", TargetID: "network",
			Details: map[string]any{"before": before, "after": in}}, nil
	})
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	s.d.Network.SettingsChanged(ctx)
	writeJSON(w, http.StatusOK, networkSettingsView{Settings: s.d.Network.Settings(ctx), Enrichment: s.d.Network.Enricher().Status()})
}

func (s *Server) handleNetworkEnrichment(w http.ResponseWriter, r *http.Request) {
	if !s.networkEnabled(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, s.d.Network.Enricher().Status())
}

func (s *Server) handleReloadNetworkEnrichment(w http.ResponseWriter, r *http.Request) {
	if !s.networkEnabled(w, r) {
		return
	}
	ctx := r.Context()
	s.d.Network.Enricher().Reload()
	st := s.d.Network.Enricher().Status()
	files := make([]string, 0, len(st.Files))
	for _, f := range st.Files {
		files = append(files, f.Name)
	}
	err := s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		return audit.Event{Action: "network.enrichment.reload", TargetType: "settings", TargetID: "network",
			Details: map[string]any{"directory": st.Directory, "files": files, "country": st.Country, "asn": st.ASN}}, nil
	})
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	writeJSON(w, http.StatusOK, st)
}
