package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"drool/internal/agentcaps"
	"drool/internal/agentpki"
	"drool/internal/agentsvc"
	"drool/internal/audit"
	"drool/internal/auth"
	"drool/internal/model"
	"drool/internal/release"
	"drool/internal/store/db"
)

// Agents (ARCHITECTURE-ALPHA §23): PKI, enrollment, identities, the
// management path of each host, updates and endpoint migration.

func (s *Server) agentsEnabled(w http.ResponseWriter, r *http.Request) bool {
	if s.d.Agents == nil {
		writeProblem(w, r, problem(http.StatusNotImplemented, "unavailable", "the agent endpoint is not configured"))
		return false
	}
	return true
}

// --- management model on host views ---

type managementJSON struct {
	Preferred     string `json:"preferred"` // ssh | agent
	Fallback      string `json:"fallback"`  // none | ssh
	SSHConfigured bool   `json:"ssh_configured"`
	// Active is the path operations use right now: agent, ssh, or none.
	Active     string `json:"active"`
	AgentState string `json:"agent_state"` // connected, not_connected, not_enrolled, incompatible, epoch_mismatch, quarantined, revoked, disabled, "" (SSH host)
	Reason     string `json:"reason,omitempty"`
	Blocked    bool   `json:"blocked"`
}

func (s *Server) management(ctx context.Context, h db.Host) *managementJSON {
	m := &managementJSON{Preferred: h.ManagementMethod, Fallback: h.ManagementFallback, SSHConfigured: h.SshConfigured != 0}
	if s.d.Router == nil {
		m.Active = "ssh"
		return m
	}
	d := s.d.Router.Decide(ctx, h)
	m.AgentState, m.Reason, m.Blocked = d.AgentState, d.Reason, d.Blocked
	switch {
	case d.Blocked:
		m.Active = "none"
	case d.UseAgent:
		m.Active = "agent"
	case d.SSHAllowed && h.SshConfigured != 0:
		m.Active = "ssh"
	default:
		m.Active = "none"
	}
	if m.Active == "none" && m.Reason == "" && h.ManagementMethod == "ssh" && h.SshConfigured == 0 {
		// A host whose Shepherd was removed and that has no SSH access
		// (dogfooding correction batch): nothing is invented.
		m.Reason = "DROOL has no way to manage this host: it has no Shepherd and no SSH access is set up. Set up SSH access in its connection settings, " +
			"install a Shepherd again, or remove the host."
	}
	return m
}

// --- PKI ---

type pkiJSON struct {
	agentsvc.Status
	// Endpoint is the port the agent endpoint listens on (inside the
	// container, for a containerized controller).
	Endpoint string `json:"endpoint_port"`
	// AdvertisedEndpoints are where Shepherds enrolled now connect, as
	// this request's web address resolves them (the same resolver as
	// enrollment tokens and installation over SSH); AdvertisedFrom says
	// why: "advertise" (agent.advertise) or "web_address" (the web UI's
	// host name with agent.public_port, else agent.port).
	AdvertisedEndpoints []string `json:"advertised_endpoints"`
	AdvertisedFrom      string   `json:"advertised_from"`
	// AdvertisedPort is the port an address without one gets
	// (agent.public_port, else agent.port).
	AdvertisedPort  int    `json:"advertised_port"`
	ControllerEpoch string `json:"controller_epoch"`
	// The newest trust manifest a Shepherd reported (newer than this
	// controller's after a restore from a backup older than a rotation).
	NewestAgentManifest uint64 `json:"newest_agent_manifest"`
}

func (s *Server) handleAgentPKI(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w, r) {
		return
	}
	from := "web_address"
	if s.d.Agents.EndpointsConfigured() {
		from = "advertise"
	}
	writeJSON(w, http.StatusOK, pkiJSON{Status: s.d.Agents.PKI().Status(), Endpoint: fmt.Sprint(s.d.Agents.ListenPort()),
		AdvertisedEndpoints: s.d.Agents.Endpoints(r.Host), AdvertisedFrom: from, AdvertisedPort: s.d.Agents.AdvertisedPort(),
		ControllerEpoch: s.d.Agents.Epoch(), NewestAgentManifest: s.d.Agents.PKI().NewestAgentManifest(r.Context())})
}

func (s *Server) handleAgentRootCert(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", `attachment; filename="drool-agent-root-ca.crt"`)
	w.Write(agentpki.CertPEM(s.d.Agents.PKI().Root()))
}

type exportRootJSON struct {
	CertPEM string `json:"cert_pem"`
	KeyPEM  string `json:"key_pem"`
}

// handleAgentExportRoot returns the root private key for offline storage.
func (s *Server) handleAgentExportRoot(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w, r) {
		return
	}
	cert, key, err := s.d.Agents.PKI().ExportRoot()
	if err != nil {
		writeProblem(w, r, errConflict("root_offline", err.Error()))
		return
	}
	if err := s.d.Audit.Record(r.Context(), audit.Event{Action: "agent_ca.export_root", TargetType: "agent_ca",
		Details: map[string]any{"root_fingerprint": s.d.Agents.PKI().RootFingerprint()}}); err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, exportRootJSON{CertPEM: string(cert), KeyPEM: string(key)})
}

type removeRootRequest struct {
	// Confirm must be the root fingerprint: proof the administrator looked
	// at what they exported.
	Confirm string `json:"confirm"`
}

func (s *Server) handleAgentRemoveRoot(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w, r) {
		return
	}
	var req removeRootRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	fp := s.d.Agents.PKI().RootFingerprint()
	if req.Confirm != fp {
		writeProblem(w, r, errValidation("confirm with the root fingerprint "+fp))
		return
	}
	if !s.d.Agents.PKI().RootOnline() {
		writeProblem(w, r, errConflict("root_offline", "the root key is already offline"))
		return
	}
	if err := s.d.Agents.PKI().RemoveRootKey(r.Context()); err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	s.d.Audit.Record(r.Context(), audit.Event{Action: "agent_ca.remove_root", TargetType: "agent_ca", Details: map[string]any{"root_fingerprint": fp}})
	writeJSON(w, http.StatusOK, s.d.Agents.PKI().Status())
}

type rotateRequest struct {
	RootKeyPEM string `json:"root_key_pem"` // when the root is offline; used once, not stored
	RevokeOld  bool   `json:"revoke_old"`
}

func (s *Server) handleAgentRotate(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w, r) {
		return
	}
	var req rotateRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	v, err := s.d.Agents.PKI().Rotate(r.Context(), []byte(req.RootKeyPEM), req.RevokeOld)
	if err != nil {
		if errors.Is(err, agentpki.ErrRootOffline) {
			writeProblem(w, r, errConflict("root_offline", "the root key is offline: paste it (PEM) to sign the new intermediate and manifest; it is used once and not stored"))
			return
		}
		writeProblem(w, r, errValidation(err.Error()))
		return
	}
	s.d.Audit.Record(r.Context(), audit.Event{Action: "agent_ca.rotate", TargetType: "agent_ca",
		Details: map[string]any{"manifest_version": v, "revoke_old": req.RevokeOld, "root_supplied": req.RootKeyPEM != ""}})
	writeJSON(w, http.StatusOK, s.d.Agents.PKI().Status())
}

func (s *Server) handleAgentImportManifest(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w, r) {
		return
	}
	var sm agentpki.SignedManifest
	if p := decodeJSON(w, r, &sm); p != nil {
		writeProblem(w, r, p)
		return
	}
	v, err := s.d.Agents.PKI().ImportManifest(r.Context(), &sm)
	if err != nil {
		writeProblem(w, r, errValidation(err.Error()))
		return
	}
	s.d.Audit.Record(r.Context(), audit.Event{Action: "agent_ca.import_manifest", TargetType: "agent_ca", Details: map[string]any{"manifest_version": v}})
	writeJSON(w, http.StatusOK, s.d.Agents.PKI().Status())
}

// --- presets ---

type presetJSON struct {
	Name           string         `json:"name"`
	Caps           agentcaps.Caps `json:"caps"`
	RootEquivalent []string       `json:"root_equivalent"`
	File           string         `json:"file"` // the agent.yaml it writes
}

func (s *Server) handleAgentPresets(w http.ResponseWriter, r *http.Request) {
	out := []presetJSON{}
	for _, n := range []string{agentcaps.PresetMonitor, agentcaps.PresetManaged, agentcaps.PresetFull} {
		c, _ := agentcaps.Preset(n)
		re := c.RootEquivalent()
		if re == nil {
			re = []string{}
		}
		out = append(out, presetJSON{Name: n, Caps: c, RootEquivalent: re, File: string(agentcaps.Render(c, n))})
	}
	writeJSON(w, http.StatusOK, listResponse[presetJSON]{Items: out})
}

// --- enrollment tokens ---

type enrollTokenJSON struct {
	ID          string  `json:"id"`
	Description string  `json:"description"`
	HostID      *string `json:"host_id"`
	HostName    string  `json:"host_name"`
	GroupID     *string `json:"group_id"`
	GroupName   string  `json:"group_name"`
	Preset      string  `json:"preset"`
	CreatedAt   string  `json:"created_at"`
	ExpiresAt   string  `json:"expires_at"`
	UsedAt      *string `json:"used_at"`
	RevokedAt   *string `json:"revoked_at"`
	State       string  `json:"state"` // active | used | expired | revoked
}

func (s *Server) handleListAgentTokens(w http.ResponseWriter, r *http.Request) {
	rows, err := s.d.Store.R.ListEnrollmentTokens(r.Context())
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	now := time.Now().UnixMilli()
	out := make([]enrollTokenJSON, 0, len(rows))
	for _, t := range rows {
		st := "active"
		switch {
		case t.UsedAt.Valid:
			st = "used"
		case t.RevokedAt.Valid:
			st = "revoked"
		case t.ExpiresAt <= now:
			st = "expired"
		}
		out = append(out, enrollTokenJSON{ID: t.ID, Description: t.Description, HostID: strPtr(t.HostID.String), HostName: t.HostName,
			GroupID: strPtr(t.GroupID.String), GroupName: t.GroupName, Preset: t.Preset, CreatedAt: msTime(t.CreatedAt),
			ExpiresAt: msTime(t.ExpiresAt), UsedAt: nullMsTime(t.UsedAt), RevokedAt: nullMsTime(t.RevokedAt), State: st})
	}
	writeJSON(w, http.StatusOK, listResponse[enrollTokenJSON]{Items: out})
}

type createEnrollTokenRequest struct {
	Description string `json:"description"`
	HostID      string `json:"host_id"`
	GroupID     string `json:"group_id"`
	Preset      string `json:"preset"`
	TTLMinutes  int    `json:"ttl_minutes"`
}

type createdTokenJSON struct {
	agentsvc.NewToken
	InstallCommand  string `json:"install_command"`
	RootFingerprint string `json:"root_fingerprint"`
	PinnedWebKey    string `json:"pinned_web_key"`
}

// webBase is the controller's web URL as the administrator uses it.
func (s *Server) webBase(r *http.Request) string {
	scheme := "https"
	if s.d.Config.Server.TLS.Mode == "off" {
		scheme = "http"
		if r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
	}
	return scheme + "://" + r.Host
}

func (s *Server) webPin() string {
	if s.d.WebPin == nil || s.d.Config.Server.TLS.Mode == "off" {
		return ""
	}
	return s.d.WebPin()
}

func (s *Server) handleCreateAgentToken(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w, r) {
		return
	}
	var req createEnrollTokenRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	ttl := time.Duration(req.TTLMinutes) * time.Minute
	tok, err := s.d.Agents.CreateToken(r.Context(), agentsvc.TokenOptions{Description: req.Description, HostID: req.HostID,
		GroupID: req.GroupID, Preset: req.Preset, TTL: ttl, UserID: principalFrom(r.Context()).UserID, WebHost: r.Host})
	if err != nil {
		writeProblem(w, r, errValidation(err.Error()))
		return
	}
	pin := s.webPin()
	writeJSON(w, http.StatusCreated, createdTokenJSON{NewToken: *tok, RootFingerprint: s.d.Agents.PKI().RootFingerprint(), PinnedWebKey: pin,
		InstallCommand: agentsvc.InstallCommand(s.webBase(r), pin, tok.Token, req.Preset)})
}

func (s *Server) handleRevokeAgentToken(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w, r) {
		return
	}
	id := r.PathValue("id")
	if err := s.d.Agents.RevokeToken(r.Context(), id); err != nil {
		writeProblem(w, r, errConflict("not_revocable", err.Error()))
		return
	}
	s.d.Audit.Record(r.Context(), audit.Event{Action: "agent.token.revoke", TargetType: "agent_token", TargetID: id})
	w.WriteHeader(http.StatusNoContent)
}

// --- public installer endpoints ---

// handleAgentInstallScript serves the installer. It holds no secret: the
// token comes from the administrator's command line, the download is
// authenticated by the pinned web key, and enrollment by the pinned Agent
// Root CA in the token.
func (s *Server) handleAgentInstallScript(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	io.WriteString(w, s.d.Agents.InstallScript(s.webBase(r), s.webPin()))
}

func (s *Server) handleAgentDownload(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w, r) {
		return
	}
	plat := r.PathValue("platform") // linux-amd64
	arch, ok := strings.CutPrefix(plat, "linux-")
	if !ok {
		writeProblem(w, r, errNotFound("agent build"))
		return
	}
	b, err := s.d.Agents.AgentBinary(arch)
	if err != nil {
		writeProblem(w, r, problem(http.StatusNotFound, "not_found", err.Error()))
		return
	}
	f, err := os.Open(b.Path)
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="drool-agent"`)
	w.Header().Set("X-Checksum-Sha256", b.SHA256)
	http.ServeContent(w, r, "drool-agent", time.Time{}, f)
}

// --- identities and host management ---

type identityJSON struct {
	ID              string          `json:"id"`
	HostID          string          `json:"host_id"`
	Status          string          `json:"status"`
	KeyFingerprint  string          `json:"key_fingerprint"`
	EnrolledAt      string          `json:"enrolled_at"`
	CertNotAfter    string          `json:"cert_not_after"`
	CertRenewAfter  string          `json:"cert_renew_after"`
	RecoveredAt     *string         `json:"recovered_at"`
	RevokedAt       *string         `json:"revoked_at"`
	RevokeReason    string          `json:"revoke_reason,omitempty"`
	Conflict        json.RawMessage `json:"conflict,omitempty"`
	AgentVersion    string          `json:"agent_version"`
	Protocol        int64           `json:"protocol"`
	ProtocolMin     int64           `json:"protocol_min"`
	ProtocolMax     int64           `json:"protocol_max"`
	Compatible      bool            `json:"compatible"`
	CompatReason    string          `json:"compat_reason,omitempty"`
	Hostname        string          `json:"hostname"`
	OS              string          `json:"os"`
	Arch            string          `json:"arch"`
	Ceiling         json.RawMessage `json:"ceiling"`
	Effective       json.RawMessage `json:"effective"`
	RootEquivalent  []string        `json:"root_equivalent"`
	ManifestVersion int64           `json:"manifest_version"`
	EpochReported   string          `json:"epoch_reported"`
	EpochMatches    bool            `json:"epoch_matches"`
	LastSeenAt      *string         `json:"last_seen_at"`
	LastAddress     string          `json:"last_address"`
	Connected       bool            `json:"connected"`
	UpdateState     string          `json:"update_state"`
	UpdateVersion   string          `json:"update_version"`
	UpdateDetail    string          `json:"update_detail"`
	UpdateAt        *string         `json:"update_at"`
	// Compatibility with this controller (M16).
	Compatibility agentsvc.Compatibility `json:"compatibility"`
	// ReleaseTrust: which signatures the Shepherd accepts for its updates,
	// as it last said (null: built before 1.0, it doesn't say).
	ReleaseTrust *agentsvc.ReleaseTrust `json:"release_trust"`
}

// newestRelease is the newest stored agent release for an identity's
// platform that is newer than what it runs ("" when none). A version that
// already failed or rolled back there isn't offered again.
func newestRelease(rels []db.AgentRelease, id db.AgentIdentity) string {
	failed := id.UpdateState == "rolled_back" || id.UpdateState == "failed" || id.UpdateState == "refused"
	best := ""
	for _, rel := range rels {
		if failed && rel.Version == id.UpdateVersion {
			continue
		}
		if rel.Os == id.Os && rel.Arch == id.Arch && release.CompareVersions(rel.Version, id.AgentVersion) > 0 &&
			(best == "" || release.CompareVersions(rel.Version, best) > 0) {
			best = rel.Version
		}
	}
	return best
}

// compatibility judges an identity with what its host's configuration
// needs from it (M16, ARCHITECTURE-ALPHA §33.8).
func (s *Server) compatibility(ctx context.Context, id db.AgentIdentity, rels []db.AgentRelease) agentsvc.Compatibility {
	needed := map[string]string{}
	if s.d.Firewall != nil && s.d.Firewall.HostConfig(ctx, id.HostID).Mode != "off" {
		needed["firewall-1"] = "its firewall is managed"
	}
	if s.d.Recovery != nil {
		if rec, _ := s.d.Recovery.HostStatus(ctx, id.HostID); rec != nil && rec.State != "reconciled" && rec.State != "recovered" {
			needed["epoch-1"] = "this controller was restored and the host isn't reconciled yet"
		}
	}
	return agentsvc.Judge(id, agentsvc.IdentityFeatures(id), needed, newestRelease(rels, id), s.d.Config.Agent.RecoveryGrace, time.Now())
}

func (s *Server) toIdentityJSON(id db.AgentIdentity) identityJSON {
	out := identityJSON{ID: id.ID, HostID: id.HostID, Status: id.Status, KeyFingerprint: id.Fingerprint, EnrolledAt: msTime(id.EnrolledAt),
		CertNotAfter: msTime(id.CertNotAfter), RecoveredAt: nullMsTime(id.RecoveredAt), RevokedAt: nullMsTime(id.RevokedAt),
		RevokeReason: id.RevokeReason, AgentVersion: id.AgentVersion, Protocol: id.Protocol, ProtocolMin: id.ProtocolMin,
		ProtocolMax: id.ProtocolMax, Compatible: id.Compatible != 0, CompatReason: id.CompatReason, Hostname: id.Hostname, OS: id.Os,
		Arch: id.Arch, Ceiling: json.RawMessage(nonEmptyJSON(id.CeilingJson)), Effective: json.RawMessage(nonEmptyJSON(id.EffectiveJson)),
		ManifestVersion: id.ManifestVersion, EpochReported: id.EpochReported, LastSeenAt: nullMsTime(id.LastSeenAt),
		LastAddress: id.LastAddress, Connected: id.Connected != 0, UpdateState: id.UpdateState, UpdateVersion: id.UpdateVersion,
		UpdateDetail: id.UpdateDetail, UpdateAt: nullMsTime(id.UpdateAt), RootEquivalent: []string{}, ReleaseTrust: agentsvc.IdentityTrust(id)}
	if id.CertIssuedAt > 0 {
		out.CertRenewAfter = msTime(id.CertIssuedAt + (id.CertNotAfter-id.CertIssuedAt)*2/3)
	}
	if id.ConflictJson != "" {
		out.Conflict = json.RawMessage(id.ConflictJson)
	}
	if s.d.Agents != nil {
		out.EpochMatches = id.EpochReported == "" || id.EpochReported == s.d.Agents.Epoch()
	}
	var eff agentcaps.Caps
	if json.Unmarshal([]byte(id.EffectiveJson), &eff) == nil {
		if re := eff.RootEquivalent(); re != nil {
			out.RootEquivalent = re
		}
	}
	return out
}

func nonEmptyJSON(s string) string {
	if s == "" {
		return "{}"
	}
	return s
}

type sessionJSON struct {
	Address     string `json:"address"`
	ConnectedAt string `json:"connected_at"`
	LastSeen    string `json:"last_seen"`
	Protocol    uint32 `json:"protocol"`
	Version     string `json:"agent_version"`
	Quarantined bool   `json:"quarantined"`
}

type stateVersionJSON struct {
	Domain           string `json:"domain"`
	Epoch            string `json:"epoch"`
	Revision         int64  `json:"revision"`
	ReportedEpoch    string `json:"reported_epoch"`
	ReportedRevision int64  `json:"reported_revision"`
	Status           string `json:"status"`
}

type hostAgentJSON struct {
	Management    *managementJSON    `json:"management"`
	Identity      *identityJSON      `json:"identity"`
	Sessions      []sessionJSON      `json:"sessions"`
	StateVersions []stateVersionJSON `json:"state_versions"`
	Requested     json.RawMessage    `json:"requested_caps"`
	// SSHBypassesCeiling: SSH fallback with root access exists; it is not
	// limited by the agent's ceiling (used only when the agent is
	// unreachable, never to work around a refusal).
	SSHBypassesCeiling bool   `json:"ssh_bypasses_ceiling"`
	UpdateAvailable    string `json:"update_available,omitempty"`
}

func (s *Server) handleHostAgent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	h, err := s.d.Store.R.GetHost(ctx, r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, errNotFound("host"))
		return
	}
	out := hostAgentJSON{Management: s.management(ctx, h), Sessions: []sessionJSON{}, StateVersions: []stateVersionJSON{},
		Requested:          json.RawMessage("null"),
		SSHBypassesCeiling: h.SshConfigured != 0 && h.ManagementFallback == "ssh" && (h.SshUser == "root" || h.BecomeMethod == "sudo")}
	if h.RequestedCapsJson != "" {
		out.Requested = json.RawMessage(h.RequestedCapsJson)
	}
	ids, _ := s.d.Store.R.ListIdentitiesForHost(ctx, h.ID)
	if len(ids) > 0 {
		j := s.toIdentityJSON(ids[0])
		rels, _ := s.d.Store.R.ListReleases(ctx)
		j.Compatibility = s.compatibility(ctx, ids[0], rels)
		out.Identity = &j
		if s.d.Agents != nil {
			for _, se := range s.d.Agents.HostSessions(h.ID) {
				out.Sessions = append(out.Sessions, sessionJSON{Address: se.Address, ConnectedAt: se.ConnectedAt.UTC().Format(time.RFC3339),
					LastSeen: se.LastSeen.UTC().Format(time.RFC3339), Protocol: se.Protocol, Version: se.AgentVersion, Quarantined: se.Quarantined})
			}
			// A version that already failed or rolled back here isn't
			// offered again from the host page (a rollout still can).
			out.UpdateAvailable = newestRelease(rels, ids[0])
		}
	}
	svs, _ := s.d.Store.R.ListStateVersions(ctx, h.ID)
	for _, v := range svs {
		out.StateVersions = append(out.StateVersions, stateVersionJSON{Domain: v.Domain, Epoch: v.Epoch, Revision: v.Revision,
			ReportedEpoch: v.ReportedEpoch, ReportedRevision: v.ReportedRevision, Status: v.Status})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	rows, err := s.d.Store.R.ListLiveIdentities(r.Context())
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	rels, _ := s.d.Store.R.ListReleases(r.Context())
	out := make([]identityJSON, 0, len(rows))
	for _, id := range rows {
		j := s.toIdentityJSON(id)
		j.Compatibility = s.compatibility(r.Context(), id, rels)
		out = append(out, j)
	}
	writeJSON(w, http.StatusOK, listResponse[identityJSON]{Items: out})
}

type managementRequest struct {
	Preferred string `json:"preferred"`
	Fallback  string `json:"fallback"`
}

func (s *Server) handleSetManagement(w http.ResponseWriter, r *http.Request) {
	var req managementRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	h, err := s.d.Store.R.GetHost(ctx, r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, errNotFound("host"))
		return
	}
	if req.Preferred != "ssh" && req.Preferred != "agent" || req.Fallback != "none" && req.Fallback != "ssh" {
		writeProblem(w, r, errValidation("preferred must be ssh or agent, fallback none or ssh"))
		return
	}
	if (req.Preferred == "ssh" || req.Fallback == "ssh") && h.SshConfigured == 0 {
		writeProblem(w, r, errValidation("this host has no SSH configuration"))
		return
	}
	if req.Preferred == "ssh" {
		req.Fallback = "none"
	}
	err = s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		if err := q.SetHostManagement(ctx, db.SetHostManagementParams{ManagementMethod: req.Preferred, ManagementFallback: req.Fallback,
			UpdatedAt: time.Now().UnixMilli(), ID: h.ID}); err != nil {
			return audit.Event{}, err
		}
		return audit.Event{Action: "host.management", TargetType: "host", TargetID: h.ID,
			Details: map[string]any{"preferred": req.Preferred, "fallback": req.Fallback,
				"was_preferred": h.ManagementMethod, "was_fallback": h.ManagementFallback}}, nil
	})
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	h, _ = s.d.Store.R.GetHost(ctx, h.ID)
	writeJSON(w, http.StatusOK, s.management(ctx, h))
}

type capsRequest struct {
	// Caps narrows the agent's ceiling; null removes the narrowing.
	Caps *agentcaps.Caps `json:"caps"`
}

func (s *Server) handleSetAgentCaps(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w, r) {
		return
	}
	var req capsRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	id := r.PathValue("id")
	if err := s.d.Agents.SetRequestedCaps(r.Context(), id, req.Caps); err != nil {
		writeProblem(w, r, errValidation(err.Error()))
		return
	}
	s.d.Audit.Record(r.Context(), audit.Event{Action: "agent.caps", TargetType: "host", TargetID: id, Details: map[string]any{"requested": req.Caps}})
	w.WriteHeader(http.StatusNoContent)
}

type installRequest struct {
	Preset     string `json:"preset"`
	ReleaseKey string `json:"release_key"`
}

type installResultJSON struct {
	OK     bool   `json:"ok"`
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
}

// handleInstallAgent installs the agent over the host's trusted SSH
// connection (synchronous; up to a few minutes).
func (s *Server) handleInstallAgent(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w, r) {
		return
	}
	var req installRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	id := r.PathValue("id")
	release, ok := s.d.Engine.Locks().TryAcquire(id, "agent-install", model.LockExclusive)
	if !ok {
		writeProblem(w, r, errConflict("host_busy", "a job is using this host right now; try again when it has finished"))
		return
	}
	defer release()
	var out strings.Builder
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	err := s.d.Agents.InstallViaSSH(ctx, agentsvc.InstallOptions{HostID: id, Preset: req.Preset, ReleaseKey: req.ReleaseKey,
		UserID: principalFrom(r.Context()).UserID, WebHost: r.Host}, &out)
	res := installResultJSON{OK: err == nil, Output: out.String()}
	if err != nil {
		res.Error = err.Error()
		s.d.Audit.Record(context.WithoutCancel(r.Context()), audit.Event{Action: "agent.install", TargetType: "host", TargetID: id,
			Result: audit.Failure, Details: map[string]any{"preset": req.Preset, "error": err.Error()}})
	}
	writeJSON(w, http.StatusOK, res)
}

// handlePlanUninstallAgent says what removing the host's Shepherd would do
// and which ways work now (the Shepherd itself, SSH); it changes nothing.
func (s *Server) handlePlanUninstallAgent(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w, r) {
		return
	}
	plan, err := s.d.Agents.PlanUninstall(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeProblem(w, r, errNotFound("host"))
		return
	}
	if err != nil {
		writeProblem(w, r, s.internalError(r, err))
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

// handleUninstallAgent removes the host's Shepherd (agentsvc.Uninstall):
// the Shepherd removes itself when it can, else DROOL removes it over SSH,
// or, asked for, DROOL only forgets it. agents.manage and hosts.write (the
// management path changes), confirmed by a recent sign-in, under the host's
// exclusive lock. A refusal changes nothing.
func (s *Server) handleUninstallAgent(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w, r) {
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	var in struct {
		Method string `json:"method"`
	}
	if r.ContentLength != 0 {
		if p := decodeJSON(w, r, &in); p != nil {
			writeProblem(w, r, p)
			return
		}
	}
	switch in.Method {
	case "", agentsvc.MethodShepherd, agentsvc.MethodSSH, agentsvc.MethodForget:
	default:
		writeProblem(w, r, errValidation("method must be shepherd, ssh or forget"))
		return
	}
	if !principalFrom(ctx).Can(auth.PermHostsWrite) {
		writeProblem(w, r, errForbidden("removing a Shepherd changes how DROOL manages the host, which also requires hosts.write"))
		return
	}
	if _, err := s.d.Store.R.GetHost(ctx, id); err != nil {
		writeProblem(w, r, errNotFound("host"))
		return
	}
	release, ok := s.d.Engine.Locks().TryAcquire(id, "agent-uninstall", model.LockExclusive)
	if !ok {
		writeProblem(w, r, errConflict("host_busy", "a job is using this host right now; try again when it has finished"))
		return
	}
	defer release()
	uctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	res, err := s.d.Agents.Uninstall(uctx, id, principalFrom(ctx).UserID, in.Method)
	var refused *agentsvc.ErrUninstallRefused
	switch {
	case errors.As(err, &refused):
		s.d.Audit.Record(context.WithoutCancel(ctx), audit.Event{Action: "agent.uninstall", Result: audit.Denied, TargetType: "host", TargetID: id,
			Details: map[string]any{"refused": refused.Code, "reason": refused.Msg, "method": in.Method}})
		writeProblem(w, r, errConflict(refused.Code, refused.Msg))
		return
	case err != nil:
		writeProblem(w, r, s.internalError(r, err))
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type revokeRequest struct {
	Reason string `json:"reason"`
	// LeaveUnmanaged acknowledges that the host has no other way in for
	// DROOL (no SSH): after the revocation only someone with access to the
	// host can enroll its Shepherd again (M16).
	LeaveUnmanaged bool `json:"leave_unmanaged"`
}

func (s *Server) handleRevokeAgent(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w, r) {
		return
	}
	var req revokeRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		writeProblem(w, r, errValidation("give a reason (it is recorded in the audit log)"))
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	if cur, err := s.d.Store.R.GetAgentIdentity(ctx, id); err == nil && !req.LeaveUnmanaged {
		if h, err := s.d.Store.R.GetHost(ctx, cur.HostID); err == nil && h.SshConfigured == 0 {
			writeProblem(w, r, errConflict("last_management_path", "the Shepherd is DROOL's only way to manage "+h.Name+" (no SSH): after the revocation "+
				"DROOL can't reach it until someone with access to the host enrolls its Shepherd again. Confirm that this is what you want."))
			return
		}
	}
	var ident db.AgentIdentity
	err := s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		var err error
		ident, err = s.d.Agents.RevokeIdentity(ctx, q, id, principalFrom(ctx).UserID, req.Reason)
		if err != nil {
			return audit.Event{}, err
		}
		return audit.Event{Action: "agent.revoke", TargetType: "host", TargetID: ident.HostID,
			Details: map[string]any{"identity_id": id, "reason": req.Reason, "key_fingerprint": ident.Fingerprint}}, nil
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, errNotFound("agent identity"))
			return
		}
		writeProblem(w, r, errConflict("not_revocable", err.Error()))
		return
	}
	s.d.Agents.DisconnectIdentity(id, "revoked", "this agent identity was revoked: "+req.Reason)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleResolveConflict(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w, r) {
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	err := s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		if err := s.d.Agents.ResolveConflict(ctx, q, id); err != nil {
			return audit.Event{}, err
		}
		return audit.Event{Action: "agent.conflict.resolve", TargetType: "agent_identity", TargetID: id}, nil
	})
	if err != nil {
		writeProblem(w, r, errConflict("conflict_unresolved", err.Error()))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdoptState(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w, r) {
		return
	}
	host, domain := r.PathValue("id"), r.PathValue("domain")
	if err := s.d.Agents.AdoptHostState(r.Context(), host, domain); err != nil {
		writeProblem(w, r, errConflict("not_ahead", err.Error()))
		return
	}
	s.d.Audit.Record(r.Context(), audit.Event{Action: "state.adopt", TargetType: "host", TargetID: host, Details: map[string]any{"domain": domain}})
	w.WriteHeader(http.StatusNoContent)
}

// --- updates ---

func (s *Server) handleGetUpdatePolicy(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, s.d.Agents.GetUpdatePolicy(r.Context()))
}

func (s *Server) handleSetUpdatePolicy(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w, r) {
		return
	}
	var p agentsvc.UpdatePolicy
	if prob := decodeJSON(w, r, &p); prob != nil {
		writeProblem(w, r, prob)
		return
	}
	ctx := r.Context()
	err := s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		if err := s.d.Agents.SetUpdatePolicy(ctx, q, p, principalFrom(ctx).UserID); err != nil {
			return audit.Event{}, err
		}
		return audit.Event{Action: "agent.update_policy", Details: map[string]any{"policy": p}}, nil
	})
	if err != nil {
		writeProblem(w, r, errValidation(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, s.d.Agents.GetUpdatePolicy(ctx))
}

// handleUpdateStatus is the Shepherd update overview (agents.read).
func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w, r) {
		return
	}
	st, err := s.d.Agents.UpdateStatus(r.Context())
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleCheckUpdates starts a check for official releases now; the status
// says when it is done.
func (s *Server) handleCheckUpdates(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w, r) {
		return
	}
	s.d.Agents.TriggerReleaseCheck()
	st, err := s.d.Agents.UpdateStatus(r.Context())
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	writeJSON(w, http.StatusAccepted, st)
}

type releaseKeyJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
	AddedAt   string `json:"added_at"`
}

func (s *Server) handleListReleaseKeys(w http.ResponseWriter, r *http.Request) {
	rows, err := s.d.Store.R.ListReleaseKeys(r.Context())
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	out := make([]releaseKeyJSON, 0, len(rows))
	for _, k := range rows {
		out = append(out, releaseKeyJSON{ID: k.ID, Name: k.Name, PublicKey: release.FormatPublicKey(k.PublicKey, k.Name), AddedAt: msTime(k.AddedAt)})
	}
	writeJSON(w, http.StatusOK, listResponse[releaseKeyJSON]{Items: out})
}

type addReleaseKeyRequest struct {
	PublicKey string `json:"public_key"` // "drool-release-ed25519 <base64> name"
}

func (s *Server) handleAddReleaseKey(w http.ResponseWriter, r *http.Request) {
	var req addReleaseKeyRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	k, err := release.ParsePublicKey(req.PublicKey)
	if err != nil {
		writeProblem(w, r, errValidation(err.Error()))
		return
	}
	ctx := r.Context()
	err = s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		if err := q.InsertReleaseKey(ctx, db.InsertReleaseKeyParams{ID: k.ID(), PublicKey: k.Key, Name: k.Name,
			AddedBy: sql.NullString{String: principalFrom(ctx).UserID, Valid: true}, AddedAt: time.Now().UnixMilli()}); err != nil {
			return audit.Event{}, err
		}
		return audit.Event{Action: "agent.release_key.add", TargetType: "release_key", TargetID: k.ID(), Details: map[string]any{"name": k.Name}}, nil
	})
	if err != nil {
		writeProblem(w, r, errConflict("exists", "this key is already registered"))
		return
	}
	writeJSON(w, http.StatusCreated, releaseKeyJSON{ID: k.ID(), Name: k.Name, PublicKey: release.FormatPublicKey(k.Key, k.Name), AddedAt: time.Now().UTC().Format(time.RFC3339)})
}

func (s *Server) handleDeleteReleaseKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	err := s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		n, err := q.DeleteReleaseKey(ctx, id)
		if err != nil {
			return audit.Event{}, err
		}
		if n == 0 {
			return audit.Event{}, sql.ErrNoRows
		}
		return audit.Event{Action: "agent.release_key.delete", TargetType: "release_key", TargetID: id}, nil
	})
	if err != nil {
		writeProblem(w, r, errNotFound("release key"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type releaseJSON struct {
	ID          string `json:"id"`
	Version     string `json:"version"`
	Channel     string `json:"channel"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	ProtocolMin int64  `json:"protocol_min"`
	ProtocolMax int64  `json:"protocol_max"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	KeyID       string `json:"key_id"`
	// Official: signed with the DROOL project's release key (a custom
	// release is signed with an operator's own).
	Official bool `json:"official"`
	// ShepherdsBehind: active Shepherds on this release's platform that run
	// an older version, i.e. what updating to it would update.
	ShepherdsBehind int    `json:"shepherds_behind"`
	CreatedAt       string `json:"created_at"`
}

func toReleaseJSON(r db.AgentRelease) releaseJSON {
	return releaseJSON{ID: r.ID, Version: r.Version, Channel: r.Channel, OS: r.Os, Arch: r.Arch, ProtocolMin: r.ProtocolMin,
		ProtocolMax: r.ProtocolMax, SHA256: r.Sha256, Size: r.Size, KeyID: r.KeyID, Official: release.IsOfficialKey(r.KeyID),
		CreatedAt: msTime(r.CreatedAt)}
}

func (s *Server) handleListReleases(w http.ResponseWriter, r *http.Request) {
	rows, err := s.d.Store.R.ListReleases(r.Context())
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	idents, _ := s.d.Store.R.ListLiveIdentities(r.Context())
	out := make([]releaseJSON, 0, len(rows))
	for _, rel := range rows {
		j := toReleaseJSON(rel)
		for _, id := range idents {
			if id.Status == "active" && id.Os == rel.Os && id.Arch == rel.Arch && release.CompareVersions(id.AgentVersion, rel.Version) < 0 {
				j.ShepherdsBehind++
			}
		}
		out = append(out, j)
	}
	writeJSON(w, http.StatusOK, listResponse[releaseJSON]{Items: out})
}

// handleUploadRelease takes the binary as the body and the signed
// manifest (the .release.json file) base64-encoded in X-DROOL-Release.
func (s *Server) handleUploadRelease(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w, r) {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(r.Header.Get("X-DROOL-Release"))
	if err != nil || len(raw) == 0 {
		writeProblem(w, r, errValidation("the signed release manifest (.release.json) must be sent base64-encoded in the X-DROOL-Release header"))
		return
	}
	signed, err := agentsvc.ParseSignedRelease(raw)
	if err != nil {
		writeProblem(w, r, errValidation(err.Error()))
		return
	}
	http.NewResponseController(w).SetReadDeadline(time.Time{})
	// No body: the manifest describes a Shepherd binary this controller
	// ships (an official release's description, uploaded by hand).
	var bin io.Reader
	if r.ContentLength != 0 {
		bin = http.MaxBytesReader(w, r.Body, 256<<20)
	}
	rel, err := s.d.Agents.AddRelease(r.Context(), signed, bin, principalFrom(r.Context()).UserID)
	if err != nil {
		var ue *agentsvc.ReleaseUploadError
		if errors.As(err, &ue) {
			writeProblem(w, r, errValidation(ue.Msg))
			return
		}
		writeProblem(w, r, errInternal)
		return
	}
	s.d.Audit.Record(r.Context(), audit.Event{Action: "agent.release.add", TargetType: "agent_release", TargetID: rel.ID,
		Details: map[string]any{"version": rel.Version, "os": rel.Os, "arch": rel.Arch, "sha256": rel.Sha256, "key_id": rel.KeyID,
			"official": release.IsOfficialKey(rel.KeyID), "binary_uploaded": bin != nil}})
	writeJSON(w, http.StatusCreated, toReleaseJSON(*rel))
}

func (s *Server) handleDeleteRelease(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	n, err := s.d.Store.W.DeleteRelease(r.Context(), id)
	if err != nil || n == 0 {
		writeProblem(w, r, errNotFound("release"))
		return
	}
	s.d.Audit.Record(r.Context(), audit.Event{Action: "agent.release.delete", TargetType: "agent_release", TargetID: id})
	w.WriteHeader(http.StatusNoContent)
}

type rolloutJSON struct {
	ID            string            `json:"id"`
	Version       string            `json:"version"`
	Status        string            `json:"status"`
	Canary        int64             `json:"canary"`
	BatchSize     int64             `json:"batch_size"`
	MaxFailures   int64             `json:"max_failures"`
	HealthWindowS int64             `json:"health_window_s"`
	Failures      int64             `json:"failures"`
	PausedReason  string            `json:"paused_reason"`
	Automatic     bool              `json:"automatic"`
	Retry         bool              `json:"retry"`  // tries a version again where it failed
	Kind          string            `json:"kind"`   // update | install (SSH → agent conversion)
	Preset        string            `json:"preset"` // install: the capability preset
	CreatedAt     string            `json:"created_at"`
	UpdatedAt     string            `json:"updated_at"`
	Hosts         []rolloutHostJSON `json:"hosts,omitempty"`
}

type rolloutHostJSON struct {
	HostID      string `json:"host_id"`
	HostName    string `json:"host_name"`
	Wave        int64  `json:"wave"`
	State       string `json:"state"`
	FromVersion string `json:"from_version"`
	Detail      string `json:"detail"`
	Offline     bool   `json:"offline"`
	UpdatedAt   string `json:"updated_at"`
}

func toRolloutJSON(r db.AgentRollout) rolloutJSON {
	return rolloutJSON{ID: r.ID, Version: r.Version, Status: r.Status, Canary: r.Canary, BatchSize: r.BatchSize, MaxFailures: r.MaxFailures,
		HealthWindowS: r.HealthWindowS, Failures: r.Failures, PausedReason: r.PausedReason, Automatic: r.Automatic != 0, Retry: r.Retry != 0,
		Kind: r.Kind, Preset: r.Preset, CreatedAt: msTime(r.CreatedAt), UpdatedAt: msTime(r.UpdatedAt)}
}

func (s *Server) handleListRollouts(w http.ResponseWriter, r *http.Request) {
	rows, err := s.d.Store.R.ListRollouts(r.Context())
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	out := make([]rolloutJSON, 0, len(rows))
	for _, ro := range rows {
		out = append(out, toRolloutJSON(ro))
	}
	writeJSON(w, http.StatusOK, listResponse[rolloutJSON]{Items: out})
}

func (s *Server) handleGetRollout(w http.ResponseWriter, r *http.Request) {
	ro, err := s.d.Store.R.GetRollout(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, errNotFound("rollout"))
		return
	}
	out := toRolloutJSON(ro)
	hosts, _ := s.d.Store.R.ListRolloutHosts(r.Context(), ro.ID)
	out.Hosts = []rolloutHostJSON{}
	for _, h := range hosts {
		offline := true
		if s.d.Agents != nil {
			_, offline = s.d.Agents.SessionFor(h.HostID)
			offline = !offline
		}
		out.Hosts = append(out.Hosts, rolloutHostJSON{HostID: h.HostID, HostName: h.HostName, Wave: h.Wave, State: h.State,
			FromVersion: h.FromVersion, Detail: h.Detail, Offline: offline, UpdatedAt: msTime(h.UpdatedAt)})
	}
	writeJSON(w, http.StatusOK, out)
}

type startRolloutRequest struct {
	Version string   `json:"version"`
	HostIDs []string `json:"host_ids"` // empty: every agent
	// Retry tries the version again on hosts where it failed before (a
	// person's Try again); a Shepherd never retries one by itself.
	Retry bool `json:"retry"`
}

func (s *Server) handleStartRollout(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w, r) {
		return
	}
	var req startRolloutRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	rels, _ := s.d.Store.R.ListReleases(r.Context())
	found := false
	for _, rel := range rels {
		found = found || rel.Version == req.Version
	}
	if !found {
		writeProblem(w, r, errValidation("no uploaded release has version "+req.Version))
		return
	}
	id, err := s.d.Agents.StartRollout(r.Context(), req.Version, agentsvc.RolloutOptions{HostIDs: req.HostIDs, Policy: s.d.Agents.GetUpdatePolicy(r.Context()),
		Retry: req.Retry, UserID: principalFrom(r.Context()).UserID})
	if errors.Is(err, agentsvc.ErrNothingToUpdate) {
		writeProblem(w, r, errConflict("up_to_date", err.Error()))
		return
	}
	if err != nil {
		writeProblem(w, r, errValidation(err.Error()))
		return
	}
	ro, _ := s.d.Store.R.GetRollout(r.Context(), id)
	writeJSON(w, http.StatusCreated, toRolloutJSON(ro))
}

func (s *Server) handleRolloutAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.agentsEnabled(w, r) {
			return
		}
		id := r.PathValue("id")
		ro, err := s.d.Store.R.GetRollout(r.Context(), id)
		if err != nil {
			writeProblem(w, r, errNotFound("rollout"))
			return
		}
		next := map[string]string{"pause": "paused", "resume": "running", "cancel": "cancelled"}[action]
		if ro.Status == "completed" || ro.Status == "cancelled" {
			writeProblem(w, r, errConflict("finished", "this rollout is "+ro.Status))
			return
		}
		reason := ""
		switch action {
		case "pause":
			reason = "paused by " + principalFrom(r.Context()).Username
		case "cancel":
			reason = "cancelled by " + principalFrom(r.Context()).Username
		}
		if action == "resume" {
			// Resuming accepts the failures so far: count from zero again.
			s.d.Store.W.SetRolloutFailures(r.Context(), db.SetRolloutFailuresParams{Failures: 0, UpdatedAt: time.Now().UnixMilli(), ID: id})
		}
		if err := s.d.Agents.SetRolloutStatus(r.Context(), id, next, reason); err != nil {
			writeProblem(w, r, errInternal)
			return
		}
		s.d.Audit.Record(r.Context(), audit.Event{Action: "agent.rollout." + action, TargetType: "agent_rollout", TargetID: id})
		ro, _ = s.d.Store.R.GetRollout(r.Context(), id)
		writeJSON(w, http.StatusOK, toRolloutJSON(ro))
	}
}

// --- endpoint migration ---

type endpointMigrationJSON struct {
	ID          string  `json:"id"`
	Endpoint    string  `json:"endpoint"`
	Status      string  `json:"status"`
	CreatedAt   string  `json:"created_at"`
	CommittedAt *string `json:"committed_at"`
	Confirmed   int     `json:"confirmed"`
	Failed      int     `json:"failed"`
	Committed   int     `json:"committed"`
	Agents      int     `json:"agents"`
}

func (s *Server) handleListEndpointMigrations(w http.ResponseWriter, r *http.Request) {
	rows, err := s.d.Store.R.ListEndpointMigrations(r.Context())
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	live, _ := s.d.Store.R.ListLiveIdentities(r.Context())
	out := make([]endpointMigrationJSON, 0, len(rows))
	for _, m := range rows {
		j := endpointMigrationJSON{ID: m.ID, Endpoint: m.Endpoint, Status: m.Status, CreatedAt: msTime(m.CreatedAt),
			CommittedAt: nullMsTime(m.CommittedAt), Agents: len(live)}
		tests, _ := s.d.Store.R.ListEndpointTests(r.Context(), m.ID)
		for _, t := range tests {
			if t.Ok != 0 {
				j.Confirmed++
			} else {
				j.Failed++
			}
			if t.Committed != 0 {
				j.Committed++
			}
		}
		out = append(out, j)
	}
	writeJSON(w, http.StatusOK, listResponse[endpointMigrationJSON]{Items: out})
}

type proposeEndpointRequest struct {
	Endpoint string `json:"endpoint"` // host:port
}

func (s *Server) handleProposeEndpoint(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w, r) {
		return
	}
	var req proposeEndpointRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	ep, err := s.d.Agents.Endpoint(req.Endpoint)
	if err != nil {
		writeProblem(w, r, errValidation(err.Error()))
		return
	}
	id, err := s.d.Agents.ProposeEndpoint(r.Context(), ep, principalFrom(r.Context()).UserID)
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	s.d.Audit.Record(r.Context(), audit.Event{Action: "agent.endpoint.propose", TargetType: "agent_endpoint", TargetID: id, Details: map[string]any{"endpoint": ep}})
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (s *Server) handleCommitEndpoint(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w, r) {
		return
	}
	id := r.PathValue("id")
	n, err := s.d.Agents.CommitEndpoint(r.Context(), id)
	if err != nil {
		writeProblem(w, r, errConflict("not_committable", err.Error()))
		return
	}
	s.d.Audit.Record(r.Context(), audit.Event{Action: "agent.endpoint.commit", TargetType: "agent_endpoint", TargetID: id, Details: map[string]any{"agents_now": n}})
	writeJSON(w, http.StatusOK, map[string]int{"committed_now": n})
}
