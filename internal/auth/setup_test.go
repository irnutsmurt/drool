package auth

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"testing"
	"time"

	"drool/internal/store/db"
)

func noAudit(*db.Queries, db.User) error { return nil }

func TestSetupClaimHappyPath(t *testing.T) {
	svc, _, st := newTestService(t)
	ctx := context.Background()

	required, has, err := svc.SetupStatus(ctx)
	if err != nil || !required || has {
		t.Fatalf("fresh status: required=%v has=%v err=%v", required, has, err)
	}
	code, _, err := svc.IssueSetupCode(ctx, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^\d{6}$`).MatchString(code) {
		t.Fatalf("code format %q", code)
	}
	// Only the hash is stored.
	var stored []byte
	if err := st.Reader.QueryRow(`SELECT code_hash FROM setup_codes`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if string(stored) == code || len(stored) != 32 {
		t.Fatal("setup code stored in plaintext or wrong hash length")
	}

	audited := false
	res, err := svc.ClaimSetup(ctx, " "+code[:3]+" "+code[3:]+" ", "admin", goodPassword, "10.0.0.1", "ua",
		func(q *db.Queries, u db.User) error { audited = u.Role == RoleAdmin; return nil })
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !audited || res.Principal.Role != RoleAdmin || res.Cookie == "" {
		t.Fatalf("claim result: audited=%v %+v", audited, res.Principal)
	}
	if n, _ := st.R.CountSetupCodes(ctx); n != 0 {
		t.Fatalf("%d setup codes left after claim", n)
	}
	// Setup is closed for good, even with a fresh code attempt.
	if _, err := svc.ClaimSetup(ctx, code, "admin2", goodPassword, "10.0.0.1", "", noAudit); !errors.Is(err, ErrSetupClosed) {
		t.Fatalf("second claim: %v", err)
	}
	if _, _, err := svc.IssueSetupCode(ctx, time.Hour); !errors.Is(err, ErrSetupClosed) {
		t.Fatalf("issue after setup: %v", err)
	}
}

func TestSetupCodeExpiresAndIsReplaced(t *testing.T) {
	svc, c, _ := newTestService(t)
	ctx := context.Background()
	old, _, _ := svc.IssueSetupCode(ctx, 30*time.Minute)
	newer, _, _ := svc.IssueSetupCode(ctx, 30*time.Minute)
	if old != newer {
		if _, err := svc.ClaimSetup(ctx, old, "admin", goodPassword, "1.1.1.1", "", noAudit); !errors.Is(err, ErrSetupCodeInvalid) {
			t.Fatalf("replaced code accepted: %v", err)
		}
	}
	c.advance(31 * time.Minute)
	if _, err := svc.ClaimSetup(ctx, newer, "admin", goodPassword, "1.1.1.2", "", noAudit); !errors.Is(err, ErrSetupCodeInvalid) {
		t.Fatalf("expired code accepted: %v", err)
	}
	if _, has, _ := svc.SetupStatus(ctx); has {
		t.Fatal("status reports a valid code after expiry")
	}
}

func TestSetupProvidedSecret(t *testing.T) {
	svc, c, _ := newTestService(t)
	ctx := context.Background()
	if err := svc.RegisterProvidedSetupSecret(ctx, "short"); !errors.Is(err, ErrSetupSecretWeak) {
		t.Fatalf("weak secret accepted: %v", err)
	}
	secret := "automation-bootstrap-secret-42"
	if err := svc.RegisterProvidedSetupSecret(ctx, secret); err != nil {
		t.Fatal(err)
	}
	c.advance(30 * 24 * time.Hour) // provided secrets don't expire
	if _, err := svc.ClaimSetup(ctx, secret, "admin", goodPassword, "", "", noAudit); err != nil {
		t.Fatalf("claim with provided secret: %v", err)
	}
}

func TestSetupValidationDoesNotConsumeOrCount(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	code, _, _ := svc.IssueSetupCode(ctx, time.Hour)
	for i := 0; i < setupMaxFailuresPerIP+2; i++ {
		if _, err := svc.ClaimSetup(ctx, code, "admin", "short", "9.9.9.9", "", noAudit); !errors.Is(err, ErrWeakPassword) {
			t.Fatalf("weak password: %v", err)
		}
	}
	if _, err := svc.ClaimSetup(ctx, code, "admin", goodPassword, "9.9.9.9", "", noAudit); err != nil {
		t.Fatalf("validation errors burned the code or rate limit: %v", err)
	}
}

func TestSetupAuditFailureRollsBack(t *testing.T) {
	svc, _, st := newTestService(t)
	ctx := context.Background()
	code, _, _ := svc.IssueSetupCode(ctx, time.Hour)
	boom := errors.New("audit down")
	if _, err := svc.ClaimSetup(ctx, code, "admin", goodPassword, "", "", func(*db.Queries, db.User) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if n, _ := st.R.CountAdmins(ctx); n != 0 {
		t.Fatal("admin created although audit failed")
	}
	if _, err := svc.ClaimSetup(ctx, code, "admin", goodPassword, "", "", noAudit); err != nil {
		t.Fatalf("code should still be valid after rollback: %v", err)
	}
}

func TestSetupRateLimit(t *testing.T) {
	svc, c, _ := newTestService(t)
	ctx := context.Background()
	code, _, _ := svc.IssueSetupCode(ctx, time.Hour)
	wrong := "000000"
	if wrong == code {
		wrong = "111111"
	}
	for i := 0; i < setupMaxFailuresPerIP; i++ {
		if _, err := svc.ClaimSetup(ctx, wrong, "admin", goodPassword, "6.6.6.6", "", noAudit); !errors.Is(err, ErrSetupCodeInvalid) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, err := svc.ClaimSetup(ctx, code, "admin", goodPassword, "6.6.6.6", "", noAudit); !errors.Is(err, ErrSetupRateLimited) {
		t.Fatalf("per-IP limit not enforced: %v", err)
	}
	// The global limit stops distributed guessing too.
	for i := 0; i < setupMaxFailuresGlobal; i++ {
		ip := "7.7.7." + string(rune('a'+i))
		svc.ClaimSetup(ctx, wrong, "admin", goodPassword, ip, "", noAudit)
	}
	if _, err := svc.ClaimSetup(ctx, code, "admin", goodPassword, "8.8.8.8", "", noAudit); !errors.Is(err, ErrSetupRateLimited) {
		t.Fatalf("global limit not enforced: %v", err)
	}
	c.advance(limiterWindow + time.Second)
	if _, err := svc.ClaimSetup(ctx, code, "admin", goodPassword, "8.8.8.8", "", noAudit); err != nil {
		t.Fatalf("after window: %v", err)
	}
}

func TestSetupConcurrentClaimsCreateOneAdmin(t *testing.T) {
	svc, _, st := newTestService(t)
	ctx := context.Background()
	code, _, _ := svc.IssueSetupCode(ctx, time.Hour)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := "admin" + string(rune('a'+i))
			if _, err := svc.ClaimSetup(ctx, code, name, goodPassword, "", "", noAudit); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if n, _ := st.R.CountAdmins(ctx); ok != 1 || n != 1 {
		t.Fatalf("successful claims = %d, admins = %d; want exactly 1", ok, n)
	}
}
