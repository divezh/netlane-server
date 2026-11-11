package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/divezh/netlane-server/internal/config"
	"github.com/divezh/netlane-server/internal/security"
	"github.com/divezh/netlane-server/internal/store"
)

type apiClient struct {
	t    *testing.T
	base string
	http *http.Client
}

func newClient(t *testing.T, base string) *apiClient {
	jar, _ := cookiejar.New(nil)
	return &apiClient{t: t, base: base, http: &http.Client{Jar: jar}}
}

func (c *apiClient) call(method, path, body, csrf string) (int, map[string]any) {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.base+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return resp.StatusCode, out
}

func (c *apiClient) login(password string) (int, string) {
	status, out := c.call("POST", "/api/v1/auth/login", `{"username":"admin","password":`+quoteJSON(password)+`}`, "")
	csrf, _ := out["csrf_token"].(string)
	return status, csrf
}

func quoteJSON(v string) string { b, _ := json.Marshal(v); return string(b) }

func TestPasswordChangeAndLoginJournal(t *testing.T) {
	var journal bytes.Buffer
	log.SetOutput(&journal)
	defer log.SetOutput(os.Stderr)
	dir := t.TempDir()
	cfg, err := config.New("vpn.example.net", "admin", "administrator-password", dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err = config.SaveNew(path, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg, err = config.Load(path); err != nil {
		t.Fatal(err)
	}
	key, _ := cfg.Key()
	db, err := store.Open(filepath.Join(dir, "db.sqlite"), key)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, err := New(cfg, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	attacker := newClient(t, srv.URL)
	if status, _ := attacker.login("wrong-password-value"); status != 401 {
		t.Fatalf("wrong password: %d", status)
	}
	if !strings.Contains(journal.String(), `auth: login failed user="admin" ip=127.0.0.1`) {
		t.Fatalf("failed login not journaled for fail2ban: %q", journal.String())
	}
	other := newClient(t, srv.URL)
	if status, _ := other.login("administrator-password"); status != 200 {
		t.Fatal("second session login failed")
	}
	admin := newClient(t, srv.URL)
	status, csrf := admin.login("administrator-password")
	if status != 200 {
		t.Fatal("login failed")
	}
	if status, _ := admin.call("POST", "/api/v1/auth/password", `{"current_password":"administrator-password","new_password":"brand-new-password"}`, ""); status != 403 {
		t.Fatal("password change bypassed CSRF")
	}
	if status, out := admin.call("POST", "/api/v1/auth/password", `{"current_password":"not-the-password","new_password":"brand-new-password"}`, csrf); status != 403 || out["error"] != "invalid_current_password" {
		t.Fatalf("wrong current password accepted: %d %v", status, out)
	}
	if status, _ := admin.call("POST", "/api/v1/auth/password", `{"current_password":"administrator-password","new_password":"short"}`, csrf); status != 400 {
		t.Fatal("weak password accepted")
	}
	if status, _ := admin.call("POST", "/api/v1/auth/password", `{"current_password":"administrator-password","new_password":"brand-new-password"}`, csrf); status != 200 {
		t.Fatal("password change failed")
	}
	saved, err := config.Load(path)
	if err != nil || !security.CheckPassword(saved.PasswordHash, "brand-new-password") || saved.EncryptionKey != cfg.EncryptionKey {
		t.Fatal("new password not persisted or config damaged")
	}
	if status, _ := admin.call("GET", "/api/v1/users", "", ""); status != 200 {
		t.Fatal("current session revoked by its own password change")
	}
	if status, _ := other.call("GET", "/api/v1/users", "", ""); status != 401 {
		t.Fatal("other sessions survived password change")
	}
	fresh := newClient(t, srv.URL)
	if status, _ := fresh.login("administrator-password"); status != 401 {
		t.Fatal("old password still accepted")
	}
	if status, _ := fresh.login("brand-new-password"); status != 200 {
		t.Fatal("new password rejected")
	}
	_, events := admin.call("GET", "/api/v1/audit", "", "")
	actions := ""
	for _, e := range events["events"].([]any) {
		actions += e.(map[string]any)["action"].(string) + " "
	}
	if !strings.Contains(actions, "admin.password_changed") || !strings.Contains(actions, "auth.login") {
		t.Fatalf("audit missing events: %s", actions)
	}
	if strings.Contains(journal.String(), "brand-new-password") || strings.Contains(journal.String(), "administrator-password") {
		t.Fatal("password written to journal")
	}
}

func TestSecurityHeadersAndLocalFonts(t *testing.T) {
	dir := t.TempDir()
	cfg, _ := config.New("vpn.example.net", "admin", "administrator-password", dir)
	key, _ := cfg.Key()
	db, err := store.Open(filepath.Join(dir, "db.sqlite"), key)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, err := New(cfg, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(h)
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Strict-Transport-Security"), "max-age=") {
		t.Fatal("HSTS missing on TLS")
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if strings.Contains(csp, "googleapis") || strings.Contains(csp, "gstatic") || !strings.Contains(csp, "font-src 'self'") {
		t.Fatalf("CSP still allows external fonts: %s", csp)
	}
	css, err := srv.Client().Get(srv.URL + "/app.css")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(css.Body)
	css.Body.Close()
	if bytes.Contains(body, []byte("googleapis")) {
		t.Fatal("stylesheet still imports Google Fonts")
	}
	font, err := srv.Client().Get(srv.URL + "/fonts/ibm-plex-sans-cyrillic-400-normal.woff2")
	if err != nil {
		t.Fatal(err)
	}
	font.Body.Close()
	if font.StatusCode != 200 {
		t.Fatalf("local font unavailable: %d", font.StatusCode)
	}
}
