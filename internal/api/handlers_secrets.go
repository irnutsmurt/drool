package api

import (
	"context"
	"errors"
	"net/http"

	"drool/internal/audit"
	"drool/internal/secrets"
	"drool/internal/store/db"
)

type secretJSON struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	Description string    `json:"description"`
	PublicKey   string    `json:"public_key,omitempty"`
	UsedBy      []hostRef `json:"used_by"`
	CreatedAt   string    `json:"created_at"`
	UpdatedAt   string    `json:"updated_at"`
}

type hostRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (s *Server) toSecretJSON(ctx context.Context, q *db.Queries, r db.Secret) secretJSON {
	out := secretJSON{ID: r.ID, Name: r.Name, Kind: r.Kind, Description: r.Description, PublicKey: r.PublicPart,
		CreatedAt: msTime(r.CreatedAt), UpdatedAt: msTime(r.UpdatedAt), UsedBy: []hostRef{}}
	if users, err := q.ListHostsUsingSecret(ctx, r.ID); err == nil {
		for _, u := range users {
			out.UsedBy = append(out.UsedBy, hostRef{ID: u.ID, Name: u.Name})
		}
	}
	return out
}

func (s *Server) secretError(err error) *Problem {
	var inUse *secrets.InUseError
	var keyErr *secrets.KeyError
	switch {
	case errors.Is(err, secrets.ErrNotFound):
		return errNotFound("credential")
	case errors.Is(err, secrets.ErrNameTaken):
		return errConflict("name_taken", err.Error())
	case errors.As(err, &inUse):
		return errConflict("in_use", err.Error())
	case errors.As(err, &keyErr), errors.Is(err, secrets.ErrInvalidName), errors.Is(err, secrets.ErrInvalidKind),
		errors.Is(err, secrets.ErrEmptyValue):
		return errValidation(err.Error())
	}
	s.log.Error("credential operation", "err", err)
	return errInternal
}

func (s *Server) handleListSecrets(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := s.d.Store.R.ListSecrets(ctx)
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	out := listResponse[secretJSON]{Items: make([]secretJSON, 0, len(rows))}
	for _, row := range rows {
		out.Items = append(out.Items, s.toSecretJSON(ctx, s.d.Store.R, row))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetSecret(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	row, err := s.d.Store.R.GetSecret(ctx, r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, errNotFound("credential"))
		return
	}
	writeJSON(w, http.StatusOK, s.toSecretJSON(ctx, s.d.Store.R, row))
}

type createSecretRequest struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	Value       string `json:"value"`
	Passphrase  string `json:"passphrase"`
	Generate    bool   `json:"generate"`
}

func (s *Server) handleCreateSecret(w http.ResponseWriter, r *http.Request) {
	var req createSecretRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	prep, err := s.d.Secrets.Prepare(secrets.Input{
		Name: req.Name, Kind: req.Kind, Description: req.Description, Value: req.Value,
		Passphrase: req.Passphrase, Generate: req.Generate, CreatedBy: principalFrom(ctx).UserID,
	})
	if err == nil {
		err = s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
			return audit.Event{Action: "credential.create", TargetType: "credential", TargetID: prep.Row.ID,
					Details: map[string]any{"name": prep.Row.Name, "kind": prep.Row.Kind, "generated": req.Generate}},
				s.d.Secrets.InsertTx(ctx, q, prep)
		})
	}
	if err != nil {
		writeProblem(w, r, s.secretError(err))
		return
	}
	writeJSON(w, http.StatusCreated, s.toSecretJSON(ctx, s.d.Store.R, prep.Row))
}

type updateSecretRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Value       *string `json:"value"`
	Passphrase  string  `json:"passphrase"`
	Generate    bool    `json:"generate"`
}

func (s *Server) handleUpdateSecret(w http.ResponseWriter, r *http.Request) {
	var req updateSecretRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	var row db.Secret
	err := s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		var err error
		row, err = s.d.Secrets.UpdateTx(ctx, q, id, secrets.Update{
			Name: req.Name, Description: req.Description, Value: req.Value, Passphrase: req.Passphrase, Generate: req.Generate,
		})
		return audit.Event{Action: "credential.update", TargetType: "credential", TargetID: id,
			Details: map[string]any{"name": row.Name, "value_replaced": req.Value != nil || req.Generate}}, err
	})
	if err != nil {
		writeProblem(w, r, s.secretError(err))
		return
	}
	if req.Value != nil || req.Generate {
		// Hosts using this credential must reconnect with the new value.
		users, _ := s.d.Store.R.ListHostsUsingSecret(ctx, id)
		for _, u := range users {
			s.d.Pool.Evict(u.ID)
		}
	}
	writeJSON(w, http.StatusOK, s.toSecretJSON(ctx, s.d.Store.R, row))
}

func (s *Server) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	err := s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		row, err := s.d.Secrets.DeleteTx(ctx, q, id)
		return audit.Event{Action: "credential.delete", TargetType: "credential", TargetID: id,
			Details: map[string]any{"name": row.Name}}, err
	})
	if err != nil {
		writeProblem(w, r, s.secretError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type controllerKeyResponse struct {
	CredentialID string `json:"credential_id"`
	PublicKey    string `json:"public_key"`
	InstallCmd   string `json:"install_command"`
}

func (s *Server) handleControllerKey(w http.ResponseWriter, r *http.Request) {
	row, err := s.d.Store.R.GetSecretByName(r.Context(), secrets.ControllerKeyName)
	if err != nil {
		writeProblem(w, r, errNotFound("controller key"))
		return
	}
	writeJSON(w, http.StatusOK, controllerKeyResponse{
		CredentialID: row.ID, PublicKey: row.PublicPart,
		InstallCmd: "mkdir -p ~/.ssh && chmod 700 ~/.ssh && echo '" + row.PublicPart +
			"' >> ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys",
	})
}
