package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/divezh/netlane-server/internal/config"
	"github.com/divezh/netlane-server/internal/store"
)

func TestSessionsListAndLogoutOthers(t *testing.T) {
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
	type browser struct {
		client *http.Client
		csrf   string
	}
	call := func(b *browser, method, path string) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(`{"username":"admin","password":"administrator-password"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "test-browser")
		if b.csrf != "" {
			req.Header.Set("X-CSRF-Token", b.csrf)
		}
		resp, err := b.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(data, &out)
		return resp.StatusCode, out
	}
	login := func() *browser {
		jar, _ := cookiejar.New(nil)
		b := &browser{client: &http.Client{Jar: jar}}
		status, out := call(b, "POST", "/api/v1/auth/login")
		if status != 200 {
			t.Fatalf("login %d", status)
		}
		b.csrf = out["csrf_token"].(string)
		return b
	}
	a, other := login(), login()
	status, out := call(a, "GET", "/api/v1/auth/sessions")
	list, _ := out["sessions"].([]any)
	if status != 200 || len(list) != 2 {
		t.Fatalf("sessions: %d %v", status, out)
	}
	first := list[0].(map[string]any)
	if first["current"] != true || first["agent"] != "test-browser" || first["ip"] != "127.0.0.1" || len(first["id"].(string)) < 8 {
		t.Fatalf("current session first, with its address and browser: %v", first)
	}
	if status, _ := call(a, "POST", "/api/v1/auth/sessions/logout-others"); status != 200 {
		t.Fatalf("logout others %d", status)
	}
	if status, _ := call(other, "GET", "/api/v1/auth/sessions"); status != 401 {
		t.Fatal("the other session must end")
	}
	if status, _ := call(a, "GET", "/api/v1/auth/sessions"); status != 200 {
		t.Fatal("the asking session must stay")
	}
}

func TestIdleSessionEnds(t *testing.T) {
	s := &Server{sessions: map[string]session{"t": {csrf: "c", expires: time.Now().Add(time.Hour), seen: time.Now().Add(-sessionIdle - time.Minute)}}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/info", nil)
	req.AddCookie(&http.Cookie{Name: "tt_session", Value: "t"})
	s.auth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("an idle session was accepted") })).ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("idle session: %d", rec.Code)
	}
}

func TestLoginAttemptsCountIPv6Prefix(t *testing.T) {
	if attemptKey("2001:db8:1:2:aaaa::1") != attemptKey("2001:db8:1:2:bbbb::9") {
		t.Fatal("addresses of one /64 must share a limit")
	}
	if attemptKey("2001:db8:1:3::1") == attemptKey("2001:db8:1:2::1") {
		t.Fatal("different /64 networks are different clients")
	}
	if attemptKey("192.0.2.7") != "192.0.2.7" {
		t.Fatal("IPv4 is counted per address")
	}
}
