//go:build !windows

package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/divezh/netlane-server/internal/config"
	"github.com/divezh/netlane-server/internal/endpoint"
	"github.com/divezh/netlane-server/internal/store"
)

func TestExpiryCapabilityAndAcknowledgement(t *testing.T) {
	dir, err := os.MkdirTemp("", "tt-expiry-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	cfg, err := config.New("localhost", "admin", "test-admin-password", dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ControlSocket = filepath.Join(dir, "control.sock")
	listener, err := net.Listen("unix", cfg.ControlSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var mu sync.Mutex
	supported, mismatch, updates := false, false, 0
	quotaSupported, quotaMismatch := false, false
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			func() {
				defer conn.Close()
				var req struct {
					Op   string               `json:"op"`
					User endpoint.ManagedUser `json:"user"`
				}
				if json.NewDecoder(conn).Decode(&req) != nil {
					return
				}
				mu.Lock()
				defer mu.Unlock()
				state := endpoint.ControlState{Version: 1, Capabilities: map[string]bool{"expiry": supported, "durable_quotas": quotaSupported}}
				if req.Op == "upsert" {
					updates++
					u := req.User
					expiry := u.ExpiresAt
					quota := u.QuotaBytes
					if quotaMismatch {
						quota = 0
					}
					if mismatch {
						expiry = ""
					}
					state.Users = []endpoint.AppliedUser{{ID: u.ID, Username: u.Username, Enabled: u.Enabled, Revision: u.Revision, ExpiresAt: expiry, DownloadMbps: u.DownloadMbps, UploadMbps: u.UploadMbps, QuotaBytes: quota}}
				}
				_ = json.NewEncoder(conn).Encode(map[string]any{"ok": true, "state": state})
			}()
		}
	}()
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
	csrf := ""
	call := func(path, body string) int {
		t.Helper()
		req, _ := http.NewRequest("POST", srv.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", csrf)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		if path == "/api/v1/auth/login" && res.StatusCode == 200 {
			csrf = out["csrf_token"].(string)
		}
		return res.StatusCode
	}
	if call("/api/v1/auth/login", `{"username":"admin","password":"test-admin-password"}`) != 200 {
		t.Fatal("login failed")
	}
	policy := store.Policy{Enabled: true, ExpiresAt: "2099-01-01T00:00:00Z"}
	u, err := db.Create(context.Background(), "admin", "expiry", "test-vpn-password", policy)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/users/" + u.ID + "/apply"
	if call(path, "") != 409 {
		t.Fatal("legacy endpoint accepted enabled expiry")
	}
	mu.Lock()
	if updates != 0 {
		t.Error("unsupported policy sent to endpoint")
	}
	mu.Unlock()
	policy.Enabled = false
	u, err = db.UpdatePolicy(context.Background(), "admin", u.ID, u.DesiredRevision, policy)
	if err != nil {
		t.Fatal(err)
	}
	if call(path, "") != 200 {
		t.Fatal("legacy endpoint could not disable user with saved expiry")
	}
	mu.Lock()
	supported = true
	mu.Unlock()
	policy.Enabled = true
	u, err = db.UpdatePolicy(context.Background(), "admin", u.ID, u.DesiredRevision, policy)
	if err != nil {
		t.Fatal(err)
	}
	if call(path, "") != 200 {
		t.Fatal("expiry-capable endpoint rejected policy")
	}
	mu.Lock()
	mismatch = true
	mu.Unlock()
	u, err = db.UpdatePolicy(context.Background(), "admin", u.ID, u.DesiredRevision, policy)
	if err != nil {
		t.Fatal(err)
	}
	if call(path, "") != 502 {
		t.Fatal("expiry acknowledgement mismatch accepted")
	}
	saved, _, err := db.Managed(context.Background(), u.ID)
	if err != nil || saved.AppliedRevision == saved.DesiredRevision {
		t.Fatal("bad acknowledgement recorded as applied")
	}
	mu.Lock()
	mismatch = false
	mu.Unlock()
	policy.QuotaBytes = 1024
	u, err = db.UpdatePolicy(context.Background(), "admin", u.ID, u.DesiredRevision, policy)
	if err != nil {
		t.Fatal(err)
	}
	if call(path, "") != 409 {
		t.Fatal("legacy endpoint accepted quota")
	}
	policy.Enabled = false
	u, err = db.UpdatePolicy(context.Background(), "admin", u.ID, u.DesiredRevision, policy)
	if err != nil {
		t.Fatal(err)
	}
	if call(path, "") != 200 {
		t.Fatal("legacy endpoint cannot disable user with saved quota")
	}
	mu.Lock()
	quotaSupported = true
	mu.Unlock()
	policy.Enabled = true
	u, err = db.UpdatePolicy(context.Background(), "admin", u.ID, u.DesiredRevision, policy)
	if err != nil {
		t.Fatal(err)
	}
	if call(path, "") != 200 {
		t.Fatal("quota-capable endpoint rejected quota")
	}
	mu.Lock()
	quotaMismatch = true
	mu.Unlock()
	policy.QuotaBytes = 2048
	u, err = db.UpdatePolicy(context.Background(), "admin", u.ID, u.DesiredRevision, policy)
	if err != nil {
		t.Fatal(err)
	}
	if call(path, "") != 502 {
		t.Fatal("quota acknowledgement mismatch accepted")
	}
}
