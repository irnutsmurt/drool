package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"drool/internal/auth"
	"drool/internal/store"
)

// withStdin runs f with os.Stdin reading input.
func withStdin(t *testing.T, input string, f func() error) error {
	t.Helper()
	p := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(p, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	saved := os.Stdin
	os.Stdin = in
	defer func() { os.Stdin = saved }()
	return f()
}

// drool user set-password: the recovery path for a forgotten password. It
// sets the password, ends the user's sessions, leaves two-factor alone and
// audits who did it without the password.
func TestUserSetPassword(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DROOL_DATA_DIR", dir)
	ctx := context.Background()
	st, err := store.Open(filepath.Join(dir, store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	svc := auth.NewService(st, auth.Options{})
	const oldPW, newPW = "the-forgotten-password", "a-brand-new-password-7"
	if _, err := svc.CreateUser(ctx, "alex", oldPW, auth.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	session, err := svc.Login(ctx, "alex", oldPW, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	run1 := func(input string, args ...string) error {
		return withStdin(t, input, func() error { return run(append([]string{"user", "set-password"}, args...)) })
	}
	if err := run1(newPW+"\n", "--username", "nobody", "--password-stdin"); err == nil || !strings.Contains(err.Error(), `no user "nobody"`) {
		t.Fatalf("unknown user: %v", err)
	}
	if err := run1("short\n", "--username", "alex", "--password-stdin"); err == nil || !strings.Contains(err.Error(), "password must be") {
		t.Fatalf("weak password: %v", err)
	}
	if err := run1(newPW+"\n", "--username", "alex", "--password-stdin"); err != nil {
		t.Fatal(err)
	}

	st, err = store.Open(filepath.Join(dir, store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc = auth.NewService(st, auth.Options{})
	if _, err := svc.Login(ctx, "alex", oldPW, "127.0.0.1", "test"); err == nil {
		t.Fatal("the old password still works")
	}
	if _, err := svc.Login(ctx, "alex", newPW, "127.0.0.1", "test"); err != nil {
		t.Fatalf("the new password: %v", err)
	}
	if _, err := svc.AuthenticateSession(ctx, session.Cookie); err == nil {
		t.Fatal("the session from before the change still works")
	}
	var actor, details string
	if err := st.Reader.QueryRow(`SELECT actor_name, details_json FROM audit_events WHERE action = 'user.update' ORDER BY rowid DESC LIMIT 1`).Scan(&actor, &details); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(actor, "cli:") || !strings.Contains(details, `"changes":["password"]`) || !strings.Contains(details, `"username":"alex"`) ||
		!strings.Contains(details, `"via":"cli"`) {
		t.Fatalf("audit: %s %s", actor, details)
	}
	var all string
	st.Reader.QueryRow(`SELECT group_concat(details_json, ' ') FROM audit_events`).Scan(&all)
	if strings.Contains(all, newPW) || strings.Contains(all, oldPW) {
		t.Fatal("a password reached the audit log")
	}
}
