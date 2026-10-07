package sshx

import (
	"context"
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
