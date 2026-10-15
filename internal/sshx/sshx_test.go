package sshx

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"drool/internal/model"
	"drool/internal/sshx/sshtest"
)

func target(t *testing.T, srv *sshtest.Server, id string, client ssh.Signer, pinned ssh.PublicKey) *Target {
	t.Helper()
	h, p := srv.Host()
	return &Target{HostID: id, Name: id, Address: h, Port: p, User: "deploy", Signer: client, PinnedKey: pinned}
}

func echoExec(cmd string, stdin []byte) (string, string, int) {
	switch cmd {
	case "fail":
		return "", "boom", 3
	case "cat":
		return string(stdin), "", 0
	}
	return "ran:" + cmd, "", 0
}

func TestDialRunAndPinning(t *testing.T) {
	hostKey := sshtest.NewEd25519Signer(t)
	srv := sshtest.Start(t, hostKey)
	client := sshtest.NewEd25519Signer(t)
	srv.Authorize(client.PublicKey())
	srv.SetExec(echoExec)
	d := &Dialer{}
	ctx := context.Background()

	c, err := d.Dial(ctx, target(t, srv, "h1", client, hostKey.PublicKey()))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	res, err := c.Run(ctx, "hello", RunOptions{})
	if err != nil || string(res.Stdout) != "ran:hello" || res.ExitCode != 0 {
		t.Fatalf("run: %+v %v", res, err)
	}
	res, _ = c.Run(ctx, "fail", RunOptions{})
	if res.ExitCode != 3 || string(res.Stderr) != "boom" {
		t.Fatalf("fail: %+v", res)
	}
	res, _ = c.Run(ctx, "cat", RunOptions{Stdin: []byte("secret-in\n")})
	if string(res.Stdout) != "secret-in\n" {
		t.Fatalf("stdin: %q", res.Stdout)
	}
	res, _ = c.Run(ctx, "cat", RunOptions{Stdin: make([]byte, 100), MaxOutput: 10})
	if len(res.Stdout) != 10 || !res.Truncated {
		t.Fatalf("truncation: %d %v", len(res.Stdout), res.Truncated)
	}

	// A different pinned key is a HostKeyError, never retryable.
	other := sshtest.NewEd25519Signer(t)
	_, err = d.Dial(ctx, target(t, srv, "h1", client, other.PublicKey()))
	var oe *model.OpError
	if !errors.As(err, &oe) || oe.Class != model.ClassHostKey || oe.Retryable {
		t.Fatalf("mismatch: %v", err)
	}
	var mm *HostKeyMismatchError
	if !errors.As(err, &mm) || ssh.FingerprintSHA256(mm.Presented) != ssh.FingerprintSHA256(hostKey.PublicKey()) {
		t.Fatalf("mismatch details: %v", err)
	}
	// No pinned key at all is refused.
	if _, err := d.Dial(ctx, target(t, srv, "h1", client, nil)); model.ClassOf(err) != model.ClassHostKey {
		t.Fatalf("unpinned: %v", err)
	}
}

func TestPinnedAlgorithmSelectsKeyType(t *testing.T) {
	ed := sshtest.NewEd25519Signer(t)
	rsaKey := sshtest.NewRSASigner(t)
	srv := sshtest.Start(t, ed, rsaKey) // offers both
	client := sshtest.NewEd25519Signer(t)
	srv.Authorize(client.PublicKey())
	d := &Dialer{}
	for _, pinned := range []ssh.PublicKey{ed.PublicKey(), rsaKey.PublicKey()} {
		c, err := d.Dial(context.Background(), target(t, srv, "h", client, pinned))
		if err != nil {
			t.Fatalf("pinned %s: %v", pinned.Type(), err)
		}
		c.Close()
	}
}

func TestAuthFailureClassified(t *testing.T) {
	hostKey := sshtest.NewEd25519Signer(t)
	srv := sshtest.Start(t, hostKey)
	tg := target(t, srv, "h", sshtest.NewEd25519Signer(t), hostKey.PublicKey()) // not authorized
	_, err := (&Dialer{}).Dial(context.Background(), tg)
	var oe *model.OpError
	if !errors.As(err, &oe) || oe.Class != model.ClassAuthentication || oe.Retryable {
		t.Fatalf("auth failure: %v", err)
	}
	srv.SetPassword("deploy", "pw-123")
	tg.Signer, tg.Password = nil, "pw-123"
	c, err := (&Dialer{}).Dial(context.Background(), tg)
	if err != nil {
		t.Fatalf("password auth: %v", err)
	}
	c.Close()
}

func TestConnectionRefusedClassified(t *testing.T) {
	srv := sshtest.Start(t)
	tg := target(t, srv, "h", sshtest.NewEd25519Signer(t), srv.HostKeys[0].PublicKey())
	srv.Close()
	_, err := (&Dialer{DialTimeout: 2 * time.Second}).Dial(context.Background(), tg)
	var oe *model.OpError
	if !errors.As(err, &oe) || oe.Class != model.ClassConnection || !oe.Retryable {
		t.Fatalf("refused: %v", err)
	}
}

func TestScan(t *testing.T) {
	ed := sshtest.NewEd25519Signer(t)
	rsaKey := sshtest.NewRSASigner(t)
	srv := sshtest.Start(t, ed, rsaKey)
	keys, err := (&Dialer{}).Scan(context.Background(), target(t, srv, "h", nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, k := range keys {
		got[k.Type()] = true
	}
	if len(keys) != 2 || !got[ssh.KeyAlgoED25519] || !got[ssh.KeyAlgoRSA] {
		t.Fatalf("scanned %v", got)
	}
}

func TestJumpHost(t *testing.T) {
	jumpKey, targetKey := sshtest.NewEd25519Signer(t), sshtest.NewEd25519Signer(t)
	jumpSrv, targetSrv := sshtest.Start(t, jumpKey), sshtest.Start(t, targetKey)
	client := sshtest.NewEd25519Signer(t)
	jumpSrv.Authorize(client.PublicKey())
	targetSrv.Authorize(client.PublicKey())
	targetSrv.SetExec(echoExec)
	jt := target(t, jumpSrv, "jump", client, jumpKey.PublicKey())
	tt := target(t, targetSrv, "web", client, targetKey.PublicKey())
	tt.Via = jt
	d := &Dialer{}
	ctx := context.Background()

	// Forwarding disabled on the jump host gives a clear error.
	_, err := d.Dial(ctx, tt)
	if err == nil || model.ClassOf(err) != model.ClassConnection {
		t.Fatalf("forwarding disabled: %v", err)
	}
	jumpSrv.AllowForwarding(true)
	c, err := d.Dial(ctx, tt)
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Run(ctx, "via", RunOptions{})
	c.Close()
	if err != nil || string(res.Stdout) != "ran:via" {
		t.Fatalf("run via jump: %+v %v", res, err)
	}
	// Scanning through the jump host works.
	scanT := *tt
	scanT.PinnedKey, scanT.Signer = nil, nil
	keys, err := d.Scan(ctx, &scanT)
	if err != nil || len(keys) != 1 {
		t.Fatalf("scan via jump: %v %v", keys, err)
	}
	// A wrong jump host key is reported as a HostKeyError on the jump host.
	bad := *jt
	bad.PinnedKey = sshtest.NewEd25519Signer(t).PublicKey()
	tt.Via = &bad
	_, err = d.Dial(ctx, tt)
	var oe *model.OpError
	if !errors.As(err, &oe) || oe.Class != model.ClassHostKey {
		t.Fatalf("bad jump key: %v", err)
	}
}

func TestPoolReuseAndEvict(t *testing.T) {
	hostKey := sshtest.NewEd25519Signer(t)
	srv := sshtest.Start(t, hostKey)
	client := sshtest.NewEd25519Signer(t)
	srv.Authorize(client.PublicKey())
	srv.SetExec(echoExec)
	p := NewPool(&Dialer{})
	p.MaxSessions = 2
	defer p.Close()
	ctx := context.Background()
	tg := target(t, srv, "h", client, hostKey.PublicKey())

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := p.Get(ctx, tg)
			if err != nil {
				t.Error(err)
				return
			}
			defer s.Release()
			if _, err := s.Run(ctx, "x", RunOptions{}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := srv.Connections(); n != 1 {
		t.Fatalf("pool opened %d connections, want 1", n)
	}
	// Changing settings (here: the pinned key object is the same, user changes) redials.
	tg2 := *tg
	tg2.User = "deploy2"
	srv.Authorize(client.PublicKey())
	s, err := p.Get(ctx, &tg2)
	if err != nil {
		t.Fatal(err)
	}
	s.Release()
	// The test server counts a connection after its side of the handshake
	// returns, which can be just after the client's.
	for end := time.Now().Add(2 * time.Second); srv.Connections() < 2 && time.Now().Before(end); {
		time.Sleep(5 * time.Millisecond)
	}
	if n := srv.Connections(); n != 2 {
		t.Fatalf("settings change: %d connections, want 2", n)
	}
	p.Evict("h")
	if p.Len() != 0 {
		t.Fatal("evict left connections")
	}
}

func TestPoolSharesJumpConnection(t *testing.T) {
	jumpKey := sshtest.NewEd25519Signer(t)
	jumpSrv := sshtest.Start(t, jumpKey)
	jumpSrv.AllowForwarding(true)
	client := sshtest.NewEd25519Signer(t)
	jumpSrv.Authorize(client.PublicKey())
	jt := target(t, jumpSrv, "jump", client, jumpKey.PublicKey())
	p := NewPool(&Dialer{})
	defer p.Close()
	for i, id := range []string{"a", "b", "c"} {
		k := sshtest.NewEd25519Signer(t)
		srv := sshtest.Start(t, k)
		srv.Authorize(client.PublicKey())
		srv.SetExec(echoExec)
		tg := target(t, srv, id, client, k.PublicKey())
		tg.Via = jt
		s, err := p.Get(context.Background(), tg)
		if err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		if _, err := s.Run(context.Background(), "x", RunOptions{}); err != nil {
			t.Fatal(err)
		}
		s.Release()
	}
	if n := jumpSrv.Connections(); n != 1 {
		t.Fatalf("jump host saw %d connections, want 1 shared", n)
	}
}

func TestRunCancellation(t *testing.T) {
	hostKey := sshtest.NewEd25519Signer(t)
	srv := sshtest.Start(t, hostKey)
	client := sshtest.NewEd25519Signer(t)
	srv.Authorize(client.PublicKey())
	block := make(chan struct{})
	defer close(block)
	srv.SetExec(func(string, []byte) (string, string, int) { <-block; return "", "", 0 })
	c, err := (&Dialer{}).Dial(context.Background(), target(t, srv, "h", client, hostKey.PublicKey()))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err = c.Run(ctx, "sleep", RunOptions{})
	if model.ClassOf(err) != model.ClassTimeout {
		t.Fatalf("cancel: %v", err)
	}
}

// blackhole forwards TCP to addr until stop is closed, then silently drops
// everything in both directions without closing: what a host that lost
// power (or a VM behind user-mode networking) looks like.
func blackhole(t *testing.T, addr string, stop <-chan struct{}) (string, int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			in, err := l.Accept()
			if err != nil {
				return
			}
			out, err := net.Dial("tcp", addr)
			if err != nil {
				in.Close()
				continue
			}
			pipe := func(dst, src net.Conn) {
				buf := make([]byte, 32<<10)
				for {
					n, err := src.Read(buf)
					if err != nil {
						return
					}
					select {
					case <-stop:
						continue // swallow
					default:
					}
					if _, err := dst.Write(buf[:n]); err != nil {
						return
					}
				}
			}
			go pipe(out, in)
			go pipe(in, out)
		}
	}()
	return "127.0.0.1", l.Addr().(*net.TCPAddr).Port
}

// A command on a connection that goes silent must still return at its
// timeout instead of hanging forever (found when a VM powered off during a
// reboot poll).
func TestRunReturnsWhenConnectionGoesSilent(t *testing.T) {
	unresponsiveGrace = 300 * time.Millisecond
	hostKey := sshtest.NewEd25519Signer(t)
	srv := sshtest.Start(t, hostKey)
	client := sshtest.NewEd25519Signer(t)
	srv.Authorize(client.PublicKey())
	block := make(chan struct{})
	defer close(block)
	srv.SetExec(func(string, []byte) (string, string, int) { <-block; return "", "", 0 })
	stop := make(chan struct{})
	h, p := srv.Host()
	ph, pp := blackhole(t, net.JoinHostPort(h, strconv.Itoa(p)), stop)
	tg := target(t, srv, "h", client, hostKey.PublicKey())
	tg.Address, tg.Port = ph, pp
	c, err := (&Dialer{}).Dial(context.Background(), tg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// The host vanishes while the command runs.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	go func() { time.Sleep(100 * time.Millisecond); close(stop) }()
	done := make(chan error, 1)
	go func() { _, err := c.Run(ctx, "sleep", RunOptions{}); done <- err }()
	select {
	case err := <-done:
		if model.ClassOf(err) != model.ClassTimeout {
			t.Fatalf("got %v, want a timeout", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run hung on a silent connection")
	}

	// Opening a session on the dead connection also gives up at the deadline.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	go func() { _, err := c.Run(ctx2, "true", RunOptions{}); done <- err }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("opening a session hung on a silent connection")
	}
}

// Cancelling a command must not close a healthy (shared) connection.
func TestCancelKeepsHealthyConnection(t *testing.T) {
	hostKey := sshtest.NewEd25519Signer(t)
	srv := sshtest.Start(t, hostKey)
	client := sshtest.NewEd25519Signer(t)
	srv.Authorize(client.PublicKey())
	srv.SetExec(func(string, []byte) (string, string, int) { return "ok", "", 0 })
	c, err := (&Dialer{}).Dial(context.Background(), target(t, srv, "h", client, hostKey.PublicKey()))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Run(ctx, "true", RunOptions{}); err == nil {
		t.Fatal("cancelled run succeeded")
	}
	if r, err := c.Run(context.Background(), "true", RunOptions{}); err != nil || string(r.Stdout) != "ok" {
		t.Fatalf("connection unusable after a cancelled command: %v", err)
	}
}

// --- connections authenticated with certificates (JIT SSH) ---

// testClock is the pool's and the certificate issuer's clock in the
// certificate tests. It stands still and moves only by advance, and it
// starts on a whole second: certificates carry their validity in whole
// seconds, so the expiry boundaries the tests probe (one second either side
// of the reuse margin) are exact however long a run takes. A clock that
// followed real time lost up to a second to that rounding, and a slow run
// (-race, load) crossed the boundary early (ARCHITECTURE-ALPHA §35 #9). The
// servers judge certificates by real time shifted to the clock's instant.
type testClock struct {
	mu   sync.Mutex
	t    time.Time
	srvs []*sshtest.Server
}

func newTestClock(srvs ...*sshtest.Server) *testClock {
	c := &testClock{t: time.Now().Truncate(time.Second), srvs: srvs}
	c.syncServers()
	return c
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
	c.syncServers()
}

func (c *testClock) syncServers() {
	off := time.Until(c.now())
	for _, s := range c.srvs {
		s.SetClockSkew(off)
	}
}

// certIssuer issues user certificates from a test CA the way sshca does: a
// fresh key for every handshake, valid from 5 minutes before now to ttl
// after it.
type certIssuer struct {
	ca    ssh.Signer
	ttl   time.Duration
	clock func() time.Time
	mu    sync.Mutex
	certs []*ssh.Certificate
}

func (ci *certIssuer) issue(context.Context) (ssh.Signer, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	key, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	ci.mu.Lock()
	defer ci.mu.Unlock()
	now := ci.clock()
	cert := &ssh.Certificate{Key: key.PublicKey(), Serial: uint64(len(ci.certs) + 1), CertType: ssh.UserCert,
		KeyId: "drool:test", ValidPrincipals: []string{"deploy"},
		ValidAfter: uint64(now.Add(-5 * time.Minute).Unix()), ValidBefore: uint64(now.Add(ci.ttl).Unix())}
	if err := cert.SignCert(rand.Reader, ci.ca); err != nil {
		return nil, err
	}
	s, err := ssh.NewCertSigner(cert, key)
	if err != nil {
		return nil, err
	}
	ci.certs = append(ci.certs, cert)
	return s, nil
}

func (ci *certIssuer) issued() []*ssh.Certificate {
	ci.mu.Lock()
	defer ci.mu.Unlock()
	return append([]*ssh.Certificate(nil), ci.certs...)
}

func certTarget(t *testing.T, srv *sshtest.Server, id string, ci *certIssuer, pinned ssh.PublicKey) *Target {
	tg := target(t, srv, id, nil, pinned)
	tg.Certificate, tg.CertIdentity = ci.issue, "test-ca|deploy"
	return tg
}

// pooledConn is the pool's current connection for id (nil: none).
func pooledConn(p *Pool, id string) *pooled {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.conns[id]
}

func runOn(t *testing.T, p *Pool, tg *Target) {
	t.Helper()
	s, err := p.Get(context.Background(), tg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Release()
	if _, err := s.Run(context.Background(), "x", RunOptions{}); err != nil {
		t.Fatal(err)
	}
}

func closedSoon(pc *pooled) bool {
	select {
	case <-pc.dead:
		return true
	case <-time.After(5 * time.Second):
		return false
	}
}

func TestPoolCertificateConnectionEndsWithItsCertificate(t *testing.T) {
	hostKey, ca := sshtest.NewEd25519Signer(t), sshtest.NewEd25519Signer(t)
	srv := sshtest.Start(t, hostKey)
	srv.SetExec(echoExec)
	srv.TrustUserCA(ca.PublicKey())
	clock := newTestClock(srv)
	ci := &certIssuer{ca: ca, ttl: 10 * time.Minute, clock: clock.now}
	p := NewPool(&Dialer{})
	p.clock = clock.now
	defer p.Close()
	tg := certTarget(t, srv, "h", ci, hostKey.PublicKey())

	// Reused while the certificate is valid, until the margin before its end.
	runOn(t, p, tg)
	first := pooledConn(p, "h")
	certs := ci.issued()
	if want := time.Unix(int64(certs[0].ValidBefore), 0).Add(-CertReuseMargin); !first.expires.Equal(want) {
		t.Fatalf("recorded expiry %v, want the certificate's end minus the margin, %v", first.expires, want)
	}
	runOn(t, p, tg)
	clock.advance(10*time.Minute - CertReuseMargin - time.Second)
	runOn(t, p, tg)
	if pooledConn(p, "h") != first || len(ci.issued()) != 1 {
		t.Fatalf("not reused while valid: %d certificates issued", len(ci.issued()))
	}

	// Inside the margin it is never handed out again: the next lease dials,
	// with a fresh key and a newly issued certificate, and the old
	// connection is closed.
	clock.advance(2 * time.Second)
	runOn(t, p, tg)
	second := pooledConn(p, "h")
	certs = ci.issued()
	if second == first || len(certs) != 2 {
		t.Fatalf("reused after its certificate: %d certificates issued", len(certs))
	}
	if last := srv.LastCertificate(); last.Serial != 2 || string(last.Key.Marshal()) == string(certs[0].Key.Marshal()) {
		t.Fatalf("the host saw certificate %d", last.Serial)
	}
	if !closedSoon(first) {
		t.Fatal("the connection whose certificate expired is still open")
	}

	// Past the new certificate's end altogether, the same.
	clock.advance(11 * time.Minute)
	runOn(t, p, tg)
	if pooledConn(p, "h") == second || len(ci.issued()) != 3 || !closedSoon(second) {
		t.Fatalf("after the second certificate expired: %d certificates issued", len(ci.issued()))
	}
}

// A lease that had to wait for a free slot while the certificate expired
// gets a new connection, not the old one.
func TestPoolCertificateCheckedAfterWaitingForASlot(t *testing.T) {
	hostKey, ca := sshtest.NewEd25519Signer(t), sshtest.NewEd25519Signer(t)
	srv := sshtest.Start(t, hostKey)
	srv.SetExec(echoExec)
	srv.TrustUserCA(ca.PublicKey())
	clock := newTestClock(srv)
	ci := &certIssuer{ca: ca, ttl: 10 * time.Minute, clock: clock.now}
	p := NewPool(&Dialer{})
	p.clock, p.MaxSessions = clock.now, 1
	defer p.Close()
	tg := certTarget(t, srv, "h", ci, hostKey.PublicKey())

	held, err := p.Get(context.Background(), tg)
	if err != nil {
		t.Fatal(err)
	}
	first := pooledConn(p, "h")
	got := make(chan *Session, 1)
	go func() {
		s, err := p.Get(context.Background(), tg)
		if err != nil {
			t.Error(err)
		}
		got <- s
	}()
	// The second lease waits for the only slot...
	for end := time.Now().Add(5 * time.Second); ; {
		p.mu.Lock()
		refs := first.refs
		p.mu.Unlock()
		if refs == 2 {
			break
		}
		if time.Now().After(end) {
			t.Fatal("the second lease never queued for the slot")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// ...while the certificate runs out.
	clock.advance(10 * time.Minute)
	held.Release()
	s := <-got
	if s == nil {
		t.FailNow()
	}
	defer s.Release()
	if s.Client.Client == first.client || len(ci.issued()) != 2 {
		t.Fatalf("the lease got the expired connection (%d certificates issued)", len(ci.issued()))
	}
	if _, err := s.Run(context.Background(), "x", RunOptions{}); err != nil {
		t.Fatal(err)
	}
}

// Key and password connections are pooled as before: no expiry, whatever
// the time.
func TestPoolStaticCredentialsUnaffected(t *testing.T) {
	hostKey := sshtest.NewEd25519Signer(t)
	srv := sshtest.Start(t, hostKey)
	srv.SetExec(echoExec)
	client := sshtest.NewEd25519Signer(t)
	srv.Authorize(client.PublicKey())
	srv.SetPassword("deploy", "correct horse")
	clock := newTestClock(srv)
	p := NewPool(&Dialer{})
	p.clock = clock.now
	defer p.Close()
	byKey := target(t, srv, "key", client, hostKey.PublicKey())
	byPassword := target(t, srv, "password", nil, hostKey.PublicKey())
	byPassword.Password = "correct horse"

	runOn(t, p, byKey)
	runOn(t, p, byPassword)
	k, pw := pooledConn(p, "key"), pooledConn(p, "password")
	if !k.expires.IsZero() || !pw.expires.IsZero() {
		t.Fatalf("static connections got an expiry: %v %v", k.expires, pw.expires)
	}
	for i := 0; i < 3; i++ {
		clock.advance(2 * time.Hour)
		runOn(t, p, byKey)
		runOn(t, p, byPassword)
	}
	if pooledConn(p, "key") != k || pooledConn(p, "password") != pw {
		t.Fatal("a static connection was replaced")
	}
	if n := srv.Connections(); n != 2 {
		t.Fatalf("%d connections, want 2", n)
	}
}

func TestPoolCertificateJumpPaths(t *testing.T) {
	ca, client := sshtest.NewEd25519Signer(t), sshtest.NewEd25519Signer(t)
	jumpKey, innerKey := sshtest.NewEd25519Signer(t), sshtest.NewEd25519Signer(t)
	jumpSrv, inner := sshtest.Start(t, jumpKey), sshtest.Start(t, innerKey)
	jumpSrv.AllowForwarding(true)
	inner.SetExec(echoExec)
	for _, s := range []*sshtest.Server{jumpSrv, inner} {
		s.Authorize(client.PublicKey())
		s.TrustUserCA(ca.PublicKey())
	}
	clock := newTestClock(jumpSrv, inner)
	ci := &certIssuer{ca: ca, ttl: 10 * time.Minute, clock: clock.now}
	p := NewPool(&Dialer{})
	p.clock = clock.now
	defer p.Close()

	// A key-authenticated host behind a jump host in certificate mode: the
	// tunnel runs over the certificate connection, so nothing on that path
	// is handed out once the jump host's certificate is about to expire.
	certJump := certTarget(t, jumpSrv, "jump", ci, jumpKey.PublicKey())
	keyInner := target(t, inner, "inner", client, innerKey.PublicKey())
	keyInner.Via = certJump
	runOn(t, p, keyInner)
	runOn(t, p, keyInner)
	jump1, inner1 := pooledConn(p, "jump"), pooledConn(p, "inner")
	if !inner1.expires.Equal(jump1.expires) || jump1.expires.IsZero() {
		t.Fatalf("tunnelled connection expires %v, its certificate jump connection %v", inner1.expires, jump1.expires)
	}
	clock.advance(10*time.Minute - CertReuseMargin - time.Second)
	runOn(t, p, keyInner)
	if pooledConn(p, "jump") != jump1 || pooledConn(p, "inner") != inner1 || len(ci.issued()) != 1 {
		t.Fatal("the path was not reused while the jump certificate was valid")
	}
	clock.advance(2 * time.Second)
	runOn(t, p, keyInner)
	if pooledConn(p, "jump") == jump1 || pooledConn(p, "inner") == inner1 || len(ci.issued()) != 2 {
		t.Fatalf("the path outlived the jump host's certificate (%d certificates issued)", len(ci.issued()))
	}
	if !closedSoon(inner1) || !closedSoon(jump1) {
		t.Fatal("the expired path is still open")
	}
	if last := jumpSrv.LastCertificate(); last.Serial != 2 {
		t.Fatalf("the jump host saw certificate %d, want the new one", last.Serial)
	}

	// A certificate host behind a key-authenticated jump host: its own
	// certificate decides, and the jump connection is kept as it is.
	keyJump := target(t, jumpSrv, "jump2", client, jumpKey.PublicKey())
	certInner := certTarget(t, inner, "inner2", ci, innerKey.PublicKey())
	certInner.Via = keyJump
	runOn(t, p, certInner)
	jump2, inner2 := pooledConn(p, "jump2"), pooledConn(p, "inner2")
	if !jump2.expires.IsZero() || inner2.expires.IsZero() {
		t.Fatalf("expiries: jump %v, certificate host %v", jump2.expires, inner2.expires)
	}
	clock.advance(10*time.Minute - CertReuseMargin + time.Second)
	runOn(t, p, certInner)
	if pooledConn(p, "jump2") != jump2 || pooledConn(p, "inner2") == inner2 || len(ci.issued()) != 4 {
		t.Fatalf("certificate host behind a key jump host: %d certificates issued", len(ci.issued()))
	}
	if last := inner.LastCertificate(); last.Serial != 4 {
		t.Fatalf("the host behind the jump host saw certificate %d, want the new one", last.Serial)
	}
}
