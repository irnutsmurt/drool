// Package sshaccount sets up DROOL's own SSH account on a host (After M17,
// ARCHITECTURE-ALPHA §36). An account the administrator already has is
// used only for the setup: DROOL creates a dedicated account that signs in
// with DROOL's key and may use sudo without a password, proves it on a
// brand-new connection, and only then switches the host to it. Credentials
// entered just for the setup are forgotten afterwards, unless another host
// still uses them. The setup account itself is never changed or deleted.
package sshaccount

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"drool/internal/audit"
	"drool/internal/driver"
	"drool/internal/hostexec"
	"drool/internal/hostops"
	"drool/internal/inventory"
	"drool/internal/model"
	"drool/internal/secrets"
	"drool/internal/sshx"
	"drool/internal/store"
	"drool/internal/store/db"
)

// Hosts is what the setup needs from the inventory.
type Hosts interface {
	Resolve(ctx context.Context, hostID string, requirePinned bool) (*inventory.Conn, error)
	RunOnHost(ctx context.Context, hostID, cmd string, stdin []byte, asRoot bool, timeout time.Duration) (*sshx.RunResult, error)
	RootRunner(ctx context.Context, hostID string, fresh bool) (hostexec.Runner, hostops.Become, func(), error)
	Probe(ctx context.Context, hostID string) (*inventory.ProbeOutcome, error)
	RecordProbeTx(ctx context.Context, q *db.Queries, hostID string, out *inventory.ProbeOutcome) error
}

// Deps are the service's dependencies.
type Deps struct {
	Store   *store.Store
	Secrets *secrets.Service
	Hosts   Hosts
	Dialer  *sshx.Dialer // fresh, unpooled connections
	Pool    *sshx.Pool   // dropped for the host once it switched
	Audit   *audit.Writer
	// Controller names this installation in the host's marker file.
	Controller string
	Log        *slog.Logger
	Now        func() time.Time
}

// Service sets up DROOL's own SSH account on hosts.
type Service struct{ d Deps }

func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Service{d: d}
}

// RootEquivalent is said wherever the account is offered or set up.
const RootEquivalent = "Full management gives this DROOL account passwordless sudo and therefore root-equivalent administrative capability."

// Credential outcomes once the host has switched.
const (
	OutcomeDelete = "delete" // entered for the setup only: forgotten
	OutcomeRetain = "retain" // entered for the setup, but another host still uses it: kept
	OutcomeKeep   = "keep"   // a stored credential: stays in Credentials
)

// CredentialPlan is what happens to one of the host's current credentials.
type CredentialPlan struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Temporary bool     `json:"temporary"`
	Outcome   string   `json:"outcome"`
	UsedBy    []string `json:"used_by"` // other hosts that use it
}

// Check is what the host itself said, read-only, over the setup account.
type Check struct {
	OK bool `json:"ok"`
	// Account: "new" (DROOL will create it) or "existing" (DROOL created it
	// on an earlier attempt and continues with it).
	Account string `json:"account,omitempty"`
	Shell   string `json:"shell,omitempty"`
	// SSHDLimits: sshd's AllowUsers/AllowGroups/DenyUsers/DenyGroups, which
	// may refuse the new account.
	SSHDLimits string `json:"sshd_limits,omitempty"`
	// SudoWarning: visudo -c already reports problems on the host (not
	// DROOL's); the setup then checks that its rule adds none.
	SudoWarning string `json:"sudo_warning,omitempty"`
	Code        string `json:"code,omitempty"`
	Message     string `json:"message,omitempty"`
}

// Plan is what setting up the account on a host will do, shown to the
// administrator before anything changes.
type Plan struct {
	HostID    string `json:"host_id"`
	Host      string `json:"host"`
	User      string `json:"user"`
	SetupUser string `json:"setup_user"`
	// Key is DROOL's own SSH key, which the account will accept.
	Key         string           `json:"key"`
	Changes     []string         `json:"changes"`
	Credentials []CredentialPlan `json:"credentials"`
	Notes       []string         `json:"notes"`
	Warning     string           `json:"warning"`
	Check       *Check           `json:"check"`
	// Blockers say why it can't be set up now; empty when it can.
	Blockers []string `json:"blockers"`
}

// controllerKey is DROOL's own SSH key, which the account signs in with.
type controllerKey struct {
	row         db.Secret
	line        string // authorized_keys line
	fingerprint string
	signer      ssh.Signer
}

func (s *Service) controllerKey(ctx context.Context, withSigner bool) (*controllerKey, error) {
	q := s.d.Store.R
	row, err := q.GetSecretByName(ctx, secrets.ControllerKeyName)
	if err != nil {
		return nil, fmt.Errorf("DROOL's own SSH key (%s): %w", secrets.ControllerKeyName, err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(row.PublicPart))
	if err != nil {
		return nil, fmt.Errorf("DROOL's own SSH key: %w", err)
	}
	k := &controllerKey{row: row, line: strings.TrimSpace(row.PublicPart), fingerprint: ssh.FingerprintSHA256(pub)}
	if withSigner {
		_, value, err := s.d.Secrets.Reveal(ctx, q, row.ID)
		if err != nil {
			return nil, err
		}
		if k.signer, err = secrets.Signer(value); err != nil {
			return nil, err
		}
	}
	return k, nil
}

// blockers are the reasons the account can't be set up on h now, before
// anything is asked of the host.
func blockers(h db.Host, pinned bool, user string) []string {
	out := []string{} // never null in JSON
	if err := model.ValidSSHAccountUser(user); err != nil {
		out = append(out, err.Error())
	}
	switch {
	case h.SshConfigured == 0:
		out = append(out, h.Name+" has no SSH access configured: it's managed by its Shepherd only.")
	case h.JumpOnly != 0:
		out = append(out, h.Name+" is a jump host only: DROOL signs in there just to reach other hosts, so it doesn't need an account with sudo.")
	case h.SshAuth == "certificate":
		out = append(out, h.Name+" signs in with DROOL's SSH certificates. Switch it back to its credential first, set up the account, then switch to certificates again (they then name the new account).")
	case !pinned:
		out = append(out, "Approve "+h.Name+"'s host key first: DROOL only signs in to hosts it can recognize.")
	case h.SshUser == user:
		out = append(out, h.Name+" already signs in as "+user+".")
	}
	return out
}

// credentialPlan says what becomes of the host's current credentials once
// it signs in with DROOL's key: ones entered only for the setup are
// forgotten unless another host uses them; stored ones are kept.
func credentialPlan(ctx context.Context, q *db.Queries, h db.Host) ([]CredentialPlan, error) {
	var out []CredentialPlan
	seen := map[string]bool{}
	for _, id := range []sql.NullString{h.SshCredentialID, h.BecomeCredentialID} {
		if !id.Valid || seen[id.String] {
			continue
		}
		seen[id.String] = true
		row, err := q.GetSecret(ctx, id.String)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		} else if err != nil {
			return nil, err
		}
		if row.Name == secrets.ControllerKeyName {
			continue // DROOL's own key: the account signs in with it too
		}
		users, err := q.ListHostsUsingSecret(ctx, row.ID)
		if err != nil {
			return nil, err
		}
		c := CredentialPlan{ID: row.ID, Name: row.Name, Kind: row.Kind, Temporary: row.Temporary != 0, Outcome: OutcomeKeep, UsedBy: []string{}}
		for _, u := range users {
			if u.ID != h.ID {
				c.UsedBy = append(c.UsedBy, u.Name)
			}
		}
		if c.Temporary {
			c.Outcome = OutcomeDelete
			if len(c.UsedBy) > 0 {
				c.Outcome = OutcomeRetain
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// Blockers reports why the account can't be set up on a host now (empty
// when it can), without contacting the host.
func (s *Service) Blockers(ctx context.Context, hostID, user string) ([]string, error) {
	q := s.d.Store.R
	h, err := q.GetHost(ctx, hostID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, inventory.ErrHostNotFound
	} else if err != nil {
		return nil, err
	}
	_, perr := q.GetPinnedHostKey(ctx, hostID)
	if perr != nil && !errors.Is(perr, sql.ErrNoRows) {
		return nil, perr
	}
	return blockers(h, perr == nil, user), nil
}

// Plan says what setting up the account named user on a host would do,
// and asks the host (read-only, as root over its current access) whether
// it can: the name is free or DROOL's own, and nothing it needs is missing.
func (s *Service) Plan(ctx context.Context, hostID, user string) (*Plan, error) {
	q := s.d.Store.R
	h, err := q.GetHost(ctx, hostID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, inventory.ErrHostNotFound
	} else if err != nil {
		return nil, err
	}
	_, perr := q.GetPinnedHostKey(ctx, hostID)
	if perr != nil && !errors.Is(perr, sql.ErrNoRows) {
		return nil, perr
	}
	key, err := s.controllerKey(ctx, false)
	if err != nil {
		return nil, err
	}
	p := &Plan{HostID: h.ID, Host: h.Name, User: user, SetupUser: h.SshUser, Key: key.fingerprint,
		Warning: RootEquivalent, Blockers: blockers(h, perr == nil, user), Changes: []string{}, Notes: []string{}}
	if p.Credentials, err = credentialPlan(ctx, q, h); err != nil {
		return nil, err
	}
	if p.Credentials == nil {
		p.Credentials = []CredentialPlan{}
	}
	p.Notes = s.notes(ctx, q, h, user, key)
	if len(p.Blockers) > 0 {
		return p, nil
	}
	script, err := renderScript(scriptInput{Mode: "check", User: user, Key: key.line, Fingerprint: key.fingerprint, Controller: s.d.Controller})
	if err != nil {
		return nil, err
	}
	res, rerr := s.d.Hosts.RunOnHost(ctx, hostID, "sh -s", []byte(script), true, time.Minute)
	c := &Check{}
	p.Check = c
	switch {
	case rerr != nil:
		c.Code, c.Message = "connection", sentence("DROOL couldn't run the check on "+h.Name+" as "+h.SshUser+": "+opMessage(rerr))
	default:
		vals, code, msg := parseOutput(res.Stdout)
		switch {
		case code != "":
			c.Code, c.Message = code, msg
		case res.ExitCode != 0 || vals["ok"] != "1":
			c.Code, c.Message = "root", rootFailure(h, res)
		default:
			c.OK, c.Account, c.Shell, c.SSHDLimits = true, vals["account"], vals["shell"], strings.TrimSuffix(vals["sshd_limits"], ";")
			c.SudoWarning = strings.TrimSpace(vals["sudo_warning"])
		}
	}
	if !c.OK {
		p.Blockers = append(p.Blockers, c.Message)
		return p, nil
	}
	p.Changes = changes(h, user, key, c)
	return p, nil
}

func changes(h db.Host, user string, key *controllerKey, c *Check) []string {
	var out []string
	if c.Account == "existing" {
		out = append(out, fmt.Sprintf("Continue with the account %s, which DROOL created on an earlier attempt, and check its setup.", user))
	} else {
		out = append(out, fmt.Sprintf("Create the Linux account %s (a system account with a home directory and the shell %s). It has no password, so nobody can sign in as it with one.", user, c.Shell))
	}
	out = append(out,
		fmt.Sprintf("Let DROOL's own SSH key (%s, %s) sign in as %s: ~%s/.ssh/authorized_keys.", secrets.ControllerKeyName, key.fingerprint, user, user),
		fmt.Sprintf("Let %s run any command as root without a password: %s, checked with visudo before sudo reads it.", user, sudoersPath(user)),
		fmt.Sprintf("Record that DROOL created %s, so it never mistakes someone else's account for its own: %s (readable by root only).", user, markerPath(user)),
		fmt.Sprintf("Sign in as %s on a brand-new connection and run sudo without a password. Only if that works does %s switch to %s.", user, h.Name, user),
	)
	return out
}

// notes say what DROOL leaves alone, and how jump hosts are involved.
func (s *Service) notes(ctx context.Context, q *db.Queries, h db.Host, user string, key *controllerKey) []string {
	out := []string{fmt.Sprintf("DROOL won't delete or change the account it uses for the setup (%s).", h.SshUser)}
	if h.SshCredentialID.Valid && h.SshCredentialID.String == key.row.ID {
		out = append(out, fmt.Sprintf("DROOL's key stays in %s's authorized_keys: remove it there yourself if DROOL shouldn't be able to sign in as %s any more.", h.SshUser, h.SshUser))
	}
	if via, err := q.GetHostHop(ctx, h.ID); err == nil {
		if vh, err := q.GetHost(ctx, via); err == nil {
			out = append(out, fmt.Sprintf("DROOL reaches %s through the jump host %s, also for the new account; %s's own account and credentials don't change.", h.Name, vh.Name, vh.Name))
		}
	}
	if deps, err := q.ListHostsViaHop(ctx, h.ID); err == nil && len(deps) > 0 {
		names := make([]string, len(deps))
		for i, d := range deps {
			names[i] = d.Name
		}
		out = append(out, fmt.Sprintf("%s is the jump host for %s: DROOL checks that %s may forward their connections before switching.", h.Name, strings.Join(names, ", "), user))
	}
	return out
}

// opText is an error's message without its class prefix.
func opText(err error) string {
	oe := model.AsOpError(err)
	return strings.TrimPrefix(oe.Error(), string(oe.Class)+": ")
}

// opMessage is an error's message and hint, as sentences.
func opMessage(err error) string {
	msg := opText(err)
	if h := strings.TrimSpace(model.AsOpError(err).Hint); h != "" {
		msg = sentence(msg) + " " + strings.TrimSuffix(h, ".")
	}
	return msg
}

// sentence ends s with a full stop unless it has one.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasSuffix(s, ".") || strings.HasSuffix(s, "?") || strings.HasSuffix(s, "!") {
		return s
	}
	return s + "."
}

// rootFailure explains a setup script that didn't get to run as root.
func rootFailure(h db.Host, res *sshx.RunResult) string {
	out := string(res.Stderr) + string(res.Stdout)
	if hostops.LooksLikeSudoFailure(out) || strings.Contains(strings.ToLower(out), "sudo") {
		detail, hint := hostops.ClassifySudo(out)
		msg := fmt.Sprintf("%s couldn't become root on %s: %s", h.SshUser, h.Name, detail)
		if hint != "" {
			msg = sentence(msg) + " " + strings.TrimSuffix(hint, ".")
		}
		return msg
	}
	if s := strings.TrimSpace(out); s != "" {
		if len(s) > 300 {
			s = s[:300]
		}
		return fmt.Sprintf("the setup check failed on %s (exit %d): %s", h.Name, res.ExitCode, s)
	}
	return fmt.Sprintf("the setup check failed on %s (exit %d)", h.Name, res.ExitCode)
}

func newToken() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// snapshot is how DROOL signs in to a host when the setup starts. The
// switch happens only if it is still exactly this: an edit made meanwhile
// wins, and the setup is run again.
type snapshot struct {
	user, cred, becomeMethod, becomeCred, address, auth, via, pinned string
	port, jumpOnly, configured, pty                                  int64
}

func takeSnapshot(h db.Host, via, pinned string) snapshot {
	return snapshot{user: h.SshUser, cred: h.SshCredentialID.String, becomeMethod: h.BecomeMethod, becomeCred: h.BecomeCredentialID.String,
		address: h.ConnectAddress, auth: h.SshAuth, via: via, pinned: pinned, port: h.SshPort, jumpOnly: h.JumpOnly, configured: h.SshConfigured,
		pty: h.SshNeedsPty}
}

func currentSnapshot(ctx context.Context, q *db.Queries, hostID string) (snapshot, db.Host, error) {
	h, err := q.GetHost(ctx, hostID)
	if err != nil {
		return snapshot{}, h, err
	}
	via, err := q.GetHostHop(ctx, hostID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return snapshot{}, h, err
	}
	pinned := ""
	if k, err := q.GetPinnedHostKey(ctx, hostID); err == nil {
		pinned = k.FingerprintSha256
	} else if !errors.Is(err, sql.ErrNoRows) {
		return snapshot{}, h, err
	}
	return takeSnapshot(h, via, pinned), h, nil
}

// CredentialResult is what happened to one of the setup credentials.
type CredentialResult struct {
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	Outcome string   `json:"outcome"` // deleted, retained, kept
	UsedBy  []string `json:"used_by,omitempty"`
}

// Result is the step's result data (job step result_json).
type Result struct {
	User      string `json:"user"`
	SetupUser string `json:"setup_user"`
	// Phase is the last part the setup reached: check, account, verify,
	// switch, done.
	Phase string `json:"phase"`
	// Account: created (by this run), existing (from an earlier attempt).
	Account     string             `json:"account,omitempty"`
	Verified    bool               `json:"verified"`
	Switched    bool               `json:"switched"`
	Credentials []CredentialResult `json:"credentials"`
	// OutcomeUnknown: the connection was lost while the host was being
	// changed; DROOL doesn't know how far it got (nothing was switched).
	OutcomeUnknown bool `json:"outcome_unknown,omitempty"`
}

func (r *Result) data() map[string]any {
	creds := make([]map[string]any, len(r.Credentials))
	for i, c := range r.Credentials {
		creds[i] = map[string]any{"name": c.Name, "kind": c.Kind, "outcome": c.Outcome, "used_by": c.UsedBy}
	}
	m := map[string]any{"user": r.User, "setup_user": r.SetupUser, "phase": r.Phase, "account": r.Account,
		"verified": r.Verified, "switched": r.Switched, "credentials": creds}
	if r.OutcomeUnknown {
		m["outcome_unknown"] = true
	}
	return m
}

func note(out driver.OutputSink, format string, args ...any) {
	fmt.Fprintf(out.Stdout(), format+"\n", args...)
}

// ErrChanged: the host's sign-in settings changed while the account was
// being set up.
var ErrChanged = errors.New("the host's connection settings changed while DROOL was setting up its account")

// Execute runs the ssh_account step for one host (a controller-side job
// step, under the host's exclusive lock): check, create or continue the
// account, prove it on a fresh connection, then switch and forget the
// setup credentials. Until the switch nothing about how DROOL signs in
// changes; after a failure the host keeps working as before.
func (s *Service) Execute(ctx context.Context, hostID string, op model.Operation, out driver.OutputSink) (*driver.OpResult, error) {
	if op.SSHAccount == nil {
		return nil, &model.OpError{Class: model.ClassValidation, Message: "missing account settings"}
	}
	user := op.SSHAccount.User
	info := driver.StepInfoFrom(ctx)
	res := &Result{User: user, Phase: "check", Credentials: []CredentialResult{}}
	h, err := s.d.Store.R.GetHost(ctx, hostID)
	if err != nil {
		return nil, err
	}
	res.SetupUser = h.SshUser
	fail := func(err error) (*driver.OpResult, error) {
		s.audit(ctx, info, h, res, err)
		changed := res.Account == "created"
		return &driver.OpResult{Changed: &changed, Data: res.data()}, err
	}
	failMsg := func(cls model.ErrorClass, msg, hint string) (*driver.OpResult, error) {
		return fail(&model.OpError{Class: cls, Message: msg, Hint: hint})
	}
	conn, err := s.d.Hosts.Resolve(ctx, hostID, true)
	if err != nil {
		return fail(err)
	}
	via := ""
	if conn.Target.Via != nil {
		via = conn.Target.Via.HostID
	}
	pinned := ""
	if conn.Target.PinnedKey != nil {
		pinned = ssh.FingerprintSHA256(conn.Target.PinnedKey)
	}
	if b := blockers(conn.Host, pinned != "", user); len(b) > 0 {
		return failMsg(model.ClassConfiguration, strings.Join(b, " "), "")
	}
	snap := takeSnapshot(conn.Host, via, pinned)
	key, err := s.controllerKey(ctx, true)
	if err != nil {
		return fail(err)
	}
	unchanged := fmt.Sprintf("Nothing was switched: %s still signs in as %s, and the credentials it uses were kept.", h.Name, h.SshUser)

	// 1-2. Over the setup account, as root: check, then create the
	// account or continue with the one DROOL created before.
	note(out, "Signing in to %s as %s (the account used for the setup) to create %s", h.Name, h.SshUser, user)
	script, err := renderScript(scriptInput{Mode: "apply", User: user, Key: key.line, Fingerprint: key.fingerprint,
		Token: newToken(), Controller: s.d.Controller})
	if err != nil {
		return fail(err)
	}
	runner, become, release, err := s.d.Hosts.RootRunner(ctx, hostID, false)
	if err != nil {
		return failMsg(model.ClassOf(err), fmt.Sprintf("DROOL couldn't sign in to %s as %s for the setup: %s %s", h.Name, h.SshUser, sentence(opMessage(err)), unchanged), "")
	}
	res.Phase = "account"
	cmd, prefix := become.Wrap("sh -s")
	rctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	run, err := runner.Run(rctx, cmd, hostexec.RunOptions{Stdin: append(prefix, script...), PTY: become.PTY, MaxOutput: 1 << 20})
	cancel()
	release()
	if err != nil {
		// Signed in a moment ago: the setup may have run, partly or
		// completely.
		res.OutcomeUnknown = true
		what := fmt.Sprintf("the connection to %s was lost while DROOL was setting up %s", h.Name, user)
		if model.ClassOf(err) == model.ClassTimeout {
			what = fmt.Sprintf("the setup of %s on %s didn't finish in time", user, h.Name)
		}
		return failMsg(model.ClassOf(err), fmt.Sprintf("%s, so DROOL doesn't know how far it got. %s", what, unchanged),
			"Run the setup again: it checks what is already on the host and continues.")
	}
	vals, code, msg := parseOutput(run.Stdout)
	if vals["created"] == "1" {
		res.Account = "created"
	} else if vals["account"] == "existing" {
		res.Account = "existing"
	}
	if code != "" || run.ExitCode != 0 || vals["done"] != "1" {
		if code == "" {
			msg = rootFailure(h, run)
		}
		cls := model.ClassExecution
		switch code {
		case "exists", "foreign", "replaced", "group", "prereq", "nosudoersd":
			cls = model.ClassConfiguration
		case "":
			cls = model.ClassAuthorization
		}
		if code == "" || code == "notroot" || vals["account"] == "" {
			res.Phase = "check"
		}
		left := ""
		if res.Account != "" {
			left = fmt.Sprintf(" The account %s is on the host now; running the setup again continues with it.", user)
		}
		return failMsg(cls, sentence(msg)+" "+unchanged+left, "")
	}
	if w := strings.TrimSpace(vals["sudo_warning"]); w != "" {
		note(out, "warning: %s", w)
	}
	if res.Account == "created" {
		note(out, "Created the account %s (home %s, shell %s) without a password", user, vals["home"], vals["shell"])
	} else {
		note(out, "The account %s exists from an earlier setup by DROOL; checked it", user)
	}
	if vals["key_added"] == "1" {
		note(out, "Authorized DROOL's key (%s) for %s", key.fingerprint, user)
	}
	if vals["sudoers_changed"] == "1" {
		note(out, "Installed %s (passwordless sudo, checked with visudo)", sudoersPath(user))
	}

	// 3. Prove it: a brand-new connection, every hop dialled again and the
	// pinned host key checked, as the new account with DROOL's key; then
	// sudo without a password over that same connection.
	res.Phase = "verify"
	path := ""
	if conn.Target.Via != nil {
		path = " through " + conn.Target.Via.Name
	}
	note(out, "Signing in as %s on a brand-new connection%s", user, path)
	if err := s.verify(ctx, conn, user, key.signer, out); err != nil {
		return fail(&model.OpError{Class: model.ClassOf(err), Err: err,
			Message: fmt.Sprintf("DROOL couldn't use %s on a fresh connection: %s %s The account %s stays on the host; running the setup again continues with it.", user, sentence(opText(err)), unchanged, user),
			Hint:    verifyHint(err)})
	}
	res.Verified = true
	note(out, "Signed in as %s and ran sudo without a password on a fresh connection", user)

	// 4. Switch, in one transaction that also forgets the setup
	// credentials nothing else uses.
	res.Phase = "switch"
	creds, err := s.switchTx(ctx, info, hostID, snap, user, key, res)
	if err != nil {
		if errors.Is(err, ErrChanged) {
			return failMsg(model.ClassConfiguration, fmt.Sprintf("%s's connection settings were changed while DROOL was setting up %s, so it didn't switch: your change wins. The account %s is ready on the host; run the setup again to switch to it.", h.Name, user, user), "")
		}
		return fail(err)
	}
	res.Switched, res.Credentials, res.Phase = true, creds, "done"
	if s.d.Pool != nil {
		s.d.Pool.Evict(hostID) // pooled connections signed in as the setup account
	}
	note(out, "Switched: %s now signs in as %s with DROOL's key", h.Name, user)
	for _, c := range creds {
		switch c.Outcome {
		case "deleted":
			note(out, "Forgot the setup credential %s", c.Name)
		case "retained":
			note(out, "Kept the setup credential %s: %s still use%s it", c.Name, strings.Join(c.UsedBy, ", "), map[bool]string{true: "s", false: ""}[len(c.UsedBy) == 1])
		default:
			note(out, "Kept the stored credential %s (delete it under Credentials when nothing needs it)", c.Name)
		}
	}
	// 5. Carry on as the new account: a connection test now; inventory
	// follows when the job releases the host.
	if p, err := s.d.Hosts.Probe(ctx, hostID); err == nil {
		_ = s.d.Store.InTx(ctx, func(q *db.Queries) error { return s.d.Hosts.RecordProbeTx(ctx, q, hostID, p) })
		if p.Result != nil && !p.Result.OK {
			note(out, "warning: the connection test as %s reports: %s", user, p.Detail)
		}
	}
	changed := true
	return &driver.OpResult{Changed: &changed, Data: res.data()}, nil
}

// verify proves the account on a brand-new connection: never the pool,
// never the setup connection. Each hop is dialled again and checked
// against its pinned host key; the target signs in as user with DROOL's
// key alone, then sudo must work without a password. A host that is a
// jump host for others must also forward connections for user.
func (s *Service) verify(ctx context.Context, conn *inventory.Conn, user string, signer ssh.Signer, out driver.OutputSink) error {
	t := *conn.Target
	t.User, t.Signer, t.Password = user, signer, ""
	t.Certificate, t.CertIdentity, t.CertHint = nil, "", ""
	vctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cl, err := s.d.Dialer.Dial(vctx, &t)
	// A connection failure (not a refusal) is retried briefly.
	for wait := 500 * time.Millisecond; err != nil && model.ClassOf(err) == model.ClassConnection && wait <= 2*time.Second; wait *= 2 {
		select {
		case <-vctx.Done():
		case <-time.After(wait):
			cl, err = s.d.Dialer.Dial(vctx, &t)
		}
	}
	if err != nil {
		return err
	}
	defer cl.Close()
	r, err := cl.Run(vctx, "id -un", sshx.RunOptions{MaxOutput: 4096})
	if err != nil {
		return err
	}
	if got := strings.TrimSpace(string(r.Stdout)); r.ExitCode != 0 || got != user {
		return &model.OpError{Class: model.ClassAuthentication, Message: fmt.Sprintf("signed in, but as %q instead of %q", got, user)}
	}
	b := hostops.Become{Method: "sudo", PTY: conn.Become.PTY}
	cmd, stdin := b.Wrap("id -u")
	r, err = cl.Run(vctx, cmd, sshx.RunOptions{Stdin: stdin, PTY: b.PTY, MaxOutput: 4096})
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(r.Stdout)) != "0" || r.ExitCode != 0 {
		detail, _ := hostops.ClassifySudo(string(r.Stderr) + string(r.Stdout))
		return &model.OpError{Class: model.ClassAuthorization, Message: "sudo without a password didn't work: " + detail}
	}
	deps, err := s.d.Store.R.ListHostsViaHop(ctx, conn.Host.ID)
	if err != nil {
		return err
	}
	for _, d := range deps {
		dh, err := s.d.Store.R.GetHost(ctx, d.ID)
		if err != nil {
			return err
		}
		addr := net.JoinHostPort(dh.ConnectAddress, strconv.Itoa(int(dh.SshPort)))
		if err := forwards(vctx, cl, addr); err != nil {
			return &model.OpError{Class: model.ClassConnection, Err: err,
				Message: fmt.Sprintf("%s can't forward DROOL's connection to %s (%s), which uses this host as its jump host: %v", user, dh.Name, addr, err),
				Hint:    "sshd may allow forwarding only to some users (AllowTcpForwarding in a Match block)."}
		}
		note(out, "%s forwards connections to %s", user, dh.Name)
	}
	return nil
}

// forwards opens a forwarded connection to addr through cl and reads the
// SSH identification the far end sends first.
func forwards(ctx context.Context, cl *sshx.Client, addr string) error {
	type result struct {
		c   net.Conn
		err error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := cl.Dial("tcp", addr)
		ch <- result{c, err}
	}()
	var c net.Conn
	select {
	case r := <-ch:
		if r.err != nil {
			return r.err
		}
		c = r.c
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(15 * time.Second):
		return errors.New("timed out")
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(15 * time.Second))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil {
		return fmt.Errorf("no SSH server answered: %w", err)
	}
	if string(buf) != "SSH-" {
		return errors.New("something other than an SSH server answered")
	}
	return nil
}

func verifyHint(err error) string {
	switch model.ClassOf(err) {
	case model.ClassAuthentication:
		return "Common causes: sshd allows only some users or groups (AllowUsers, AllowGroups, DenyUsers, DenyGroups), or PAM refuses the account."
	case model.ClassAuthorization:
		return "Check that /etc/sudoers.d is read on this host and that no other rule takes the account's sudo away."
	}
	return model.AsOpError(err).Hint
}

// switchTx switches the host to the account, but only if how DROOL signs
// in is still what the setup started from, then forgets the setup
// credentials nothing else uses, all in one transaction with its audit
// records.
func (s *Service) switchTx(ctx context.Context, info driver.StepInfo, hostID string, snap snapshot, user string, key *controllerKey, res *Result) ([]CredentialResult, error) {
	var creds []CredentialResult
	var emits []func()
	err := s.d.Store.InTx(ctx, func(q *db.Queries) error {
		emits = nil
		cur, h, err := currentSnapshot(ctx, q, hostID)
		if err != nil {
			return err
		}
		if cur != snap {
			return ErrChanged
		}
		plan, err := credentialPlan(ctx, q, h)
		if err != nil {
			return err
		}
		now := s.d.Now().UnixMilli()
		if err := q.SetHostSSHAccount(ctx, db.SetHostSSHAccountParams{SshUser: user, SshCredentialID: sql.NullString{String: key.row.ID, Valid: true},
			Status: inventory.StatusUntested, StatusDetail: "signs in as " + user + " now; checking the connection",
			SshAccountAt: sql.NullInt64{Int64: now, Valid: true}, UpdatedAt: now, ID: hostID}); err != nil {
			return err
		}
		actor := s.actor(ctx, q, info)
		creds = make([]CredentialResult, 0, len(plan))
		for _, c := range plan {
			r := CredentialResult{Name: c.Name, Kind: c.Kind, Outcome: "kept"}
			if c.Temporary {
				// The host no longer uses it; delete it only if nothing
				// else does either (DeleteTx checks every host, jump hosts
				// included, inside this transaction).
				_, err := s.d.Secrets.DeleteTx(ctx, q, c.ID)
				var inUse *secrets.InUseError
				switch {
				case err == nil:
					r.Outcome = "deleted"
					emit, err := s.d.Audit.RecordTx(ctx, q, audit.Event{Action: "credential.delete", Actor: &actor, TargetType: "credential", TargetID: c.ID,
						Details: map[string]any{"name": c.Name, "kind": c.Kind, "temporary": true, "reason": "entered to set up DROOL's own SSH account on " + h.Name + "; no longer used", "job_id": info.JobID}})
					if err != nil {
						return err
					}
					emits = append(emits, emit)
				case errors.As(err, &inUse):
					r.Outcome, r.UsedBy = "retained", inUse.Hosts
				default:
					return err
				}
			}
			creds = append(creds, r)
		}
		res.Switched, res.Credentials = true, creds
		emit, err := s.d.Audit.RecordTx(ctx, q, s.event(info, actor, h, res, nil))
		if err != nil {
			return err
		}
		emits = append(emits, emit)
		return nil
	})
	if err != nil {
		res.Switched, res.Credentials = false, []CredentialResult{}
		return nil, err
	}
	for _, e := range emits {
		e()
	}
	return creds, nil
}

// actor is the user who asked for the setup.
func (s *Service) actor(ctx context.Context, q *db.Queries, info driver.StepInfo) audit.Actor {
	if info.UserID == "" {
		return audit.System
	}
	a := audit.Actor{Type: audit.ActorUser, ID: info.UserID}
	if u, err := q.GetUser(ctx, info.UserID); err == nil {
		a.Name = u.Username
	}
	return a
}

// event is the audit record of a setup. Never any secret: names, kinds
// and outcomes only.
func (s *Service) event(info driver.StepInfo, actor audit.Actor, h db.Host, res *Result, err error) audit.Event {
	creds := make([]map[string]any, len(res.Credentials))
	for i, c := range res.Credentials {
		creds[i] = map[string]any{"name": c.Name, "kind": c.Kind, "outcome": c.Outcome, "used_by": c.UsedBy}
	}
	ev := audit.Event{Action: "host.ssh_account", Actor: &actor, TargetType: "host", TargetID: h.ID,
		Details: map[string]any{"name": h.Name, "user": res.User, "setup_user": res.SetupUser, "requested": "set up DROOL's own SSH account and switch to it",
			"account": res.Account, "phase": res.Phase, "verified": res.Verified, "switched": res.Switched, "credentials": creds, "job_id": info.JobID}}
	if err != nil {
		ev.Result = audit.Failure
		ev.Details["error"] = opMessage(err)
		if res.OutcomeUnknown {
			ev.Details["outcome_unknown"] = true
		}
	}
	return ev
}

func (s *Service) audit(ctx context.Context, info driver.StepInfo, h db.Host, res *Result, err error) {
	ctx = context.WithoutCancel(ctx) // a step that timed out is audited all the same
	actor := s.actor(ctx, s.d.Store.R, info)
	if aerr := s.d.Audit.Record(ctx, s.event(info, actor, h, res, err)); aerr != nil {
		s.d.Log.Error("audit DROOL's SSH account setup", "host_id", h.ID, "err", aerr)
	}
}
