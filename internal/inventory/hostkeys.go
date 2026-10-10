package inventory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"golang.org/x/crypto/ssh"

	"drool/internal/driver"
	"drool/internal/driver/sshdriver"
	"drool/internal/ids"
	"drool/internal/model"
	"drool/internal/secrets"
	"drool/internal/sshx"
	"drool/internal/store/db"
)

var ErrHostKeyNotFound = errors.New("host key not found")

// Conn is a resolved connection target plus privilege escalation settings.
type Conn struct {
	Target *sshx.Target
	Become sshdriver.Become
	Host   db.Host
}

// Resolve builds the connection target for a host, decrypting credentials.
// With requirePinned=false the target host may lack a pinned key (scanning);
// a jump host must always be pinned.
func (s *Service) Resolve(ctx context.Context, hostID string, requirePinned bool) (*Conn, error) {
	q := s.st.R
	h, err := q.GetHost(ctx, hostID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrHostNotFound
	} else if err != nil {
		return nil, err
	}
	t, err := s.target(ctx, q, h, requirePinned)
	if err != nil {
		return nil, err
	}
	if via, err := q.GetHostHop(ctx, h.ID); err == nil {
		vh, err := q.GetHost(ctx, via)
		if err != nil {
			return nil, err
		}
		if t.Via, err = s.target(ctx, q, vh, true); err != nil {
			return nil, fmt.Errorf("jump host %s: %w", vh.Name, err)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	c := &Conn{Target: t, Host: h, Become: sshdriver.Become{Method: h.BecomeMethod, PTY: h.SshNeedsPty != 0}}
	if h.BecomeCredentialID.Valid {
		_, pw, err := s.secrets.Reveal(ctx, q, h.BecomeCredentialID.String)
		if err != nil {
			return nil, err
		}
		c.Become.Password = string(pw)
	}
	return c, nil
}

func (s *Service) target(ctx context.Context, q *db.Queries, h db.Host, requirePinned bool) (*sshx.Target, error) {
	t := &sshx.Target{HostID: h.ID, Name: h.Name, Address: h.ConnectAddress, Port: int(h.SshPort), User: h.SshUser}
	switch {
	case h.SshAuth == "certificate" && !requirePinned:
		// Scanning host keys doesn't authenticate: nothing to issue.
	case h.SshAuth == "certificate":
		// JIT SSH (M16): a short-lived certificate per connection, never the
		// credential, even if one is still assigned (no silent fallback).
		certs := s.certIssuer()
		if certs == nil {
			return nil, model.Errorf(model.ClassConfiguration, false, "host %s uses SSH certificates, but the certificate authority is not available", h.Name)
		}
		if err := certs.ForHost(ctx, h, t); err != nil {
			return nil, &model.OpError{Class: model.ClassConfiguration, Err: err,
				Message: fmt.Sprintf("host %s uses SSH certificates, but DROOL can't issue one: %v", h.Name, err),
				Hint:    "Check the SSH certificate authority under Credentials, or switch the host back to its credential."}
		}
	case !h.SshCredentialID.Valid:
		return nil, model.Errorf(model.ClassConfiguration, false, "host %s has no SSH credential", h.Name)
	default:
		row, value, err := s.secrets.Reveal(ctx, q, h.SshCredentialID.String)
		if err != nil {
			return nil, err
		}
		switch row.Kind {
		case secrets.KindSSHKey:
			if t.Signer, err = secrets.Signer(value); err != nil {
				return nil, err
			}
		default:
			t.Password = string(value)
		}
	}
	pinned, err := q.GetPinnedHostKey(ctx, h.ID)
	switch {
	case err == nil:
		if t.PinnedKey, err = ssh.ParsePublicKey(pinned.PublicKey); err != nil {
			return nil, err
		}
	case errors.Is(err, sql.ErrNoRows):
		if requirePinned {
			return nil, &model.OpError{Class: model.ClassHostKey,
				Message: fmt.Sprintf("host %s has no approved host key", h.Name),
				Hint:    "Scan and approve the host key first."}
		}
	default:
		return nil, err
	}
	return t, nil
}

// ScannedKey is a key offered by a host during a scan.
type ScannedKey struct {
	db.SshHostKey
}

// ScanHostKeys connects without authenticating and records the offered keys
// as "offered" (pinned keys that are offered again are left as is). Network
// work happens outside any transaction; record runs in the caller's.
func (s *Service) ScanHostKeys(ctx context.Context, hostID string) ([]ssh.PublicKey, error) {
	c, err := s.Resolve(ctx, hostID, false)
	if err != nil {
		return nil, err
	}
	c.Target.Signer, c.Target.Password, c.Target.PinnedKey, c.Target.Certificate = nil, "", nil, nil
	return s.dialer.Scan(ctx, c.Target)
}

// RecordOfferedKeysTx stores scanned keys and returns all keys for the host.
func (s *Service) RecordOfferedKeysTx(ctx context.Context, q *db.Queries, hostID string, keys []ssh.PublicKey) ([]db.SshHostKey, error) {
	now := s.now().UnixMilli()
	pinned, perr := q.GetPinnedHostKey(ctx, hostID)
	for _, k := range keys {
		fp := ssh.FingerprintSHA256(k)
		if perr == nil && pinned.FingerprintSha256 == fp {
			if err := q.TouchHostKey(ctx, db.TouchHostKeyParams{LastSeenAt: now, ID: pinned.ID}); err != nil {
				return nil, err
			}
			continue
		}
		if existing, err := q.FindOfferedHostKey(ctx, db.FindOfferedHostKeyParams{HostID: hostID, FingerprintSha256: fp}); err == nil {
			if err := q.TouchHostKey(ctx, db.TouchHostKeyParams{LastSeenAt: now, ID: existing.ID}); err != nil {
				return nil, err
			}
			continue
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err := q.InsertHostKey(ctx, db.InsertHostKeyParams{
			ID: ids.New(ids.HostKey), HostID: hostID, KeyType: k.Type(), PublicKey: k.Marshal(),
			FingerprintSha256: fp, FirstSeenAt: now, LastSeenAt: now,
		}); err != nil {
			return nil, err
		}
	}
	return q.ListHostKeys(ctx, hostID)
}

// ApproveHostKeyTx pins an offered key, retiring any previously pinned key
// and discarding other offered keys. The host becomes "untested".
func (s *Service) ApproveHostKeyTx(ctx context.Context, q *db.Queries, hostID, keyID, userID string) (db.SshHostKey, error) {
	k, err := q.GetHostKey(ctx, keyID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && k.HostID != hostID) {
		return db.SshHostKey{}, ErrHostKeyNotFound
	} else if err != nil {
		return db.SshHostKey{}, err
	}
	if k.Status == "pinned" {
		return k, nil
	}
	if k.Status != "offered" {
		return db.SshHostKey{}, invalid("only offered keys can be approved; scan the host again")
	}
	now := s.now().UnixMilli()
	if err := s.retirePinned(ctx, q, hostID, now); err != nil {
		return db.SshHostKey{}, err
	}
	if err := q.PinHostKey(ctx, db.PinHostKeyParams{DecidedBy: nullStr(userID), DecidedAt: nullInt(now), ID: keyID}); err != nil {
		return db.SshHostKey{}, err
	}
	if err := q.DeleteOfferedHostKeys(ctx, hostID); err != nil {
		return db.SshHostKey{}, err
	}
	h, err := q.GetHost(ctx, hostID)
	if err != nil {
		return db.SshHostKey{}, err
	}
	if err := q.UpdateHostStatus(ctx, db.UpdateHostStatusParams{
		Status: StatusUntested, StatusDetail: "host key approved; run a connection test",
		LastProbeAt: h.LastProbeAt, UpdatedAt: now, ID: hostID,
	}); err != nil {
		return db.SshHostKey{}, err
	}
	s.pool.Evict(hostID)
	k.Status = "pinned"
	return k, nil
}

func (s *Service) retirePinned(ctx context.Context, q *db.Queries, hostID string, now int64) error {
	cur, err := q.GetPinnedHostKey(ctx, hostID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	// Keep one retired row per fingerprint (unique index on host, fingerprint, status).
	if err := q.DeleteRetiredHostKeyDuplicate(ctx, db.DeleteRetiredHostKeyDuplicateParams{
		HostID: hostID, FingerprintSha256: cur.FingerprintSha256,
	}); err != nil {
		return err
	}
	return q.RetirePinnedHostKey(ctx, db.RetirePinnedHostKeyParams{DecidedAt: nullInt(now), HostID: hostID})
}

// ProbeOutcome is a probe result plus the status it implies.
type ProbeOutcome struct {
	Result       *driver.ProbeResult
	Status       string
	Detail       string
	PresentedKey ssh.PublicKey // set when the host key changed
}

// Probe runs a connection test over a fresh connection. It does not write to
// the database; record the outcome with RecordProbeTx.
func (s *Service) Probe(ctx context.Context, hostID string) (*ProbeOutcome, error) {
	c, err := s.Resolve(ctx, hostID, true)
	if err != nil {
		var oe *model.OpError
		if errors.As(err, &oe) && oe.Class == model.ClassHostKey {
			return &ProbeOutcome{Status: StatusPendingHostKey, Detail: oe.Message, Result: &driver.ProbeResult{
				Steps: []driver.ProbeStep{{Name: "host key", Status: driver.StepFailed, Detail: oe.Message,
					ErrorClass: string(oe.Class), Hint: oe.Hint}}}}, nil
		}
		return nil, err
	}
	res, dialErr := sshdriver.Probe(ctx, s.dialer, c.Target, sshdriver.ProbeOptions{Become: c.Become, JumpOnly: c.Host.JumpOnly != 0})
	out := &ProbeOutcome{Result: res, Status: StatusReady, Detail: "connection test passed"}
	if f := res.FirstFailure(); f != nil {
		out.Detail = f.Name + ": " + f.Detail
		switch model.ErrorClass(f.ErrorClass) {
		case model.ClassHostKey:
			out.Status = StatusHostKeyChanged
			var mm *sshx.HostKeyMismatchError
			// Only the target's own key change is recorded; a jump host's
			// changed key is reported on the jump host.
			if errors.As(dialErr, &mm) && mm.Host == c.Target.Name {
				out.PresentedKey = mm.Presented
			}
		case model.ClassAuthentication, model.ClassAuthorization:
			out.Status = StatusAuthFailed
		case model.ClassConfiguration:
			out.Status = StatusUnsupported
		default:
			out.Status = StatusUnreachable
		}
	}
	return out, nil
}

// RecordProbeTx stores a probe outcome: the host status and, if the host key
// changed, the newly presented key as "offered" for review.
func (s *Service) RecordProbeTx(ctx context.Context, q *db.Queries, hostID string, out *ProbeOutcome) error {
	now := s.now().UnixMilli()
	if out.PresentedKey != nil {
		if _, err := s.RecordOfferedKeysTx(ctx, q, hostID, []ssh.PublicKey{out.PresentedKey}); err != nil {
			return err
		}
	}
	return q.UpdateHostStatus(ctx, db.UpdateHostStatusParams{
		Status: out.Status, StatusDetail: truncate(out.Detail, 500), LastProbeAt: nullInt(now), UpdatedAt: now, ID: hostID,
	})
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
