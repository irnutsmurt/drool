package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"drool/internal/agent"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"drool/internal/audit"
	"drool/internal/auth"
)

// totp computes an RFC 6238 code independently of the auth package.
func totp(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(at.Unix()/30))
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[off:off+4])&0x7fffffff)%1_000_000)
}

func (c *client) reauth(t *testing.T, body map[string]string) resp {
	t.Helper()
	return c.do("POST", "/api/v1/auth/reauth", body)
}

func TestReauthForHighImpactActions(t *testing.T) {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	c := h.login("admin")
	newUser := func(name string) resp {
		return c.do("POST", "/api/v1/users", map[string]string{"username": name, "password": pw, "role": "viewer"})
	}
	// Signing in counts as confirming.
	if r := newUser("first"); r.status != 201 {
		t.Fatalf("right after sign-in: %d %s", r.status, r.body)
	}
	var me meResponse
	c.do("GET", "/api/v1/auth/me", nil).json(t, &me)
	if me.ReauthUntil == nil || me.ReauthWindowS != 300 {
		t.Fatalf("me: %+v", me)
	}
	h.clock.Store(int64(6 * time.Minute))
	r := newUser("second")
	if r.status != 403 || r.problemCode(t) != "reauth_required" {
		t.Fatalf("after the window: %d %s", r.status, r.body)
	}
	var p Problem
	r.json(t, &p)
	if p.Reauth == nil || p.Reauth.MFA || p.Reauth.WindowS != 300 {
		t.Fatalf("problem: %s", r.body)
	}
	if _, err := h.st.R.GetUserByUsername(context.Background(), "second"); err == nil {
		t.Fatal("refused action happened anyway")
	}
	if r := c.reauth(t, map[string]string{"password": "not the password"}); r.status != 403 || r.problemCode(t) != "invalid_credentials" {
		t.Fatalf("wrong password: %d %s", r.status, r.body)
	}
	if r := c.reauth(t, map[string]string{"password": pw}); r.status != 200 {
		t.Fatalf("reauth: %d %s", r.status, r.body)
	}
	if r := newUser("second"); r.status != 201 {
		t.Fatalf("after reauth: %d %s", r.status, r.body)
	}
	// A token never gets through, whatever its owner did recently.
	r = c.do("POST", "/api/v1/tokens", map[string]any{"name": "ci"})
	if r.status != 201 {
		t.Fatalf("token: %d %s", r.status, r.body)
	}
	var tok createTokenResponse
	r.json(t, &tok)
	tc := &client{h: h, bearer: tok.Token}
	if r := tc.do("DELETE", "/api/v1/users/"+userID(t, h, "first"), nil); r.status != 403 || !strings.Contains(string(r.body), "interactive session") {
		t.Fatalf("token on a reauth route: %d %s", r.status, r.body)
	}
	if r := tc.do("POST", "/api/v1/auth/reauth", map[string]string{"password": pw}); r.status != 403 {
		t.Fatalf("token reauth: %d", r.status)
	}
	// Failures count toward the sign-in limit: guessing is bounded.
	for i := 0; i < 5; i++ {
		c.reauth(t, map[string]string{"password": "guess guess guess"})
	}
	if r := c.reauth(t, map[string]string{"password": pw}); r.status != 429 {
		t.Fatalf("after five wrong passwords: %d %s", r.status, r.body)
	}
	assertAudit(t, h, "auth.reauth", "failure")
}

func userID(t *testing.T, h *harness, name string) string {
	t.Helper()
	u, err := h.st.R.GetUserByUsername(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func assertAudit(t *testing.T, h *harness, action, result string) {
	t.Helper()
	var n int
	h.st.Reader.QueryRow(`SELECT count(*) FROM audit_events WHERE action = ? AND result = ?`, action, result).Scan(&n)
	if n == 0 {
		t.Fatalf("no %s %s audit record", action, result)
	}
}

// enrollMFA turns two-factor on for c's user and returns the secret and
// recovery codes.
func enrollMFA(t *testing.T, h *harness, c *client) (string, []string) {
	t.Helper()
	r := c.do("POST", "/api/v1/auth/mfa/enroll", nil)
	if r.status != 200 || r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("enroll: %d %s", r.status, r.body)
	}
	var en mfaEnrollResponse
	r.json(t, &en)
	if !strings.HasPrefix(en.URI, "otpauth://totp/DROOL:") || !strings.Contains(en.URI, "secret="+en.Secret) {
		t.Fatalf("uri %s", en.URI)
	}
	if r := c.do("POST", "/api/v1/auth/mfa/confirm", map[string]string{"code": "000000"}); r.status != 422 {
		t.Fatalf("wrong confirm code: %d %s", r.status, r.body)
	}
	r = c.do("POST", "/api/v1/auth/mfa/confirm", map[string]string{"code": totp(t, en.Secret, h.now())})
	if r.status != 200 {
		t.Fatalf("confirm: %d %s", r.status, r.body)
	}
	var rc recoveryCodesResponse
	r.json(t, &rc)
	if len(rc.RecoveryCodes) != 10 {
		t.Fatalf("recovery codes: %v", rc.RecoveryCodes)
	}
	return en.Secret, rc.RecoveryCodes
}

func loginStart(t *testing.T, h *harness, name string) mfaChallengeResponse {
	t.Helper()
	r := (&client{h: h}).do("POST", "/api/v1/auth/login", map[string]string{"username": name, "password": pw})
	var ch mfaChallengeResponse
	r.json(t, &ch)
	if r.status != 200 || !ch.MFARequired || ch.Challenge == "" || r.header.Get("Set-Cookie") != "" {
		t.Fatalf("password step: %d %s", r.status, r.body)
	}
	return ch
}

func TestTwoFactorSignIn(t *testing.T) {
	h := newHarness(t)
	h.mkUser("alex", auth.RoleOperator)
	other := h.login("alex")
	c := h.login("alex")
	secret, codes := enrollMFA(t, h, c)
	// Turning it on signs out the other sessions, not this one.
	if r := other.do("GET", "/api/v1/auth/me", nil); r.status != 401 {
		t.Fatalf("other session survived: %d", r.status)
	}
	var me meResponse
	c.do("GET", "/api/v1/auth/me", nil).json(t, &me)
	if !me.MFAEnabled {
		t.Fatal("me: mfa_enabled false")
	}
	// The secret is never stored in plaintext, and never in the audit log.
	var stored []byte
	h.st.Reader.QueryRow(`SELECT secret_ciphertext FROM user_mfa`).Scan(&stored)
	raw, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if len(stored) == 0 || strings.Contains(string(stored), string(raw)) {
		t.Fatal("secret stored in the clear")
	}
	var leaks int
	h.st.Reader.QueryRow(`SELECT count(*) FROM audit_events WHERE details_json LIKE '%' || ? || '%'`, secret).Scan(&leaks)
	if leaks != 0 {
		t.Fatal("secret in the audit log")
	}
	for _, code := range codes {
		var n int
		h.st.Reader.QueryRow(`SELECT count(*) FROM user_recovery_codes WHERE code_hash = ?`, []byte(code)).Scan(&n)
		if n != 0 {
			t.Fatal("recovery code stored in the clear")
		}
	}

	// Password alone isn't enough any more.
	ch := loginStart(t, h, "alex")
	finish := func(body map[string]string) resp {
		body["challenge"] = ch.Challenge
		return (&client{h: h}).do("POST", "/api/v1/auth/login/mfa", body)
	}
	if r := finish(map[string]string{"code": "123456"}); r.status != 401 || r.problemCode(t) != "invalid_code" {
		t.Fatalf("wrong code: %d %s", r.status, r.body)
	}
	// The code that confirmed enrollment was used: a step works once.
	h.clock.Add(int64(31 * time.Second))
	code := totp(t, secret, h.now())
	r := finish(map[string]string{"code": code})
	if r.status != 200 || r.header.Get("Set-Cookie") == "" {
		t.Fatalf("right code: %d %s", r.status, r.body)
	}
	// The challenge is spent, and so is the code.
	if r := finish(map[string]string{"code": code}); r.status != 401 || r.problemCode(t) != "challenge_expired" {
		t.Fatalf("challenge reuse: %d %s", r.status, r.body)
	}
	ch = loginStart(t, h, "alex")
	if r := finish(map[string]string{"code": code}); r.status != 401 || r.problemCode(t) != "invalid_code" {
		t.Fatalf("code replay: %d %s", r.status, r.body)
	}
	// A recovery code works once, in any case and with or without dashes.
	ch = loginStart(t, h, "alex")
	if r := finish(map[string]string{"recovery_code": strings.ToUpper(strings.ReplaceAll(codes[0], "-", ""))}); r.status != 200 {
		t.Fatalf("recovery code: %d %s", r.status, r.body)
	}
	ch = loginStart(t, h, "alex")
	if r := finish(map[string]string{"recovery_code": codes[0]}); r.status != 401 {
		t.Fatalf("recovery code reuse: %d %s", r.status, r.body)
	}
	assertAudit(t, h, "auth.login", "failure")
	var details string
	h.st.Reader.QueryRow(`SELECT details_json FROM audit_events WHERE action = 'auth.login' AND details_json LIKE '%recovery_code%' ORDER BY seq DESC LIMIT 1`).Scan(&details)
	if !strings.Contains(details, `"recovery_codes_left":9`) {
		t.Fatalf("recovery code login audit: %s", details)
	}

	// Re-authentication now needs the code too.
	h.clock.Add(int64(10 * time.Minute))
	if r := c.reauth(t, map[string]string{"password": pw}); r.status != 403 || r.problemCode(t) != "invalid_code" {
		t.Fatalf("reauth without code: %d %s", r.status, r.body)
	}
	if r := c.reauth(t, map[string]string{"password": pw, "code": totp(t, secret, h.now())}); r.status != 200 {
		t.Fatalf("reauth with code: %d %s", r.status, r.body)
	}
	// New recovery codes replace the old ones.
	r = c.do("POST", "/api/v1/auth/mfa/recovery-codes", nil)
	var rc recoveryCodesResponse
	r.json(t, &rc)
	if r.status != 200 || len(rc.RecoveryCodes) != 10 {
		t.Fatalf("regenerate: %d %s", r.status, r.body)
	}
	ch = loginStart(t, h, "alex")
	if r := finish(map[string]string{"recovery_code": codes[1]}); r.status != 401 {
		t.Fatalf("old recovery code after regenerating: %d", r.status)
	}
	// Turning it off needs a fresh confirmation.
	h.clock.Add(int64(10 * time.Minute))
	if r := c.do("DELETE", "/api/v1/auth/mfa", nil); r.status != 403 || r.problemCode(t) != "reauth_required" {
		t.Fatalf("disable without reauth: %d %s", r.status, r.body)
	}
	var p Problem
	c.do("DELETE", "/api/v1/auth/mfa", nil).json(t, &p)
	if p.Reauth == nil || !p.Reauth.MFA {
		t.Fatalf("problem should ask for a code: %+v", p)
	}
}

func TestTwoFactorGuessingIsBounded(t *testing.T) {
	h := newHarness(t)
	h.mkUser("alex", auth.RoleViewer)
	secret, _ := enrollMFA(t, h, h.login("alex"))
	ch := loginStart(t, h, "alex")
	for i := 0; i < 5; i++ {
		(&client{h: h}).do("POST", "/api/v1/auth/login/mfa", map[string]string{"challenge": ch.Challenge, "code": fmt.Sprintf("%06d", i)})
	}
	// The challenge is gone after five wrong codes...
	r := (&client{h: h}).do("POST", "/api/v1/auth/login/mfa", map[string]string{"challenge": ch.Challenge, "code": totp(t, secret, h.now().Add(30*time.Second))})
	if r.status != 401 || r.problemCode(t) != "challenge_expired" {
		t.Fatalf("spent challenge: %d %s", r.status, r.body)
	}
	// ...and a correct password doesn't reset the count: no more guesses.
	r = (&client{h: h}).do("POST", "/api/v1/auth/login", map[string]string{"username": "alex", "password": pw})
	if r.status != 429 {
		t.Fatalf("password after five wrong codes: %d %s", r.status, r.body)
	}
}

func TestAdminResetsTwoFactor(t *testing.T) {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	h.mkUser("alex", auth.RoleOperator)
	alex := h.login("alex")
	enrollMFA(t, h, alex)
	admin := h.login("admin")
	var list listResponse[userJSON]
	admin.do("GET", "/api/v1/users", nil).json(t, &list)
	found := false
	for _, u := range list.Items {
		found = found || u.Username == "alex" && u.MFAEnabled
	}
	if !found {
		t.Fatalf("users list doesn't show alex's two-factor: %+v", list.Items)
	}
	id := userID(t, h, "alex")
	if r := alex.do("POST", "/api/v1/users/"+id+"/mfa/reset", nil); r.status != 403 {
		t.Fatalf("non-admin reset: %d", r.status)
	}
	if r := admin.do("POST", "/api/v1/users/"+id+"/mfa/reset", nil); r.status != 204 {
		t.Fatalf("reset: %d %s", r.status, r.body)
	}
	if r := alex.do("GET", "/api/v1/auth/me", nil); r.status != 401 {
		t.Fatal("alex's session survived the reset")
	}
	h.login("alex") // password only again
	if r := admin.do("POST", "/api/v1/users/"+id+"/mfa/reset", nil); r.status != 409 {
		t.Fatalf("second reset: %d", r.status)
	}
	assertAudit(t, h, "user.mfa_reset", "success")
}

func TestAuditIntegrityAPI(t *testing.T) {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	h.mkUser("aud", auth.RoleAuditor)
	h.mkUser("view", auth.RoleViewer)
	admin, aud, view := h.login("admin"), h.login("aud"), h.login("view")
	if r := view.do("GET", "/api/v1/audit/integrity", nil); r.status != 403 {
		t.Fatalf("viewer: %d", r.status)
	}
	verify := func(c *client) integrityJSON {
		t.Helper()
		r := c.do("POST", "/api/v1/audit/integrity/verify", nil)
		if r.status != 202 {
			t.Fatalf("verify: %d %s", r.status, r.body)
		}
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			var st integrityJSON
			c.do("GET", "/api/v1/audit/integrity", nil).json(t, &st)
			if st.Running == nil && st.Last != nil {
				return st
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("verification didn't finish")
		return integrityJSON{}
	}
	st := verify(aud)
	if st.State != "ok" || st.Last.Records == 0 || st.HeadSeq == 0 {
		t.Fatalf("intact: %+v", st)
	}
	var list listResponse[auditJSON]
	aud.do("GET", "/api/v1/audit?limit=5", nil).json(t, &list)
	if len(list.Items) == 0 || list.Items[0].Seq == nil || len(list.Items[0].Hash) != 64 {
		t.Fatalf("audit records carry no chain fields: %+v", list.Items)
	}

	// Someone edits the database behind DROOL's back.
	if _, err := h.st.Writer.Exec(`DROP TRIGGER audit_no_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.Writer.Exec(`UPDATE audit_events SET actor_name = 'nobody' WHERE seq = 3`); err != nil {
		t.Fatal(err)
	}
	st = verify(aud)
	if st.State != "failed" || st.LastFailed == nil || st.LastFailed.FirstBadSeq != 3 || st.LastFailed.EvidenceFile == "" {
		t.Fatalf("tampered: %+v", st.LastFailed)
	}
	ev, err := os.ReadFile(st.LastFailed.EvidenceFile)
	if err != nil || !strings.Contains(string(ev), `"actor_name": "nobody"`) {
		t.Fatalf("evidence: %v %s", err, ev)
	}
	if fi, _ := os.Stat(st.LastFailed.EvidenceFile); fi.Mode().Perm() != 0o600 {
		t.Fatalf("evidence mode %v", fi.Mode())
	}
	assertAudit(t, h, "audit.verify", "failure")
	// Only an administrator can acknowledge, with a note, and the break
	// stays reported.
	if r := aud.do("POST", "/api/v1/audit/integrity/acknowledge", map[string]string{"note": "x"}); r.status != 403 {
		t.Fatalf("auditor acknowledge: %d", r.status)
	}
	if r := admin.do("POST", "/api/v1/audit/integrity/acknowledge", map[string]string{"note": ""}); r.status != 422 {
		t.Fatalf("no note: %d", r.status)
	}
	r := admin.do("POST", "/api/v1/audit/integrity/acknowledge", map[string]string{"note": "restored from a disk image; investigated"})
	if r.status != 200 {
		t.Fatalf("acknowledge: %d %s", r.status, r.body)
	}
	if st := verify(aud); st.State != "acknowledged" || st.Last.Result != "failed" {
		t.Fatalf("after acknowledging: %s %s", st.State, st.Last.Result)
	}
	// A new break is not covered by the acknowledgement.
	if _, err := h.st.Writer.Exec(`UPDATE audit_events SET action = 'x' WHERE seq = 5`); err != nil {
		t.Fatal(err)
	}
	if st := verify(aud); st.State != "failed" || st.LastFailed.ProblemCount != 2 {
		t.Fatalf("new break: %s %+v", st.State, st.LastFailed)
	}
}

func TestAuditSinkAPI(t *testing.T) {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	admin := h.login("admin")
	var mu sync.Mutex
	var got []map[string]any
	var auth []string
	fail := true
	recv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		auth = append(auth, r.Header.Get("Authorization"))
		if fail {
			http.Error(w, "receiver down", 503)
			return
		}
		var body map[string]any
		json.Unmarshal(b, &body)
		got = append(got, body)
	}))
	defer recv.Close()
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: recv.Certificate().Raw}))
	secret := "Bearer s3cret-sink-token"

	put := func(body map[string]any) resp { return admin.do("PUT", "/api/v1/audit/sink", body) }
	if r := put(map[string]any{"enabled": true, "url": strings.Replace(recv.URL, "https", "http", 1)}); r.status != 422 {
		t.Fatalf("plain http accepted: %d", r.status)
	}
	if r := put(map[string]any{"enabled": true, "url": "https://user:pw@example.net/x"}); r.status != 422 {
		t.Fatalf("credentials in URL accepted: %d", r.status)
	}
	// Without the receiver's CA the TLS check fails: no skip-verify.
	if r := admin.do("POST", "/api/v1/audit/sink/test", map[string]any{"enabled": true, "url": recv.URL}); !strings.Contains(string(r.body), `"ok":false`) {
		t.Fatalf("untrusted certificate: %s", r.body)
	}
	r := put(map[string]any{"enabled": true, "url": recv.URL + "/drool", "ca_pem": caPEM, "header_name": "Authorization", "header_value": secret})
	if r.status != 200 {
		t.Fatalf("configure: %d %s", r.status, r.body)
	}
	var st audit.SinkStatus
	r.json(t, &st)
	if !st.HeaderSet || strings.Contains(string(r.body), "s3cret") || st.Backlog == 0 {
		t.Fatalf("status: %s", r.body)
	}
	var leak int
	h.st.Reader.QueryRow(`SELECT count(*) FROM audit_events WHERE details_json LIKE '%s3cret%'`).Scan(&leak)
	if leak != 0 {
		t.Fatal("header value in the audit log")
	}
	// The receiver is down: nothing is lost, the backlog grows, retries back off.
	if _, err := h.sink.DeliverOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	admin.do("GET", "/api/v1/audit/sink", nil).json(t, &st)
	if st.State != "failing" || st.Failures != 1 || !strings.Contains(st.LastError, "503") || st.CursorSeq != 0 || st.NextAttemptAt == nil {
		t.Fatalf("failing: %+v", st)
	}
	if more, _ := h.sink.DeliverOnce(context.Background()); more {
		t.Fatal("retried before the backoff")
	}
	// The receiver comes back.
	mu.Lock()
	fail = false
	mu.Unlock()
	if _, err := h.st.Writer.Exec(`UPDATE audit_sink SET next_attempt_at = 0`); err != nil {
		t.Fatal(err)
	}
	for {
		more, err := h.sink.DeliverOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	admin.do("GET", "/api/v1/audit/sink", nil).json(t, &st)
	if st.State != "ok" || st.Backlog != 0 || st.CursorSeq != st.HeadSeq || st.Failures != 0 {
		t.Fatalf("delivered: %+v", st)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, a := range auth {
		if a != secret {
			t.Fatalf("header not sent: %q", a)
		}
	}
	// The receiver can verify the chain with nothing but the export.
	var prev string
	n := 0
	for _, b := range got {
		if b["format"] != audit.ExportFormat || b["chain_id"] == "" {
			t.Fatalf("body: %v", b)
		}
		for _, x := range b["records"].([]any) {
			rec := x.(map[string]any)
			n++
			if int(rec["seq"].(float64)) != n {
				t.Fatalf("seq %v, want %d", rec["seq"], n)
			}
			if n > 1 && rec["prev_hash"] != prev {
				t.Fatalf("record %d doesn't link", n)
			}
			if h := receiverHash(rec); h != rec["hash"] {
				t.Fatalf("record %d: recomputed %s, sent %s", n, h, rec["hash"])
			}
			prev = rec["hash"].(string)
		}
	}
	if n == 0 {
		t.Fatal("nothing delivered")
	}
}

// receiverHash is the user guide's verification recipe (docs/wiki/Remote-Audit-Copy-Reference.md), written without the
// audit package.
func receiverHash(rec map[string]any) string {
	var b strings.Builder
	ns := func(s string) { fmt.Fprintf(&b, "%d:%s,", len(s), s) }
	ns("drool-audit-v1")
	ns(rec["prev_hash"].(string))
	ns(fmt.Sprint(int64(rec["seq"].(float64))))
	ns(rec["id"].(string))
	ns(fmt.Sprint(int64(rec["ts"].(float64))))
	for _, k := range []string{"actor_type", "actor_id", "actor_name", "action", "target_type", "target_id", "result", "source_ip", "request_id", "details_json"} {
		ns(rec[k].(string))
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// TestLastPathAndKeyGuards (M16): replacing a credential hosts use, and
// revoking a host's only management path, are refused until acknowledged;
// they, a changed host key and the agent CA need a recent sign-in.
func TestLastPathAndKeyGuards(t *testing.T) {
	e := newAgentEnv(t)
	h, c := e.h, e.c
	ctx := context.Background()

	// A credential a host uses.
	var cred struct {
		ID string `json:"id"`
	}
	c.do("POST", "/api/v1/secrets", map[string]any{"name": "deploy-pw", "kind": "password", "value": "first password"}).json(t, &cred)
	hostID := readyHost(t, h, c, "web")
	if _, err := h.st.Writer.Exec(`UPDATE hosts SET ssh_credential_id = ? WHERE id = ?`, cred.ID, hostID); err != nil {
		t.Fatal(err)
	}
	r := c.do("PATCH", "/api/v1/secrets/"+cred.ID, map[string]any{"value": "second password"})
	if r.status != 409 || r.problemCode(t) != "credential_in_use" || !strings.Contains(string(r.body), "web") {
		t.Fatalf("replacing an in-use credential: %d %s", r.status, r.body)
	}
	h.clock.Store(int64(6 * time.Minute))
	if r := c.do("PATCH", "/api/v1/secrets/"+cred.ID, map[string]any{"value": "second password", "replace_in_use": true}); r.status != 403 || r.problemCode(t) != "reauth_required" {
		t.Fatalf("acknowledged, without a recent sign-in: %d %s", r.status, r.body)
	}
	c.reauth(t, map[string]string{"password": pw})
	if r := c.do("PATCH", "/api/v1/secrets/"+cred.ID, map[string]any{"value": "second password", "replace_in_use": true}); r.status != 200 {
		t.Fatalf("acknowledged and confirmed: %d %s", r.status, r.body)
	}
	// Renaming needs neither.
	h.clock.Store(int64(12 * time.Minute))
	if r := c.do("PATCH", "/api/v1/secrets/"+cred.ID, map[string]any{"name": "deploy-password"}); r.status != 200 {
		t.Fatalf("rename: %d %s", r.status, r.body)
	}

	// A host whose Shepherd is its only way in.
	tok := e.token(t, map[string]any{"preset": "full", "description": "only-path"})
	p := agentPaths(t, fullCaps(""))
	id, err := agent.Enroll(ctx, p, tok.Token, "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	c.reauth(t, map[string]string{"password": pw})
	r = c.do("POST", "/api/v1/agents/"+id.IdentityID+"/revoke", map[string]any{"reason": "decommissioned"})
	if r.status != 409 || r.problemCode(t) != "last_management_path" {
		t.Fatalf("revoking the only path: %d %s", r.status, r.body)
	}
	if r := c.do("POST", "/api/v1/agents/"+id.IdentityID+"/revoke", map[string]any{"reason": "decommissioned", "leave_unmanaged": true}); r.status != 204 {
		t.Fatalf("acknowledged: %d %s", r.status, r.body)
	}

	// Trust decisions need a recent sign-in.
	h.clock.Store(int64(20 * time.Minute))
	for _, call := range []struct {
		method, path string
		body         map[string]any
	}{
		{"POST", "/api/v1/agents/pki/rotate", nil},
		{"POST", "/api/v1/agents/release-keys", nil},
		{"POST", "/api/v1/hosts/" + hostID + "/hostkeys/approve", map[string]any{"fingerprint": "SHA256:x"}}, // a pinned key exists
		{"POST", "/api/v1/agents/" + id.IdentityID + "/resolve-conflict", nil},
	} {
		r := c.do(call.method, call.path, call.body)
		if r.status != 403 || r.problemCode(t) != "reauth_required" {
			t.Errorf("%s %s without a recent sign-in: %d %s", call.method, call.path, r.status, r.body)
		}
	}
}
