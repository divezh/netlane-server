package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/divezh/netlane-server/internal/config"
	"github.com/divezh/netlane-server/internal/store"
)

// consoleClient starts a panel and returns a caller that keeps the session cookie.
func consoleClient(t *testing.T) (*store.Store, func(method, path, body, csrf string) (int, map[string]any)) {
	t.Helper()
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
	t.Cleanup(func() { db.Close() })
	h, err := New(cfg, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	return db, func(method, path, body, csrf string) (int, map[string]any) {
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
		return resp.StatusCode, out
	}
}

func TestAuditRecordsFailedLoginsAndPages(t *testing.T) {
	db, call := consoleClient(t)
	if status, _ := call("POST", "/api/v1/auth/login", `{"username":"admin","password":"wrong-password"}`, ""); status != 401 {
		t.Fatalf("wrong password: %d", status)
	}
	if status, _ := call("POST", "/api/v1/auth/login", `{"username":"admin","password":"administrator-password"}`, ""); status != 200 {
		t.Fatalf("login: %d", status)
	}
	for i := 0; i < 130; i++ {
		if err := db.Audit(context.Background(), "admin", "group.create", "group-"+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	status, first := call("GET", "/api/v1/audit", "", "")
	events, _ := first["events"].([]any)
	if status != 200 || len(events) != store.AuditPage || first["more"] != true {
		t.Fatalf("first page: %d, %d events, more=%v", status, len(events), first["more"])
	}
	oldest := int64(events[len(events)-1].(map[string]any)["id"].(float64))
	status, second := call("GET", "/api/v1/audit?before="+strconv.FormatInt(oldest, 10), "", "")
	older, _ := second["events"].([]any)
	if status != 200 || len(older) != 132-store.AuditPage || second["more"] != false {
		t.Fatalf("second page: %d, %d events, more=%v", status, len(older), second["more"])
	}
	failed := older[len(older)-1].(map[string]any)
	if failed["action"] != "auth.login_failed" || failed["target"] != "127.0.0.1" {
		t.Fatalf("the oldest event is not the failed login: %v", failed)
	}
	if status, _ := call("GET", "/api/v1/audit?before=x", "", ""); status != 400 {
		t.Fatalf("bad cursor: %d", status)
	}
}

func TestPreviewNeedsSession(t *testing.T) {
	_, call := consoleClient(t)
	if status, _ := call("GET", "/preview/index.html", "", ""); status != 401 {
		t.Fatalf("anonymous preview: %d", status)
	}
	if status, _ := call("POST", "/api/v1/auth/login", `{"username":"admin","password":"administrator-password"}`, ""); status != 200 {
		t.Fatalf("login: %d", status)
	}
	if status, _ := call("GET", "/preview/index.html", "", ""); status != 200 {
		t.Fatalf("preview after login: %d", status)
	}
}

func TestNodeUpdateValidates(t *testing.T) {
	_, call := consoleClient(t)
	_, out := call("POST", "/api/v1/auth/login", `{"username":"admin","password":"administrator-password"}`, "")
	csrf := out["csrf_token"].(string)
	if status, _ := call("PUT", "/api/v1/nodes/missing", `{"name":"Варшава","country":"pl"}`, csrf); status != 404 {
		t.Fatalf("unknown node: %d", status)
	}
	if status, _ := call("PUT", "/api/v1/nodes/missing", `{"name":"Варшава","country":"POL"}`, csrf); status != 400 {
		t.Fatalf("bad country: %d", status)
	}
	if status, _ := call("PUT", "/api/v1/nodes/missing", `{"name":"Варшава","country":"PL"}`, ""); status != 403 {
		t.Fatalf("CSRF: %d", status)
	}
}
