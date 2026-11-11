package api

import (
	"log"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/divezh/netlane-server/internal/config"
	"github.com/divezh/netlane-server/internal/security"
)

func (s *Server) currentHash() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.passwordHash
}

// logName keeps attacker-supplied login names short and on one journal line.
func logName(name string) string {
	if len(name) > 64 {
		name = name[:64]
	}
	return strconv.Quote(name)
}

// changePassword replaces the administrator password after re-checking the current one.
// The new hash is written to the config file first; other sessions are then revoked.
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if s.cfg.Path == "" {
		problem(w, 409, "password_change_unavailable")
		return
	}
	now := time.Now()
	s.mu.Lock()
	key := "password:" + ip
	a := s.attempts[key]
	if a.since.IsZero() || now.Sub(a.since) > time.Minute {
		a = attempt{since: now}
	}
	a.count++
	s.attempts[key] = a
	s.mu.Unlock()
	if a.count > 5 {
		w.Header().Set("Retry-After", "60")
		problem(w, 429, "too_many_attempts")
		return
	}
	var input struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !decode(w, r, &input) {
		return
	}
	if len(input.NewPassword) < 12 || len(input.NewPassword) > 1024 {
		problem(w, 400, "weak_password")
		return
	}
	if input.NewPassword == input.CurrentPassword {
		problem(w, 400, "password_unchanged")
		return
	}
	select {
	case s.loginGate <- struct{}{}:
		defer func() { <-s.loginGate }()
	default:
		problem(w, 429, "login_busy")
		return
	}
	if !security.CheckPassword(s.currentHash(), input.CurrentPassword) {
		log.Printf("auth: password change rejected ip=%s", ip)
		problem(w, 403, "invalid_current_password")
		return
	}
	// Re-read the file so concurrent manual edits of other settings are kept.
	current, err := config.Load(s.cfg.Path)
	if err != nil {
		log.Printf("auth: cannot load config for password change: %v", err)
		problem(w, 500, "config_unavailable")
		return
	}
	if err = current.SetPassword(input.NewPassword); err != nil {
		problem(w, 400, "weak_password")
		return
	}
	if err = config.Save(s.cfg.Path, current); err != nil {
		log.Printf("auth: cannot save config for password change: %v", err)
		problem(w, 500, "config_write_failed")
		return
	}
	c, _ := r.Cookie("tt_session")
	s.mu.Lock()
	s.passwordHash = current.PasswordHash
	for token := range s.sessions {
		if token != c.Value {
			delete(s.sessions, token)
		}
	}
	delete(s.attempts, key)
	s.mu.Unlock()
	log.Printf("auth: password changed user=%s ip=%s", logName(s.cfg.Admin), ip)
	if err = s.db.Audit(r.Context(), s.cfg.Admin, "admin.password_changed", ip); err != nil {
		log.Printf("auth: audit of password change failed: %v", err)
	}
	respond(w, 200, map[string]bool{"password_changed": true})
}
