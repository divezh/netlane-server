package api

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	webassets "github.com/divezh/netlane-server"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/divezh/netlane-server/internal/config"
	"github.com/divezh/netlane-server/internal/endpoint"
	"github.com/divezh/netlane-server/internal/geoip"
	"github.com/divezh/netlane-server/internal/security"
	"github.com/divezh/netlane-server/internal/store"
	"github.com/divezh/netlane-server/internal/update"
)

type session struct {
	csrf    string
	expires time.Time
	// What «Безопасность → Сеансы» shows; id is a public handle, never the cookie value.
	id      string
	created time.Time
	seen    time.Time
	ip      string
	agent   string
}

// sessionIdle ends a session nobody has used for this long; sessions also end 8 h after login.
const sessionIdle = 2 * time.Hour

type attempt struct {
	count int
	since time.Time
}
type Server struct {
	cfg       config.Config
	db        *store.Store
	metrics   *endpoint.Metrics
	control   *endpoint.Control
	applyMu   sync.Mutex
	mu        sync.Mutex
	sessions  map[string]session
	attempts  map[string]attempt
	loginGate chan struct{}
	// passwordHash, serverName and serverCountry may change at runtime; read and write them under mu.
	hub           *Hub
	passwordHash  string
	serverName    string
	serverCountry string
	// geo resolves client addresses to countries. It answers nothing when the
	// optional database is not installed, and is safe for concurrent use.
	geo *geoip.Lookup
	// The release channel is read through fetchManifest and cached in update. An update is
	// only requested: the root timer installs it. Tests replace all three.
	updateMu       sync.Mutex
	update         updateResult
	fetchManifest  func(context.Context) (update.Manifest, error)
	requestUpdate  func(string) error
	timerInstalled func() bool
	// instantStart: the path unit that starts an update at once is installed (0.5.19+).
	instantStart func() bool
	// reportDir is where the root updater writes its progress (update.ReportDir); tests set their own.
	reportDir string
}

// New builds the panel. hub wakes nodes on changes; nil makes a private one (tests, no nodes).
func New(cfg config.Config, db *store.Store, hub *Hub) (http.Handler, error) {
	if hub == nil {
		hub = NewHub()
	}
	hub.SetIdentity(cfg.ServerName, cfg.ServerCountry)
	m, err := endpoint.NewMetrics(cfg.MetricsAddress)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, db: db, metrics: m, sessions: map[string]session{}, attempts: map[string]attempt{}, loginGate: make(chan struct{}, 2), passwordHash: cfg.PasswordHash, serverName: cfg.ServerName, serverCountry: cfg.ServerCountry, hub: hub}
	// The panel is unprivileged on the server: the update it starts is the fixed command
	// granted by /etc/sudoers.d/netlane-update.
	s.fetchManifest = func(ctx context.Context) (update.Manifest, error) { return update.Fetch(ctx, nil, update.Channel) }
	s.requestUpdate = requestVersion(filepath.Dir(cfg.Database))
	s.reportDir = update.ReportDir
	s.timerInstalled = func() bool {
		// A local stand can point at any file to see the card with its button; the update
		// itself still needs the real timer, because only root may install it.
		path := update.TimerUnit
		if override := os.Getenv("NETLANE_UPDATE_TIMER"); override != "" {
			path = override
		}
		_, err := os.Stat(path)
		return err == nil
	}
	s.instantStart = func() bool {
		if os.Getenv("NETLANE_UPDATE_TIMER") != "" {
			return true
		}
		_, err := os.Stat(update.PathUnit)
		return err == nil
	}
	s.geo, err = geoip.Open(cfg.GeoIPFile())
	if err != nil {
		return nil, err
	}
	s.control, err = endpoint.NewControl(cfg.ControlSocket)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.Handle("GET /api/v1/auth/2fa", s.auth(http.HandlerFunc(s.factorStatus)))
	mux.Handle("POST /api/v1/auth/2fa/setup", s.auth(http.HandlerFunc(s.factorSetup)))
	mux.Handle("POST /api/v1/auth/2fa", s.auth(http.HandlerFunc(s.factorChange)))
	mux.Handle("POST /api/v1/auth/password", s.auth(http.HandlerFunc(s.changePassword)))
	mux.Handle("GET /api/v1/auth/sessions", s.auth(http.HandlerFunc(s.listSessions)))
	mux.Handle("POST /api/v1/auth/sessions/logout-others", s.auth(http.HandlerFunc(s.logoutOthers)))
	mux.Handle("GET /api/v1/diagnostics", s.auth(http.HandlerFunc(s.diagnostics)))
	mux.Handle("GET /api/v1/statistics", s.auth(http.HandlerFunc(s.statistics)))
	mux.Handle("GET /api/v1/info", s.auth(http.HandlerFunc(s.info)))
	mux.Handle("GET /api/v1/updates", s.auth(http.HandlerFunc(s.updates)))
	mux.Handle("POST /api/v1/updates/apply", s.auth(http.HandlerFunc(s.applyUpdate)))
	mux.Handle("PUT /api/v1/server", s.auth(http.HandlerFunc(s.updateServer)))
	mux.Handle("GET /api/v1/client-settings", s.auth(http.HandlerFunc(s.getClientSettings)))
	mux.Handle("PUT /api/v1/client-settings", s.auth(http.HandlerFunc(s.putClientSettings)))
	mux.Handle("GET /api/v1/nodes", s.auth(http.HandlerFunc(s.listNodes)))
	mux.Handle("GET /api/v1/nodes/{id}", s.auth(http.HandlerFunc(s.nodeDetails)))
	mux.Handle("POST /api/v1/nodes/invite", s.auth(http.HandlerFunc(s.inviteNode)))
	mux.Handle("PUT /api/v1/nodes/{id}", s.auth(http.HandlerFunc(s.updateNode)))
	mux.Handle("DELETE /api/v1/nodes/{id}", s.auth(http.HandlerFunc(s.deleteNode)))
	mux.Handle("GET /api/v1/groups", s.auth(http.HandlerFunc(s.listGroups)))
	mux.Handle("POST /api/v1/groups", s.auth(http.HandlerFunc(s.createGroup)))
	mux.Handle("PUT /api/v1/groups/{id}", s.auth(http.HandlerFunc(s.updateGroup)))
	mux.Handle("DELETE /api/v1/groups/{id}", s.auth(http.HandlerFunc(s.deleteGroup)))
	mux.Handle("PUT /api/v1/users/{id}/group", s.auth(http.HandlerFunc(s.setUserGroup)))
	mux.Handle("POST /api/v1/users/groups", s.auth(http.HandlerFunc(s.setUsersGroup)))
	mux.Handle("/api/v1/", s.auth(http.HandlerFunc(s.handle)))
	// Demo screens: not secret, but no reason to show them to anyone without a session.
	mux.Handle("GET /preview/", s.auth(http.StripPrefix("/preview/", webassets.Preview())))
	app := webassets.App()
	mux.Handle("GET /{$}", app)
	mux.Handle("GET /app.js", app)
	mux.Handle("GET /app.css", app)
	mux.Handle("GET /favicon.ico", app)
	mux.Handle("GET /icon.png", app)
	mux.Handle("GET /fonts/", app)
	mux.Handle("GET /flags/", app)
	mux.Handle("GET /twemoji/", app)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		if r.TLS != nil {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		if r.URL.Path == "/" || r.URL.Path == "/app.js" || r.URL.Path == "/app.css" {
			w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		}
		if strings.HasPrefix(r.URL.Path, "/preview/") {
			w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; font-src 'self'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'")
		}
		w.Header().Set("Referrer-Policy", "no-referrer")
		if !s.sameOrigin(r) {
			problem(w, 403, "origin_not_allowed")
			return
		}
		mux.ServeHTTP(w, r)
	}), nil
}

// allAttempts counts every login attempt in the current minute, whatever the address.
const allAttempts = "*"

// attemptKey is the address a login attempt is counted against: IPv4 as is, IPv6 by its /64.
func attemptKey(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() != nil {
		return ip
	}
	return parsed.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

func (s *Server) sameOrigin(r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		if err != nil || u.Scheme != scheme || u.Host != r.Host || u.User != nil {
			return false
		}
	}
	return r.Header.Get("Sec-Fetch-Site") != "cross-site"
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	now := time.Now()
	s.mu.Lock()
	for key, a := range s.attempts {
		if now.Sub(a.since) > time.Minute {
			delete(s.attempts, key)
		}
	}
	// One IPv6 subscriber holds a whole /64: counting single addresses would let one client
	// rotate through millions of them. All attempts together are capped as well.
	key := attemptKey(ip)
	a := s.attempts[key]
	if a.since.IsZero() {
		a.since = now
	}
	a.count++
	s.attempts[key] = a
	all := s.attempts[allAttempts]
	if all.since.IsZero() {
		all.since = now
	}
	all.count++
	s.attempts[allAttempts] = all
	s.mu.Unlock()
	if a.count > 5 || all.count > 60 {
		// Same journal format as a failed login, so fail2ban counts sustained guessing.
		log.Printf("auth: login rate limited ip=%s", ip)
		w.Header().Set("Retry-After", "60")
		problem(w, 429, "too_many_attempts")
		return
	}
	select {
	case s.loginGate <- struct{}{}:
		defer func() { <-s.loginGate }()
	default:
		problem(w, 429, "login_busy")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decode(w, r, &input) {
		return
	}
	// Always verify the hash, even for an unknown login.
	hash := s.currentHash()
	valid := security.CheckPassword(hash, input.Password)
	if subtle.ConstantTimeCompare([]byte(input.Username), []byte(s.cfg.Admin)) != 1 || !valid {
		log.Printf("auth: login failed user=%s ip=%s", logName(input.Username), ip)
		// The attempt limit above keeps this to a few rows a minute per address.
		if err := s.db.Audit(r.Context(), "-", "auth.login_failed", ip); err != nil {
			log.Printf("auth: audit of a failed login: %v", err)
		}
		problem(w, 401, "invalid_credentials")
		return
	}
	token := security.Token(32)
	csrf := security.Token(32)
	s.mu.Lock()
	for key, v := range s.sessions {
		if now.After(v.expires) {
			delete(s.sessions, key)
		}
	}
	if len(s.sessions) >= 100 {
		s.mu.Unlock()
		problem(w, 429, "session_limit")
		return
	}
	if s.passwordHash != hash {
		s.mu.Unlock()
		problem(w, 401, "invalid_credentials")
		return
	}
	if !s.checkLoginFactor(w, r, input.Code, ip, now) {
		s.mu.Unlock()
		return
	}
	agent := r.UserAgent()
	if len(agent) > 160 {
		agent = agent[:160]
	}
	s.sessions[token] = session{csrf: csrf, expires: now.Add(8 * time.Hour), id: security.Token(9), created: now, seen: now, ip: ip, agent: agent}
	s.mu.Unlock()
	log.Printf("auth: login ok user=%s ip=%s", logName(input.Username), ip)
	if err := s.db.Audit(r.Context(), s.cfg.Admin, "auth.login", ip); err != nil {
		log.Printf("auth: audit of login failed: %v", err)
	}
	http.SetCookie(w, &http.Cookie{Name: "tt_session", Value: token, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: 8 * 60 * 60})
	respond(w, 200, map[string]string{"csrf_token": csrf, "username": s.cfg.Admin})
}
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("tt_session")
		if err != nil {
			problem(w, 401, "authentication_required")
			return
		}
		s.mu.Lock()
		v, ok := s.sessions[c.Value]
		now := time.Now()
		if ok && (now.After(v.expires) || (!v.seen.IsZero() && now.Sub(v.seen) > sessionIdle)) {
			delete(s.sessions, c.Value)
			ok = false
		}
		if ok {
			v.seen = now
			s.sessions[c.Value] = v
		}
		s.mu.Unlock()
		if !ok {
			problem(w, 401, "authentication_required")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(v.csrf)) != 1 {
			problem(w, 403, "csrf_required")
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == "GET" && r.URL.Path == "/api/v1/usage":
		month, after := r.URL.Query().Get("month"), r.URL.Query().Get("after")
		if len(month) > 7 || len(after) > 128 {
			problem(w, 400, "invalid_usage_query")
			return
		}
		usage, err := s.control.Usage(r.Context(), month, after)
		if err != nil {
			controlProblem(w, err)
			return
		}
		mainBytes := usage.Totals.UploadBytes + usage.Totals.DownloadBytes
		if err = s.addNodeUsage(r.Context(), usage); err != nil {
			problem(w, 500, "storage_error")
			return
		}
		nodeBytes, err := s.db.UsageByNode(r.Context(), usage.Month)
		if err != nil {
			problem(w, 500, "storage_error")
			return
		}
		respond(w, 200, struct {
			*endpoint.UsageReport
			MainBytes uint64            `json:"main_bytes"`
			NodeBytes map[string]uint64 `json:"node_bytes"`
		}{usage, mainBytes, nodeBytes})
	case r.Method == "GET" && r.URL.Path == "/api/v1/auth/session":
		c, _ := r.Cookie("tt_session")
		s.mu.Lock()
		v := s.sessions[c.Value]
		s.mu.Unlock()
		respond(w, 200, map[string]string{"username": s.cfg.Admin, "csrf_token": v.csrf})
	case r.Method == "GET" && r.URL.Path == "/api/v1/audit":
		var before int64
		if raw := r.URL.Query().Get("before"); raw != "" {
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || parsed < 1 {
				problem(w, 400, "invalid_audit_query")
				return
			}
			before = parsed
		}
		events, err := s.db.RecentAudit(r.Context(), before)
		if err != nil {
			problem(w, 500, "storage_error")
			return
		}
		respond(w, 200, map[string]any{"events": events, "more": len(events) == store.AuditPage})
	case r.Method == "GET" && r.URL.Path == "/api/v1/status":
		caps := map[string]bool{"users_store": true, "metrics_read": true, "hot_reload": false, "disconnect_user": false, "speed_limits": false, "durable_quotas": false, "expiry": false}
		state, err := s.control.Status(r.Context())
		if err == nil {
			for _, name := range []string{"hot_reload", "disconnect_user", "remove_user", "speed_limits", "expiry", "durable_quotas"} {
				caps[name] = state.Capabilities[name]
			}
		}
		respond(w, 200, map[string]any{"domain": s.cfg.Domain, "stage": "managed-endpoint-preview", "endpoint_connected": err == nil, "capabilities": caps})
	case r.URL.Path == "/api/v1/policy" && (r.Method == "GET" || r.Method == "PUT"):
		s.policy(w, r)
	case r.Method == "GET" && r.URL.Path == "/api/v1/endpoint/control":
		state, err := s.control.Status(r.Context())
		if err != nil {
			controlProblem(w, err)
			return
		}
		respond(w, 200, state)
	case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/api/v1/users/"):
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/users/"), "/")
		if len(parts) != 2 || parts[0] == "" {
			problem(w, 404, "not_found")
			return
		}
		s.applyMu.Lock()
		defer s.applyMu.Unlock()
		u, password, err := s.db.Managed(r.Context(), parts[0])
		if errors.Is(err, sql.ErrNoRows) {
			problem(w, 404, "user_not_found")
			return
		}
		if err != nil {
			problem(w, 500, "storage_error")
			return
		}
		if parts[1] == "profile" {
			s.profile(w, r, u, password)
			return
		}
		if parts[1] == "subscription" {
			s.subscriptionLink(w, r, u)
			return
		}
		if parts[1] != "apply" && parts[1] != "disconnect" {
			problem(w, 404, "not_found")
			return
		}
		state, err := s.control.Status(r.Context())
		if err != nil {
			controlProblem(w, err)
			return
		}
		if parts[1] == "apply" && u.Policy.Enabled && u.Policy.QuotaBytes != 0 && !state.Capabilities["durable_quotas"] {
			problem(w, 409, "endpoint_quota_not_supported")
			return
		}
		if parts[1] == "apply" && u.Policy.Enabled && u.Policy.ExpiresAt != "" && !state.Capabilities["expiry"] {
			problem(w, 409, "endpoint_expiry_not_supported")
			return
		}
		if parts[1] == "disconnect" {
			found := false
			for _, live := range state.Users {
				if live.ID == u.ID && live.Username == u.Username {
					found = true
				}
			}
			hasNodes, err := s.db.HasNodes(r.Context())
			if err != nil {
				problem(w, 500, "storage_error")
				return
			}
			if !found && !hasNodes {
				problem(w, 409, "user_not_applied")
				return
			}
			if err = s.db.Audit(r.Context(), s.cfg.Admin, "user.disconnect_requested", u.ID); err != nil {
				problem(w, 500, "audit_error")
				return
			}
			count := 0
			if found {
				if count, err = s.control.Disconnect(r.Context(), u.Username); err != nil {
					controlProblem(w, err)
					return
				}
			}
			if hasNodes {
				if err = s.db.QueueDisconnect(r.Context(), u.Username, time.Now()); err != nil {
					problem(w, 500, "storage_error")
					return
				}
				s.hub.Notify()
			}
			respond(w, 200, map[string]any{"disconnect_requested": count})
			return
		}
		if err = s.db.Audit(r.Context(), s.cfg.Admin, "user.apply_requested", u.ID); err != nil {
			problem(w, 500, "audit_error")
			return
		}
		expiresAt := u.Policy.ExpiresAt
		quotaBytes := u.Policy.QuotaBytes
		if !state.Capabilities["durable_quotas"] {
			quotaBytes = 0
		}
		if !state.Capabilities["expiry"] {
			expiresAt = ""
		}
		applied, err := s.control.Upsert(r.Context(), state.Generation, endpoint.ManagedUser{ID: u.ID, Username: u.Username, Password: password, Enabled: u.Policy.Enabled, DownloadMbps: u.Policy.DownloadMbps, UploadMbps: u.Policy.UploadMbps, ExpiresAt: expiresAt, QuotaBytes: quotaBytes, Revision: u.DesiredRevision})
		if err != nil {
			controlProblem(w, err)
			return
		}
		confirmed := false
		for _, live := range applied.Users {
			if live.ID == u.ID && live.Username == u.Username && live.Revision == u.DesiredRevision && live.Enabled == u.Policy.Enabled && live.DownloadMbps == u.Policy.DownloadMbps && live.UploadMbps == u.Policy.UploadMbps && live.ExpiresAt == expiresAt && live.QuotaBytes == quotaBytes {
				confirmed = true
			}
		}
		if !confirmed {
			problem(w, 502, "endpoint_acknowledgement_mismatch")
			return
		}
		if err = s.db.MarkApplied(r.Context(), s.cfg.Admin, u.ID, u.DesiredRevision); err != nil {
			problem(w, 500, "applied_but_reconciliation_required")
			return
		}
		s.hub.Notify()
		respond(w, 200, map[string]any{"vpn_applied": true, "applied_revision": u.DesiredRevision, "generation": applied.Generation})
	case r.Method == "POST" && r.URL.Path == "/api/v1/auth/logout":
		c, _ := r.Cookie("tt_session")
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "tt_session", Path: "/", MaxAge: -1, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
		w.WriteHeader(204)
	case r.Method == "GET" && r.URL.Path == "/api/v1/users":
		users, err := s.usersWithLocations(r.Context())
		if err != nil {
			problem(w, 500, "storage_error")
			return
		}
		respond(w, 200, map[string]any{"users": users})
	case r.Method == "POST" && r.URL.Path == "/api/v1/users":
		var input struct {
			Username string       `json:"username"`
			Password string       `json:"password"`
			Policy   store.Policy `json:"policy"`
			GroupID  string       `json:"group_id"`
		}
		if !decode(w, r, &input) {
			return
		}
		// A group decides which locations the user may use, so a user cannot exist
		// without one: the panel asks for it explicitly instead of guessing.
		if input.GroupID == "" {
			problem(w, 400, "group_required")
			return
		}
		if _, err := s.db.GroupByID(r.Context(), input.GroupID); err != nil {
			s.groupProblem(w, err)
			return
		}
		if !s.quotaAllowed(w, r, input.Policy) {
			return
		}
		generated := input.Password == ""
		if generated {
			input.Password = security.Token(24)
		}
		u, err := s.db.Create(r.Context(), s.cfg.Admin, input.Username, input.Password, input.Policy)
		if errors.Is(err, store.ErrConflict) {
			problem(w, 409, "username_exists")
			return
		}
		if err != nil {
			problem(w, 400, "invalid_user")
			return
		}
		if err = s.db.SetUserGroup(r.Context(), u.ID, input.GroupID); err != nil {
			s.groupProblem(w, err)
			return
		}
		output := map[string]any{"user": u, "vpn_applied": false}
		if generated {
			output["generated_password"] = input.Password
		}
		respond(w, 201, output)
	case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/v1/users/"):
		s.deleteUser(w, r)
	case r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/api/v1/users/"):
		s.applyMu.Lock()
		defer s.applyMu.Unlock()
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/users/")
		if id == "" || strings.Contains(id, "/") {
			problem(w, 404, "not_found")
			return
		}
		var input struct {
			ExpectedRevision int64        `json:"expected_revision"`
			Policy           store.Policy `json:"policy"`
		}
		if !decode(w, r, &input) {
			return
		}
		if input.ExpectedRevision < 1 || input.Policy.Validate() != nil {
			problem(w, 400, "invalid_policy")
			return
		}
		if !s.quotaAllowed(w, r, input.Policy) {
			return
		}
		u, err := s.db.UpdatePolicy(r.Context(), s.cfg.Admin, id, input.ExpectedRevision, input.Policy)
		if errors.Is(err, sql.ErrNoRows) {
			problem(w, 409, "revision_conflict_or_missing_user")
			return
		}
		if err != nil {
			problem(w, 500, "storage_error")
			return
		}
		respond(w, 200, map[string]any{"user": u, "vpn_applied": false})
	case r.Method == "GET" && r.URL.Path == "/api/v1/endpoint/clients":
		clients, err := s.metrics.Clients(r.Context())
		if err != nil {
			problem(w, 503, "endpoint_metrics_unavailable")
			return
		}
		for i := range clients {
			if clients[i].IP != nil {
				clients[i].Country = s.geo.Country(*clients[i].IP)
			}
		}
		respond(w, 200, map[string]any{"clients": clients, "observed_at": time.Now().UTC().Format(time.RFC3339), "counters": "since endpoint start; not durable quota accounting"})
	default:
		problem(w, 404, "not_found")
	}
}
func controlProblem(w http.ResponseWriter, err error) {
	var e *endpoint.ControlError
	if errors.As(err, &e) {
		switch e.Code {
		case "generation_conflict", "revision_conflict":
			problem(w, 409, e.Code)
			return
		case "usage_month_unavailable":
			problem(w, 400, e.Code)
			return
		case "invalid_user":
			problem(w, 400, "invalid_endpoint_user")
			return
		}
	}
	problem(w, 503, "endpoint_control_unavailable")
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeLimit(w, r, v, 16<<10)
}
func decodeLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		problem(w, 415, "json_required")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		problem(w, 400, "invalid_json")
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		problem(w, 400, "invalid_json")
		return false
	}
	return true
}
func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func problem(w http.ResponseWriter, status int, code string) {
	respond(w, status, map[string]string{"error": code})
}
