package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/divezh/netlane-server/internal/config"
	"github.com/divezh/netlane-server/internal/store"
)

func TestTOTPVector(t *testing.T) {
	// RFC 6238 appendix B, SHA-1, 59 seconds. Use its six least significant digits.
	if got := totp([]byte("12345678901234567890"), 59/30); got != "287082" {
		t.Fatalf("TOTP = %s", got)
	}
}

func TestAdminFactorLogin(t *testing.T) {
	const password = "administrator-password"
	dir := t.TempDir()
	cfg, err := config.New("vpn.example.net", "admin", password, dir)
	if err != nil {
		t.Fatal(err)
	}
	key, err := cfg.Key()
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "test.sqlite"), key)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	start := func() *httptest.Server {
		handler, err := New(cfg, db, nil)
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		return server
	}
	server := start()
	admin := newClient(t, server.URL)
	status, csrf := admin.login(password)
	if status != 200 || csrf == "" {
		t.Fatal("initial login failed")
	}
	other := newClient(t, server.URL)
	if _, token := other.login(password); token == "" {
		t.Fatal("second session failed")
	}
	status, out := admin.call(http.MethodPost, "/api/v1/auth/2fa/setup", "", csrf)
	secret, _ := out["secret"].(string)
	raw, err := factorEncoding.DecodeString(secret)
	if status != 200 || err != nil || len(raw) != 20 {
		t.Fatal("setup failed")
	}
	if picture, _ := out["qr"].(string); !strings.HasPrefix(picture, "data:image/svg+xml;base64,") {
		t.Fatal("setup has no QR code")
	}
	code := totp(raw, time.Now().Unix()/30)
	status, out = admin.call(http.MethodPost, "/api/v1/auth/2fa", `{"action":"enable","password":`+quoteJSON(password)+`,"secret":`+quoteJSON(secret)+`,"code":`+quoteJSON(code)+`}`, csrf)
	codes, _ := out["recovery_codes"].([]any)
	if status != 200 || len(codes) != 8 {
		t.Fatalf("enable failed: %d", status)
	}
	if status, _ := other.call(http.MethodGet, "/api/v1/auth/session", "", ""); status != 401 {
		t.Fatal("old session was not revoked")
	}
	server.Close()
	server = start()
	client := newClient(t, server.URL)
	login := func(code string) (int, map[string]any) {
		return client.call(http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":`+quoteJSON(password)+`,"code":`+quoteJSON(code)+`}`, "")
	}
	if status, out := login(""); status != 200 || out["totp_required"] != true || out["csrf_token"] != nil {
		t.Fatal("password step must require 2FA after restart")
	}
	if status, _ := client.call(http.MethodGet, "/api/v1/auth/session", "", ""); status != 401 {
		t.Fatal("password step created a session")
	}
	if status, _ := login(code); status != 401 {
		t.Fatal("enrollment code was accepted twice")
	}
	recovery := codes[0].(string)
	status, out = login(recovery)
	sessionCSRF, _ := out["csrf_token"].(string)
	if status != 200 || sessionCSRF == "" {
		t.Fatal("recovery login failed")
	}
	if status, _ := login(recovery); status != 401 {
		t.Fatal("recovery code was accepted twice")
	}
	disable := `{"action":"disable","password":` + quoteJSON(password) + `,"code":` + quoteJSON(codes[1].(string)) + `}`
	if status, _ := client.call(http.MethodPost, "/api/v1/auth/2fa", disable, sessionCSRF); status != 200 {
		t.Fatal("disable failed")
	}
	if status, out := login(""); status != 200 || out["csrf_token"] == nil {
		t.Fatal("password login failed after disable")
	}
}
