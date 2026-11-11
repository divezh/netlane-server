package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/divezh/netlane-server/internal/config"
	"github.com/divezh/netlane-server/internal/store"
)

func TestAuthenticatedUserLifecycle(t *testing.T) {
	dir := t.TempDir()
	cfg, err := config.New("vpn.example.net", "admin", "administrator-password", dir)
	if err != nil {
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
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	call := func(method, path, body, csrf string) (int, map[string]any, string) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(data, &out)
		return resp.StatusCode, out, string(data)
	}
	if status, _, _ := call("GET", "/api/v1/statistics", "", ""); status != 401 {
		t.Fatal("anonymous statistics allowed")
	}
	if status, _, _ := call("GET", "/api/v1/users", "", ""); status != 401 {
		t.Fatal("anonymous access allowed")
	}
	status, out, _ := call("POST", "/api/v1/auth/login", `{"username":"admin","password":"administrator-password"}`, "")
	if status != 200 {
		t.Fatalf("login: %d", status)
	}
	csrf := out["csrf_token"].(string)
	if status, report, _ := call("GET", "/api/v1/statistics?period=1h", "", ""); status != 200 || report["scope"] != "main" {
		t.Fatal("authenticated statistics failed", status, report)
	}
	if status, restored, _ := call("GET", "/api/v1/auth/session", "", ""); status != 200 || restored["csrf_token"] != csrf {
		t.Fatal("session cannot be restored after browser reload")
	}
	body := `{"username":"alex","password":"vpn-password-123","group_id":"all","policy":{"enabled":true,"quota_bytes":1000000000,"download_mbps":20,"upload_mbps":10}}`
	if status, _, _ := call("POST", "/api/v1/users", body, ""); status != 403 {
		t.Fatal("CSRF not enforced")
	}
	status, out, raw := call("POST", "/api/v1/users", body, csrf)
	if status != 201 || strings.Contains(raw, "vpn-password-123") {
		t.Fatalf("create: %d %s", status, raw)
	}
	user := out["user"].(map[string]any)
	id := user["id"].(string)
	profilePath := "/api/v1/users/" + id + "/profile"
	if status, _, _ := call("POST", profilePath, `{"protocol":"http2","anti_dpi":false}`, ""); status != 403 {
		t.Fatal("profile export bypassed CSRF")
	}
	if status, _, _ := call("POST", profilePath, `{"protocol":"http2","anti_dpi":false}`, csrf); status != 409 {
		t.Fatal("profile exported for unapplied user")
	}
	if status, _, _ := call("POST", profilePath, `{"protocol":"invalid"}`, csrf); status != 400 {
		t.Fatal("invalid profile protocol accepted")
	}
	if user["applied_revision"].(float64) != 0 || out["vpn_applied"] != false {
		t.Fatal("unapplied settings advertised as active")
	}
	if status, _, _ := call("POST", "/api/v1/users", body, csrf); status != 409 {
		t.Fatal("duplicate not rejected")
	}
	update := `{"expected_revision":1,"policy":{"enabled":false,"quota_bytes":1000000000,"download_mbps":10,"upload_mbps":10}}`
	if status, _, _ := call("PUT", "/api/v1/users/"+id, update, csrf); status != 200 {
		t.Fatal("update failed")
	}
	if status, _, _ := call("PUT", "/api/v1/users/"+id, update, csrf); status != 409 {
		t.Fatal("stale update accepted")
	}
	if status, _, raw := call("GET", "/api/v1/users", "", ""); status != 200 || bytes.Contains([]byte(raw), []byte("password")) {
		t.Fatal("list leaked credentials")
	}
	if status, events, raw := call("GET", "/api/v1/audit", "", ""); status != 200 || len(events["events"].([]any)) < 2 || strings.Contains(raw, "vpn-password-123") {
		t.Fatal("audit missing or leaked secret")
	}
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/users", strings.NewReader(body))
	req.Header.Set("Origin", "https://attacker.example")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("cross-origin write allowed")
	}
	if status, _, _ := call("POST", "/api/v1/auth/logout", `{}`, csrf); status != 204 {
		t.Fatal("logout failed")
	}
	if status, _, _ := call("GET", "/api/v1/statistics", "", ""); status != 401 {
		t.Fatal("anonymous statistics allowed")
	}
	if status, _, _ := call("GET", "/api/v1/users", "", ""); status != 401 {
		t.Fatal("logged out session still active")
	}
}
