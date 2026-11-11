package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/divezh/netlane-server/internal/qrcode"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/divezh/netlane-server/internal/config"
	"github.com/divezh/netlane-server/internal/store"
)

// Profile export is an explicit CSRF-protected operation, audited before disclosing credentials.
func (s *Server) profile(w http.ResponseWriter, r *http.Request, u store.User, password string) {
	var input struct {
		Protocol string `json:"protocol"`
		AntiDPI  bool   `json:"anti_dpi"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Protocol != "http2" && input.Protocol != "http3" {
		problem(w, 400, "invalid_protocol")
		return
	}
	if !u.Policy.Enabled || u.DesiredRevision != u.AppliedRevision || u.AppliedRevision == 0 {
		problem(w, 409, "enabled_applied_user_required")
		return
	}
	if u.Policy.ExpiresAt != "" {
		deadline, err := time.Parse(time.RFC3339, u.Policy.ExpiresAt)
		if err != nil || !time.Now().Before(deadline) {
			problem(w, 409, "user_expired")
			return
		}
	}
	state, err := s.control.Status(r.Context())
	if err != nil {
		controlProblem(w, err)
		return
	}
	confirmed := false
	for _, live := range state.Users {
		if live.ID == u.ID && live.QuotaExhausted {
			problem(w, 409, "user_quota_exhausted")
			return
		}
		if live.ID == u.ID && live.Username == u.Username && live.Enabled && !live.Expired && live.Revision == u.DesiredRevision && live.ExpiresAt == u.Policy.ExpiresAt && live.QuotaBytes == u.Policy.QuotaBytes {
			confirmed = true
		}
	}
	if !confirmed {
		problem(w, 409, "enabled_applied_user_required")
		return
	}
	if err := s.db.Audit(r.Context(), s.cfg.Admin, "user.profile_exported", u.ID); err != nil {
		problem(w, 500, "audit_error")
		return
	}
	// IPv6 is conservatively off until endpoint routing capability is explicitly reported.
	address := net.JoinHostPort(s.cfg.Domain, "443")
	toml := fmt.Sprintf("hostname = %s\naddresses = [%s]\nhas_ipv6 = false\nusername = %s\npassword = %s\nskip_verification = false\nupstream_protocol = %s\nanti_dpi = %t\n", tomlString(s.cfg.Domain), tomlString(address), tomlString(u.Username), tomlString(password), tomlString(input.Protocol), input.AntiDPI)
	link := profileLink(s.cfg.Domain, address, u.Username, password, input.Protocol, input.AntiDPI, profileName(s.identity()))
	// A link too long for a QR code still works by copying; the picture is simply left out.
	picture, _ := qrcode.DataURI(link)
	respond(w, 200, map[string]string{"filename": u.Username + ".toml", "toml": toml, "deep_link": link, "qr": picture})
}

func tomlString(value string) string {
	// JSON basic-string escapes are also valid TOML; Go's \x escapes are not.
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// Version 1 is the format of the pinned v1.1.0 endpoint and clients; no subscription tags.
// profileName is the name clients show for this server: the country flag (a regional-indicator
// pair) and the name, the name alone, or "" when the owner has not named the server.
func profileName(cfg config.Config) string {
	if cfg.ServerName == "" {
		return ""
	}
	if len(cfg.ServerCountry) != 2 {
		return cfg.ServerName
	}
	flag := string([]rune{0x1F1E6 + rune(cfg.ServerCountry[0]-'A'), 0x1F1E6 + rune(cfg.ServerCountry[1]-'A')})
	return flag + " " + cfg.ServerName
}

// Tag 12 (name) is written only when the server has one; clients then fall back to the host.
func profileLink(host, address, username, password, protocol string, antiDPI bool, name string) string {
	var data []byte
	field := func(tag byte, value []byte) {
		data = append(data, tag)
		data = appendLength(data, len(value))
		data = append(data, value...)
	}
	field(0, []byte{1})
	field(1, []byte(host))
	field(2, []byte(address))
	field(4, []byte{0})
	field(5, []byte(username))
	field(6, []byte(password))
	p := byte(1)
	if protocol == "http3" {
		p = 2
	}
	field(9, []byte{p})
	if antiDPI {
		field(10, []byte{1})
	}
	if name != "" {
		field(12, []byte(name))
	}
	return "tt://?" + base64.RawURLEncoding.EncodeToString(data)
}

func appendLength(dst []byte, n int) []byte {
	if n < 64 {
		return append(dst, byte(n))
	}
	return append(dst, byte(n>>8)|0x40, byte(n))
}

// identity is the configuration with the current server name and country (they change at runtime).
func (s *Server) identity() config.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.cfg
	c.ServerName, c.ServerCountry = s.serverName, s.serverCountry
	return c
}

// updateServer stores the server name and country in the config file and in memory.
func (s *Server) updateServer(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ServerName    string `json:"server_name"`
		ServerCountry string `json:"server_country"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.ServerName = strings.TrimSpace(input.ServerName)
	if config.ValidateServerIdentity(input.ServerName, input.ServerCountry) != nil {
		problem(w, 400, "invalid_server_identity")
		return
	}
	if s.cfg.Path == "" {
		problem(w, 409, "config_unavailable")
		return
	}
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	// Re-read the file so concurrent manual edits of other settings are kept.
	current, err := config.Load(s.cfg.Path)
	if err != nil {
		log.Printf("server: cannot load config: %v", err)
		problem(w, 500, "config_unavailable")
		return
	}
	current.ServerName, current.ServerCountry = input.ServerName, input.ServerCountry
	if err = config.Save(s.cfg.Path, current); err != nil {
		log.Printf("server: cannot save config: %v", err)
		problem(w, 500, "config_write_failed")
		return
	}
	s.mu.Lock()
	s.serverName, s.serverCountry = input.ServerName, input.ServerCountry
	s.mu.Unlock()
	s.hub.SetIdentity(input.ServerName, input.ServerCountry)
	if err = s.db.Audit(r.Context(), s.cfg.Admin, "server.updated", input.ServerCountry+" "+input.ServerName); err != nil {
		log.Printf("server: audit failed: %v", err)
	}
	respond(w, 200, map[string]string{"server_name": input.ServerName, "server_country": input.ServerCountry})
}
