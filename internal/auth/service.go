// Package auth implements local accounts, browser sessions, API tokens,
// login rate limiting and permission checks.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"fleetmgr/internal/ids"
	"fleetmgr/internal/store"
	"fleetmgr/internal/store/db"
)

var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrRateLimited        = errors.New("too many failed login attempts; try again later")
	ErrUnauthenticated    = errors.New("authentication required")
	ErrUserDisabled       = errors.New("user is disabled")
)

// PrincipalKind distinguishes how a request authenticated.
type PrincipalKind string

const (
	KindSession PrincipalKind = "session"
	KindToken   PrincipalKind = "token"
)

// Principal is the authenticated actor for a request.
type Principal struct {
	Kind     PrincipalKind
	UserID   string
	Username string
	Role     string

	// Session-only fields.
	SessionHash []byte
	CSRFToken   string

	// Token-only fields.
	TokenID string
}

// Can reports whether the principal holds permission p.
func (p *Principal) Can(perm Permission) bool {
	return p != nil && RoleHas(p.Role, perm)
}

// Options configures the service.
type Options struct {
	IdleTimeout     time.Duration
	AbsoluteTimeout time.Duration
	Now             func() time.Time // for tests; defaults to time.Now
}

// Service is the authentication service.
type Service struct {
	st      *store.Store
	opts    Options
	limiter *loginLimiter
}

func NewService(st *store.Store, opts Options) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Service{st: st, opts: opts, limiter: newLoginLimiter(opts.Now)}
}

func (s *Service) now() int64 { return s.opts.Now().UnixMilli() }

// LoginResult is returned by a successful login.
type LoginResult struct {
	Cookie    string // raw session token for the cookie; never stored
	Principal *Principal
	ExpiresAt time.Time
}

// LoginFailure carries details for audit logging of a failed attempt.
type LoginFailure struct {
	Err    error  // ErrInvalidCredentials, ErrRateLimited or ErrUserDisabled
	UserID string // set when the username exists
	Reason string // internal reason for audit (unknown_user, bad_password, disabled, rate_limited)
}

func (f *LoginFailure) Error() string { return f.Err.Error() }
func (f *LoginFailure) Unwrap() error { return f.Err }

// Login verifies credentials and creates a session.
func (s *Service) Login(ctx context.Context, username, password, ip, userAgent string) (*LoginResult, error) {
	username = strings.TrimSpace(username)
	if !s.limiter.allow(username, ip) {
		return nil, &LoginFailure{Err: ErrRateLimited, Reason: "rate_limited"}
	}

	u, err := s.st.R.GetUserByUsername(ctx, username)
	if errors.Is(err, sql.ErrNoRows) {
		VerifyPassword(dummyHash, password) // equalize timing
		s.limiter.fail(username, ip)
		return nil, &LoginFailure{Err: ErrInvalidCredentials, Reason: "unknown_user"}
	}
	if err != nil {
		return nil, err
	}
	ok, err := VerifyPassword(u.PasswordHash, password)
	if err != nil {
		return nil, err
	}
	if !ok {
		s.limiter.fail(username, ip)
		return nil, &LoginFailure{Err: ErrInvalidCredentials, UserID: u.ID, Reason: "bad_password"}
	}
	if u.Disabled != 0 {
		// Report as invalid credentials to the client; audit records the reason.
		return nil, &LoginFailure{Err: ErrInvalidCredentials, UserID: u.ID, Reason: "disabled"}
	}
	s.limiter.succeed(username)

	cookie, hash, err := newSecret()
	if err != nil {
		return nil, err
	}
	csrf, _, err := newSecret()
	if err != nil {
		return nil, err
	}
	now := s.opts.Now()
	expires := now.Add(s.opts.AbsoluteTimeout)
	err = s.st.InTx(ctx, func(q *db.Queries) error {
		if err := q.CreateSession(ctx, db.CreateSessionParams{
			TokenHash:  hash,
			UserID:     u.ID,
			CsrfToken:  csrf,
			CreatedAt:  now.UnixMilli(),
			LastSeenAt: now.UnixMilli(),
			ExpiresAt:  expires.UnixMilli(),
			SourceIp:   ip,
			UserAgent:  truncate(userAgent, 512),
		}); err != nil {
			return err
		}
		return q.TouchUserLogin(ctx, db.TouchUserLoginParams{LastLoginAt: sql.NullInt64{Int64: now.UnixMilli(), Valid: true}, ID: u.ID})
	})
	if err != nil {
		return nil, err
	}
	return &LoginResult{
		Cookie:    cookie,
		ExpiresAt: expires,
		Principal: &Principal{
			Kind: KindSession, UserID: u.ID, Username: u.Username, Role: u.Role,
			SessionHash: hash, CSRFToken: csrf,
		},
	}, nil
}

// touchInterval limits last_seen writes to one per session per minute.
const touchInterval = time.Minute

// AuthenticateSession validates a session cookie value.
func (s *Service) AuthenticateSession(ctx context.Context, cookie string) (*Principal, error) {
	if cookie == "" {
		return nil, ErrUnauthenticated
	}
	hash := hashSecret(cookie)
	row, err := s.st.R.GetSessionWithUser(ctx, hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	now := s.now()
	idleLimit := row.Session.LastSeenAt + s.opts.IdleTimeout.Milliseconds()
	if now > row.Session.ExpiresAt || now > idleLimit || row.User.Disabled != 0 {
		_ = s.st.W.DeleteSession(ctx, hash)
		return nil, ErrUnauthenticated
	}
	if now-row.Session.LastSeenAt > touchInterval.Milliseconds() {
		_ = s.st.W.TouchSession(ctx, db.TouchSessionParams{LastSeenAt: now, TokenHash: hash})
	}
	return &Principal{
		Kind: KindSession, UserID: row.User.ID, Username: row.User.Username, Role: row.User.Role,
		SessionHash: hash, CSRFToken: row.Session.CsrfToken,
	}, nil
}

// Logout deletes the session.
func (s *Service) Logout(ctx context.Context, p *Principal) error {
	if p == nil || p.Kind != KindSession {
		return nil
	}
	return s.st.W.DeleteSession(ctx, p.SessionHash)
}

// CheckCSRF compares the request header value with the session's token.
func CheckCSRF(p *Principal, header string) bool {
	if p.Kind != KindSession {
		return true // bearer tokens are not sent automatically by browsers
	}
	return header != "" && subtle.ConstantTimeCompare([]byte(header), []byte(p.CSRFToken)) == 1
}

// TokenPrefix starts every API token string.
const TokenPrefix = "fm_"

// CreateTokenResult holds the one-time plaintext token.
type CreateTokenResult struct {
	Token string
	Row   db.ApiToken
}

// CreateAPIToken issues a token for userID. expiresAt may be zero (no expiry).
func (s *Service) CreateAPIToken(ctx context.Context, userID, name string, expiresAt time.Time) (*CreateTokenResult, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	secret := hex.EncodeToString(b)
	row := db.ApiToken{
		ID:        ids.New(ids.Token),
		UserID:    userID,
		Name:      name,
		TokenHash: hashSecret(secret),
		CreatedAt: s.now(),
	}
	if !expiresAt.IsZero() {
		row.ExpiresAt = sql.NullInt64{Int64: expiresAt.UnixMilli(), Valid: true}
	}
	if err := s.st.W.CreateAPIToken(ctx, db.CreateAPITokenParams{
		ID: row.ID, UserID: row.UserID, Name: row.Name, TokenHash: row.TokenHash,
		CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt,
	}); err != nil {
		return nil, err
	}
	return &CreateTokenResult{Token: TokenPrefix + row.ID + "_" + secret, Row: row}, nil
}

// AuthenticateToken validates a bearer token of the form fm_<token id>_<hex secret>.
func (s *Service) AuthenticateToken(ctx context.Context, token string) (*Principal, error) {
	rest, ok := strings.CutPrefix(token, TokenPrefix)
	if !ok {
		return nil, ErrUnauthenticated
	}
	i := strings.LastIndexByte(rest, '_')
	if i <= 0 {
		return nil, ErrUnauthenticated
	}
	id, secret := rest[:i], rest[i+1:]
	if !ids.HasPrefix(id, ids.Token) {
		return nil, ErrUnauthenticated
	}
	row, err := s.st.R.GetAPITokenWithUser(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare(hashSecret(secret), row.ApiToken.TokenHash) != 1 {
		return nil, ErrUnauthenticated
	}
	now := s.now()
	if row.ApiToken.ExpiresAt.Valid && now > row.ApiToken.ExpiresAt.Int64 {
		return nil, ErrUnauthenticated
	}
	if row.User.Disabled != 0 {
		return nil, ErrUnauthenticated
	}
	if !row.ApiToken.LastUsedAt.Valid || now-row.ApiToken.LastUsedAt.Int64 > touchInterval.Milliseconds() {
		_ = s.st.W.TouchAPIToken(ctx, db.TouchAPITokenParams{LastUsedAt: sql.NullInt64{Int64: now, Valid: true}, ID: id})
	}
	return &Principal{
		Kind: KindToken, UserID: row.User.ID, Username: row.User.Username, Role: row.User.Role, TokenID: id,
	}, nil
}

// CleanupExpired removes expired and idle sessions.
func (s *Service) CleanupExpired(ctx context.Context) (int64, error) {
	now := s.now()
	return s.st.W.DeleteExpiredSessions(ctx, db.DeleteExpiredSessionsParams{
		ExpiresAt:  now,
		LastSeenAt: now - s.opts.IdleTimeout.Milliseconds(),
	})
}

func newSecret() (raw string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	return raw, hashSecret(raw), nil
}

func hashSecret(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Login rate limits (ARCHITECTURE-ALPHA §12.1).
const (
	limiterWindow      = 15 * time.Minute
	maxFailuresPerUser = 5
	maxFailuresPerIP   = 20
)

// loginLimiter tracks recent failures per username and per source IP.
type loginLimiter struct {
	mu    sync.Mutex
	now   func() time.Time
	fails map[string][]time.Time
}

func newLoginLimiter(now func() time.Time) *loginLimiter {
	return &loginLimiter{now: now, fails: map[string][]time.Time{}}
}

func userKey(u string) string { return "u:" + strings.ToLower(u) }
func ipKey(ip string) string  { return "ip:" + ip }

func (l *loginLimiter) recent(key string) int {
	cutoff := l.now().Add(-limiterWindow)
	ts := l.fails[key]
	i := 0
	for i < len(ts) && ts[i].Before(cutoff) {
		i++
	}
	ts = ts[i:]
	if len(ts) == 0 {
		delete(l.fails, key)
	} else {
		l.fails[key] = ts
	}
	return len(ts)
}

func (l *loginLimiter) allow(username, ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.recent(userKey(username)) < maxFailuresPerUser && l.recent(ipKey(ip)) < maxFailuresPerIP
}

func (l *loginLimiter) fail(username, ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.fails[userKey(username)] = append(l.fails[userKey(username)], now)
	l.fails[ipKey(ip)] = append(l.fails[ipKey(ip)], now)
}

func (l *loginLimiter) succeed(username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, userKey(username))
}
