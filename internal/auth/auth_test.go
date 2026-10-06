package auth

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fleetmgr/internal/store"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestService(t *testing.T) (*Service, *clock, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	c := &clock{t: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	svc := NewService(st, Options{IdleTimeout: time.Hour, AbsoluteTimeout: 24 * time.Hour, Now: c.now})
	return svc, c, st
}

const goodPassword = "correct horse battery"

func TestPasswordHashing(t *testing.T) {
	h, err := HashPassword(goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("unexpected encoding %q", h)
	}
	if ok, err := VerifyPassword(h, goodPassword); err != nil || !ok {
		t.Fatalf("verify good: ok=%v err=%v", ok, err)
	}
	if ok, _ := VerifyPassword(h, "wrong password!!"); ok {
		t.Fatal("verify accepted wrong password")
	}
	if _, err := VerifyPassword("$bcrypt$nope", goodPassword); err == nil {
		t.Fatal("expected format error")
	}
	if err := ValidatePassword("short"); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("short password accepted: %v", err)
	}
}

func TestLoginSessionLifecycle(t *testing.T) {
	svc, c, _ := newTestService(t)
	ctx := context.Background()
	u, err := svc.CreateUser(ctx, "alice", goodPassword, RoleOperator)
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.Login(ctx, "ALICE", goodPassword, "10.0.0.1", "test") // usernames are case-insensitive
	if err != nil {
		t.Fatal(err)
	}
	if res.Principal.UserID != u.ID || res.Principal.CSRFToken == "" {
		t.Fatalf("principal = %+v", res.Principal)
	}
	p, err := svc.AuthenticateSession(ctx, res.Cookie)
	if err != nil || p.UserID != u.ID || p.Role != RoleOperator {
		t.Fatalf("authenticate: %+v %v", p, err)
	}
	if !CheckCSRF(p, res.Principal.CSRFToken) || CheckCSRF(p, "") || CheckCSRF(p, "nope") {
		t.Fatal("CSRF check misbehaves")
	}

	// Activity keeps the session alive past the idle timeout...
	c.advance(50 * time.Minute)
	if _, err := svc.AuthenticateSession(ctx, res.Cookie); err != nil {
		t.Fatalf("session expired early: %v", err)
	}
	c.advance(50 * time.Minute)
	if _, err := svc.AuthenticateSession(ctx, res.Cookie); err != nil {
		t.Fatalf("touch did not extend idle window: %v", err)
	}
	// ...but idleness ends it.
	c.advance(61 * time.Minute)
	if _, err := svc.AuthenticateSession(ctx, res.Cookie); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("idle session still valid: %v", err)
	}
}

func TestAbsoluteExpiry(t *testing.T) {
	svc, c, _ := newTestService(t)
	ctx := context.Background()
	if _, err := svc.CreateUser(ctx, "bob", goodPassword, RoleViewer); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Login(ctx, "bob", goodPassword, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 26; i++ { // 26 × 59m > 24h absolute limit
		c.advance(59 * time.Minute)
		if _, err := svc.AuthenticateSession(ctx, res.Cookie); err != nil {
			break
		}
	}
	if _, err := svc.AuthenticateSession(ctx, res.Cookie); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("session outlived absolute timeout")
	}
}

func TestLoginFailuresAndRateLimit(t *testing.T) {
	svc, c, _ := newTestService(t)
	ctx := context.Background()
	if _, err := svc.CreateUser(ctx, "carol", goodPassword, RoleAdmin); err != nil {
		t.Fatal(err)
	}
	var lf *LoginFailure
	_, err := svc.Login(ctx, "nobody", goodPassword, "1.1.1.1", "")
	if !errors.As(err, &lf) || lf.Reason != "unknown_user" || !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown user: %v", err)
	}
	for i := 0; i < maxFailuresPerUser; i++ {
		if _, err := svc.Login(ctx, "carol", "wrong password xx", "2.2.2.2", ""); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	// Even the correct password is refused while limited.
	if _, err := svc.Login(ctx, "carol", goodPassword, "3.3.3.3", ""); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected rate limit, got %v", err)
	}
	c.advance(limiterWindow + time.Second)
	if _, err := svc.Login(ctx, "carol", goodPassword, "3.3.3.3", ""); err != nil {
		t.Fatalf("after window: %v", err)
	}
}

func TestDisabledUser(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	admin, _ := svc.CreateUser(ctx, "admin", goodPassword, RoleAdmin)
	u, _ := svc.CreateUser(ctx, "dave", goodPassword, RoleOperator)
	res, err := svc.Login(ctx, "dave", goodPassword, "", "")
	if err != nil {
		t.Fatal(err)
	}
	actor := &Principal{UserID: admin.ID, Role: RoleAdmin, Kind: KindSession}
	yes := true
	if _, err := svc.UpdateUser(ctx, actor, u.ID, UserUpdate{Disabled: &yes}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AuthenticateSession(ctx, res.Cookie); err == nil {
		t.Fatal("disabled user's session still valid")
	}
	var lf *LoginFailure
	if _, err := svc.Login(ctx, "dave", goodPassword, "", ""); !errors.As(err, &lf) || lf.Reason != "disabled" {
		t.Fatalf("disabled login: %v", err)
	}
}

func TestLastAdminProtection(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	a1, _ := svc.CreateUser(ctx, "admin1", goodPassword, RoleAdmin)
	a2, _ := svc.CreateUser(ctx, "admin2", goodPassword, RoleAdmin)
	p1 := &Principal{UserID: a1.ID, Role: RoleAdmin, Kind: KindSession}
	p2 := &Principal{UserID: a2.ID, Role: RoleAdmin, Kind: KindSession}

	viewer := RoleViewer
	if _, err := svc.UpdateUser(ctx, p1, a1.ID, UserUpdate{Role: &viewer}); !errors.Is(err, ErrSelfModify) {
		t.Fatalf("self demote: %v", err)
	}
	if err := svc.DeleteUser(ctx, p1, a1.ID); !errors.Is(err, ErrSelfModify) {
		t.Fatalf("self delete: %v", err)
	}
	if err := svc.DeleteUser(ctx, p1, a2.ID); err != nil {
		t.Fatalf("delete second admin: %v", err)
	}
	// a1 is now the only admin; a stale principal for a2 must not remove it.
	if _, err := svc.UpdateUser(ctx, p2, a1.ID, UserUpdate{Role: &viewer}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("demote last admin: %v", err)
	}
}

func TestPasswordChangeRevokesOtherSessions(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	u, _ := svc.CreateUser(ctx, "erin", goodPassword, RoleOperator)
	s1, _ := svc.Login(ctx, "erin", goodPassword, "", "")
	s2, _ := svc.Login(ctx, "erin", goodPassword, "", "")
	newPw := "another long passphrase"
	if _, err := svc.UpdateUser(ctx, s1.Principal, u.ID, UserUpdate{Password: &newPw}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AuthenticateSession(ctx, s1.Cookie); err != nil {
		t.Fatalf("current session revoked: %v", err)
	}
	if _, err := svc.AuthenticateSession(ctx, s2.Cookie); err == nil {
		t.Fatal("other session survived password change")
	}
	if _, err := svc.Login(ctx, "erin", newPw, "", ""); err != nil {
		t.Fatalf("login with new password: %v", err)
	}
}

func TestAPITokens(t *testing.T) {
	svc, c, _ := newTestService(t)
	ctx := context.Background()
	u, _ := svc.CreateUser(ctx, "frank", goodPassword, RoleOperator)
	res, err := svc.CreateAPIToken(ctx, u.ID, "ci", c.now().Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Token, "fm_tok_") {
		t.Fatalf("token format %q", res.Token)
	}
	p, err := svc.AuthenticateToken(ctx, res.Token)
	if err != nil || p.UserID != u.ID || p.Kind != KindToken || p.TokenID != res.Row.ID {
		t.Fatalf("authenticate token: %+v %v", p, err)
	}
	if !CheckCSRF(p, "") {
		t.Fatal("bearer tokens should not need CSRF")
	}
	bad := []string{
		"", "fm_", "fm_nounderscore", res.Token + "x", strings.Replace(res.Token, "fm_", "xx_", 1),
		"fm_" + res.Row.ID + "_" + strings.Repeat("0", 64),
	}
	for _, b := range bad {
		if _, err := svc.AuthenticateToken(ctx, b); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("token %q accepted: %v", b, err)
		}
	}
	c.advance(49 * time.Hour)
	if _, err := svc.AuthenticateToken(ctx, res.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("expired token accepted")
	}
}

func TestCreateUserValidation(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	if _, err := svc.CreateUser(ctx, "bad name", goodPassword, RoleAdmin); !errors.Is(err, ErrInvalidUsername) {
		t.Fatalf("username: %v", err)
	}
	if _, err := svc.CreateUser(ctx, "ok", goodPassword, "root"); !errors.Is(err, ErrInvalidRole) {
		t.Fatalf("role: %v", err)
	}
	if _, err := svc.CreateUser(ctx, "ok", goodPassword, RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateUser(ctx, "OK", goodPassword, RoleAdmin); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("duplicate (case-insensitive): %v", err)
	}
}

func TestRolePermissions(t *testing.T) {
	if RoleHas(RoleViewer, PermJobsExecute) || !RoleHas(RoleOperator, PermJobsExecute) {
		t.Fatal("jobs.execute mapping wrong")
	}
	if RoleHas(RoleOperator, PermCredentialsManage) || !RoleHas(RoleAdmin, PermCredentialsManage) {
		t.Fatal("credentials.manage mapping wrong")
	}
	if !RoleHas(RoleAuditor, PermAuditRead) || RoleHas(RoleOperator, PermAuditRead) {
		t.Fatal("audit.read mapping wrong")
	}
}
