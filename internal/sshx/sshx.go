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
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"fleetmgr/internal/model"
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

func clientConfig(t *Target, hostKeyCB ssh.HostKeyCallback, algos []string, timeout time.Duration) *ssh.ClientConfig {
	var auth []ssh.AuthMethod
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
		ClientVersion:     "SSH-2.0-fleetmgr",
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

// dialOne authenticates to t (no jump handling) over an optional jump client.
func (d *Dialer) dialOne(ctx context.Context, t *Target, jump *ssh.Client) (*ssh.Client, error) {
	conn, err := d.connTo(ctx, t, jump)
	if err != nil {
		return nil, classify(t, err)
	}
	var algos []string
	if t.PinnedKey != nil {
		algos = HostKeyAlgorithms(t.PinnedKey.Type())
	}
	c, err := d.handshake(conn, t, clientConfig(t, pinnedCallback(t), algos, d.handshakeTimeout()))
	if err != nil {
		return nil, classify(t, err)
	}
	return c, nil
}

// Dial opens a new, unpooled connection to t, through its jump host if any.
// Each call dials every hop from scratch, so it is suitable for verifying
// that a management path works (ARCHITECTURE-ALPHA §8.3, DialFresh).
func (d *Dialer) Dial(ctx context.Context, t *Target) (*Client, error) {
	var jump *ssh.Client
	if t.Via != nil {
		j, err := d.dialOne(ctx, t.Via, nil)
		if err != nil {
			return nil, viaError(t.Via, err)
		}
		jump = j
	}
	c, err := d.dialOne(ctx, t, jump)
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
		j, err := d.dialOne(ctx, t.Via, nil)
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
			User: "fleetmgr-scan",
			HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
				got = key
				return errScanDone
			},
			HostKeyAlgorithms: algos,
			Timeout:           d.handshakeTimeout(),
			ClientVersion:     "SSH-2.0-fleetmgr",
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
			Hint: "If the host was reinstalled or its keys rotated, review and approve the new key. Otherwise treat this as a possible attack."}
	}
	msg := err.Error()
	var ne net.Error
	switch {
	case strings.Contains(msg, "unable to authenticate"):
		return &model.OpError{Class: model.ClassAuthentication, Err: err,
			Message: fmt.Sprintf("authentication as %q failed on %s", t.User, t.label()),
			Hint:    "Check the SSH user and that the credential's public key is in ~/.ssh/authorized_keys."}
	case errors.As(err, &ne) && ne.Timeout(), strings.Contains(msg, "timeout"):
		return &model.OpError{Class: model.ClassConnection, Retryable: true, Err: err,
			Message: fmt.Sprintf("connection to %s timed out", t.addr())}
	case strings.Contains(msg, "connection refused"):
		return &model.OpError{Class: model.ClassConnection, Retryable: true, Err: err,
			Message: fmt.Sprintf("connection to %s refused", t.addr()), Hint: "Is sshd running and listening on this port?"}
	case strings.Contains(msg, "no such host"):
		return &model.OpError{Class: model.ClassConnection, Retryable: true, Err: err,
			Message: fmt.Sprintf("cannot resolve %s", t.Address)}
	case strings.Contains(msg, "administratively prohibited"):
		return &model.OpError{Class: model.ClassConnection, Err: err,
			Message: "the jump host refused to forward the connection", Hint: "The jump host's sshd may have AllowTcpForwarding disabled."}
	}
	return &model.OpError{Class: model.ClassConnection, Retryable: true, Err: err,
		Message: fmt.Sprintf("connection to %s failed: %v", t.addr(), err)}
}

// RunResult is the outcome of a command.
type RunResult struct {
	Stdout    []byte
	Stderr    []byte
	ExitCode  int
	Truncated bool
}

// RunOptions control Run.
type RunOptions struct {
	Stdin     []byte
	PTY       bool
	MaxOutput int // per stream; 0 = 1 MiB
}

// Run executes cmd in a new session and waits for it. Cancelling ctx closes
// the session. A non-zero exit status is reported in ExitCode, not as an error.
func (c *Client) Run(ctx context.Context, cmd string, opt RunOptions) (*RunResult, error) {
	sess, err := c.NewSession()
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
		<-done
		return nil, &model.OpError{Class: model.ClassTimeout, Retryable: true, Err: ctx.Err(), Message: "command timed out or was cancelled"}
	}
	res := &RunResult{Stdout: stdout.buf.Bytes(), Stderr: stderr.buf.Bytes(), Truncated: stdout.truncated || stderr.truncated}
	var exitErr *ssh.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitStatus()
	case errors.Is(err, io.EOF), strings.Contains(err.Error(), "exited without exit status"):
		res.ExitCode = -1
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
