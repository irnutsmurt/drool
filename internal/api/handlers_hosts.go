package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"drool/internal/audit"
	"drool/internal/auth"
	"drool/internal/driver"
	"drool/internal/inventory"
	"drool/internal/model"
	"drool/internal/packdeps"
	"drool/internal/store/db"
)

type hostKeyJSON struct {
	ID          string  `json:"id"`
	Type        string  `json:"type"`
	Fingerprint string  `json:"fingerprint"`
	Status      string  `json:"status"`
	FirstSeenAt string  `json:"first_seen_at"`
	LastSeenAt  string  `json:"last_seen_at"`
	DecidedAt   *string `json:"decided_at"`
}

func toHostKeyJSON(k db.SshHostKey) hostKeyJSON {
	return hostKeyJSON{ID: k.ID, Type: k.KeyType, Fingerprint: k.FingerprintSha256, Status: k.Status,
		FirstSeenAt: msTime(k.FirstSeenAt), LastSeenAt: msTime(k.LastSeenAt), DecidedAt: nullMsTime(k.DecidedAt)}
}

type hostJSON struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	Description        string            `json:"description"`
	Address            string            `json:"address"`
	Port               int64             `json:"port"`
	User               string            `json:"user"`
	CredentialID       string            `json:"credential_id"`
	BecomeMethod       string            `json:"become_method"`
	BecomeCredentialID *string           `json:"become_credential_id"`
	NeedsPTY           bool              `json:"needs_pty"`
	JumpOnly           bool              `json:"jump_only"`
	ViaHostID          *string           `json:"via_host_id"`
	Status             string            `json:"status"`
	StatusDetail       string            `json:"status_detail"`
	LastProbeAt        *string           `json:"last_probe_at"`
	Tags               []string          `json:"tags"`
	GroupIDs           []string          `json:"group_ids"`
	PinnedKey          *hostKeyJSON      `json:"pinned_key"`
	Facts              *factsSummaryJSON `json:"facts"`
	Health             *healthJSON       `json:"health"`
	Management         *managementJSON   `json:"management"`
	// SSHAuth: how DROOL authenticates over SSH, "credential" or
	// "certificate" (short-lived certificates from its CA, M16).
	SSHAuth      string `json:"ssh_auth"`
	SSHPrincipal string `json:"ssh_principal"`
	// SSHAccountAt: when the host switched to DROOL's own SSH account
	// (After M17); null once its sign-in was edited.
	SSHAccountAt *string `json:"ssh_account_at"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (s *Server) toHostJSON(v inventory.HostView) hostJSON {
	out := hostJSON{
		ID: v.ID, Name: v.Name, Description: v.Description, Address: v.ConnectAddress, Port: v.SshPort,
		User: v.SshUser, CredentialID: v.SshCredentialID.String, BecomeMethod: v.BecomeMethod,
		BecomeCredentialID: strPtr(v.BecomeCredentialID.String), NeedsPTY: v.SshNeedsPty != 0,
		JumpOnly: v.JumpOnly != 0, ViaHostID: strPtr(v.ViaHostID), Status: v.Status, StatusDetail: v.StatusDetail,
		LastProbeAt: nullMsTime(v.LastProbeAt), Tags: v.Tags, GroupIDs: v.GroupIDs,
		SSHAuth: v.SshAuth, SSHPrincipal: v.SshPrincipal, SSHAccountAt: nullMsTime(v.SshAccountAt),
		CreatedAt: msTime(v.CreatedAt), UpdatedAt: msTime(v.UpdatedAt),
	}
	if out.Tags == nil {
		out.Tags = []string{}
	}
	if out.GroupIDs == nil {
		out.GroupIDs = []string{}
	}
	if v.PinnedKey != nil {
		k := toHostKeyJSON(*v.PinnedKey)
		out.PinnedKey = &k
	}
	out.Facts, out.Health = toFactsSummary(v.Facts), s.toHealth(v.Health)
	out.Management = s.management(context.Background(), v.Host)
	return out
}

func (s *Server) inventoryError(err error) *Problem {
	var ve *inventory.ValidationError
	var ce *inventory.ConflictError
	var oe *model.OpError
	switch {
	case errors.Is(err, inventory.ErrHostNotFound):
		return errNotFound("host")
	case errors.Is(err, inventory.ErrGroupNotFound):
		return errNotFound("group")
	case errors.Is(err, inventory.ErrHostKeyNotFound):
		return errNotFound("host key")
	case errors.Is(err, inventory.ErrHostNameTaken), errors.Is(err, inventory.ErrGroupTaken):
		return errConflict("name_taken", err.Error())
	case errors.As(err, &ce):
		return errConflict("conflict", err.Error())
	case errors.As(err, &ve):
		return errValidation(err.Error())
	case errors.As(err, &oe):
		p := problem(http.StatusBadGateway, "host_error", oe.Error())
		p.ErrorClass = string(oe.Class)
		p.Retryable = &oe.Retryable
		if oe.Hint != "" {
			p.Detail += " — " + oe.Hint
		}
		return p
	}
	s.log.Error("inventory operation", "err", err)
	return errInternal
}

func (s *Server) handleListHosts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	hosts, err := s.d.Inventory.ListHosts(r.Context(), inventory.HostFilter{
		Query: q.Get("q"), Tag: q.Get("tag"), GroupID: q.Get("group"), Status: q.Get("status"),
	})
	if err != nil {
		writeProblem(w, r, s.inventoryError(err))
		return
	}
	out := listResponse[hostJSON]{Items: make([]hostJSON, 0, len(hosts))}
	for _, h := range hosts {
		out.Items = append(out.Items, s.toHostJSON(h))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetHost(w http.ResponseWriter, r *http.Request) {
	v, err := s.d.Inventory.GetHost(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, s.inventoryError(err))
		return
	}
	writeJSON(w, http.StatusOK, s.toHostJSON(*v))
}

type hostRequest struct {
	Name               *string   `json:"name"`
	Description        *string   `json:"description"`
	Address            *string   `json:"address"`
	Port               *int      `json:"port"`
	User               *string   `json:"user"`
	CredentialID       *string   `json:"credential_id"`
	BecomeMethod       *string   `json:"become_method"`
	BecomeCredentialID *string   `json:"become_credential_id"` // "" or null clears
	NeedsPTY           *bool     `json:"needs_pty"`
	JumpOnly           *bool     `json:"jump_only"`
	ViaHostID          *string   `json:"via_host_id"` // "" or null clears
	Tags               *[]string `json:"tags"`
	GroupIDs           *[]string `json:"group_ids"`
	// Creation only: a host that already trusts DROOL's SSH certificate
	// authority. Existing hosts switch with PUT /hosts/{id}/ssh-auth.
	SSHAuth      *string `json:"ssh_auth"`
	SSHPrincipal *string `json:"ssh_principal"`
	// Creation only: credentials entered just to set up DROOL's own SSH
	// account on the host, stored as temporary credentials.
	SetupCredentials *setupCredentials `json:"setup_credentials"`
}

// switchesSignIn reports whether an edit changes how DROOL signs in to the
// host and becomes root there: the user, the credential, or the privilege
// escalation and its credential.
func (req hostRequest) switchesSignIn(h db.Host) bool {
	differs := func(p *string, cur string) bool { return p != nil && *p != cur }
	return differs(req.User, h.SshUser) || differs(req.CredentialID, h.SshCredentialID.String) ||
		differs(req.BecomeMethod, h.BecomeMethod) || differs(req.BecomeCredentialID, h.BecomeCredentialID.String)
}

func (req hostRequest) apply(in *inventory.HostInput) {
	set := func(dst *string, src *string) {
		if src != nil {
			*dst = *src
		}
	}
	set(&in.SSHAuth, req.SSHAuth)
	set(&in.Principal, req.SSHPrincipal)
	set(&in.Name, req.Name)
	set(&in.Description, req.Description)
	set(&in.Address, req.Address)
	set(&in.User, req.User)
	set(&in.CredentialID, req.CredentialID)
	set(&in.BecomeMethod, req.BecomeMethod)
	set(&in.BecomeCredentialID, req.BecomeCredentialID)
	set(&in.ViaHostID, req.ViaHostID)
	if req.Port != nil {
		in.Port = *req.Port
	}
	if req.NeedsPTY != nil {
		in.NeedsPTY = *req.NeedsPTY
	}
	if req.JumpOnly != nil {
		in.JumpOnly = *req.JumpOnly
	}
	if req.Tags != nil {
		in.Tags = *req.Tags
	}
	if req.GroupIDs != nil {
		in.GroupIDs = *req.GroupIDs
		if in.GroupIDs == nil {
			in.GroupIDs = []string{}
		}
	}
}

func (s *Server) handleCreateHost(w http.ResponseWriter, r *http.Request) {
	var req hostRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	setup := req.SetupCredentials
	if setup != nil {
		if !principalFrom(ctx).Can(auth.PermCredentialsManage) {
			writeProblem(w, r, errForbidden("storing credentials for the setup requires credentials.manage"))
			return
		}
		if p := setup.validate(req); p != nil {
			writeProblem(w, r, p)
			return
		}
	}
	var in inventory.HostInput
	req.apply(&in)
	if in.CredentialID == "" && in.SSHAuth != "certificate" {
		// Default to the controller's own key: the simplest path is
		// "paste this public key into authorized_keys".
		if row, err := s.d.Store.R.GetSecretByName(ctx, "controller-default"); err == nil {
			in.CredentialID = row.ID
		}
	}
	var view *inventory.HostView
	err := s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		var stored []map[string]any
		if setup != nil {
			var err error
			if stored, err = s.storeSetupCredentials(ctx, q, setup, in.Name, &in.CredentialID, &in.BecomeCredentialID); err != nil {
				return audit.Event{}, err
			}
		}
		h, err := s.d.Inventory.CreateHostTx(ctx, q, in)
		if err != nil {
			return audit.Event{}, err
		}
		view, err = s.d.Inventory.HostViewTx(ctx, q, h.ID)
		details := map[string]any{"name": h.Name, "address": h.ConnectAddress, "port": h.SshPort,
			"user": h.SshUser, "via_host_id": in.ViaHostID}
		if stored != nil {
			details["setup_credentials"] = stored // names and kinds; never values
		}
		return audit.Event{Action: "host.create", TargetType: "host", TargetID: h.ID, Details: details}, err
	})
	if err != nil {
		if isSecretInputError(err) {
			writeProblem(w, r, s.secretError(err))
			return
		}
		writeProblem(w, r, s.inventoryError(err))
		return
	}
	writeJSON(w, http.StatusCreated, s.toHostJSON(*view))
}

func (s *Server) handleUpdateHost(w http.ResponseWriter, r *http.Request) {
	var req hostRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	if req.SSHAuth != nil || req.SSHPrincipal != nil {
		writeProblem(w, r, errValidation("change how DROOL signs in with PUT /hosts/{id}/ssh-auth (it is verified first)"))
		return
	}
	if req.SetupCredentials != nil {
		writeProblem(w, r, errValidation("setup_credentials only apply when adding a host; to use other credentials, store them under Credentials and choose them here"))
		return
	}
	// Switching an established host's sign-in (who DROOL signs in as, with
	// which credential, how it becomes root) changes how DROOL gets
	// administrative access to it, and so does removing the credential of a
	// host that signs in with certificates (DROOL's CA is then its only way
	// in): high-impact steps. A host whose key isn't trusted yet is still
	// being added; renaming, notes, tags, Packs and the like never ask.
	if cur, err := s.d.Store.R.GetHost(ctx, id); err == nil {
		dropsCredential := req.CredentialID != nil && *req.CredentialID == "" && cur.SshAuth == "certificate" && cur.SshCredentialID.Valid
		_, pinned := s.d.Store.R.GetPinnedHostKey(ctx, id)
		if (dropsCredential || req.switchesSignIn(cur) && pinned == nil) && !s.requireReauth(w, r, reauthHostAccess) {
			return
		}
	}
	var view *inventory.HostView
	var emits []func()
	err := s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		cur, err := s.d.Inventory.HostViewTx(ctx, q, id)
		if err != nil {
			return audit.Event{}, err
		}
		in := inventory.InputFrom(cur)
		req.apply(&in)
		if _, err := s.d.Inventory.UpdateHostTx(ctx, q, id, in); err != nil {
			return audit.Event{}, err
		}
		// Setup-only credentials the host stopped signing in with go,
		// unless another host uses them.
		forgot, emit, err := s.forgetSetupCredentials(ctx, q, cur.Name, "no longer used to sign in to it",
			cur.SshCredentialID.String, cur.BecomeCredentialID.String)
		if err != nil {
			return audit.Event{}, err
		}
		emits = emit
		view, err = s.d.Inventory.HostViewTx(ctx, q, id)
		details := map[string]any{"name": in.Name, "address": in.Address, "port": in.Port, "user": in.User,
			"via_host_id": in.ViaHostID, "status": view.Status}
		if len(forgot) > 0 {
			details["setup_credentials_deleted"] = forgot
		}
		return audit.Event{Action: "host.update", TargetType: "host", TargetID: id, Details: details}, err
	})
	if err != nil {
		writeProblem(w, r, s.inventoryError(err))
		return
	}
	for _, e := range emits {
		e()
	}
	writeJSON(w, http.StatusOK, s.toHostJSON(*view))
}

// forgetSetupCredentials deletes the setup-only credentials among ids that no
// host uses any more (secrets.ForgetTemporaryTx), with a credential.delete
// audit record each (name and kind, never a value). It returns their names
// and the records' emit functions, to call after the commit.
func (s *Server) forgetSetupCredentials(ctx context.Context, q *db.Queries, hostName, why string, ids ...string) ([]string, []func(), error) {
	forgot, err := s.d.Secrets.ForgetTemporaryTx(ctx, q, ids...)
	if err != nil {
		return nil, nil, err
	}
	names := []string{}
	var emits []func()
	for _, c := range forgot {
		emit, err := s.d.Audit.RecordTx(ctx, q, audit.Event{Action: "credential.delete", TargetType: "credential", TargetID: c.ID,
			Details: map[string]any{"name": c.Name, "kind": c.Kind, "temporary": true,
				"reason": "entered to set up DROOL's own SSH account on " + hostName + "; " + why + ", and no other host uses it"}})
		if err != nil {
			return nil, nil, err
		}
		emits = append(emits, emit)
		names = append(names, c.Name)
	}
	return names, emits, nil
}

// handleDeleteHost removes a host from DROOL. Setup-only credentials it
// signed in with (typed while adding it, before DROOL's own account took
// over) go with it, unless another host uses them; stored credentials stay.
func (s *Server) handleDeleteHost(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	var emits []func()
	err := s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		h, err := s.d.Inventory.DeleteHostTx(ctx, q, id)
		if err != nil {
			return audit.Event{}, err
		}
		forgot, emit, err := s.forgetSetupCredentials(ctx, q, h.Name, "it was removed from DROOL", h.SshCredentialID.String, h.BecomeCredentialID.String)
		if err != nil {
			return audit.Event{}, err
		}
		emits = emit
		details := map[string]any{"name": h.Name, "address": h.ConnectAddress}
		if len(forgot) > 0 {
			details["setup_credentials_deleted"] = forgot
		}
		return audit.Event{Action: "host.delete", TargetType: "host", TargetID: id, Details: details}, nil
	})
	if err != nil {
		writeProblem(w, r, s.inventoryError(err))
		return
	}
	for _, e := range emits {
		e()
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListHostKeys(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if _, err := s.d.Inventory.GetHost(ctx, id); err != nil {
		writeProblem(w, r, s.inventoryError(err))
		return
	}
	keys, err := s.d.Store.R.ListHostKeys(ctx, id)
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, keysResponse(keys))
}

func keysResponse(keys []db.SshHostKey) listResponse[hostKeyJSON] {
	out := listResponse[hostKeyJSON]{Items: make([]hostKeyJSON, 0, len(keys))}
	for _, k := range keys {
		out.Items = append(out.Items, toHostKeyJSON(k))
	}
	return out
}

func (s *Server) handleScanHostKeys(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	keys, err := s.d.Inventory.ScanHostKeys(ctx, id)
	if err != nil {
		_ = s.d.Audit.Record(ctx, audit.Event{Action: "hostkey.scan", Result: audit.Failure, TargetType: "host",
			TargetID: id, Details: map[string]any{"error": err.Error()}})
		writeProblem(w, r, s.inventoryError(err))
		return
	}
	fps := make([]string, len(keys))
	for i, k := range keys {
		fps[i] = ssh.FingerprintSHA256(k)
	}
	var all []db.SshHostKey
	err = s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		var err error
		all, err = s.d.Inventory.RecordOfferedKeysTx(ctx, q, id, keys)
		return audit.Event{Action: "hostkey.scan", TargetType: "host", TargetID: id,
			Details: map[string]any{"fingerprints": fps}}, err
	})
	if err != nil {
		writeProblem(w, r, s.inventoryError(err))
		return
	}
	writeJSON(w, http.StatusOK, keysResponse(all))
}

type approveKeyRequest struct {
	KeyID       string `json:"key_id"`
	Fingerprint string `json:"fingerprint"`
}

func (s *Server) handleApproveHostKey(w http.ResponseWriter, r *http.Request) {
	var req approveKeyRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	if (req.KeyID == "") == (req.Fingerprint == "") {
		writeProblem(w, r, errValidation("provide exactly one of key_id or fingerprint"))
		return
	}
	// Replacing a pinned key (the host's key changed) is a trust decision
	// an attacker in the middle would want: confirmed by a recent sign-in
	// (M16). The first key of a new host isn't.
	if _, err := s.d.Store.R.GetPinnedHostKey(ctx, id); err == nil && !s.requireReauth(w, r, reauthHostKey) {
		return
	}
	var key db.SshHostKey
	err := s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		keyID := req.KeyID
		if req.Fingerprint != "" {
			k, err := q.FindOfferedHostKey(ctx, db.FindOfferedHostKeyParams{HostID: id, FingerprintSha256: req.Fingerprint})
			if err != nil {
				return audit.Event{}, &inventory.ValidationError{Msg: "no scanned key matches that fingerprint; scan the host and compare again"}
			}
			keyID = k.ID
		}
		var err error
		key, err = s.d.Inventory.ApproveHostKeyTx(ctx, q, id, keyID, principalFrom(ctx).UserID)
		return audit.Event{Action: "hostkey.approve", TargetType: "host", TargetID: id,
			Details: map[string]any{"fingerprint": key.FingerprintSha256, "type": key.KeyType,
				"matched_expected_fingerprint": req.Fingerprint != ""}}, err
	})
	if err != nil {
		writeProblem(w, r, s.inventoryError(err))
		return
	}
	writeJSON(w, http.StatusOK, toHostKeyJSON(key))
}

type probeResponse struct {
	driver.ProbeResult
	Host hostJSON `json:"host"`
}

func (s *Server) handleProbeHost(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	out, err := s.d.Inventory.Probe(ctx, id)
	if err != nil {
		writeProblem(w, r, s.inventoryError(err))
		return
	}
	result := audit.Success
	if !out.Result.OK {
		result = audit.Failure
	}
	err = s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		ev := audit.Event{Action: "host.probe", Result: result, TargetType: "host", TargetID: id,
			Details: map[string]any{"status": out.Status, "detail": out.Detail}}
		if out.PresentedKey != nil {
			ev.Details["presented_fingerprint"] = ssh.FingerprintSHA256(out.PresentedKey)
		}
		return ev, s.d.Inventory.RecordProbeTx(ctx, q, id, out)
	})
	if err != nil {
		writeProblem(w, r, s.inventoryError(err))
		return
	}
	if out.Result.OK && s.d.Poller != nil {
		s.d.Poller.RunNow(id) // collect facts and status right away
	}
	if out.Status == inventory.StatusHostKeyChanged {
		s.secLog.Warn("host key changed", "host_id", id, "detail", out.Detail, "request_id", requestIDFrom(ctx))
	}
	v, err := s.d.Inventory.GetHost(ctx, id)
	if err != nil {
		writeProblem(w, r, s.inventoryError(err))
		return
	}
	writeJSON(w, http.StatusOK, probeResponse{ProbeResult: *out.Result, Host: s.toHostJSON(*v)})
}

// ---- Groups ----

type groupJSON struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	HostIDs     []string `json:"host_ids"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

func toGroupJSON(g inventory.GroupView) groupJSON {
	return groupJSON{ID: g.ID, Name: g.Name, Description: g.Description, HostIDs: g.HostIDs,
		CreatedAt: msTime(g.CreatedAt), UpdatedAt: msTime(g.UpdatedAt)}
}

func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	gs, err := s.d.Inventory.ListGroups(r.Context())
	if err != nil {
		writeProblem(w, r, s.inventoryError(err))
		return
	}
	out := listResponse[groupJSON]{Items: make([]groupJSON, 0, len(gs))}
	for _, g := range gs {
		out.Items = append(out.Items, toGroupJSON(g))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetGroup(w http.ResponseWriter, r *http.Request) {
	g, err := s.d.Inventory.GetGroup(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, s.inventoryError(err))
		return
	}
	writeJSON(w, http.StatusOK, toGroupJSON(*g))
}

type groupRequest struct {
	Name        *string   `json:"name"`
	Description *string   `json:"description"`
	HostIDs     *[]string `json:"host_ids"`
}

func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	var req groupRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	name, desc := "", ""
	if req.Name != nil {
		name = *req.Name
	}
	if req.Description != nil {
		desc = *req.Description
	}
	var hosts []string
	if req.HostIDs != nil {
		hosts = *req.HostIDs
	}
	var g *inventory.GroupView
	err := s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		var err error
		g, err = s.d.Inventory.CreateGroupTx(ctx, q, name, desc, hosts)
		if err != nil {
			return audit.Event{}, err
		}
		return audit.Event{Action: "group.create", TargetType: "group", TargetID: g.ID,
			Details: map[string]any{"name": g.Name, "host_ids": g.HostIDs}}, nil
	})
	if err != nil {
		writeProblem(w, r, s.inventoryError(err))
		return
	}
	writeJSON(w, http.StatusCreated, toGroupJSON(*g))
}

func (s *Server) handleUpdateGroup(w http.ResponseWriter, r *http.Request) {
	var req groupRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	var g *inventory.GroupView
	err := s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		var err error
		if req.HostIDs != nil {
			if err := s.d.Inventory.SetMembersTx(ctx, q, id, *req.HostIDs); err != nil {
				return audit.Event{}, err
			}
		}
		g, err = s.d.Inventory.UpdateGroupTx(ctx, q, id, req.Name, req.Description)
		if err != nil {
			return audit.Event{}, err
		}
		return audit.Event{Action: "group.update", TargetType: "group", TargetID: id,
			Details: map[string]any{"name": g.Name, "host_ids": g.HostIDs}}, nil
	})
	if err != nil {
		writeProblem(w, r, s.inventoryError(err))
		return
	}
	writeJSON(w, http.StatusOK, toGroupJSON(*g))
}

type groupMembersRequest struct {
	HostIDs []string `json:"host_ids"`
}

// handleAddGroupMembers adds hosts to a pack and keeps its other members:
// membership is managed after the pack exists, from the pack or the host,
// and two people adding hosts at once both get theirs.
func (s *Server) handleAddGroupMembers(w http.ResponseWriter, r *http.Request) {
	var req groupMembersRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	var g *inventory.GroupView
	err := s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		added, err := s.d.Inventory.AddMembersTx(ctx, q, id, req.HostIDs)
		if err != nil {
			return audit.Event{}, err
		}
		if g, err = s.d.Inventory.GroupTx(ctx, q, id); err != nil {
			return audit.Event{}, err
		}
		return audit.Event{Action: "group.members.add", TargetType: "group", TargetID: id,
			Details: map[string]any{"name": g.Name, "added": added}}, nil
	})
	if err != nil {
		writeProblem(w, r, s.inventoryError(err))
		return
	}
	writeJSON(w, http.StatusOK, toGroupJSON(*g))
}

// handleRemoveGroupMember takes one host out of a pack; the pack stays,
// even with no hosts left.
func (s *Server) handleRemoveGroupMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, host := r.PathValue("id"), r.PathValue("host_id")
	var g *inventory.GroupView
	err := s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		removed, err := s.d.Inventory.RemoveMemberTx(ctx, q, id, host)
		if err != nil {
			return audit.Event{}, err
		}
		if g, err = s.d.Inventory.GroupTx(ctx, q, id); err != nil {
			return audit.Event{}, err
		}
		return audit.Event{Action: "group.members.remove", TargetType: "group", TargetID: id,
			Details: map[string]any{"name": g.Name, "host_id": host, "removed": removed}}, nil
	})
	if err != nil {
		writeProblem(w, r, s.inventoryError(err))
		return
	}
	writeJSON(w, http.StatusOK, toGroupJSON(*g))
}

// handleGroupDependencies lists what depends on a Pack: what blocks
// deleting it, and what deleting it drops harmlessly (packdeps).
func (s *Server) handleGroupDependencies(w http.ResponseWriter, r *http.Request) {
	rep, err := packdeps.Query(r.Context(), s.d.Store.Reader, r.PathValue("id"), time.Now())
	if errors.Is(err, sql.ErrNoRows) {
		writeProblem(w, r, errNotFound("pack"))
		return
	}
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// handleDeleteGroup deletes a Pack only when nothing that changes what
// happens to its hosts still names it: the dependency query runs in the
// deleting transaction, and a Pack still in use is refused (409
// pack_in_use, with the list) instead of cascading or leaving dangling
// references. Its hosts always stay.
func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	var rep *packdeps.Report
	var emit func()
	err := s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
		var err error
		if rep, err = packdeps.Query(ctx, tx, id, time.Now()); err != nil {
			return err
		}
		if !rep.Deletable() {
			return errPackInUse
		}
		if err := packdeps.ReleaseTx(ctx, tx, id); err != nil {
			return err
		}
		q := db.New(tx)
		if _, err := s.d.Inventory.DeleteGroupTx(ctx, q, id); err != nil {
			return err
		}
		removed := make([]string, 0, len(rep.Removed))
		for _, x := range rep.Removed {
			removed = append(removed, x.Detail)
		}
		emit, err = s.d.Audit.RecordTx(ctx, q, audit.Event{Action: "group.delete", TargetType: "group", TargetID: id,
			Details: map[string]any{"name": rep.PackName, "members": rep.Members, "removed": removed}})
		return err
	})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeProblem(w, r, errNotFound("pack"))
		return
	case errors.Is(err, errPackInUse):
		kinds := map[string]int{}
		for _, b := range rep.Blocking {
			kinds[b.Kind]++
		}
		p := problem(http.StatusConflict, "pack_in_use", fmt.Sprintf("%s is still in use (%d reference%s: %s), so it wasn't deleted. Change those first; "+
			"GET /groups/%s/dependencies lists each one and where to change it.", rep.PackName, len(rep.Blocking), map[bool]string{true: "", false: "s"}[len(rep.Blocking) == 1],
			packKinds(kinds), id))
		p.Dependencies = rep
		writeProblem(w, r, p)
		return
	case err != nil:
		writeProblem(w, r, s.inventoryError(err))
		return
	}
	emit()
	w.WriteHeader(http.StatusNoContent)
}

var errPackInUse = errors.New("pack in use")

// packKinds names the kinds of references, most first.
func packKinds(kinds map[string]int) string {
	labels := map[string]string{packdeps.KindFirewallPolicy: "firewall policy", packdeps.KindSchedule: "schedule",
		packdeps.KindNotificationRoute: "notification route", packdeps.KindMaintenance: "maintenance window",
		packdeps.KindDesiredAssignment: "Desired State baseline", packdeps.KindHealthOverride: "monitoring setting",
		packdeps.KindEnrollmentToken: "enrollment token", packdeps.KindNetworkRule: "network classification"}
	type kc struct {
		k string
		n int
	}
	var list []kc
	for k, n := range kinds {
		list = append(list, kc{k, n})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].n > list[j].n || list[i].n == list[j].n && list[i].k < list[j].k })
	parts := make([]string, 0, len(list))
	for _, x := range list {
		l := labels[x.k]
		if l == "" {
			l = x.k
		}
		if x.n > 1 {
			l += "s"
		}
		parts = append(parts, fmt.Sprintf("%d %s", x.n, l))
	}
	return strings.Join(parts, ", ")
}
