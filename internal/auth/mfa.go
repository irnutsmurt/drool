package auth

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"drool/internal/store/db"
)

// Two-factor authentication (ARCHITECTURE-ALPHA §33.3): one TOTP
// authenticator per user, confirmed before it counts, with single-use
// recovery codes. The secret is sealed with the master key and leaves the
// server only once, during enrollment.

var (
	ErrMFAAlreadyEnabled = errors.New("two-factor authentication is already on; turn it off first to set up a new authenticator")
	ErrMFANotEnabled     = errors.New("two-factor authentication is not on for this account")
	ErrMFANoEnrollment   = errors.New("start setting up two-factor authentication first")
	ErrInvalidCode       = errors.New("the code is not valid (codes change every 30 seconds and work once)")
	ErrChallengeExpired  = errors.New("the sign-in attempt expired; sign in again")
	ErrNoSealer          = errors.New("two-factor authentication needs the master key")
)

// Sealer encrypts TOTP secrets (secrets.Box).
type Sealer interface {
	Seal(id, kind string, plaintext []byte) (ciphertext, nonce []byte, keyVersion int, err error)
	Open(id, kind string, ciphertext, nonce []byte, keyVersion int) ([]byte, error)
}

const mfaKind = "mfa-totp"

// MFAIssuer is the name authenticator apps show.
const MFAIssuer = "DROOL"

// SetSealer gives the service the master key (it is loaded after the
// service is created).
func (s *Service) SetSealer(b Sealer) {
	s.mu.Lock()
	s.sealer = b
	s.mu.Unlock()
}

func (s *Service) box() Sealer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sealer
}

// MFAStatus is a user's two-factor state.
type MFAStatus struct {
	Enabled           bool
	Pending           bool // an authenticator was set up but not confirmed yet
	EnabledAt         time.Time
	RecoveryRemaining int64
}

// MFAStatus reports a user's two-factor state.
func (s *Service) MFAStatus(ctx context.Context, q *db.Queries, userID string) (MFAStatus, error) {
	if q == nil {
		q = s.st.R
	}
	var st MFAStatus
	m, err := q.GetUserMFA(ctx, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	st.Enabled, st.Pending = m.EnabledAt.Valid, !m.EnabledAt.Valid
	if m.EnabledAt.Valid {
		st.EnabledAt = time.UnixMilli(m.EnabledAt.Int64)
		st.RecoveryRemaining, err = q.CountUnusedRecoveryCodes(ctx, userID)
	}
	return st, err
}

// Enrollment is what the user needs to add the authenticator: shown once.
type Enrollment struct {
	Secret string // base32, for typing in
	URI    string // otpauth://, for the QR code
}

// BeginMFAEnrollment creates a new, unconfirmed authenticator for the user
// (replacing an unconfirmed one). It refuses while two-factor is on.
func (s *Service) BeginMFAEnrollment(ctx context.Context, userID, account string) (*Enrollment, error) {
	b := s.box()
	if b == nil {
		return nil, ErrNoSealer
	}
	st, err := s.MFAStatus(ctx, nil, userID)
	if err != nil {
		return nil, err
	}
	if st.Enabled {
		return nil, ErrMFAAlreadyEnabled
	}
	secret, err := newTOTPSecret()
	if err != nil {
		return nil, err
	}
	ct, nonce, ver, err := b.Seal(userID, mfaKind, secret)
	if err != nil {
		return nil, err
	}
	now := s.now()
	if err := s.st.W.UpsertPendingMFA(ctx, db.UpsertPendingMFAParams{UserID: userID, SecretCiphertext: ct, SecretNonce: nonce,
		SecretKeyVersion: int64(ver), CreatedAt: now, UpdatedAt: now}); err != nil {
		return nil, err
	}
	return &Enrollment{Secret: b32.EncodeToString(secret), URI: otpauthURI(secret, MFAIssuer, account)}, nil
}

func (s *Service) mfaSecret(m db.UserMfa) ([]byte, error) {
	b := s.box()
	if b == nil {
		return nil, ErrNoSealer
	}
	return b.Open(m.UserID, mfaKind, m.SecretCiphertext, m.SecretNonce, int(m.SecretKeyVersion))
}

// ConfirmMFAEnrollmentTx turns two-factor on once the user entered a code
// from the new authenticator, and returns fresh recovery codes (shown once).
// The caller revokes the user's other sessions and audits it in the same
// transaction.
func (s *Service) ConfirmMFAEnrollmentTx(ctx context.Context, q *db.Queries, userID, code string) ([]string, error) {
	m, err := q.GetUserMFA(ctx, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrMFANoEnrollment
	}
	if err != nil {
		return nil, err
	}
	if m.EnabledAt.Valid {
		return nil, ErrMFAAlreadyEnabled
	}
	secret, err := s.mfaSecret(m)
	if err != nil {
		return nil, err
	}
	step := checkTOTP(secret, code, s.opts.Now(), 0)
	if step == 0 {
		return nil, ErrInvalidCode
	}
	now := s.now()
	if n, err := q.EnableMFA(ctx, db.EnableMFAParams{EnabledAt: sql.NullInt64{Int64: now, Valid: true}, LastStep: step, UpdatedAt: now, UserID: userID}); err != nil {
		return nil, err
	} else if n == 0 {
		return nil, ErrMFAAlreadyEnabled
	}
	return s.newRecoveryCodesTx(ctx, q, userID)
}

func (s *Service) newRecoveryCodesTx(ctx context.Context, q *db.Queries, userID string) ([]string, error) {
	if err := q.DeleteRecoveryCodes(ctx, userID); err != nil {
		return nil, err
	}
	now := s.now()
	codes := make([]string, 0, recoveryCodes)
	for len(codes) < recoveryCodes {
		c, err := newRecoveryCode()
		if err != nil {
			return nil, err
		}
		if err := q.InsertRecoveryCode(ctx, db.InsertRecoveryCodeParams{UserID: userID, CodeHash: hashRecoveryCode(c), CreatedAt: now}); err != nil {
			return nil, err
		}
		codes = append(codes, c)
	}
	return codes, nil
}

// RegenerateRecoveryCodesTx replaces the user's recovery codes; the old
// ones stop working.
func (s *Service) RegenerateRecoveryCodesTx(ctx context.Context, q *db.Queries, userID string) ([]string, error) {
	st, err := s.MFAStatus(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	if !st.Enabled {
		return nil, ErrMFANotEnabled
	}
	return s.newRecoveryCodesTx(ctx, q, userID)
}

// DisableMFATx turns two-factor off for the user (self, with recent
// authentication, or an administrator's reset). It reports whether it was
// set up at all.
func (s *Service) DisableMFATx(ctx context.Context, q *db.Queries, userID string) (bool, error) {
	n, err := q.DeleteUserMFA(ctx, userID)
	if err != nil {
		return false, err
	}
	return n > 0, q.DeleteRecoveryCodes(ctx, userID)
}

// verifySecondFactor checks a TOTP code or a recovery code (one of them)
// and consumes it. method is "totp" or "recovery_code".
func (s *Service) verifySecondFactor(ctx context.Context, userID, code, recovery string) (method string, err error) {
	m, err := s.st.R.GetUserMFA(ctx, userID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !m.EnabledAt.Valid {
		return "", ErrMFANotEnabled
	}
	if err != nil {
		return "", err
	}
	now := s.now()
	if recovery != "" {
		n, err := s.st.W.UseRecoveryCode(ctx, db.UseRecoveryCodeParams{UsedAt: sql.NullInt64{Int64: now, Valid: true}, UserID: userID,
			CodeHash: hashRecoveryCode(recovery)})
		if err != nil {
			return "", err
		}
		if n == 0 {
			return "", ErrInvalidCode
		}
		return "recovery_code", nil
	}
	secret, err := s.mfaSecret(m)
	if err != nil {
		return "", err
	}
	step := checkTOTP(secret, code, s.opts.Now(), m.LastStep)
	if step == 0 {
		return "", ErrInvalidCode
	}
	// Conditional on the step still being newer: two requests with the
	// same code can't both win.
	n, err := s.st.W.UseTOTPStep(ctx, db.UseTOTPStepParams{LastStep: step, UpdatedAt: now, UserID: userID, LastStep_2: step})
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", ErrInvalidCode
	}
	return "totp", nil
}

// --- the second step of signing in ---

// challengeTTL is how long a user has to enter the code after the
// password was accepted.
const (
	challengeTTL      = 5 * time.Minute
	challengeAttempts = 5
)

type loginChallenge struct {
	userID    string
	expires   time.Time
	attempts  int
	ip        string
	userAgent string
}

type challenges struct {
	mu sync.Mutex
	m  map[string]*loginChallenge
}

func (c *challenges) put(h []byte, ch *loginChallenge) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]*loginChallenge{}
	}
	for k, v := range c.m {
		if time.Now().After(v.expires) {
			delete(c.m, k)
		}
	}
	c.m[string(h)] = ch
}

func (c *challenges) get(h []byte) *loginChallenge {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[string(h)]
}

func (c *challenges) drop(h []byte) {
	c.mu.Lock()
	delete(c.m, string(h))
	c.mu.Unlock()
}

// CompleteLogin finishes a sign-in whose password was accepted (Login
// returned a challenge) with a TOTP or recovery code.
func (s *Service) CompleteLogin(ctx context.Context, challenge, code, recovery, ip, userAgent string) (*LoginResult, error) {
	h := hashSecret(challenge)
	ch := s.challenges.get(h)
	if ch == nil || s.opts.Now().After(ch.expires) {
		s.challenges.drop(h)
		return nil, &LoginFailure{Err: ErrChallengeExpired, Reason: "challenge_expired"}
	}
	u, err := s.st.R.GetUser(ctx, ch.userID)
	if err != nil {
		return nil, err
	}
	if !s.limiter.under(map[string]int{userKey(u.Username): maxFailuresPerUser, ipKey(ip): maxFailuresPerIP}) {
		s.challenges.drop(h)
		return nil, &LoginFailure{Err: ErrRateLimited, UserID: u.ID, Reason: "rate_limited"}
	}
	method, err := s.verifySecondFactor(ctx, u.ID, code, recovery)
	if err != nil {
		if !errors.Is(err, ErrInvalidCode) {
			return nil, err
		}
		s.limiter.record(userKey(u.Username), ipKey(ip))
		s.challenges.mu.Lock()
		ch.attempts++
		spent := ch.attempts >= challengeAttempts
		s.challenges.mu.Unlock()
		if spent {
			s.challenges.drop(h)
		}
		return nil, &LoginFailure{Err: ErrInvalidCode, UserID: u.ID, Reason: "bad_code"}
	}
	s.challenges.drop(h)
	if u.Disabled != 0 {
		return nil, &LoginFailure{Err: ErrInvalidCredentials, UserID: u.ID, Reason: "disabled"}
	}
	s.limiter.reset(userKey(u.Username))
	var res *LoginResult
	err = s.st.InTx(ctx, func(q *db.Queries) error {
		var err error
		res, err = s.createSession(ctx, q, u, ip, userAgent)
		return err
	})
	if res != nil {
		res.SecondFactor = method
	}
	return res, err
}

// --- re-authentication ---

// ErrReauthNeeded means a high-impact action needs the user to confirm who
// they are again.
var ErrReauthNeeded = errors.New("confirm it's you: enter your password again")

// RecentlyAuthenticated reports whether the principal is a browser session
// that proved who it is within the re-authentication window.
func (s *Service) RecentlyAuthenticated(p *Principal) bool {
	if p == nil || p.Kind != KindSession || p.ReauthAt == 0 {
		return false
	}
	return s.opts.Now().UnixMilli()-p.ReauthAt <= s.reauthWindow().Milliseconds()
}

func (s *Service) reauthWindow() time.Duration {
	if s.opts.ReauthWindow > 0 {
		return s.opts.ReauthWindow
	}
	return 5 * time.Minute
}

// ReauthWindow is how long a confirmation lasts.
func (s *Service) ReauthWindow() time.Duration { return s.reauthWindow() }

// Reauthenticate checks the password (and the second factor when the user
// has one) and records the confirmation on the session. Failures count
// toward the sign-in rate limit.
func (s *Service) Reauthenticate(ctx context.Context, p *Principal, password, code, recovery, ip string) (method string, err error) {
	if p == nil || p.Kind != KindSession {
		return "", ErrUnauthenticated
	}
	if !s.limiter.under(map[string]int{userKey(p.Username): maxFailuresPerUser, ipKey(ip): maxFailuresPerIP}) {
		return "", &LoginFailure{Err: ErrRateLimited, UserID: p.UserID, Reason: "rate_limited"}
	}
	u, err := s.st.R.GetUser(ctx, p.UserID)
	if err != nil {
		return "", err
	}
	ok, err := VerifyPassword(u.PasswordHash, password)
	if err != nil {
		return "", err
	}
	if !ok {
		s.limiter.record(userKey(u.Username), ipKey(ip))
		return "", &LoginFailure{Err: ErrInvalidCredentials, UserID: u.ID, Reason: "bad_password"}
	}
	method = "password"
	st, err := s.MFAStatus(ctx, nil, u.ID)
	if err != nil {
		return "", err
	}
	if st.Enabled {
		if code == "" && recovery == "" {
			return "", &LoginFailure{Err: ErrInvalidCode, UserID: u.ID, Reason: "code_missing"}
		}
		m, err := s.verifySecondFactor(ctx, u.ID, code, recovery)
		if err != nil {
			if errors.Is(err, ErrInvalidCode) {
				s.limiter.record(userKey(u.Username), ipKey(ip))
				return "", &LoginFailure{Err: ErrInvalidCode, UserID: u.ID, Reason: "bad_code"}
			}
			return "", err
		}
		method = "password+" + m
	}
	s.limiter.reset(userKey(u.Username))
	now := s.now()
	if err := s.st.W.SetSessionReauth(ctx, db.SetSessionReauthParams{ReauthAt: sql.NullInt64{Int64: now, Valid: true}, TokenHash: p.SessionHash}); err != nil {
		return "", err
	}
	p.ReauthAt = now
	return method, nil
}
