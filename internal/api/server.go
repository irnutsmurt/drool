// Package api implements the public REST API (/api/v1), health endpoints and
// static serving of the embedded web UI.
package api

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"drool/internal/audit"
	"drool/internal/auth"
	"drool/internal/config"
	"drool/internal/driver/sshdriver"
	"drool/internal/engine"
	"drool/internal/inventory"
	"drool/internal/logging"
	"drool/internal/poller"
	"drool/internal/secrets"
	"drool/internal/sshx"
	"drool/internal/store"
	"drool/internal/store/db"
)

// Deps are the server's dependencies.
type Deps struct {
	Config   *config.Loaded
	Store    *store.Store
	Auth     *auth.Service
	Audit    *audit.Writer
	Logs     *logging.Loggers
	UI       fs.FS // contents of web/dist; may lack index.html
	Registry *prometheus.Registry
	TLSMode  string

	Secrets   *secrets.Service
	Inventory *inventory.Service
	Pool      *sshx.Pool
	Engine    *engine.Engine
	Poller    *poller.Poller // may be nil (tests)
	Simulator *sshdriver.Executor
	// StaleAfter is how old a status check may be before it is shown as stale.
	StaleAfter time.Duration
}

// Server is the HTTP API server.
type Server struct {
	d              Deps
	log            *slog.Logger
	secLog         *slog.Logger
	trustedProxies []netip.Prefix
	secureCookie   bool
	ready          atomic.Bool
	metrics        *httpMetrics
	handler        http.Handler
}

// authMode controls authentication for a route.
type authMode int

const (
	public authMode = iota // no authentication
	authed                 // any authenticated principal
)

// route is one API endpoint. The route table is also used by the OpenAPI
// conformance test, so every endpoint must be declared here.
type route struct {
	method      string
	path        string // ServeMux pattern path, e.g. /api/v1/users/{id}
	auth        authMode
	perm        auth.Permission // required permission ("" = none beyond auth mode)
	sessionOnly bool            // reject bearer-token principals
	h           http.HandlerFunc
}

// APIPrefix is the versioned API base path.
const APIPrefix = "/api/v1"

func (s *Server) routes() []route {
	return []route{
		{method: "GET", path: APIPrefix + "/setup/status", auth: public, h: s.handleSetupStatus},
		{method: "POST", path: APIPrefix + "/setup/claim", auth: public, h: s.handleSetupClaim},

		{method: "POST", path: APIPrefix + "/auth/login", auth: public, h: s.handleLogin},
		{method: "POST", path: APIPrefix + "/auth/logout", auth: authed, h: s.handleLogout},
		{method: "GET", path: APIPrefix + "/auth/me", auth: authed, h: s.handleMe},
		{method: "POST", path: APIPrefix + "/auth/password", auth: authed, sessionOnly: true, h: s.handleChangeOwnPassword},

		{method: "GET", path: APIPrefix + "/users", auth: authed, perm: auth.PermUsersManage, h: s.handleListUsers},
		{method: "POST", path: APIPrefix + "/users", auth: authed, perm: auth.PermUsersManage, h: s.handleCreateUser},
		{method: "GET", path: APIPrefix + "/users/{id}", auth: authed, perm: auth.PermUsersManage, h: s.handleGetUser},
		{method: "PATCH", path: APIPrefix + "/users/{id}", auth: authed, perm: auth.PermUsersManage, h: s.handleUpdateUser},
		{method: "DELETE", path: APIPrefix + "/users/{id}", auth: authed, perm: auth.PermUsersManage, h: s.handleDeleteUser},

		{method: "GET", path: APIPrefix + "/tokens", auth: authed, h: s.handleListTokens},
		{method: "POST", path: APIPrefix + "/tokens", auth: authed, sessionOnly: true, h: s.handleCreateToken},
		{method: "DELETE", path: APIPrefix + "/tokens/{id}", auth: authed, h: s.handleDeleteToken},

		{method: "GET", path: APIPrefix + "/audit", auth: authed, perm: auth.PermAuditRead, h: s.handleListAudit},

		{method: "GET", path: APIPrefix + "/system/info", auth: authed, h: s.handleSystemInfo},

		{method: "GET", path: APIPrefix + "/secrets", auth: authed, perm: auth.PermCredentialsUse, h: s.handleListSecrets},
		{method: "POST", path: APIPrefix + "/secrets", auth: authed, perm: auth.PermCredentialsManage, h: s.handleCreateSecret},
		{method: "GET", path: APIPrefix + "/secrets/controller-ssh-key", auth: authed, perm: auth.PermHostsRead, h: s.handleControllerKey},
		{method: "GET", path: APIPrefix + "/secrets/{id}", auth: authed, perm: auth.PermCredentialsUse, h: s.handleGetSecret},
		{method: "PATCH", path: APIPrefix + "/secrets/{id}", auth: authed, perm: auth.PermCredentialsManage, h: s.handleUpdateSecret},
		{method: "DELETE", path: APIPrefix + "/secrets/{id}", auth: authed, perm: auth.PermCredentialsManage, h: s.handleDeleteSecret},

		{method: "GET", path: APIPrefix + "/hosts", auth: authed, perm: auth.PermHostsRead, h: s.handleListHosts},
		{method: "POST", path: APIPrefix + "/hosts", auth: authed, perm: auth.PermHostsWrite, h: s.handleCreateHost},
		{method: "GET", path: APIPrefix + "/hosts/{id}", auth: authed, perm: auth.PermHostsRead, h: s.handleGetHost},
		{method: "PATCH", path: APIPrefix + "/hosts/{id}", auth: authed, perm: auth.PermHostsWrite, h: s.handleUpdateHost},
		{method: "DELETE", path: APIPrefix + "/hosts/{id}", auth: authed, perm: auth.PermHostsWrite, h: s.handleDeleteHost},
		{method: "GET", path: APIPrefix + "/hosts/{id}/hostkeys", auth: authed, perm: auth.PermHostsRead, h: s.handleListHostKeys},
		{method: "POST", path: APIPrefix + "/hosts/{id}/hostkeys/scan", auth: authed, perm: auth.PermHostsWrite, h: s.handleScanHostKeys},
		{method: "POST", path: APIPrefix + "/hosts/{id}/hostkeys/approve", auth: authed, perm: auth.PermHostKeysApprove, h: s.handleApproveHostKey},
		{method: "POST", path: APIPrefix + "/hosts/{id}/probe", auth: authed, perm: auth.PermJobsExecute, h: s.handleProbeHost},
		{method: "POST", path: APIPrefix + "/hosts/{id}/refresh", auth: authed, perm: auth.PermJobsExecute, h: s.handleRefreshHost},
		{method: "GET", path: APIPrefix + "/hosts/{id}/facts", auth: authed, perm: auth.PermHostsRead, h: s.handleHostFacts},

		{method: "GET", path: APIPrefix + "/groups", auth: authed, perm: auth.PermHostsRead, h: s.handleListGroups},
		{method: "POST", path: APIPrefix + "/groups", auth: authed, perm: auth.PermHostsWrite, h: s.handleCreateGroup},
		{method: "GET", path: APIPrefix + "/groups/{id}", auth: authed, perm: auth.PermHostsRead, h: s.handleGetGroup},
		{method: "PATCH", path: APIPrefix + "/groups/{id}", auth: authed, perm: auth.PermHostsWrite, h: s.handleUpdateGroup},
		{method: "DELETE", path: APIPrefix + "/groups/{id}", auth: authed, perm: auth.PermHostsWrite, h: s.handleDeleteGroup},

		{method: "POST", path: APIPrefix + "/jobs/preview", auth: authed, perm: auth.PermJobsRead, h: s.handlePreviewJob},
		{method: "POST", path: APIPrefix + "/jobs", auth: authed, perm: auth.PermJobsExecute, h: s.handleCreateJob},
		{method: "POST", path: APIPrefix + "/jobs/simulate", auth: authed, perm: auth.PermJobsExecute, h: s.handleSimulateJob},
		{method: "GET", path: APIPrefix + "/jobs", auth: authed, perm: auth.PermJobsRead, h: s.handleListJobs},
		{method: "GET", path: APIPrefix + "/jobs/{id}", auth: authed, perm: auth.PermJobsRead, h: s.handleGetJob},
		{method: "GET", path: APIPrefix + "/jobs/{id}/hosts", auth: authed, perm: auth.PermJobsRead, h: s.handleListJobHosts},
		{method: "GET", path: APIPrefix + "/jobs/{id}/hosts/{hostId}", auth: authed, perm: auth.PermJobsRead, h: s.handleGetJobHost},
		{method: "GET", path: APIPrefix + "/jobs/{id}/hosts/{hostId}/output", auth: authed, perm: auth.PermJobsRead, h: s.handleJobOutput},
		{method: "GET", path: APIPrefix + "/jobs/{id}/events", auth: authed, perm: auth.PermJobsRead, h: s.handleJobEvents},
		{method: "POST", path: APIPrefix + "/jobs/{id}/cancel", auth: authed, perm: auth.PermJobsExecute, h: s.handleCancelJob},
		{method: "POST", path: APIPrefix + "/jobs/{id}/resume", auth: authed, perm: auth.PermJobsExecute, h: s.handleResumeJob},
		{method: "POST", path: APIPrefix + "/jobs/{id}/rerun", auth: authed, perm: auth.PermJobsExecute, h: s.handleRerunJob},
	}
}

// New builds the server.
func New(d Deps) (*Server, error) {
	if d.StaleAfter <= 0 {
		d.StaleAfter = 15 * time.Minute
	}
	s := &Server{
		d:            d,
		log:          d.Logs.App,
		secLog:       d.Logs.Security,
		secureCookie: !d.Config.IsInsecurePlaintext(),
	}
	for _, p := range d.Config.Server.TrustedProxies {
		pfx, err := netip.ParsePrefix(p)
		if err != nil {
			addr, err2 := netip.ParseAddr(p)
			if err2 != nil {
				return nil, err
			}
			pfx = netip.PrefixFrom(addr, addr.BitLen())
		}
		s.trustedProxies = append(s.trustedProxies, pfx)
	}
	s.metrics = newHTTPMetrics(d.Registry)

	mux := http.NewServeMux()
	for _, rt := range s.routes() {
		mux.Handle(rt.method+" "+rt.path, s.wrapRoute(rt))
	}
	mux.Handle(APIPrefix+"/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, errNotFound("endpoint"))
	}))
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /ready", s.handleReady)
	mux.Handle("GET /metrics", metricsHandler(d.Registry))
	mux.Handle("/", s.uiHandler())

	s.handler = s.withRequestContext(s.withRecover(s.withSecurityHeaders(s.withAccessLog(mux))))
	return s, nil
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler { return s.handler }

// SetReady marks the server ready to receive traffic.
func (s *Server) SetReady(v bool) { s.ready.Store(v) }

// wrapRoute applies authentication, CSRF and permission checks.
func (s *Server) wrapRoute(rt route) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rt.auth == public {
			rt.h(w, r)
			return
		}
		p, prob := s.authenticate(r)
		if prob != nil {
			writeProblem(w, r, prob)
			return
		}
		if isStateChanging(r.Method) && !auth.CheckCSRF(p, r.Header.Get(csrfHeader)) {
			s.secLog.Warn("csrf check failed", "user_id", p.UserID, "path", r.URL.Path, "request_id", requestIDFrom(r.Context()))
			writeProblem(w, r, errCSRF)
			return
		}
		if rt.sessionOnly && p.Kind != auth.KindSession {
			writeProblem(w, r, errForbidden("this endpoint requires an interactive session, not an API token"))
			return
		}
		if rt.perm != "" && !p.Can(rt.perm) {
			ctx := withPrincipal(r.Context(), p)
			_ = s.d.Audit.Record(ctx, audit.Event{
				Action: "authz.denied", Result: audit.Denied,
				Details: map[string]any{"permission": string(rt.perm), "method": r.Method, "path": r.URL.Path},
			})
			writeProblem(w, r, errForbidden("missing permission "+string(rt.perm)))
			return
		}
		rt.h(w, r.WithContext(withPrincipal(r.Context(), p)))
	})
}

const (
	sessionCookie = "drool_session"
	csrfHeader    = "X-DROOL-CSRF"
)

func isStateChanging(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

func (s *Server) authenticate(r *http.Request) (*auth.Principal, *Problem) {
	ctx := r.Context()
	if h := r.Header.Get("Authorization"); h != "" {
		tok, ok := cutPrefixFold(h, "Bearer ")
		if !ok {
			return nil, errUnauthenticated
		}
		p, err := s.d.Auth.AuthenticateToken(ctx, tok)
		if err != nil {
			if err != auth.ErrUnauthenticated {
				s.log.Error("token authentication", "err", err)
				return nil, errInternal
			}
			s.secLog.Warn("invalid API token", "source_ip", clientIPFrom(ctx), "request_id", requestIDFrom(ctx))
			return nil, errUnauthenticated
		}
		return p, nil
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil, errUnauthenticated
	}
	p, err := s.d.Auth.AuthenticateSession(ctx, c.Value)
	if err != nil {
		if err != auth.ErrUnauthenticated {
			s.log.Error("session authentication", "err", err)
			return nil, errInternal
		}
		return nil, errUnauthenticated
	}
	return p, nil
}

func (s *Server) setSessionCookie(w http.ResponseWriter, value string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: value, Path: "/",
		Expires: expires, HttpOnly: true, Secure: s.secureCookie, SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.secureCookie, SameSite: http.SameSiteStrictMode,
	})
}

// inTxAudited runs fn in a write transaction together with the audit record
// it returns, so a change and its audit trail commit or roll back together.
// On failure nothing is recorded; callers audit failed attempts separately
// where useful.
func (s *Server) inTxAudited(ctx context.Context, fn func(q *db.Queries) (audit.Event, error)) error {
	var emit func()
	err := s.d.Store.InTx(ctx, func(q *db.Queries) error {
		ev, err := fn(q)
		if err != nil {
			return err
		}
		emit, err = s.d.Audit.RecordTx(ctx, q, ev)
		return err
	})
	if err == nil {
		emit()
	}
	return err
}

// RunSessionCleanup periodically deletes expired sessions until ctx ends.
func (s *Server) RunSessionCleanup(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := s.d.Auth.CleanupExpired(ctx); err != nil {
				s.log.Error("session cleanup", "err", err)
			} else if n > 0 {
				s.log.Debug("session cleanup", "deleted", n)
			}
		}
	}
}
