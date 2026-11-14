package api

import (
	"net"
	"net/http"
	"sort"
	"time"
)

type sessionView struct {
	ID       string `json:"id"`
	Current  bool   `json:"current"`
	IP       string `json:"ip"`
	Agent    string `json:"agent"`
	Created  string `json:"created_at"`
	LastSeen string `json:"last_seen_at"`
	Expires  string `json:"expires_at"`
}

// listSessions shows the administrator where the panel is open right now.
func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	c, _ := r.Cookie("tt_session")
	now := time.Now()
	s.mu.Lock()
	views := []sessionView{}
	for token, v := range s.sessions {
		if now.After(v.expires) || (!v.seen.IsZero() && now.Sub(v.seen) > sessionIdle) {
			continue
		}
		views = append(views, sessionView{ID: v.id, Current: c != nil && token == c.Value, IP: v.ip, Agent: v.agent, Created: v.created.UTC().Format(time.RFC3339), LastSeen: v.seen.UTC().Format(time.RFC3339), Expires: v.expires.UTC().Format(time.RFC3339)})
	}
	s.mu.Unlock()
	sort.Slice(views, func(i, j int) bool {
		if views[i].Current != views[j].Current {
			return views[i].Current
		}
		return views[i].LastSeen > views[j].LastSeen
	})
	respond(w, 200, map[string]any{"sessions": views, "idle_minutes": int(sessionIdle / time.Minute)})
}

// logoutOthers ends every session but the one asking: «выйти на всех других устройствах».
func (s *Server) logoutOthers(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("tt_session")
	if err != nil {
		problem(w, 401, "authentication_required")
		return
	}
	s.mu.Lock()
	ended := 0
	for token := range s.sessions {
		if token != c.Value {
			delete(s.sessions, token)
			ended++
		}
	}
	s.mu.Unlock()
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if err := s.db.Audit(r.Context(), s.cfg.Admin, "auth.sessions_revoked", ip); err != nil {
		problem(w, 500, "audit_error")
		return
	}
	respond(w, 200, map[string]int{"ended": ended})
}
