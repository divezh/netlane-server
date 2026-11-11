//go:build !windows

package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/divezh/netlane-server/internal/config"
	"github.com/divezh/netlane-server/internal/endpoint"
	"github.com/divezh/netlane-server/internal/store"
)

// fakeEndpoint answers status, upsert and disconnect like the managed endpoint.
func fakeEndpoint(t *testing.T, path string) *sync.Mutex {
	t.Helper()
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	var mu sync.Mutex
	state := endpoint.ControlState{Version: 1, Generation: 1, Capabilities: map[string]bool{"remove_user": true, "expiry": true}}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			func() {
				defer conn.Close()
				var req struct {
					Op       string               `json:"op"`
					User     endpoint.ManagedUser `json:"user"`
					Username string               `json:"username"`
				}
				if json.NewDecoder(conn).Decode(&req) != nil {
					return
				}
				mu.Lock()
				defer mu.Unlock()
				switch req.Op {
				case "upsert":
					u := req.User
					state.Users = append(state.Users, endpoint.AppliedUser{ID: u.ID, Username: u.Username, Enabled: u.Enabled, DownloadMbps: u.DownloadMbps, UploadMbps: u.UploadMbps, ExpiresAt: u.ExpiresAt, Revision: u.Revision})
					state.Generation++
				case "disconnect":
					json.NewEncoder(conn).Encode(map[string]any{"ok": true, "disconnect_requested": 0})
					return
				}
				json.NewEncoder(conn).Encode(map[string]any{"ok": true, "state": state})
			}()
		}
	}()
	return &mu
}

type panelFixture struct {
	db     *store.Store
	hub    *Hub
	client *apiClient
	csrf   string
}

func newPanelFixture(t *testing.T) *panelFixture {
	t.Helper()
	dir, err := os.MkdirTemp("", "tt-nodes-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfg, err := config.New("vpn.example.com", "admin", "test-admin-password", dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.SyncPrefix = testPrefix
	cfg.ControlSocket = filepath.Join(dir, "control.sock")
	fakeEndpoint(t, cfg.ControlSocket)
	key, _ := cfg.Key()
	db, err := store.Open(filepath.Join(dir, "panel.sqlite"), key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	hub := NewHub()
	h, err := New(cfg, db, hub)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := newClient(t, srv.URL)
	status, csrf := c.login("test-admin-password")
	if status != 200 {
		t.Fatal("login", status)
	}
	return &panelFixture{db: db, hub: hub, client: c, csrf: csrf}
}

func (f *panelFixture) addNode(t *testing.T, name string, seen time.Time) store.Node {
	t.Helper()
	ctx := context.Background()
	code, _, _ := f.db.CreateInvite(ctx, "admin", time.Now())
	n, _, err := f.db.JoinNode(ctx, code, store.NodeJoin{Name: name, Domain: strings.ToLower(name) + ".example.com"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.RecordNodeReport(ctx, n.ID, store.NodeReport{}, seen); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestInviteCommandAndQuotaGuard(t *testing.T) {
	f := newPanelFixture(t)
	status, out := f.client.call("POST", "/api/v1/users", `{"username":"heavy","password":"heavy-password-123","group_id":"all","policy":{"enabled":true,"quota_bytes":1000}}`, f.csrf)
	if status != 201 {
		t.Fatal(status, out)
	}
	status, out = f.client.call("POST", "/api/v1/nodes/invite", `{}`, f.csrf)
	if status != 409 || out["error"] != "quota_set_remove_first" || !strings.Contains(strings.Join(toStrings(out["users"]), ","), "heavy") {
		t.Fatal(status, out)
	}
	user := out
	_ = user
	_, list := f.client.call("GET", "/api/v1/users", "", "")
	id := list["users"].([]any)[0].(map[string]any)["id"].(string)
	if status, out = f.client.call("PUT", "/api/v1/users/"+id, `{"expected_revision":1,"policy":{"enabled":true}}`, f.csrf); status != 200 {
		t.Fatal(status, out)
	}
	status, out = f.client.call("POST", "/api/v1/nodes/invite", `{}`, f.csrf)
	cmd, _ := out["command"].(string)
	if status != 200 || !strings.Contains(cmd, "--join 'https://vpn.example.com/"+testPrefix+"/node/join#") || out["expires_at"] == nil {
		t.Fatal(status, out)
	}
}

func toStrings(v any) []string {
	var out []string
	items, _ := v.([]any)
	for _, item := range items {
		s, _ := item.(string)
		out = append(out, s)
	}
	return out
}

func TestQuotaRejectedWithNodes(t *testing.T) {
	f := newPanelFixture(t)
	f.addNode(t, "Berlin", time.Now())
	status, out := f.client.call("POST", "/api/v1/users", `{"username":"heavy","password":"heavy-password-123","group_id":"all","policy":{"enabled":true,"quota_bytes":1000}}`, f.csrf)
	if status != 409 || out["error"] != "quota_not_supported_with_nodes" {
		t.Fatal(status, out)
	}
	f.client.call("POST", "/api/v1/users", `{"username":"light","password":"light-password-123","group_id":"all","policy":{"enabled":true}}`, f.csrf)
	_, list := f.client.call("GET", "/api/v1/users", "", "")
	id := list["users"].([]any)[0].(map[string]any)["id"].(string)
	if status, out = f.client.call("PUT", "/api/v1/users/"+id, `{"expected_revision":1,"policy":{"enabled":true,"quota_bytes":5}}`, f.csrf); status != 409 || out["error"] != "quota_not_supported_with_nodes" {
		t.Fatal(status, out)
	}
}

func TestLocationsCount(t *testing.T) {
	f := newPanelFixture(t)
	ctx := context.Background()
	confirmed := f.addNode(t, "Berlin", time.Now())
	f.addNode(t, "Paris", time.Now())
	status, out := f.client.call("POST", "/api/v1/users", `{"username":"alice","password":"alice-password-123","group_id":"all","policy":{"enabled":true}}`, f.csrf)
	if status != 201 {
		t.Fatal(status, out)
	}
	id := out["user"].(map[string]any)["id"].(string)
	if status, out = f.client.call("POST", "/api/v1/users/"+id+"/apply", `{}`, f.csrf); status != 200 {
		t.Fatal(status, out)
	}
	f.db.RecordNodeReport(ctx, confirmed.ID, store.NodeReport{Users: []store.UserRevision{{ID: id, Revision: 1}}}, time.Now())
	_, list := f.client.call("GET", "/api/v1/users", "", "")
	loc := list["users"].([]any)[0].(map[string]any)["locations"].(map[string]any)
	if loc["applied"] != 2.0 || loc["total"] != 3.0 {
		t.Fatal(loc)
	}
	_, nodes := f.client.call("GET", "/api/v1/nodes", "", "")
	if len(nodes["nodes"].([]any)) != 2 || nodes["nodes"].([]any)[0].(map[string]any)["online"] != true {
		t.Fatal(nodes)
	}
}

func TestSubscriptionURLStable(t *testing.T) {
	f := newPanelFixture(t)
	_, out := f.client.call("POST", "/api/v1/users", `{"username":"alice","password":"alice-password-123","group_id":"all","policy":{"enabled":true}}`, f.csrf)
	id := out["user"].(map[string]any)["id"].(string)
	_, a := f.client.call("POST", "/api/v1/users/"+id+"/subscription", `{"rotate":false}`, f.csrf)
	_, b := f.client.call("POST", "/api/v1/users/"+id+"/subscription", `{"rotate":false}`, f.csrf)
	_, c := f.client.call("POST", "/api/v1/users/"+id+"/subscription", `{"rotate":true}`, f.csrf)
	url, _ := a["url"].(string)
	if !strings.HasPrefix(url, "https://vpn.example.com/"+testPrefix+"/sub/") || b["url"] != url || c["url"] == url {
		t.Fatal(a, b, c)
	}
	if picture, _ := a["qr"].(string); !strings.HasPrefix(picture, "data:image/svg+xml;base64,") || picture == c["qr"] {
		t.Fatal("subscription QR code missing or not rotated")
	}
}

func TestApplyNotifiesHub(t *testing.T) {
	f := newPanelFixture(t)
	_, out := f.client.call("POST", "/api/v1/users", `{"username":"alice","password":"alice-password-123","group_id":"all","policy":{"enabled":true}}`, f.csrf)
	id := out["user"].(map[string]any)["id"].(string)
	ticket := f.hub.Ticket()
	if status, out := f.client.call("POST", "/api/v1/users/"+id+"/apply", `{}`, f.csrf); status != 200 {
		t.Fatal(status, out)
	}
	if !f.hub.Wait(context.Background(), ticket, 100*time.Millisecond) {
		t.Fatal("apply did not wake nodes")
	}
	f.addNode(t, "Berlin", time.Now())
	ticket = f.hub.Ticket()
	if status, out := f.client.call("POST", "/api/v1/users/"+id+"/disconnect", `{}`, f.csrf); status != 200 {
		t.Fatal(status, out)
	}
	if !f.hub.Wait(context.Background(), ticket, 100*time.Millisecond) {
		t.Fatal("disconnect did not wake nodes")
	}
}

func TestNodeUsageAddsToTotals(t *testing.T) {
	f := newPanelFixture(t)
	ctx := context.Background()
	n := f.addNode(t, "Berlin", time.Now())
	u, _ := f.db.Create(ctx, "admin", "alice", "alice-password-123", store.Policy{Enabled: true})
	f.db.RecordNodeReport(ctx, n.ID, store.NodeReport{Usage: []store.UsageRow{{UserID: u.ID, Month: "2026-11", UploadBytes: 5, DownloadBytes: 7}}}, time.Now())
	s := &Server{db: f.db}
	report := &endpoint.UsageReport{UsageMeta: endpoint.UsageMeta{Month: "2026-11"}, Records: []endpoint.UsageRecord{{UserID: u.ID, Month: "2026-11", UploadBytes: 1, DownloadBytes: 1, ChargedBytes: 2}}, Totals: endpoint.UsageTotals{ChargedBytes: 2, UploadBytes: 1, DownloadBytes: 1}}
	if err := s.addNodeUsage(ctx, report); err != nil {
		t.Fatal(err)
	}
	if report.Records[0].ChargedBytes != 14 || report.Totals.ChargedBytes != 14 || report.Totals.UploadBytes != 6 {
		t.Fatalf("%+v %+v", report.Records[0], report.Totals)
	}
}

func TestDeleteNodeWakesWaiters(t *testing.T) {
	f := newPanelFixture(t)
	n := f.addNode(t, "Berlin", time.Now())
	ticket := f.hub.Ticket()
	if status, out := f.client.call("DELETE", "/api/v1/nodes/"+n.ID, "", f.csrf); status != 204 {
		t.Fatal(status, out)
	}
	if !f.hub.Wait(context.Background(), ticket, 100*time.Millisecond) {
		t.Fatal("deleted node keeps waiting in its long poll")
	}
}
