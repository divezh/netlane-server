package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/divezh/netlane-server/internal/qrcode"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/divezh/netlane-server/internal/security"
)

var factorEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

type adminFactor struct {
	Secret   string
	LastStep int64
	Recovery []string
}

func totp(secret []byte, step int64) string {
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	mac := hmac.New(sha1.New, secret)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 15
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[offset:])&0x7fffffff)%1000000)
}

func (f *adminFactor) consume(code string, now time.Time, allowRecovery bool) bool {
	code = strings.TrimSpace(code)
	if len(code) == 6 {
		key, err := factorEncoding.DecodeString(f.Secret)
		if err != nil || len(key) != 20 {
			return false
		}
		step := now.Unix() / 30
		for _, candidate := range []int64{step, step - 1, step + 1} {
			if candidate > f.LastStep && subtle.ConstantTimeCompare([]byte(code), []byte(totp(key, candidate))) == 1 {
				f.LastStep = candidate
				return true
			}
		}
	}
	if allowRecovery {
		hash := recoveryHash(code)
		for i, expected := range f.Recovery {
			if subtle.ConstantTimeCompare([]byte(hash), []byte(expected)) == 1 {
				f.Recovery = append(f.Recovery[:i], f.Recovery[i+1:]...)
				return true
			}
		}
	}
	return false
}

func recoveryHash(code string) string {
	hash := sha256.Sum256([]byte(code))
	return hex.EncodeToString(hash[:])
}

func (s *Server) readFactor(ctx context.Context) (adminFactor, string, error) {
	var factor adminFactor
	sealed, err := s.db.AdminFactor(ctx)
	if err != nil || sealed == "" {
		return factor, sealed, err
	}
	key, err := s.cfg.Key()
	if err != nil {
		return factor, sealed, err
	}
	raw, err := security.Open(key, "admin-totp", sealed)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &factor)
	}
	return factor, sealed, err
}

func (s *Server) saveFactor(ctx context.Context, previous string, factor adminFactor) bool {
	sealed := ""
	if factor.Secret != "" {
		key, err := s.cfg.Key()
		if err != nil {
			return false
		}
		raw, err := json.Marshal(factor)
		if err != nil {
			return false
		}
		sealed, err = security.Seal(key, "admin-totp", string(raw))
		if err != nil {
			return false
		}
	}
	ok, err := s.db.ReplaceAdminFactor(ctx, previous, sealed)
	return err == nil && ok
}

// Called with mu held so a session cannot be issued across a factor change.
func (s *Server) checkLoginFactor(w http.ResponseWriter, r *http.Request, code, ip string, now time.Time) bool {
	factor, sealed, err := s.readFactor(r.Context())
	if err != nil {
		problem(w, 500, "storage_error")
		return false
	}
	if sealed == "" {
		return true
	}
	if code == "" {
		respond(w, 200, map[string]bool{"totp_required": true})
		return false
	}
	if !factor.consume(code, now, true) {
		log.Printf("auth: login failed user=%s ip=%s", logName(s.cfg.Admin), ip)
		problem(w, 401, "invalid_second_factor")
		return false
	}
	if !s.saveFactor(r.Context(), sealed, factor) {
		problem(w, 409, "factor_retry")
		return false
	}
	return true
}

func (s *Server) factorStatus(w http.ResponseWriter, r *http.Request) {
	factor, sealed, err := s.readFactor(r.Context())
	if err != nil {
		problem(w, 500, "storage_error")
		return
	}
	respond(w, 200, map[string]any{"enabled": sealed != "", "recovery_remaining": len(factor.Recovery)})
}

func (s *Server) factorSetup(w http.ResponseWriter, r *http.Request) {
	var secret [20]byte
	if _, err := rand.Read(secret[:]); err != nil {
		problem(w, 500, "storage_error")
		return
	}
	key := factorEncoding.EncodeToString(secret[:])
	// The authenticator app reads the same key from the otpauth:// QR code.
	account := url.PathEscape("Netlane " + s.cfg.Domain + ":" + s.cfg.Admin)
	// Spaces as %20, not "+": some authenticator apps show a "+" in the issuer literally.
	query := strings.ReplaceAll(url.Values{"secret": {key}, "issuer": {"Netlane " + s.cfg.Domain}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}.Encode(), "+", "%20")
	uri := "otpauth://totp/" + account + "?" + query
	picture, err := qrcode.DataURI(uri)
	if err != nil {
		picture = ""
	}
	respond(w, 200, map[string]string{"secret": key, "qr": picture})
}

func (s *Server) factorChange(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Action   string `json:"action"`
		Password string `json:"password"`
		Secret   string `json:"secret"`
		Code     string `json:"code"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Action != "enable" && input.Action != "disable" && input.Action != "rotate" {
		problem(w, 400, "invalid_factor_action")
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	now := time.Now()
	s.mu.Lock()
	key := "factor:" + ip
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
	select {
	case s.loginGate <- struct{}{}:
		defer func() { <-s.loginGate }()
	default:
		problem(w, 429, "login_busy")
		return
	}
	hash := s.currentHash()
	if !security.CheckPassword(hash, input.Password) {
		problem(w, 403, "invalid_current_password")
		return
	}
	cookie, _ := r.Cookie("tt_session")
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[cookie.Value]; !ok || s.passwordHash != hash {
		problem(w, 401, "authentication_required")
		return
	}
	factor, sealed, err := s.readFactor(r.Context())
	if err != nil {
		problem(w, 500, "storage_error")
		return
	}
	if (input.Action == "enable") != (sealed == "") {
		problem(w, 409, "factor_retry")
		return
	}
	if input.Action == "enable" {
		factor = adminFactor{Secret: input.Secret}
	}
	if !factor.consume(input.Code, now, input.Action != "enable") {
		problem(w, 400, "invalid_second_factor")
		return
	}
	var codes []string
	if input.Action == "disable" {
		factor = adminFactor{}
	} else {
		factor.Recovery = nil
		for i := 0; i < 8; i++ {
			code := security.Token(16)
			codes = append(codes, code)
			factor.Recovery = append(factor.Recovery, recoveryHash(code))
		}
	}
	if !s.saveFactor(r.Context(), sealed, factor) {
		problem(w, 409, "factor_retry")
		return
	}
	for token := range s.sessions {
		if token != cookie.Value {
			delete(s.sessions, token)
		}
	}
	delete(s.attempts, key)
	if err := s.db.Audit(r.Context(), s.cfg.Admin, "admin.2fa_"+input.Action, ip); err != nil {
		log.Printf("auth: audit of 2FA change failed: %v", err)
	}
	respond(w, 200, map[string]any{"enabled": factor.Secret != "", "recovery_codes": codes})
}
