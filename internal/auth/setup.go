package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"fleetmgr/internal/ids"
	"fleetmgr/internal/store/db"
)

// First-admin bootstrap ("claim"), ARCHITECTURE-ALPHA §12.5.
//
// While no administrator exists, the controller accepts a one-time setup code
// at /setup. Codes are either generated (6 digits, short-lived, displayed once
// on the console) or provided by the operator for automated deployments.
// Only SHA-256 hashes are stored. Creating the first admin deletes every code
// in the same transaction, and setup stays closed once any admin exists.

var (
	ErrSetupClosed      = errors.New("setup is already complete")
	ErrSetupCodeInvalid = errors.New("invalid or expired setup code")
	ErrSetupRateLimited = errors.New("too many failed setup attempts; try again later")
	ErrSetupSecretWeak  = fmt.Errorf("provided setup secret must be at least %d characters", MinSetupSecretLength)
)

// Setup claim rate limits. The global limit bounds guessing across all source
// addresses: with 6-digit codes and a 60-minute lifetime, an attacker gets at
// most ~80 guesses per code (≈0.008% success).
const (
	setupMaxFailuresGlobal = 20
	setupMaxFailuresPerIP  = 5
	setupGlobalKey         = "setup:global"

	// MinSetupSecretLength applies to operator-provided setup secrets.
	MinSetupSecretLength = 12

	setupKindGenerated = "generated"
	setupKindProvided  = "provided"
)

func setupIPKey(ip string) string { return "setup:ip:" + ip }

// SetupRequired reports whether no administrator exists yet.
func (s *Service) SetupRequired(ctx context.Context) (bool, error) {
	n, err := s.st.R.CountAdmins(ctx)
	return n == 0, err
}

// SetupStatus reports whether setup is required and whether any unexpired
// code is currently registered.
func (s *Service) SetupStatus(ctx context.Context) (required, hasValidCode bool, err error) {
	required, err = s.SetupRequired(ctx)
	if err != nil || !required {
		return required, false, err
	}
	codes, err := s.st.R.ListValidSetupCodes(ctx, sql.NullInt64{Int64: s.now(), Valid: true})
	return true, len(codes) > 0, err
}

// IssueSetupCode generates a new 6-digit code valid for ttl, replacing any
// previously generated code (a provided secret stays valid). The plaintext
// code is returned once and never stored.
func (s *Service) IssueSetupCode(ctx context.Context, ttl time.Duration) (code string, expires time.Time, err error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", time.Time{}, err
	}
	code = fmt.Sprintf("%06d", n.Int64())
	expires = s.opts.Now().Add(ttl)
	err = s.st.InTx(ctx, func(q *db.Queries) error {
		admins, err := q.CountAdmins(ctx)
		if err != nil {
			return err
		}
		if admins > 0 {
			return ErrSetupClosed
		}
		if err := q.DeleteSetupCodesByKind(ctx, setupKindGenerated); err != nil {
			return err
		}
		return q.InsertSetupCode(ctx, db.InsertSetupCodeParams{
			ID: ids.New(ids.SetupCode), Kind: setupKindGenerated, CodeHash: hashSetupCode(code),
			CreatedAt: s.now(), ExpiresAt: sql.NullInt64{Int64: expires.UnixMilli(), Valid: true},
		})
	})
	if err != nil {
		return "", time.Time{}, err
	}
	return code, expires, nil
}

// RegisterProvidedSetupSecret stores the hash of an operator-supplied setup
// secret (no expiry), replacing any previously provided one.
func (s *Service) RegisterProvidedSetupSecret(ctx context.Context, secret string) error {
	secret = strings.TrimSpace(secret)
	if len(secret) < MinSetupSecretLength {
		return ErrSetupSecretWeak
	}
	return s.st.InTx(ctx, func(q *db.Queries) error {
		admins, err := q.CountAdmins(ctx)
		if err != nil {
			return err
		}
		if admins > 0 {
			return ErrSetupClosed
		}
		if err := q.DeleteSetupCodesByKind(ctx, setupKindProvided); err != nil {
			return err
		}
		return q.InsertSetupCode(ctx, db.InsertSetupCodeParams{
			ID: ids.New(ids.SetupCode), Kind: setupKindProvided, CodeHash: hashSetupCode(secret),
			CreatedAt: s.now(),
		})
	})
}

// ClearSetupCodes deletes all setup codes (used once an admin exists).
func (s *Service) ClearSetupCodes(ctx context.Context) error {
	return s.st.W.DeleteAllSetupCodes(ctx)
}

// ClaimFailure carries the audit reason for a failed claim.
type ClaimFailure struct {
	Err    error
	Reason string // rate_limited, closed, invalid_code
}

func (f *ClaimFailure) Error() string { return f.Err.Error() }
func (f *ClaimFailure) Unwrap() error { return f.Err }

// ClaimSetup creates the first administrator if code matches a valid setup
// code, consuming all codes atomically, and logs the new admin in. auditTx
// runs inside the same transaction so the claim and its audit record commit
// together.
func (s *Service) ClaimSetup(ctx context.Context, code, username, password, ip, userAgent string,
	auditTx func(q *db.Queries, u db.User) error) (*LoginResult, error) {

	limits := map[string]int{setupGlobalKey: setupMaxFailuresGlobal, setupIPKey(ip): setupMaxFailuresPerIP}
	if !s.limiter.under(limits) {
		return nil, &ClaimFailure{Err: ErrSetupRateLimited, Reason: "rate_limited"}
	}
	// Validate the account fields first so typos don't burn rate-limit budget.
	username = strings.TrimSpace(username)
	if !usernameRE.MatchString(username) {
		return nil, ErrInvalidUsername
	}
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}

	var res *LoginResult
	err = s.st.InTx(ctx, func(q *db.Queries) error {
		admins, err := q.CountAdmins(ctx)
		if err != nil {
			return err
		}
		if admins > 0 {
			return &ClaimFailure{Err: ErrSetupClosed, Reason: "closed"}
		}
		codes, err := q.ListValidSetupCodes(ctx, sql.NullInt64{Int64: s.now(), Valid: true})
		if err != nil {
			return err
		}
		if !matchSetupCode(codes, code) {
			return &ClaimFailure{Err: ErrSetupCodeInvalid, Reason: "invalid_code"}
		}
		now := s.now()
		u := db.User{ID: ids.New(ids.User), Username: username, PasswordHash: hash, Role: RoleAdmin,
			CreatedAt: now, UpdatedAt: now, PasswordChangedAt: now}
		err = q.CreateUser(ctx, db.CreateUserParams{
			ID: u.ID, Username: u.Username, PasswordHash: u.PasswordHash, Role: u.Role,
			CreatedAt: now, UpdatedAt: now, PasswordChangedAt: now,
		})
		if isUniqueViolation(err) {
			return ErrUsernameTaken
		}
		if err != nil {
			return err
		}
		if err := q.DeleteAllSetupCodes(ctx); err != nil {
			return err
		}
		if err := auditTx(q, u); err != nil {
			return err
		}
		res, err = s.createSession(ctx, q, u, ip, userAgent)
		return err
	})
	var cf *ClaimFailure
	if errors.As(err, &cf) && cf.Reason == "invalid_code" {
		s.limiter.record(setupGlobalKey, setupIPKey(ip))
	}
	if err != nil {
		return nil, err
	}
	return res, nil
}

// normalizeSetupCode trims whitespace and, for convenience with displayed
// codes, removes inner spaces and dashes.
func normalizeSetupCode(code string) string {
	code = strings.TrimSpace(code)
	return strings.NewReplacer(" ", "", "-", "").Replace(code)
}

func hashSetupCode(code string) []byte {
	h := sha256.Sum256([]byte(normalizeSetupCode(code)))
	return h[:]
}

func matchSetupCode(codes []db.SetupCode, code string) bool {
	if normalizeSetupCode(code) == "" {
		return false
	}
	h := hashSetupCode(code)
	match := 0
	for _, c := range codes {
		match |= subtle.ConstantTimeCompare(h, c.CodeHash)
	}
	return match == 1
}
