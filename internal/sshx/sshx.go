// Package sshx reaches managed hosts over SSH: dialing (optionally through
// one jump host), host key pinning, key scanning, command execution and a
// connection pool (ARCHITECTURE-ALPHA §8).
//
// sshx knows nothing about the database; callers resolve a Target with
// decrypted credentials and the pinned host key.
package sshx

import (
	"bytes"
	"context"
	"drool/internal/hostexec"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"drool/internal/model"
)

// Target is everything needed to reach one host.
type Target struct {
	HostID  string
	Name    string
	Address string
	Port    int
	User    string

	Signer   ssh.Signer // key authentication (preferred)
	Password string     // password authentication

	// Certificate authenticates with a short-lived OpenSSH user
	// certificate instead (JIT SSH, M16): it is called once per connection,
	// during the handshake, and returns a signer over a fresh key whose
	// certificate the CA just issued. The key exists only in memory and
	// only as long as the handshake needs it. CertIdentity names what the
	// certificate stands for (CA and principal), so pooled connections are
	// keyed by it rather than by the throwaway key.
	Certificate  func(ctx context.Context) (ssh.Signer, error)
	CertIdentity string
	// CertHint explains, when a host refuses the certificate, what it has
	// to accept (CA, principal, validity window).
	CertHint string

	// PinnedKey must match the host's key. Nil is only valid for Scan.
	PinnedKey ssh.PublicKey

	// Via is the jump host, if any (at most one hop in the alpha).
	Via *Target
}

func (t *Target) addr() string { return net.JoinHostPort(t.Address, strconv.Itoa(t.Port)) }

func (t *Target) label() string {
	if t.Name != "" {
		return t.Name
	}
	return t.addr()
}

// Timeouts.
const (
	DefaultDialTimeout      = 10 * time.Second
	DefaultHandshakeTimeout = 15 * time.Second
)

// Dialer opens new connections. The zero value is usable.
type Dialer struct {
	DialTimeout      time.Duration
	HandshakeTimeout time.Duration
	// NetDial overrides TCP dialing (tests).
	NetDial func(ctx context.Context, network, addr string) (net.Conn, error)
}

func (d *Dialer) dialTimeout() time.Duration {
	if d.DialTimeout > 0 {
		return d.DialTimeout
	}
	return DefaultDialTimeout
}

func (d *Dialer) handshakeTimeout() time.Duration {
	if d.HandshakeTimeout > 0 {
		return d.HandshakeTimeout
	}
	return DefaultHandshakeTimeout
}

func (d *Dialer) netDial(ctx context.Context, addr string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, d.dialTimeout())
	defer cancel()
	if d.NetDial != nil {
		return d.NetDial(ctx, "tcp", addr)
	}
	var nd net.Dialer
	return nd.DialContext(ctx, "tcp", addr)
}

// Client is an authenticated SSH connection, possibly tunnelled through a
// jump host connection that it owns.
type Client struct {
	*ssh.Client
	jump *ssh.Client // closed with the client when owned
}

// Close closes the connection and any jump connection it owns.
func (c *Client) Close() error {
	err := c.Client.Close()
	if c.jump != nil {
		c.jump.Close()
	}
	return err
}

// certIssueError: DROOL couldn't issue the certificate (no usable CA): a
// configuration problem on DROOL's side, not the host refusing it.
type certIssueError struct{ err error }

func (e *certIssueError) Error() string { return "issue SSH certificate: " + e.err.Error() }
func (e *certIssueError) Unwrap() error { return e.err }

// HostKeyMismatchError means the presented key differs from the pinned one.
type HostKeyMismatchError struct {
	Host      string
	Expected  string // fingerprint
	Presented ssh.PublicKey
}

func (e *HostKeyMismatchError) Error() string {
	return fmt.Sprintf("host key for %s changed: expected %s, got %s",
		e.Host, e.Expected, ssh.FingerprintSHA256(e.Presented))
}

// HostKeyAlgorithms returns the handshake algorithms that select a key of
// the given type, so a server offering several key types presents the
// pinned one (ARCHITECTURE-ALPHA §8.2).
func HostKeyAlgorithms(keyType string) []string {
	switch keyType {
	case ssh.KeyAlgoRSA:
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	default:
		return []string{keyType}
	}
}

// certExpiry is when the certificate a connection authenticated with stops
// being valid: the earliest ValidBefore of those issued during its
// handshake. One whose end can't be read counts as expired already.
type certExpiry struct {
	mu sync.Mutex
	at time.Time
}

func (e *certExpiry) note(s ssh.Signer) {
	at := time.Unix(0, 0)
	if c, ok := s.PublicKey().(*ssh.Certificate); ok && c.ValidBefore != ssh.CertTimeInfinity && c.ValidBefore <= math.MaxInt64 {
		at = time.Unix(int64(c.ValidBefore), 0)
	}
	e.mu.Lock()
	if e.at.IsZero() || at.Before(e.at) {
		e.at = at
	}
	e.mu.Unlock()
}

func (e *certExpiry) get() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.at
}

func clientConfig(ctx context.Context, t *Target, hostKeyCB ssh.HostKeyCallback, algos []string, timeout time.Duration, exp *certExpiry) *ssh.ClientConfig {
	var auth []ssh.AuthMethod
	if t.Certificate != nil {
		// Issued when the server asks, never before: a certificate that is
		// minted is used straight away.
		auth = append(auth, ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
			s, err := t.Certificate(ctx)
			if err != nil {
				return nil, &certIssueError{err}
			}
			exp.note(s)
			return []ssh.Signer{s}, nil
		}))
	}
	if t.Signer != nil {
		auth = append(auth, ssh.PublicKeys(t.Signer))
	}
	if t.Password != "" {
		pw := t.Password
		auth = append(auth, ssh.Password(pw), ssh.KeyboardInteractive(
			func(_, _ string, questions []string, _ []bool) ([]string, error) {
				// Answer password prompts only; anything else fails authentication.
				answers := make([]string, len(questions))
				for i := range questions {
					answers[i] = pw
				}
				return answers, nil
			}))
	}
	return &ssh.ClientConfig{
		User:              t.User,
		Auth:              auth,
		HostKeyCallback:   hostKeyCB,
		HostKeyAlgorithms: algos,
		Timeout:           timeout,
		ClientVersion:     "SSH-2.0-drool",
	}
}

func pinnedCallback(t *Target) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if t.PinnedKey == nil {
			return model.Errorf(model.ClassHostKey, false, "no pinned host key for %s; approve its host key first", t.label())
		}
		if !bytes.Equal(key.Marshal(), t.PinnedKey.Marshal()) {
			return &HostKeyMismatchError{Host: t.label(), Expected: ssh.FingerprintSHA256(t.PinnedKey), Presented: key}
		}
		return nil
	}
}

// handshake runs the SSH handshake over conn with a deadline.
func (d *Dialer) handshake(conn net.Conn, t *Target, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	conn.SetDeadline(time.Now().Add(d.handshakeTimeout()))
	c, chans, reqs, err := ssh.NewClientConn(conn, t.addr(), cfg)
	if err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetDeadline(time.Time{})
	return ssh.NewClient(c, chans, reqs), nil
}

// connTo opens a raw connection to t, through jump if given.
func (d *Dialer) connTo(ctx context.Context, t *Target, jump *ssh.Client) (net.Conn, error) {
	if jump == nil {
		return d.netDial(ctx, t.addr())
	}
	type result struct {
		c   net.Conn
		err error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := jump.Dial("tcp", t.addr())
		ch <- result{c, err}
	}()
	select {
	case r := <-ch:
		return r.c, r.err
	case <-ctx.Done():
		go func() {
			if r := <-ch; r.c != nil {
				r.c.Close()
			}
		}()
		return nil, ctx.Err()
	case <-time.After(d.dialTimeout()):
		go func() {
			if r := <-ch; r.c != nil {
				r.c.Close()
			}
		}()
		return nil, fmt.Errorf("dial %s via jump host: timeout", t.addr())
	}
}

// dialOne authenticates to t (no jump handling) over an optional jump
// client. For a certificate target it also returns when the certificate
// that authenticated the connection expires (zero for other targets).
func (d *Dialer) dialOne(ctx context.Context, t *Target, jump *ssh.Client) (*ssh.Client, time.Time, error) {
	conn, err := d.connTo(ctx, t, jump)
	if err != nil {
		return nil, time.Time{}, classify(t, err)
	}
	var algos []string
	if t.PinnedKey != nil {
		algos = HostKeyAlgorithms(t.PinnedKey.Type())
	}
	var exp certExpiry
	c, err := d.handshake(conn, t, clientConfig(ctx, t, pinnedCallback(t), algos, d.handshakeTimeout(), &exp))
	if err != nil {
		return nil, time.Time{}, classify(t, err)
	}
	notAfter := exp.get()
	if t.Certificate != nil && notAfter.IsZero() {
		notAfter = time.Unix(0, 0) // authenticated without a certificate DROOL saw: never reused
	}
	return c, notAfter, nil
}

// Dial opens a new, unpooled connection to t, through its jump host if any.
// Each call dials every hop from scratch, so it is suitable for verifying
// that a management path works (ARCHITECTURE-ALPHA §8.3, DialFresh).
func (d *Dialer) Dial(ctx context.Context, t *Target) (*Client, error) {
	var jump *ssh.Client
	if t.Via != nil {
		j, _, err := d.dialOne(ctx, t.Via, nil)
		if err != nil {
			return nil, viaError(t.Via, err)
		}
		jump = j
	}
	c, _, err := d.dialOne(ctx, t, jump)
	if err != nil {
		if jump != nil {
			jump.Close()
		}
		return nil, err
	}
	return &Client{Client: c, jump: jump}, nil
}

// scanAlgorithmGroups are tried one handshake at a time to collect each key
// type a server offers.
var scanAlgorithmGroups = [][]string{
	{ssh.KeyAlgoED25519},
	{ssh.KeyAlgoECDSA256},
	{ssh.KeyAlgoECDSA384},
	{ssh.KeyAlgoECDSA521},
	{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA},
}

var errScanDone = errors.New("sshx: scan complete")

// Scan collects the host keys t offers without authenticating. A jump host,
// if any, must already be pinned.
func (d *Dialer) Scan(ctx context.Context, t *Target) ([]ssh.PublicKey, error) {
	var jump *ssh.Client
	if t.Via != nil {
		j, _, err := d.dialOne(ctx, t.Via, nil)
		if err != nil {
			return nil, viaError(t.Via, err)
		}
		jump = j
		defer jump.Close()
	}
	var keys []ssh.PublicKey
	var firstErr error
	for _, algos := range scanAlgorithmGroups {
		var got ssh.PublicKey
		conn, err := d.connTo(ctx, t, jump)
		if err != nil {
			return nil, classify(t, err) // the host isn't reachable at all
		}
		cfg := &ssh.ClientConfig{
			User: "drool-scan",
			HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
				got = key
				return errScanDone
			},
			HostKeyAlgorithms: algos,
			Timeout:           d.handshakeTimeout(),
			ClientVersion:     "SSH-2.0-drool",
		}
		_, err = d.handshake(conn, t, cfg)
		if got != nil {
			keys = append(keys, got)
			continue
		}
		if err != nil && firstErr == nil && !isNoCommonAlgorithm(err) {
			firstErr = err
		}
	}
	if len(keys) == 0 {
		if firstErr == nil {
			firstErr = errors.New("server offered no supported host key types")
		}
		return nil, classify(t, firstErr)
	}
	return keys, nil
}

func isNoCommonAlgorithm(err error) bool {
	return strings.Contains(err.Error(), "no common algorithm")
}

func viaError(via *Target, err error) error {
	oe := model.AsOpError(err)
	return &model.OpError{Class: oe.Class, Retryable: oe.Retryable, Hint: oe.Hint, Err: err,
		Message: "via jump host " + via.label() + ": " + strings.TrimPrefix(oe.Error(), string(oe.Class)+": ")}
}

// classify maps dial and handshake errors to error classes.
func classify(t *Target, err error) error {
	var oe *model.OpError
	if errors.As(err, &oe) {
		return err
	}
	var mm *HostKeyMismatchError
	if errors.As(err, &mm) {
		return &model.OpError{Class: model.ClassHostKey, Err: err, Message: mm.Error(),
			Hint: "If the host was reinstalled or its keys were rotated, check the new fingerprint on the host itself (ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub) and approve it on the host page. Otherwise treat this as a possible attack: DROOL will not connect until a key is approved."}
	}
	var ce *certIssueError
	if errors.As(err, &ce) {
		return &model.OpError{Class: model.ClassConfiguration, Err: err,
			Message: fmt.Sprintf("DROOL couldn't issue an SSH certificate for %s: %v", t.label(), ce.err),
			Hint:    "Check the SSH certificate authority under Credentials. A host in certificate mode is never reached with another key automatically."}
	}
	msg := err.Error()
	var ne net.Error
	switch {
	case strings.Contains(msg, "unable to authenticate") && t.Certificate != nil:
		return &model.OpError{Class: model.ClassAuthentication, Err: err,
			Message: fmt.Sprintf("%s refused DROOL's SSH certificate for %q", t.label(), t.User),
			Hint:    "The host must trust DROOL's certificate authority (TrustedUserCAKeys) and accept the certificate for this user. " + t.CertHint}
	case strings.Contains(msg, "unable to authenticate"):
		return &model.OpError{Class: model.ClassAuthentication, Err: err,
			Message: fmt.Sprintf("authentication as %q failed on %s", t.User, t.label()),
			Hint:    "Check the SSH user, and that the credential's public key is in that user's ~/.ssh/authorized_keys on the host (Credentials shows a one-line install command for DROOL's own key)."}
	case errors.As(err, &ne) && ne.Timeout(), strings.Contains(msg, "timeout"):
		return &model.OpError{Class: model.ClassConnection, Retryable: true, Err: err,
			Message: fmt.Sprintf("connection to %s timed out", t.addr()),
			Hint:    "Check that the host is up, the address and port are right, and no firewall drops SSH from the controller."}
	case strings.Contains(msg, "connection refused"):
		return &model.OpError{Class: model.ClassConnection, Retryable: true, Err: err,
			Message: fmt.Sprintf("connection to %s refused", t.addr()), Hint: "Is sshd running and listening on this port?"}
	case strings.Contains(msg, "no route to host"), strings.Contains(msg, "network is unreachable"), strings.Contains(msg, "host is unreachable"):
		return &model.OpError{Class: model.ClassConnection, Retryable: true, Err: err,
			Message: fmt.Sprintf("no network route to %s", t.addr()),
			Hint:    "The host is down or not reachable from the controller's network; check the address and that the host is up."}
	case strings.Contains(msg, "no such host"):
		return &model.OpError{Class: model.ClassConnection, Retryable: true, Err: err,
			Message: fmt.Sprintf("cannot resolve %s", t.Address),
			Hint:    "Check the host name, or use an IP address."}
	case strings.Contains(msg, "administratively prohibited"):
		return &model.OpError{Class: model.ClassConnection, Err: err,
			Message: "the jump host refused to forward the connection", Hint: "The jump host's sshd may have AllowTcpForwarding disabled."}
	}
	return &model.OpError{Class: model.ClassConnection, Retryable: true, Err: err,
		Message: fmt.Sprintf("connection to %s failed: %v", t.addr(), err),
		Hint:    "Check that the host is up and that SSH is reachable from the controller."}
}

// RunResult is the outcome of a command.
type RunResult = hostexec.RunResult

// RunOptions control Run.
type RunOptions = hostexec.RunOptions

// Run executes cmd in a new session and waits for it. Cancelling ctx closes
// the session. A non-zero exit status is reported in ExitCode, not as an error.
// unresponsiveGrace is how long a connection may take to acknowledge a
// closed session after a timeout before it is treated as dead.
var unresponsiveGrace = 5 * time.Second

// openSession opens an SSH session; a variable so tests can make it never
// answer.
var openSession = (*ssh.Client).NewSession

// newSession opens a session, giving up when ctx ends. A host that vanished
// without closing TCP (power loss, a VM's network gone) never answers, and
// fresh connections have no keepalive to notice; closing the connection is
// the only way to unblock.
func (c *Client) newSession(ctx context.Context) (*ssh.Session, error) {
	type res struct {
		s   *ssh.Session
		err error
	}
	ch := make(chan res, 1)
	go func() { s, err := openSession(c.Client); ch <- res{s, err} }()
	select {
	case r := <-ch:
		return r.s, r.err
	case <-ctx.Done():
		// A healthy connection answers promptly; only a silent one is closed
		// (a cancelled request must not tear down a shared connection).
		select {
		case r := <-ch:
			if r.s != nil {
				r.s.Close()
			}
		case <-time.After(unresponsiveGrace):
			c.Close()
			// Closing normally ends the request, but x/crypto never answers
			// one made just as the connection ended (its channel is added
			// after the others were dropped): don't wait for it. A session
			// that still arrives is closed.
			go func() {
				if r := <-ch; r.s != nil {
					r.s.Close()
				}
			}()
		}
		return nil, ctx.Err()
	}
}

func (c *Client) Run(ctx context.Context, cmd string, opt RunOptions) (*RunResult, error) {
	sess, err := c.newSession(ctx)
	if ctx.Err() != nil {
		return nil, &model.OpError{Class: model.ClassTimeout, Retryable: true, Err: ctx.Err(), Message: "command timed out or was cancelled"}
	}
	if err != nil {
		return nil, &model.OpError{Class: model.ClassConnection, Retryable: true, Err: err, Message: "open session: " + err.Error()}
	}
	defer sess.Close()
	limit := opt.MaxOutput
	if limit <= 0 {
		limit = 1 << 20
	}
	stdout, stderr := &capWriter{max: limit}, &capWriter{max: limit}
	sess.Stdout, sess.Stderr = stdout, stderr
	if opt.Stdin != nil {
		sess.Stdin = bytes.NewReader(opt.Stdin)
	}
	if opt.PTY {
		if err := sess.RequestPty("xterm", 40, 200, ssh.TerminalModes{ssh.ECHO: 0}); err != nil {
			return nil, &model.OpError{Class: model.ClassConnection, Err: err, Message: "request pty: " + err.Error()}
		}
	}
	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()
	select {
	case err = <-done:
	case <-ctx.Done():
		sess.Close()
		select {
		case <-done:
		case <-time.After(unresponsiveGrace):
			// The host doesn't even acknowledge the close: the connection is dead.
			c.Close()
			<-done
		}
		return nil, &model.OpError{Class: model.ClassTimeout, Retryable: true, Err: ctx.Err(), Message: "command timed out or was cancelled"}
	}
	res := &RunResult{Stdout: stdout.buf.Bytes(), Stderr: stderr.buf.Bytes(), Truncated: stdout.truncated || stderr.truncated}
	var exitErr *ssh.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitStatus()
	case errors.Is(err, io.EOF), strings.Contains(err.Error(), "exited without exit status"):
		return nil, ErrLostDuringCommand(err)
	default:
		return nil, &model.OpError{Class: model.ClassConnection, Retryable: true, Err: err, Message: "command failed: " + err.Error()}
	}
	return res, nil
}

type capWriter struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	if room := w.max - w.buf.Len(); room > 0 {
		if len(p) > room {
			w.buf.Write(p[:room])
			w.truncated = true
		} else {
			w.buf.Write(p)
		}
	} else if len(p) > 0 {
		w.truncated = true
	}
	return len(p), nil
}

// Running is a command started with Start.
type Running struct {
	sess *ssh.Session
	done chan struct{}
	err  error
}

// Start runs cmd in a new session, streaming output to stdout and stderr.
// It returns once the command has started. With pty, both streams arrive on
// stdout. ctx bounds only the start, as in Run: a connection that goes
// silent while the session opens or the command starts is closed after the
// grace period. Once the command was asked to start, a failure is a
// hostexec.MaybeStartedError: it may be running.
func (c *Client) Start(ctx context.Context, cmd string, stdin []byte, stdout, stderr io.Writer, pty bool) (*Running, error) {
	sess, err := c.newSession(ctx)
	if ctx.Err() != nil {
		return nil, &model.OpError{Class: model.ClassTimeout, Retryable: true, Err: ctx.Err(), Message: "starting the command timed out or was cancelled"}
	}
	if err != nil {
		return nil, &model.OpError{Class: model.ClassConnection, Retryable: true, Err: err, Message: "open session: " + err.Error()}
	}
	sess.Stdout, sess.Stderr = stdout, stderr
	sess.Stdin = bytes.NewReader(stdin) // empty stdin still sends EOF
	type res struct {
		err  error
		exec bool // the exec request was sent
	}
	ch := make(chan res, 1)
	go func() {
		if pty {
			if err := sess.RequestPty("xterm", 40, 200, ssh.TerminalModes{ssh.ECHO: 0}); err != nil {
				ch <- res{err: err}
				return
			}
		}
		ch <- res{err: sess.Start(cmd), exec: true}
	}()
	var r res
	select {
	case r = <-ch:
	case <-ctx.Done():
		select {
		case r = <-ch: // a healthy connection answers; a started command is the caller's to end
		case <-time.After(unresponsiveGrace):
			// The host doesn't answer: the connection is dead. Closing it
			// ends the requests (the session's channel already exists).
			c.Close()
			<-ch
			sess.Close()
			return nil, &hostexec.MaybeStartedError{Err: ErrLostDuringCommand(ctx.Err())}
		}
	}
	switch {
	case r.err == nil:
	case !r.exec:
		sess.Close()
		return nil, &model.OpError{Class: model.ClassConnection, Err: r.err, Message: "request pty: " + r.err.Error()}
	case strings.HasPrefix(r.err.Error(), "ssh: command ") && strings.HasSuffix(r.err.Error(), " failed"):
		// The host refused the request: nothing ran.
		sess.Close()
		return nil, &model.OpError{Class: model.ClassConnection, Retryable: true, Err: r.err, Message: "start command: " + r.err.Error()}
	default:
		// The connection failed after the request was sent.
		sess.Close()
		return nil, &hostexec.MaybeStartedError{Err: ErrLostDuringCommand(r.err)}
	}
	run := &Running{sess: sess, done: make(chan struct{})}
	go func() {
		run.err = sess.Wait()
		close(run.done)
	}()
	return run, nil
}

// Done is closed when the command finishes.
func (r *Running) Done() <-chan struct{} { return r.done }

// ExitCode returns the exit status after Done. A connection failure is
// returned as an error; -1 means the command ended without an exit status
// (e.g. killed by a signal).
func (r *Running) ExitCode() (int, error) {
	<-r.done
	var exitErr *ssh.ExitError
	switch {
	case r.err == nil:
		return 0, nil
	case errors.As(r.err, &exitErr):
		if exitErr.ExitStatus() == 0 && exitErr.Signal() != "" {
			return -1, &model.OpError{Class: model.ClassExecution, Err: r.err,
				Message: "the command was killed by signal " + exitErr.Signal()}
		}
		return exitErr.ExitStatus(), nil
	}
	// No exit status: the session or connection went away mid-command.
	return -1, ErrLostDuringCommand(r.err)
}

// ErrLostDuringCommand reports a connection that dropped after a command
// started. The outcome is unknown: the engine retries only idempotent
// operations (ARCHITECTURE-ALPHA §11.4).
func ErrLostDuringCommand(err error) error {
	return &model.OpError{Class: model.ClassConnection, Err: err,
		Message: "the connection to the host was lost while the command was running; it may have finished, failed, or still be running",
		Hint:    "The outcome is unknown. Check the host (for example with a read-only command) before running this again."}
}

// Close closes the session (the remote process may keep running; see the
// driver's cancellation).
func (r *Running) Close() { r.sess.Close() }
