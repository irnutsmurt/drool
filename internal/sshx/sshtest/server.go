// Package sshtest provides an in-process SSH server for tests.
package sshtest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// ExecFunc handles an exec request.
type ExecFunc func(cmd string, stdin []byte) (stdout, stderr string, exit int)

// Server is a minimal SSH server.
type Server struct {
	Addr     string
	HostKeys []ssh.Signer

	mu           sync.Mutex
	authorized   map[string]bool // marshaled public keys
	passwords    map[string]string
	exec         ExecFunc
	allowForward bool
	commands     []string

	conns  atomic.Int64
	ln     net.Listener
	active map[net.Conn]bool
}

// NewEd25519Signer returns a fresh ed25519 signer.
func NewEd25519Signer(t testing.TB) ssh.Signer {
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// NewRSASigner returns a fresh 2048-bit RSA signer.
func NewRSASigner(t testing.TB) ssh.Signer {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Start launches a server with the given host keys. It is closed when the
// test ends.
func Start(t testing.TB, hostKeys ...ssh.Signer) *Server {
	t.Helper()
	if len(hostKeys) == 0 {
		hostKeys = []ssh.Signer{NewEd25519Signer(t)}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Addr: ln.Addr().String(), HostKeys: hostKeys, ln: ln,
		authorized: map[string]bool{}, passwords: map[string]string{}, active: map[net.Conn]bool{},
		exec: func(cmd string, _ []byte) (string, string, int) { return "", "unknown command: " + cmd, 127 },
	}
	go s.serve()
	t.Cleanup(s.Close)
	return s
}

// Host returns the listen host and port.
func (s *Server) Host() (string, int) {
	h, p, _ := net.SplitHostPort(s.Addr)
	port, _ := strconv.Atoi(p)
	return h, port
}

func (s *Server) Authorize(pub ssh.PublicKey) {
	s.mu.Lock()
	s.authorized[string(pub.Marshal())] = true
	s.mu.Unlock()
}
func (s *Server) SetPassword(user, pw string) { s.mu.Lock(); s.passwords[user] = pw; s.mu.Unlock() }
func (s *Server) SetExec(f ExecFunc)          { s.mu.Lock(); s.exec = f; s.mu.Unlock() }
func (s *Server) AllowForwarding(v bool)      { s.mu.Lock(); s.allowForward = v; s.mu.Unlock() }
func (s *Server) Connections() int64          { return s.conns.Load() }
func (s *Server) Commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}
func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ln.Close()
}

// SetHostKeys replaces the host keys used for new connections.
func (s *Server) SetHostKeys(keys ...ssh.Signer) { s.mu.Lock(); s.HostKeys = keys; s.mu.Unlock() }

func (s *Server) config() *ssh.ServerConfig {
	s.mu.Lock()
	keys := append([]ssh.Signer(nil), s.HostKeys...)
	s.mu.Unlock()
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.authorized[string(key.Marshal())] {
				return nil, nil
			}
			return nil, fmt.Errorf("unauthorized key")
		},
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if want, ok := s.passwords[c.User()]; ok && want == string(pw) {
				return nil, nil
			}
			return nil, fmt.Errorf("bad password")
		},
	}
	for _, k := range keys {
		cfg.AddHostKey(k)
	}
	return cfg
}

func (s *Server) serve() {
	s.mu.Lock()
	ln := s.ln
	s.mu.Unlock()
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.active[c] = true
		s.mu.Unlock()
		go func() {
			s.handle(c)
			s.mu.Lock()
			delete(s.active, c)
			s.mu.Unlock()
		}()
	}
}

// Restart simulates a reboot: drop every connection, stay unreachable for
// down, then listen again on the same address (optionally with new host
// keys). before runs while the server is down.
func (s *Server) Restart(down time.Duration, before func()) {
	s.mu.Lock()
	s.ln.Close()
	for c := range s.active {
		c.Close()
	}
	s.mu.Unlock()
	go func() {
		time.Sleep(down)
		if before != nil {
			before()
		}
		ln, err := net.Listen("tcp", s.Addr)
		if err != nil {
			return
		}
		s.mu.Lock()
		s.ln = ln
		s.mu.Unlock()
		s.serve()
	}()
}

func (s *Server) handle(nc net.Conn) {
	conn, chans, reqs, err := ssh.NewServerConn(nc, s.config())
	if err != nil {
		nc.Close()
		return
	}
	s.conns.Add(1)
	defer conn.Close()
	go func() {
		for r := range reqs {
			r.Reply(r.Type == "keepalive@openssh.com", nil)
		}
	}()
	for nch := range chans {
		switch nch.ChannelType() {
		case "session":
			ch, creqs, err := nch.Accept()
			if err != nil {
				continue
			}
			go s.session(ch, creqs)
		case "direct-tcpip":
			s.mu.Lock()
			allow := s.allowForward
			s.mu.Unlock()
			if !allow {
				nch.Reject(ssh.Prohibited, "administratively prohibited")
				continue
			}
			go forward(nch)
		default:
			nch.Reject(ssh.UnknownChannelType, "unsupported")
		}
	}
}

func (s *Server) session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for r := range reqs {
		switch r.Type {
		case "pty-req", "env":
			r.Reply(true, nil)
		case "exec":
			if len(r.Payload) < 4 {
				r.Reply(false, nil)
				return
			}
			n := binary.BigEndian.Uint32(r.Payload)
			cmd := string(r.Payload[4 : 4+n])
			r.Reply(true, nil)
			stdin, _ := io.ReadAll(ch)
			s.mu.Lock()
			s.commands = append(s.commands, cmd)
			f := s.exec
			s.mu.Unlock()
			out, errOut, code := f(cmd, stdin)
			io.Copy(ch, bytes.NewBufferString(out))
			io.Copy(ch.Stderr(), bytes.NewBufferString(errOut))
			status := make([]byte, 4)
			binary.BigEndian.PutUint32(status, uint32(code))
			ch.SendRequest("exit-status", false, status)
			return
		default:
			r.Reply(false, nil)
		}
	}
}

func forward(nch ssh.NewChannel) {
	var p struct {
		Host     string
		Port     uint32
		OrigHost string
		OrigPort uint32
	}
	if err := ssh.Unmarshal(nch.ExtraData(), &p); err != nil {
		nch.Reject(ssh.ConnectionFailed, "bad payload")
		return
	}
	target, err := net.Dial("tcp", net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))))
	if err != nil {
		nch.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	ch, reqs, err := nch.Accept()
	if err != nil {
		target.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	go func() { io.Copy(ch, target); ch.Close() }()
	io.Copy(target, ch)
	target.Close()
}

// ExecLocal runs commands with the local /bin/sh, so tests exercise real
// remote-execution scripts (mktemp, setsid, kill) on the test machine.
func ExecLocal(cmd string, stdin []byte) (string, string, int) {
	c := exec.Command("/bin/sh", "-c", cmd)
	c.Stdin = bytes.NewReader(stdin)
	var out, errOut bytes.Buffer
	c.Stdout, c.Stderr = &out, &errOut
	err := c.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
		if code < 0 {
			code = 128 + 15
		}
	} else if err != nil {
		return "", err.Error(), 127
	}
	return out.String(), errOut.String(), code
}
