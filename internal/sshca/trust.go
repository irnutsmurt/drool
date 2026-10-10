package sshca

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"text/template"
	"time"

	"golang.org/x/crypto/ssh"

	"drool/internal/driver"
	"drool/internal/inventory"
	"drool/internal/model"
	"drool/internal/sshx"
	"drool/internal/store/db"
)

// Hosts is what the CA needs from the inventory.
type Hosts interface {
	Resolve(ctx context.Context, hostID string, requirePinned bool) (*inventory.Conn, error)
	RunOnHost(ctx context.Context, hostID, cmd string, stdin []byte, asRoot bool, timeout time.Duration) (*sshx.RunResult, error)
}

// Connector opens fresh, unpooled SSH connections (*sshx.Dialer) and
// drops pooled ones (*sshx.Pool).
type Connector struct {
	Dialer *sshx.Dialer
	Pool   *sshx.Pool
}

// SetHosts connects the service to the inventory (they depend on each
// other: the inventory asks the CA for certificates).
func (s *Service) SetHosts(h Hosts, c Connector) {
	s.mu.Lock()
	s.hosts, s.conn = h, c
	s.mu.Unlock()
}

func (s *Service) deps() (Hosts, Connector) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hosts, s.conn
}

// ForHost prepares certificate authentication on t for host h
// (inventory.CertIssuer): the certificate is issued during the handshake
// of each new connection.
func (s *Service) ForHost(ctx context.Context, h db.Host, t *sshx.Target) error {
	ca, err := s.Active(ctx)
	if err != nil {
		return err
	}
	return s.prepare(t, h, ca.ID, ca.Fingerprint)
}

func (s *Service) prepare(t *sshx.Target, h db.Host, caID, fingerprint string) error {
	t.Signer, t.Password = nil, ""
	t.Certificate = func(ctx context.Context) (ssh.Signer, error) {
		signer, _, err := s.Issue(ctx, h, caID)
		return signer, err
	}
	t.CertIdentity = fingerprint + "|" + principal(h)
	t.CertHint = fmt.Sprintf("DROOL's certificate names the principal %q and is valid from %s before the controller's time until %s after it "+
		"(a host whose clock is further off rejects it); the CA is %s. Run \"Trust DROOL's SSH certificate authority\" on the host to set this up and check it.",
		principal(h), s.d.Settings.ClockSkew, s.d.Settings.TTL, fingerprint)
	return nil
}

// --- the trust step on the host ---

const (
	keysFile = "/etc/ssh/drool_user_ca.pub"
	confFile = "/etc/ssh/sshd_config.d/40-drool-user-ca.conf"
)

// trustScript installs or removes DROOL's trust. It refuses to touch a
// host that already trusts another CA file (that belongs to someone else),
// checks the result with `sshd -t` and the effective setting with `sshd
// -T`, puts the previous files back when anything is off, and reloads sshd
// only when a file changed. Existing sessions are not affected by a reload.
var trustScript = template.Must(template.New("trust").Parse(`set -u
KEYS={{.Keys}}
CONF={{.Conf}}
err() { printf '@@ERR %s %s\n' "$1" "$2"; exit 1; }
kv() { printf '@@%s %s\n' "$1" "$2"; }
SSHD=$(command -v sshd 2>/dev/null || echo /usr/sbin/sshd)
[ -x "$SSHD" ] || err nosshd "sshd was not found on this host"
effective() { "$SSHD" -T 2>/dev/null | awk -v k="$1" 'tolower($1)==k {print $2; exit}'; }
reload_sshd() {
	if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
		for u in ssh sshd; do
			if systemctl is-active --quiet "$u" 2>/dev/null; then systemctl reload "$u" && return 0; fi
		done
	fi
	pid=$(cat /run/sshd.pid 2>/dev/null || cat /var/run/sshd.pid 2>/dev/null || true)
	[ -n "$pid" ] || pid=$(pgrep -o -x sshd 2>/dev/null || true)
	[ -n "$pid" ] && kill -HUP "$pid" 2>/dev/null && return 0
	return 1
}
restore() {
	if [ -f "$KEYS.drool-prev" ]; then mv -f "$KEYS.drool-prev" "$KEYS"; elif [ "$had_keys" = 0 ]; then rm -f "$KEYS"; fi
	if [ -f "$CONF.drool-prev" ]; then mv -f "$CONF.drool-prev" "$CONF"; elif [ "$had_conf" = 0 ]; then rm -f "$CONF"; fi
}
had_keys=0; [ -f "$KEYS" ] && had_keys=1
had_conf=0; [ -f "$CONF" ] && had_conf=1
"$SSHD" -T >/dev/null 2>&1 || err sshdtest "sshd -T failed on this host: $("$SSHD" -T 2>&1 | head -n 3 | tr '\n' ' ')"
{{if eq .Action "remove"}}
[ "$had_conf" = 1 ] || [ "$had_keys" = 1 ] || { kv changed 0; exit 0; }
rm -f "$CONF" "$KEYS"
if ! out=$("$SSHD" -t 2>&1); then err invalid "sshd rejected the configuration after removing DROOL's files: $out"; fi
reload_sshd || err reload "DROOL's files were removed, but sshd could not be reloaded; it applies after its next restart"
kv changed 1
{{else}}
grep -Eqi '^[[:space:]]*Include[[:space:]]+/etc/ssh/sshd_config\.d/\*\.conf' /etc/ssh/sshd_config ||
	err noinclude "/etc/ssh/sshd_config doesn't include /etc/ssh/sshd_config.d/*.conf, so DROOL can't add its setting there. Add TrustedUserCAKeys $KEYS to sshd_config yourself (with the keys below in that file), then check it from DROOL"
cur=$(effective trustedusercakeys)
case "$cur" in
	""|none|"$KEYS") ;;
	*) err foreign "this host already trusts user certificates listed in $cur, which DROOL doesn't manage; it won't change that. Add DROOL's CA key to $cur yourself, then check it from DROOL" ;;
esac
pf=$(effective authorizedprincipalsfile)
[ -n "$pf" ] && [ "$pf" != none ] && kv warning "AuthorizedPrincipalsFile is set ($pf): certificates are accepted only for principals it lists for the user"
new=$(mktemp /etc/ssh/.drool-ca.XXXXXX) || err state "cannot write in /etc/ssh"
cat >"$new" <<'DROOL_CA_KEYS'
{{.KeyLines}}DROOL_CA_KEYS
chmod 644 "$new"
[ "$had_keys" = 1 ] && cp -p "$KEYS" "$KEYS.drool-prev"
[ "$had_conf" = 1 ] && cp -p "$CONF" "$CONF.drool-prev"
changed=0
if cmp -s "$new" "$KEYS" 2>/dev/null; then rm -f "$new"; else mv -f "$new" "$KEYS"; changed=1; fi
want="# Managed by DROOL: accept user certificates from its SSH certificate authority.
TrustedUserCAKeys $KEYS"
if [ "$(cat "$CONF" 2>/dev/null)" != "$want" ]; then
	mkdir -p /etc/ssh/sshd_config.d
	printf '%s\n' "$want" >"$CONF.tmp" && chmod 644 "$CONF.tmp" && mv -f "$CONF.tmp" "$CONF" || { restore; err state "cannot write $CONF"; }
	changed=1
fi
if ! out=$("$SSHD" -t 2>&1); then restore; err invalid "sshd rejected the configuration (the previous one was put back): $out"; fi
now=$(effective trustedusercakeys)
if [ "$now" != "$KEYS" ]; then restore; err precedence "another setting takes precedence (TrustedUserCAKeys is $now); the previous configuration was put back"; fi
if [ "$changed" = 1 ] && ! reload_sshd; then restore; err reload "sshd could not be reloaded; the previous configuration was put back"; fi
rm -f "$KEYS.drool-prev" "$CONF.drool-prev"
kv changed "$changed"
{{end}}
`))

// TrustScript renders the host script for action with the keys to trust.
func TrustScript(action string, keys []string) (string, error) {
	var lines strings.Builder
	for _, k := range keys {
		if strings.ContainsAny(k, "\r\n'") || strings.Contains(k, "DROOL_CA_KEYS") {
			return "", fmt.Errorf("invalid CA key line")
		}
		lines.WriteString(k + "\n")
	}
	var b bytes.Buffer
	err := trustScript.Execute(&b, map[string]string{"Keys": keysFile, "Conf": confFile, "Action": action, "KeyLines": lines.String()})
	return b.String(), err
}

func parseOutput(out []byte) (vals map[string]string, code, msg string) {
	vals = map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		rest, ok := strings.CutPrefix(line, "@@")
		if !ok {
			continue
		}
		k, v, _ := strings.Cut(rest, " ")
		if k == "ERR" {
			code, msg, _ = strings.Cut(v, " ")
			continue
		}
		vals[k] = v
	}
	return vals, code, msg
}

// Execute runs the ssh_ca_trust step for one host (a controller-side job
// step: it needs the CA). It installs or removes trust over the host's
// current SSH access as root, then proves each authority that issues or
// is about to with a fresh connection that offers only a certificate.
func (s *Service) Execute(ctx context.Context, hostID string, op model.Operation, out driver.OutputSink) (*driver.OpResult, error) {
	hosts, _ := s.deps()
	if hosts == nil || op.SSHCATrust == nil {
		return nil, &model.OpError{Class: model.ClassConfiguration, Message: "SSH certificate trust is not available on this controller"}
	}
	conn, err := hosts.Resolve(ctx, hostID, true)
	if err != nil {
		return nil, err
	}
	h := conn.Host
	if h.SshConfigured == 0 {
		return nil, &model.OpError{Class: model.ClassConfiguration, Message: h.Name + " has no SSH access configured (it is managed by its Shepherd only)"}
	}
	action := op.SSHCATrust.Action
	if action == "remove" && h.SshAuth == "certificate" {
		return nil, &model.OpError{Class: model.ClassValidation, Message: h.Name + " signs in with DROOL's certificates; removing the trust would lock DROOL out",
			Hint: "Switch the host back to its credential first (host page → SSH access)."}
	}
	cas, err := s.TrustedKeys(ctx)
	if err != nil {
		return nil, err
	}
	if action == "install" && len(cas) == 0 {
		return nil, &model.OpError{Class: model.ClassConfiguration, Message: "DROOL has no SSH certificate authority yet", Hint: "Create one under Credentials."}
	}
	var keys []string
	for _, c := range cas {
		keys = append(keys, c.PublicKey)
	}
	script, err := TrustScript(action, keys)
	if err != nil {
		return nil, err
	}
	if action == "install" {
		note(out, fmt.Sprintf("Installing trust in %d authorit%s in %s (TrustedUserCAKeys, %s)", len(keys), map[bool]string{true: "y", false: "ies"}[len(keys) == 1], keysFile, confFile))
	} else {
		note(out, "Removing DROOL's trust files "+keysFile+" and "+confFile)
	}
	res, err := hosts.RunOnHost(ctx, hostID, "sh -s", []byte(script), true, 2*time.Minute)
	if err != nil {
		return nil, err
	}
	vals, code, msg := parseOutput(res.Stdout)
	if w := vals["warning"]; w != "" {
		note(out, "warning: "+w)
	}
	if code != "" || res.ExitCode != 0 {
		if code == "" {
			msg = strings.TrimSpace(string(res.Stderr))
		}
		cls := model.ClassExecution
		if code == "foreign" || code == "noinclude" || code == "precedence" {
			cls = model.ClassConfiguration
		}
		return nil, &model.OpError{Class: cls, Message: msg}
	}
	changed := vals["changed"] == "1"
	now := s.d.Now().UnixMilli()
	if action == "remove" {
		if _, err := s.d.Store.Writer.ExecContext(ctx, `DELETE FROM ssh_ca_host_trust WHERE host_id = ?`, hostID); err != nil {
			return nil, err
		}
		note(out, map[bool]string{true: "Removed; sshd reloaded", false: "Nothing to remove"}[changed])
		return &driver.OpResult{Changed: &changed}, nil
	}
	if changed {
		note(out, "Installed; sshd reloaded (existing sessions are not affected)")
	} else {
		note(out, "Already installed; nothing changed")
	}
	for _, c := range cas {
		if _, err := s.d.Store.Writer.ExecContext(ctx, `INSERT INTO ssh_ca_host_trust (host_id, ca_id, installed_at) VALUES (?, ?, ?)
			ON CONFLICT (host_id, ca_id) DO UPDATE SET installed_at = excluded.installed_at`, hostID, c.ID, now); err != nil {
			return nil, err
		}
	}
	// Prove it: a brand-new connection that can only authenticate with a
	// certificate, once per authority that issues or will.
	data := map[string]any{"changed": changed}
	var failed []string
	for _, c := range cas {
		if c.State != StateActive && c.State != StateNext {
			continue
		}
		if err := s.VerifyHost(ctx, hostID, c.ID); err != nil {
			note(out, fmt.Sprintf("Certificate from %s (%s): refused: %v", c.Name, c.State, err))
			failed = append(failed, c.Name)
			continue
		}
		note(out, fmt.Sprintf("Certificate from %s (%s): accepted on a fresh connection", c.Name, c.State))
	}
	data["verified"] = len(failed) == 0
	if len(failed) > 0 {
		return &driver.OpResult{Changed: &changed, Data: data}, &model.OpError{Class: model.ClassAuthentication,
			Message: fmt.Sprintf("the trust was installed, but %s refused DROOL's certificate from %s", h.Name, strings.Join(failed, ", ")),
			Hint:    "See the output above for the reason. Common causes: AuthorizedPrincipalsFile without this principal, PubkeyAuthentication off, or the host's clock far from the controller's."}
	}
	return &driver.OpResult{Changed: &changed, Data: data}, nil
}

// note writes a line of progress to the step's output.
func note(out driver.OutputSink, line string) { fmt.Fprintln(out.Stdout(), line) }

// VerifyHost opens a fresh connection to the host (through its jump host
// as configured) that offers only a certificate from caID, runs a harmless
// command, and records the outcome.
func (s *Service) VerifyHost(ctx context.Context, hostID, caID string) error {
	return s.VerifyHostAs(ctx, hostID, caID, nil)
}

// VerifyHostAs is VerifyHost with the certificate naming principal instead
// of the host's current one (when not nil): checking a change before it is
// made.
func (s *Service) VerifyHostAs(ctx context.Context, hostID, caID string, principalName *string) error {
	hosts, cn := s.deps()
	if hosts == nil || cn.Dialer == nil {
		return errors.New("verification is not available")
	}
	conn, err := hosts.Resolve(ctx, hostID, true)
	if err != nil {
		return err
	}
	c, err := s.get(ctx, s.d.Store.Reader, `id = ?`, caID)
	if err != nil {
		return err
	}
	t := *conn.Target
	h := conn.Host
	if principalName != nil {
		h.SshPrincipal = *principalName
	}
	if err := s.prepare(&t, h, caID, c.Fingerprint); err != nil {
		return err
	}
	vctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	verr := s.verifyOnce(vctx, cn, &t)
	// sshd re-executes itself on reload and is briefly not listening (or
	// resets connections): retry connection failures for a few seconds.
	// A refused certificate is an answer, not something to retry.
	for wait := 250 * time.Millisecond; verr != nil && model.ClassOf(verr) == model.ClassConnection && wait < 8*time.Second; wait *= 2 {
		select {
		case <-vctx.Done():
		case <-time.After(wait):
			verr = s.verifyOnce(vctx, cn, &t)
		}
	}
	now := s.d.Now().UnixMilli()
	if verr != nil {
		msg := verr.Error()
		if oe := model.AsOpError(verr); oe.Message != "" {
			msg = oe.Message
		}
		s.d.Store.Writer.ExecContext(ctx, `INSERT INTO ssh_ca_host_trust (host_id, ca_id, failed_at, error) VALUES (?, ?, ?, ?)
			ON CONFLICT (host_id, ca_id) DO UPDATE SET failed_at = excluded.failed_at, error = excluded.error`, hostID, caID, now, msg)
		return verr
	}
	_, err = s.d.Store.Writer.ExecContext(ctx, `INSERT INTO ssh_ca_host_trust (host_id, ca_id, verified_at) VALUES (?, ?, ?)
		ON CONFLICT (host_id, ca_id) DO UPDATE SET verified_at = excluded.verified_at, failed_at = NULL, error = ''`, hostID, caID, now)
	return err
}

// verifyOnce dials t fresh and runs a harmless command.
func (s *Service) verifyOnce(ctx context.Context, cn Connector, t *sshx.Target) error {
	cl, err := cn.Dialer.Dial(ctx, t)
	if err != nil {
		return err
	}
	defer cl.Close()
	res, err := cl.Run(ctx, "id -un", sshx.RunOptions{MaxOutput: 4096})
	switch {
	case err != nil:
		return err
	case res.ExitCode != 0:
		return fmt.Errorf("a command failed on the new connection (exit %d)", res.ExitCode)
	}
	return nil
}

// HostTrust is what DROOL knows about one host's trust in each authority.
type HostTrust struct {
	CAID        string     `json:"ca_id"`
	CAName      string     `json:"ca_name"`
	CAState     string     `json:"ca_state"`
	InstalledAt *time.Time `json:"installed_at"`
	VerifiedAt  *time.Time `json:"verified_at"`
	FailedAt    *time.Time `json:"failed_at"`
	Error       string     `json:"error,omitempty"`
}

// Trust lists a host's trust records.
func (s *Service) Trust(ctx context.Context, hostID string) ([]HostTrust, error) {
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT t.ca_id, c.name, c.state, t.installed_at, t.verified_at, t.failed_at, t.error
		FROM ssh_ca_host_trust t JOIN ssh_user_cas c ON c.id = t.ca_id WHERE t.host_id = ? ORDER BY c.created_at DESC`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HostTrust
	ms := func(v sql.NullInt64) *time.Time {
		if !v.Valid {
			return nil
		}
		t := time.UnixMilli(v.Int64).UTC()
		return &t
	}
	for rows.Next() {
		var t HostTrust
		var inst, ver, fail sql.NullInt64
		if err := rows.Scan(&t.CAID, &t.CAName, &t.CAState, &inst, &ver, &fail, &t.Error); err != nil {
			return nil, err
		}
		t.InstalledAt, t.VerifiedAt, t.FailedAt = ms(inst), ms(ver), ms(fail)
		out = append(out, t)
	}
	return out, rows.Err()
}

// ErrNotVerified: the host hasn't shown it accepts the active CA.
var ErrNotVerified = errors.New("the host didn't accept DROOL's certificate on a fresh connection")

// UseCertificatesTx switches a host to certificate mode, but only after a
// fresh connection that offered nothing but a certificate from the active
// CA worked just now (the caller runs VerifyHost first, outside the
// transaction; this checks it happened within maxAge).
func (s *Service) UseCertificatesTx(ctx context.Context, q db.DBTX, hostID, principalName string, maxAge time.Duration) error {
	var verified sql.NullInt64
	var failed sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT t.verified_at, t.failed_at FROM ssh_ca_host_trust t JOIN ssh_user_cas c ON c.id = t.ca_id
		WHERE t.host_id = ? AND c.state = 'active'`, hostID).Scan(&verified, &failed)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (!verified.Valid || failed.Valid && failed.Int64 > verified.Int64 ||
		s.d.Now().UnixMilli()-verified.Int64 > maxAge.Milliseconds()) {
		return ErrNotVerified
	}
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `UPDATE hosts SET ssh_auth = 'certificate', ssh_principal = ?, updated_at = ? WHERE id = ?`,
		principalName, s.d.Now().UnixMilli(), hostID)
	return err
}

// HostsUsingCertificates counts hosts in certificate mode, and those of
// them with no credential left (the CA is their only way in).
func (s *Service) HostsUsingCertificates(ctx context.Context) (total, certOnly int, err error) {
	err = s.d.Store.Reader.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE ssh_credential_id IS NULL)
		FROM hosts WHERE ssh_auth = 'certificate'`).Scan(&total, &certOnly)
	return
}

// EvictAll drops pooled SSH connections of hosts in certificate mode (an
// authority changed: new connections must use the right one).
func (s *Service) EvictAll(ctx context.Context) {
	_, cn := s.deps()
	if cn.Pool == nil {
		return
	}
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT id FROM hosts WHERE ssh_auth = 'certificate'`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			cn.Pool.Evict(id)
		}
	}
}
