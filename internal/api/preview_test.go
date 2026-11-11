package api

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/divezh/netlane-server/internal/config"
	"github.com/divezh/netlane-server/internal/store"
)

func TestPreviewIsClearlyDemoAndCannotReadRuntimeFiles(t *testing.T) {
	dir := t.TempDir()
	cfg, err := config.New("localhost", "admin", "test-password-123", dir)
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
	// The demo screens need a session like the rest of the console.
	for _, path := range []string{"/preview/", "/preview/app.js"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 401 {
			t.Fatalf("anonymous %s: %d", path, w.Code)
		}
	}
	login := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"test-password-123"}`))
	request.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(login, request)
	cookies := login.Result().Cookies()
	if login.Code != 200 || len(cookies) == 0 {
		t.Fatalf("login: %d", login.Code)
	}
	for _, path := range []string{"/preview/index.html", "/preview/app.js", "/preview/style.css", "/preview/icon.png"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", path, nil)
		for _, c := range cookies {
			r.AddCookie(c)
		}
		h.ServeHTTP(w, r)
		// FileServer canonicalizes index.html to its directory.
		if path == "/preview/index.html" {
			if w.Code != 301 {
				t.Fatalf("index redirect: %d", w.Code)
			}
			continue
		}
		if w.Code != 200 {
			t.Fatalf("preview %s: %d", path, w.Code)
		}
		if path == "/preview/app.js" && !strings.Contains(w.Body.String(), "демонстрационные данные") {
			t.Fatal("demo label missing")
		}
	}
	for _, path := range []string{"/preview/config.json", "/preview/PLAN.md", "/api/v1/users"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code == 200 {
			t.Fatalf("unexpected public data at %s", path)
		}
	}
	for _, path := range []string{"/", "/app.js", "/app.css", "/twemoji/1f1f3-1f1f1.svg"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || strings.Contains(w.Header().Get("Content-Security-Policy"), "unsafe-inline") {
			t.Fatalf("working UI unavailable or permissive CSP at %s", path)
		}
	}
	for _, path := range []string{"/favicon.ico", "/icon.png"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "image/") {
			t.Fatalf("icon unavailable at %s: %d %s", path, w.Code, w.Header().Get("Content-Type"))
		}
	}
	for _, path := range []string{"/config.json", "/web/app.js", "/PLAN.md", "/api/v1/auth/session", "/api/v1/audit", "/api/v1/usage"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code == 200 {
			t.Fatalf("unexpected public data at %s", path)
		}
	}
}
