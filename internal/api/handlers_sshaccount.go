package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"drool/internal/auth"
	"drool/internal/engine"
	"drool/internal/model"
	"drool/internal/secrets"
	"drool/internal/store/db"
)

// DROOL's own SSH account on a host (After M17, ARCHITECTURE-ALPHA §36):
// the plan, then a job that sets it up and switches the host to it. The
// semantics live in internal/sshaccount; the browser only asks and shows.

type sshAccountRequest struct {
	User string `json:"user"`
}

func (req sshAccountRequest) user() string {
	if u := strings.TrimSpace(req.User); u != "" {
		return u
	}
	return model.DefaultSSHAccount
}

func (s *Server) sshAccountEnabled(w http.ResponseWriter, r *http.Request) bool {
	if s.d.SSHAccount == nil {
		writeProblem(w, r, errNotFound("SSH account setup"))
		return false
	}
	return true
}

func (s *Server) handlePlanSSHAccount(w http.ResponseWriter, r *http.Request) {
	if !s.sshAccountEnabled(w, r) {
		return
	}
	var req sshAccountRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	plan, err := s.d.SSHAccount.Plan(r.Context(), r.PathValue("id"), req.user())
	if err != nil {
		writeProblem(w, r, s.inventoryError(err))
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

// handleSetUpSSHAccount submits the setup as a job of its own on the host
// (under its exclusive lock). High-impact: credentials.manage and
// hosts.write, confirmed by a recent sign-in.
func (s *Server) handleSetUpSSHAccount(w http.ResponseWriter, r *http.Request) {
	if !s.sshAccountEnabled(w, r) {
		return
	}
	var req sshAccountRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	if !principalFrom(ctx).Can(auth.PermHostsWrite) {
		writeProblem(w, r, errForbidden("changing how DROOL signs in to a host also requires hosts.write"))
		return
	}
	user := req.user()
	blockers, err := s.d.SSHAccount.Blockers(ctx, id, user)
	if err != nil {
		writeProblem(w, r, s.inventoryError(err))
		return
	}
	if len(blockers) > 0 {
		writeProblem(w, r, errConflict("ssh_account_blocked", strings.Join(blockers, " ")))
		return
	}
	h, err := s.d.Store.R.GetHost(ctx, id)
	if err != nil {
		writeProblem(w, r, s.inventoryError(err))
		return
	}
	spec := model.JobSpec{
		Name:     fmt.Sprintf("Set up DROOL's own SSH account on %s (%s)", h.Name, user),
		Steps:    []model.Operation{{Type: model.OpSSHAccount, SSHAccount: &model.SSHAccountOp{User: user}}},
		Targets:  model.TargetSpec{Hosts: []string{id}},
		Strategy: model.Strategy{Concurrency: 1},
	}
	s.submitJobWith(w, r, spec, engine.SubmitOptions{Internal: true})
}

// setupCredentials are entered while adding a host only to set up DROOL's
// own SSH account on it. They are stored as temporary credentials, which
// DROOL forgets once the host signs in with that account (unless another
// host uses them by then).
type setupCredentials struct {
	SSHPassword   string `json:"ssh_password"`
	SSHPrivateKey string `json:"ssh_private_key"`
	Passphrase    string `json:"passphrase"`
	SudoPassword  string `json:"sudo_password"`
}

func (c *setupCredentials) validate(req hostRequest) *Problem {
	switch {
	case c.SSHPassword != "" && c.SSHPrivateKey != "":
		return errValidation("give a password or a private key for the setup, not both")
	case (c.SSHPassword != "" || c.SSHPrivateKey != "") && req.CredentialID != nil && *req.CredentialID != "":
		return errValidation("give either credential_id or a password or key for the setup, not both")
	case c.SudoPassword != "" && req.BecomeCredentialID != nil && *req.BecomeCredentialID != "":
		return errValidation("give either become_credential_id or a sudo password for the setup, not both")
	case c.Passphrase != "" && c.SSHPrivateKey == "":
		return errValidation("a passphrase only applies to a private key")
	}
	return nil
}

var notNameChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// storeSetupCredentials stores what was entered as temporary credentials
// and points in at them. It returns their names and kinds for the audit
// record; never their values.
func (s *Server) storeSetupCredentials(ctx context.Context, q *db.Queries, c *setupCredentials, hostName string, cred, become *string) ([]map[string]any, error) {
	base := "setup-" + strings.Trim(notNameChars.ReplaceAllString(hostName, "-"), "-")
	if base == "setup-" {
		base = "setup-host"
	}
	if len(base) > 52 {
		base = base[:52]
	}
	var stored []map[string]any
	add := func(suffix, kind, value, passphrase string) (string, error) {
		name := base + "-" + suffix
		for n := 2; ; n++ {
			if _, err := q.GetSecretByName(ctx, name); errors.Is(err, sql.ErrNoRows) {
				break
			} else if err != nil {
				return "", err
			}
			if n > 99 {
				return "", secrets.ErrNameTaken
			}
			name = fmt.Sprintf("%s-%s-%d", base, suffix, n)
		}
		p, err := s.d.Secrets.Prepare(secrets.Input{Name: name, Kind: kind, Value: value, Passphrase: passphrase, Temporary: true,
			CreatedBy:   principalFrom(ctx).UserID,
			Description: "Entered to set up DROOL's own SSH account on " + hostName + ". DROOL forgets it once the host signs in with that account, unless another host uses it."})
		if err != nil {
			return "", err
		}
		if err := s.d.Secrets.InsertTx(ctx, q, p); err != nil {
			return "", err
		}
		stored = append(stored, map[string]any{"id": p.Row.ID, "name": name, "kind": kind})
		return p.Row.ID, nil
	}
	var err error
	switch {
	case c.SSHPassword != "":
		*cred, err = add("login", secrets.KindPassword, c.SSHPassword, "")
	case c.SSHPrivateKey != "":
		*cred, err = add("key", secrets.KindSSHKey, c.SSHPrivateKey, c.Passphrase)
	}
	if err == nil && c.SudoPassword != "" {
		*become, err = add("sudo", secrets.KindSudoPassword, c.SudoPassword, "")
	}
	return stored, err
}

// isSecretInputError reports credential problems the request can fix.
func isSecretInputError(err error) bool {
	var keyErr *secrets.KeyError
	return errors.As(err, &keyErr) || errors.Is(err, secrets.ErrInvalidName) || errors.Is(err, secrets.ErrEmptyValue) ||
		errors.Is(err, secrets.ErrInvalidKind) || errors.Is(err, secrets.ErrNameTaken)
}
