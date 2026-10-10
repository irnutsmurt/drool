package api

import (
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"drool/internal/audit"
	"drool/internal/sshca"
	"drool/internal/store/db"
)

// DROOL's SSH user certificate authority and hosts' SSH sign-in method
// (JIT SSH, ARCHITECTURE-ALPHA §33.2).

func (s *Server) sshcaEnabled(w http.ResponseWriter, r *http.Request) bool {
	if s.d.SSHCA == nil {
		writeProblem(w, r, errNotFound("SSH certificate authority"))
		return false
	}
	return true
}

type sshcaResponse struct {
	Authorities []sshca.CA `json:"authorities"`
	// Hosts that sign in with certificates, and those of them with no
	// credential left (the CA is their only way in).
	CertificateHosts     int `json:"certificate_hosts"`
	CertificateOnlyHosts int `json:"certificate_only_hosts"`
	TTLSeconds           int `json:"ttl_s"`
	ClockSkewSeconds     int `json:"clock_skew_s"`
	// Unverified lists, during a rotation, the hosts in certificate mode
	// that haven't accepted the new authority yet.
	Unverified []string `json:"unverified"`
}

func (s *Server) sshcaState(r *http.Request) (*sshcaResponse, error) {
	ctx := r.Context()
	cas, err := s.d.SSHCA.List(ctx)
	if err != nil {
		return nil, err
	}
	out := &sshcaResponse{Authorities: cas, Unverified: []string{}}
	if out.Authorities == nil {
		out.Authorities = []sshca.CA{}
	}
	if out.CertificateHosts, out.CertificateOnlyHosts, err = s.d.SSHCA.HostsUsingCertificates(ctx); err != nil {
		return nil, err
	}
	st := s.d.SSHCA.Settings()
	out.TTLSeconds, out.ClockSkewSeconds = int(st.TTL/time.Second), int(st.ClockSkew/time.Second)
	for _, c := range cas {
		if c.State == sshca.StateNext {
			if out.Unverified, err = s.d.SSHCA.Unverified(ctx, s.d.Store.Reader, c.ID); err != nil {
				return nil, err
			}
			if out.Unverified == nil {
				out.Unverified = []string{}
			}
		}
	}
	return out, nil
}

func (s *Server) handleGetSSHCA(w http.ResponseWriter, r *http.Request) {
	if !s.sshcaEnabled(w, r) {
		return
	}
	out, err := s.sshcaState(r)
	if err != nil {
		s.log.Error("ssh ca", "err", err)
		writeProblem(w, r, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) sshcaError(w http.ResponseWriter, r *http.Request, err error) {
	var se *sshca.StateError
	switch {
	case errors.As(err, &se):
		writeProblem(w, r, errConflict("ssh_ca_state", se.Msg))
	case errors.Is(err, sshca.ErrHasActive), errors.Is(err, sshca.ErrHasNext), errors.Is(err, sshca.ErrNoActiveCA):
		writeProblem(w, r, errConflict("ssh_ca_state", err.Error()))
	case errors.Is(err, sshca.ErrNoCA):
		writeProblem(w, r, errNotFound("SSH certificate authority"))
	default:
		s.log.Error("ssh ca", "err", err)
		writeProblem(w, r, errInternal)
	}
}

type sshcaNameRequest struct {
	Name string `json:"name"`
}

// sshcaChange runs one lifecycle step with its audit record in a
// transaction, then drops pooled certificate connections (new ones must
// use the right authority).
func (s *Server) sshcaChange(w http.ResponseWriter, r *http.Request, action string, fn func(tx *sql.Tx) (*sshca.CA, error)) {
	ctx := r.Context()
	var ca *sshca.CA
	err := s.inRawTxAudited(ctx, func(tx *sql.Tx, _ *db.Queries) (audit.Event, error) {
		var err error
		if ca, err = fn(tx); err != nil {
			return audit.Event{}, err
		}
		return audit.Event{Action: action, TargetType: "ssh_ca", TargetID: ca.ID,
			Details: map[string]any{"name": ca.Name, "fingerprint": ca.Fingerprint, "state": ca.State}}, nil
	})
	if err != nil {
		_ = s.d.Audit.Record(ctx, audit.Event{Action: action, Result: audit.Failure, TargetType: "ssh_ca", TargetID: r.PathValue("id"),
			Details: map[string]any{"error": err.Error()}})
		s.sshcaError(w, r, err)
		return
	}
	s.d.SSHCA.EvictAll(ctx)
	out, err := s.sshcaState(r)
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateSSHCA(w http.ResponseWriter, r *http.Request) {
	if !s.sshcaEnabled(w, r) {
		return
	}
	var req sshcaNameRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	uid := principalFrom(r.Context()).UserID
	s.sshcaChange(w, r, "ssh_ca.create", func(tx *sql.Tx) (*sshca.CA, error) {
		return s.d.SSHCA.CreateTx(r.Context(), tx, req.Name, uid)
	})
}

func (s *Server) handleRotateSSHCA(w http.ResponseWriter, r *http.Request) {
	if !s.sshcaEnabled(w, r) {
		return
	}
	var req sshcaNameRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	uid := principalFrom(r.Context()).UserID
	s.sshcaChange(w, r, "ssh_ca.introduce", func(tx *sql.Tx) (*sshca.CA, error) {
		return s.d.SSHCA.IntroduceTx(r.Context(), tx, req.Name, uid)
	})
}

func (s *Server) handleActivateSSHCA(w http.ResponseWriter, r *http.Request) {
	if !s.sshcaEnabled(w, r) {
		return
	}
	s.sshcaChange(w, r, "ssh_ca.activate", func(tx *sql.Tx) (*sshca.CA, error) {
		return s.d.SSHCA.ActivateTx(r.Context(), tx, r.PathValue("id"))
	})
}

func (s *Server) handleRetireSSHCA(w http.ResponseWriter, r *http.Request) {
	if !s.sshcaEnabled(w, r) {
		return
	}
	s.sshcaChange(w, r, "ssh_ca.retire", func(tx *sql.Tx) (*sshca.CA, error) {
		return s.d.SSHCA.RetireTx(r.Context(), tx, r.PathValue("id"))
	})
}

type hostSSHAuthResponse struct {
	Mode               string            `json:"mode"`
	Principal          string            `json:"principal"`
	CredentialAssigned bool              `json:"credential_assigned"`
	Trust              []sshca.HostTrust `json:"trust"`
}

func (s *Server) hostSSHAuth(r *http.Request, id string) (*hostSSHAuthResponse, error) {
	h, err := s.d.Store.R.GetHost(r.Context(), id)
	if err != nil {
		return nil, err
	}
	out := &hostSSHAuthResponse{Mode: h.SshAuth, Principal: h.SshPrincipal, CredentialAssigned: h.SshCredentialID.Valid, Trust: []sshca.HostTrust{}}
	if s.d.SSHCA != nil {
		t, err := s.d.SSHCA.Trust(r.Context(), id)
		if err != nil {
			return nil, err
		}
		if t != nil {
			out.Trust = t
		}
	}
	return out, nil
}

func (s *Server) handleGetHostSSHAuth(w http.ResponseWriter, r *http.Request) {
	out, err := s.hostSSHAuth(r, r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, errNotFound("host"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

var principalRE = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)

type hostSSHAuthRequest struct {
	Mode      string  `json:"mode"`
	Principal *string `json:"principal"`
}

// handleSetHostSSHAuth switches how DROOL signs in to a host. To
// certificates only after a brand-new connection that offered nothing but
// a certificate from the active CA worked, right now; back to the
// credential only when one is assigned.
func (s *Server) handleSetHostSSHAuth(w http.ResponseWriter, r *http.Request) {
	var req hostSSHAuthRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	h, err := s.d.Store.R.GetHost(ctx, id)
	if err != nil {
		writeProblem(w, r, errNotFound("host"))
		return
	}
	principal := h.SshPrincipal
	if req.Principal != nil {
		principal = strings.TrimSpace(*req.Principal)
		if principal != "" && !principalRE.MatchString(principal) {
			writeProblem(w, r, errValidation("the certificate principal may contain letters, digits, '.', '_', '@' and '-' (up to 64)"))
			return
		}
	}
	switch req.Mode {
	case "certificate":
		if !s.sshcaEnabled(w, r) {
			return
		}
		active, err := s.d.SSHCA.Active(ctx)
		if err != nil {
			s.sshcaError(w, r, err)
			return
		}
		// Proven on a brand-new connection, with the principal asked for,
		// before anything changes.
		if verr := s.d.SSHCA.VerifyHostAs(ctx, id, active.ID, &principal); verr != nil {
			_ = s.d.Audit.Record(ctx, audit.Event{Action: "host.ssh_auth", Result: audit.Failure, TargetType: "host", TargetID: id,
				Details: map[string]any{"name": h.Name, "mode": "certificate", "error": verr.Error()}})
			p := problem(http.StatusConflict, "certificate_refused",
				h.Name+" didn't accept DROOL's certificate on a fresh connection, so it keeps using its credential: "+verr.Error())
			writeProblem(w, r, p)
			return
		}
		err = s.inRawTxAudited(ctx, func(tx *sql.Tx, _ *db.Queries) (audit.Event, error) {
			return audit.Event{Action: "host.ssh_auth", TargetType: "host", TargetID: id,
					Details: map[string]any{"name": h.Name, "mode": "certificate", "principal": principal, "ca": active.Fingerprint}},
				s.d.SSHCA.UseCertificatesTx(ctx, tx, id, principal, 5*time.Minute)
		})
	case "credential":
		if !h.SshCredentialID.Valid {
			writeProblem(w, r, errConflict("no_credential", h.Name+" has no credential to go back to; assign one first (host settings)"))
			return
		}
		err = s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
			if err := q.SetHostSSHAuth(ctx, db.SetHostSSHAuthParams{SshAuth: "credential", SshPrincipal: principal,
				UpdatedAt: time.Now().UnixMilli(), ID: id}); err != nil {
				return audit.Event{}, err
			}
			return audit.Event{Action: "host.ssh_auth", TargetType: "host", TargetID: id, Details: map[string]any{"name": h.Name, "mode": "credential"}},
				q.UpdateHostStatus(ctx, db.UpdateHostStatusParams{Status: "untested", StatusDetail: "sign-in changed to the credential; run a connection test",
					LastProbeAt: h.LastProbeAt, UpdatedAt: time.Now().UnixMilli(), ID: id})
		})
	default:
		writeProblem(w, r, errValidation("mode must be certificate or credential"))
		return
	}
	if err != nil {
		if errors.Is(err, sshca.ErrNotVerified) {
			writeProblem(w, r, errConflict("certificate_refused", err.Error()))
			return
		}
		s.log.Error("set ssh auth", "err", err)
		writeProblem(w, r, errInternal)
		return
	}
	s.d.Pool.Evict(id)
	out, err := s.hostSSHAuth(r, id)
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
