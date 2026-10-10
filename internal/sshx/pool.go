package sshx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Pool defaults (ARCHITECTURE-ALPHA §8.3).
const (
	DefaultMaxSessions = 8 // OpenSSH MaxSessions defaults to 10
	DefaultIdleTimeout = 5 * time.Minute
	DefaultKeepalive   = 30 * time.Second
)

// Pool keeps one multiplexed connection per host. A jump host's connection
// is shared by every host behind it. Connections are replaced automatically
// when a target's connection settings change.
type Pool struct {
	Dialer      *Dialer
	MaxSessions int
	IdleTimeout time.Duration
	Keepalive   time.Duration

	mu      sync.Mutex
	conns   map[string]*pooled // by host id
	dialing map[string]*dialCall
	closed  bool
	stop    chan struct{}
	once    sync.Once
}

type pooled struct {
	key      string // connection settings fingerprint
	client   *ssh.Client
	expires  time.Time // zero: no age limit (certificate connections have one)
	sem      chan struct{}
	refs     int
	lastUsed time.Time
	dead     chan struct{}
	jumpID   string // host id of the jump connection this one rides on
}

// NewPool returns a pool using d.
func NewPool(d *Dialer) *Pool {
	return &Pool{Dialer: d, conns: map[string]*pooled{}, dialing: map[string]*dialCall{}, stop: make(chan struct{})}
}

func (p *Pool) maxSessions() int {
	if p.MaxSessions > 0 {
		return p.MaxSessions
	}
	return DefaultMaxSessions
}

func (p *Pool) startReaper() {
	p.once.Do(func() { go p.reap() })
}

// MaxCertConnAge bounds how long a connection authenticated with a
// certificate is reused: OpenSSH checks a certificate when a connection is
// authenticated, not afterwards, so DROOL dials (and issues) again instead
// of keeping such a connection for ever.
var MaxCertConnAge = time.Hour

// targetKey fingerprints everything that affects the connection.
func targetKey(t *Target) string {
	h := sha256.New()
	write := func(s string) { h.Write([]byte(s)); h.Write([]byte{0}) }
	write(t.Address)
	write(strconv.Itoa(t.Port))
	write(t.User)
	if t.Certificate != nil {
		write("cert:" + t.CertIdentity)
	}
	if t.Signer != nil {
		write(string(t.Signer.PublicKey().Marshal()))
	}
	write(t.Password)
	if t.PinnedKey != nil {
		write(string(t.PinnedKey.Marshal()))
	}
	if t.Via != nil {
		write(t.Via.HostID)
		write(targetKey(t.Via))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Session is a leased slot on a pooled connection.
type Session struct {
	*Client
	release func()
}

// Release returns the slot to the pool. Safe to call more than once.
func (s *Session) Release() { s.release() }

// Get leases a connection to t, dialing if needed. Call Release when done
// with the returned client; do not Close it.
func (p *Pool) Get(ctx context.Context, t *Target) (*Session, error) {
	p.startReaper()
	pc, err := p.acquire(ctx, t)
	if err != nil {
		return nil, err
	}
	select {
	case pc.sem <- struct{}{}:
	case <-ctx.Done():
		p.unref(pc)
		return nil, ctx.Err()
	}
	var once sync.Once
	return &Session{
		Client: &Client{Client: pc.client},
		release: func() {
			once.Do(func() {
				<-pc.sem
				p.unref(pc)
			})
		},
	}, nil
}

func (p *Pool) unref(pc *pooled) {
	p.mu.Lock()
	pc.refs--
	pc.lastUsed = time.Now()
	p.mu.Unlock()
}

// dialCall is an in-flight dial that concurrent callers wait on, so a burst
// of first requests to a host opens one connection, not many.
type dialCall struct {
	key  string
	done chan struct{}
	pc   *pooled
	err  error
}

// acquire returns a live pooled connection for t with refs incremented.
func (p *Pool) acquire(ctx context.Context, t *Target) (*pooled, error) {
	key := targetKey(t)
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, context.Canceled
		}
		if pc, ok := p.conns[t.HostID]; ok {
			select {
			case <-pc.dead:
				delete(p.conns, t.HostID)
			default:
				if pc.key == key && (pc.expires.IsZero() || time.Now().Before(pc.expires)) {
					pc.refs++
					p.mu.Unlock()
					return pc, nil
				}
				// Settings changed, or a certificate connection reached its
				// age: retire the old connection once idle.
				delete(p.conns, t.HostID)
				go p.closeWhenIdle(pc)
			}
		}
		if dc, ok := p.dialing[t.HostID]; ok && dc.key == key {
			p.mu.Unlock()
			select {
			case <-dc.done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			if dc.err != nil {
				return nil, dc.err
			}
			continue // the dial registered a connection; take a reference above
		}
		dc := &dialCall{key: key, done: make(chan struct{})}
		p.dialing[t.HostID] = dc
		p.mu.Unlock()

		dc.pc, dc.err = p.dial(ctx, t, key)

		p.mu.Lock()
		delete(p.dialing, t.HostID)
		if dc.err == nil {
			if p.closed {
				dc.pc.client.Close()
				dc.err = context.Canceled
			} else {
				p.conns[t.HostID] = dc.pc
			}
		}
		p.mu.Unlock()
		close(dc.done)
		return dc.pc, dc.err
	}
}

// dial opens a new connection (refs = 1), tunnelled through the pooled jump
// connection when t has one.
func (p *Pool) dial(ctx context.Context, t *Target, key string) (*pooled, error) {
	var jump *ssh.Client
	var jumpPC *pooled
	if t.Via != nil {
		var err error
		jumpPC, err = p.acquire(ctx, t.Via)
		if err != nil {
			return nil, viaError(t.Via, err)
		}
		jump = jumpPC.client
	}
	c, err := p.Dialer.dialOne(ctx, t, jump)
	if err != nil {
		if jumpPC != nil {
			p.unref(jumpPC)
		}
		return nil, err
	}
	pc := &pooled{key: key, client: c, sem: make(chan struct{}, p.maxSessions()),
		refs: 1, lastUsed: time.Now(), dead: make(chan struct{})}
	if t.Certificate != nil {
		pc.expires = time.Now().Add(MaxCertConnAge)
	}
	if jumpPC != nil {
		pc.jumpID = t.Via.HostID
	}
	// A tunnelled connection keeps a reference on its jump connection for its
	// whole lifetime, so the jump host is never reaped while in use.
	go func() {
		c.Wait()
		close(pc.dead)
		if jumpPC != nil {
			p.unref(jumpPC)
		}
	}()
	return pc, nil
}

func (p *Pool) closeWhenIdle(pc *pooled) {
	for {
		p.mu.Lock()
		idle := pc.refs == 0
		p.mu.Unlock()
		if idle {
			pc.client.Close()
			return
		}
		select {
		case <-pc.dead:
			return
		case <-time.After(time.Second):
		}
	}
}

// Evict closes the pooled connection for hostID (and connections tunnelled
// through it) once they are idle. Call after a host's settings change.
func (p *Pool) Evict(hostID string) {
	p.mu.Lock()
	var victims []*pooled
	for id, pc := range p.conns {
		if id == hostID || pc.jumpID == hostID {
			victims = append(victims, pc)
			delete(p.conns, id)
		}
	}
	p.mu.Unlock()
	for _, pc := range victims {
		go p.closeWhenIdle(pc)
	}
}

// Len reports the number of pooled connections.
func (p *Pool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.conns)
}

// Close closes every connection.
func (p *Pool) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	close(p.stop)
	conns := p.conns
	p.conns = map[string]*pooled{}
	p.mu.Unlock()
	for _, pc := range conns {
		pc.client.Close()
	}
}

// reap sends keepalives and closes idle or dead connections.
func (p *Pool) reap() {
	interval := p.Keepalive
	if interval <= 0 {
		interval = DefaultKeepalive
	}
	idle := p.IdleTimeout
	if idle <= 0 {
		idle = DefaultIdleTimeout
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
		}
		p.mu.Lock()
		var check []*pooled
		for id, pc := range p.conns {
			select {
			case <-pc.dead:
				delete(p.conns, id)
				continue
			default:
			}
			if pc.refs == 0 && (time.Since(pc.lastUsed) > idle || !pc.expires.IsZero() && time.Now().After(pc.expires)) {
				delete(p.conns, id)
				go pc.client.Close()
				continue
			}
			check = append(check, pc)
		}
		p.mu.Unlock()
		for _, pc := range check {
			go keepalive(pc)
		}
	}
}

func keepalive(pc *pooled) {
	done := make(chan error, 1)
	go func() {
		_, _, err := pc.client.SendRequest("keepalive@openssh.com", true, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			pc.client.Close()
		}
	case <-time.After(15 * time.Second):
		pc.client.Close()
	}
}
