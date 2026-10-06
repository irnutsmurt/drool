package api

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strings"
	"time"

	"drool/internal/audit"
	"drool/internal/auth"
	"drool/internal/ids"
)

type ctxKey int

const (
	principalKey ctxKey = iota
	requestIDKey
	clientIPKey
)

func withPrincipal(ctx context.Context, p *auth.Principal) context.Context {
	ctx = context.WithValue(ctx, principalKey, p)
	a := audit.Actor{Type: audit.ActorUser, ID: p.UserID, Name: p.Username}
	if p.Kind == auth.KindToken {
		a.Type = audit.ActorToken
		a.Name = p.Username + " (token " + p.TokenID + ")"
	}
	return audit.WithActor(ctx, a)
}

func principalFrom(ctx context.Context) *auth.Principal {
	p, _ := ctx.Value(principalKey).(*auth.Principal)
	return p
}

func requestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

func clientIPFrom(ctx context.Context) string {
	ip, _ := ctx.Value(clientIPKey).(string)
	return ip
}

// withRequestContext assigns a request id and resolves the client IP.
func (s *Server) withRequestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := ids.New(ids.Request)
		ip := s.clientIP(r)
		w.Header().Set("X-Request-Id", id)
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		ctx = context.WithValue(ctx, clientIPKey, ip)
		ctx = audit.WithRequest(ctx, audit.Request{ID: id, SourceIP: ip})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// clientIP returns the peer address, or the right-most untrusted address in
// X-Forwarded-For when the peer is a trusted proxy.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil || !s.isTrustedProxy(peer) {
		return host
	}
	xff := r.Header.Values("X-Forwarded-For")
	var hops []string
	for _, v := range xff {
		for _, h := range strings.Split(v, ",") {
			if h = strings.TrimSpace(h); h != "" {
				hops = append(hops, h)
			}
		}
	}
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(hops[i])
		if err != nil {
			return host // malformed chain: fall back to the peer
		}
		if !s.isTrustedProxy(a) {
			return a.String()
		}
	}
	return host
}

func (s *Server) isTrustedProxy(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range s.trustedProxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func (s *Server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.log.Error("panic in handler", "panic", v, "stack", string(debug.Stack()),
					"request_id", requestIDFrom(r.Context()))
				writeProblem(w, r, errInternal)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; connect-src 'self'; font-src 'self'; object-src 'none'; " +
	"frame-ancestors 'none'; base-uri 'self'; form-action 'self'"

func (s *Server) withSecurityHeaders(next http.Handler) http.Handler {
	hsts := s.d.TLSMode == "provided"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		if hsts {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		if strings.HasPrefix(r.URL.Path, APIPrefix+"/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (sr *statusRecorder) WriteHeader(code int) {
	if sr.status == 0 {
		sr.status = code
	}
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Write(b []byte) (int, error) {
	if sr.status == 0 {
		sr.status = http.StatusOK
	}
	n, err := sr.ResponseWriter.Write(b)
	sr.bytes += n
	return n, err
}

func (sr *statusRecorder) Unwrap() http.ResponseWriter { return sr.ResponseWriter }

// Flush supports streaming responses (SSE, M2).
func (sr *statusRecorder) Flush() {
	if f, ok := sr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) withAccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sr := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(sr, r)
		if sr.status == 0 {
			sr.status = http.StatusOK
		}
		dur := time.Since(start)
		pattern := r.Pattern
		if pattern == "" {
			pattern = "unmatched"
		}
		s.metrics.observe(r.Method, pattern, sr.status, dur)
		if r.URL.Path == "/health" || r.URL.Path == "/ready" || r.URL.Path == "/metrics" {
			return
		}
		s.log.Info("http request",
			"method", r.Method, "path", r.URL.Path, "route", pattern, "status", sr.status,
			"bytes", sr.bytes, "duration_ms", dur.Milliseconds(),
			"request_id", requestIDFrom(r.Context()), "source_ip", clientIPFrom(r.Context()))
	})
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return s, false
}
