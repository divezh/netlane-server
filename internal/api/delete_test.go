//go:build !windows

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

func TestDeleteRequiresConfirmedRevocationAndCanRetryLostAcknowledgement(t *testing.T) {
	dir, err := os.MkdirTemp("", "tt-delete-")
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
	mode := "normal"
	removes := 0
	state := endpoint.ControlState{Version: 1, Generation: 3, Capabilities: map[string]bool{"remove_user": true}}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			func() {
				defer conn.Close()
				var request struct {
					Op                 string `json:"op"`
					ID                 string `json:"id"`
					Username           string `json:"username"`
					ExpectedGeneration uint64 `json:"expected_generation"`
					ExpectedRevision   int64  `json:"expected_revision"`
				}
				if json.NewDecoder(conn).Decode(&request) != nil {
					return
				}
				mu.Lock()
				defer mu.Unlock()
				if request.Op == "remove" {
					removes++
					if request.ID != "" && request.ExpectedGeneration == state.Generation && request.ExpectedRevision == 1 && request.Username == "alice" {
						if mode != "mismatch" {
							state.Users = nil
							state.Generation++
						}
					} else {
						_ = json.NewEncoder(conn).Encode(map[string]any{"ok": false, "error": "revision_conflict"})
						return
					}
					if mode == "lost" {
						_ = json.NewEncoder(conn).Encode(map[string]any{"ok": false, "error": "state_durability_uncertain"})
						return
					}
				}
				_ = json.NewEncoder(conn).Encode(map[string]any{"ok": true, "state": state})
			}()
		}
	}()
	key, _ := cfg.Key()
	db, err := store.Open(filepath.Join(dir, "panel.sqlite"), key)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	u, err := db.Create(context.Background(), "admin", "alice", "test-password-123", store.Policy{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	state.Users = []endpoint.AppliedUser{{ID: u.ID, Username: u.Username, Revision: 1, Enabled: true}}
	h, err := New(cfg, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	csrf := ""
	call := func(method, path, body string, token bool) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(response.Body).Decode(&out)
		return response.StatusCode, out
	}
	path := "/api/v1/users/" + u.ID
	if status, _ := call("DELETE", path, `{"expected_revision":1}`, false); status != 401 {
		t.Fatal("anonymous deletion", status)
	}
	status, session := call("POST", "/api/v1/auth/login", `{"username":"admin","password":"test-admin-password"}`, false)
	if status != 200 {
		t.Fatal("login", status)
	}
	csrf = session["csrf_token"].(string)
	if status, _ := call("DELETE", path, `{"expected_revision":1}`, false); status != 403 {
		t.Fatal("missing CSRF", status)
	}
	if status, _ := call("DELETE", path, `{"expected_revision":2}`, true); status != 409 {
		t.Fatal("stale delete", status)
	}
	mu.Lock()
	if removes != 0 {
		t.Fatal("invalid request reached remove")
	}
	state.Capabilities["remove_user"] = false
	mu.Unlock()
	if status, _ := call("DELETE", path, `{"expected_revision":1}`, true); status != 409 {
		t.Fatal("unsupported endpoint", status)
	}
	mu.Lock()
	state.Capabilities["remove_user"] = true
	mode = "mismatch"
	mu.Unlock()
	if status, _ := call("DELETE", path, `{"expected_revision":1}`, true); status != 502 {
		t.Fatal("false acknowledgement accepted", status)
	}
	if _, _, err := db.Managed(context.Background(), u.ID); err != nil {
		t.Fatal("unconfirmed deletion lost record", err)
	}
	mu.Lock()
	mode = "lost"
	mu.Unlock()
	if status, _ := call("DELETE", path, `{"expected_revision":1}`, true); status != 503 {
		t.Fatal("lost acknowledgement accepted", status)
	}
	if _, _, err := db.Managed(context.Background(), u.ID); err != nil {
		t.Fatal("uncertain deletion lost record", err)
	}
	mu.Lock()
	mode = "normal"
	mu.Unlock()
	if status, out := call("DELETE", path, `{"expected_revision":1}`, true); status != 200 || out["deleted"] != true {
		t.Fatal("retry failed", status, out)
	}
	if _, _, err := db.Managed(context.Background(), u.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("credential retained", err)
	}
	if status, _ := call("DELETE", path, `{"expected_revision":1}`, true); status != 404 {
		t.Fatal("deleted user still exists", status)
	}
	v, err := db.Create(context.Background(), "admin", "bob", "test-password-123", store.Policy{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
	if status, _ := call("DELETE", "/api/v1/users/"+v.ID, `{"expected_revision":1}`, true); status != 503 {
		t.Fatal("offline delete accepted", status)
	}
	if _, _, err := db.Managed(context.Background(), v.ID); err != nil {
		t.Fatal("offline delete lost record", err)
	}
}
