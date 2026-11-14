package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/divezh/netlane-server/internal/config"
	"github.com/divezh/netlane-server/internal/store"
)

func TestBitTorrentPolicy(t *testing.T) {
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
	s := &Server{cfg: cfg, db: db}
	call := func(method, body string) policyView {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/api/v1/policy", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		s.policy(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", method, rec.Code, rec.Body)
		}
		var v policyView
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := call("GET", ""); !v.BlockBitTorrent || v.Supported {
		t.Fatalf("blocking is on by default; without an endpoint it is not applied: %+v", v)
	}
	if v := call("PUT", `{"block_bittorrent":false}`); v.BlockBitTorrent {
		t.Fatalf("turned off: %+v", v)
	}
	if on, _ := db.BlockBitTorrent(context.Background()); on {
		t.Fatal("the choice must be stored")
	}
	events, _ := db.RecentAudit(context.Background(), 0)
	if len(events) == 0 || events[0].Action != "policy.bittorrent_allowed" {
		t.Fatalf("audit: %+v", events)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/policy", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	s.policy(rec, req)
	if rec.Code != 400 {
		t.Fatalf("a body without the switch: %d", rec.Code)
	}
}
