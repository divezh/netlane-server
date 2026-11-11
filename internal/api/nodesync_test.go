package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/divezh/netlane-server/internal/endpoint"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/divezh/netlane-server/internal/config"
	"github.com/divezh/netlane-server/internal/decoy"
	"github.com/divezh/netlane-server/internal/store"
)

const testPrefix = "abcdefghijklmnopqrstuvwxyz"

type syncFixture struct {
	db   *store.Store
	hub  *Hub
	srv  *httptest.Server
	site http.Handler
	now  time.Time
}

func newSyncFixture(t *testing.T) *syncFixture {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "panel.sqlite"), bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	f := &syncFixture{db: db, hub: NewHub(), site: decoy.Handler(""), now: time.Now().UTC()}
	f.hub.SetIdentity("Амстердам", "NL")
	cfg := config.Config{Domain: "ams.example.com", SyncPrefix: testPrefix}
	f.srv = httptest.NewServer(SyncHandler(cfg, db, f.hub, f.site, func() time.Time { return f.now }))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *syncFixture) do(t *testing.T, method, path, token, body string) (int, http.Header, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, data
}

func (f *syncFixture) siteAnswer(method, path string) (int, string, []byte) {
	rec := httptest.NewRecorder()
	f.site.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader("{}")))
	return rec.Code, rec.Header().Get("Server"), rec.Body.Bytes()
}

func (f *syncFixture) join(t *testing.T) (string, string) {
	t.Helper()
	code, _, err := f.db.CreateInvite(context.Background(), "admin", f.now)
	if err != nil {
		t.Fatal(err)
	}
	status, _, body := f.do(t, "POST", "/"+testPrefix+"/node/join", "", `{"code":"`+code+`","name":"Франкфурт","country":"DE","domain":"de.example.com","endpoint_version":"1.0"}`)
	if status != 200 {
		t.Fatalf("join %d %s", status, body)
	}
	var out struct {
		NodeID    string `json:"node_id"`
		NodeToken string `json:"node_token"`
	}
	json.Unmarshal(body, &out)
	if out.NodeToken == "" {
		t.Fatalf("no token: %s", body)
	}
	return out.NodeID, out.NodeToken
}

func (f *syncFixture) appliedUser(t *testing.T, name string, p store.Policy) store.User {
	t.Helper()
	ctx := context.Background()
	u, err := f.db.Create(ctx, "admin", name, name+"-password-123", p)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.MarkApplied(ctx, "admin", u.ID, 1); err != nil {
		t.Fatal(err)
	}
	u.AppliedRevision = 1
	return u
}

func TestPrefixMissesLookLikeDecoy(t *testing.T) {
	f := newSyncFixture(t)
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/" + testPrefix + "/x", ""},
		{"GET", "/" + testPrefix + "/sub/bad", ""},
		{"POST", "/" + testPrefix + "/node/join", `{"code":"bad","name":"X","domain":"x.example.com"}`},
		{"PUT", "/" + testPrefix + "/node/sync", "{}"},
		{"GET", "/" + testPrefix + "/node/sync", ""},
		{"POST", "/" + testPrefix + "/node/sync", "{}"},
		{"GET", "/missing.html", ""},
	} {
		status, header, body := f.do(t, c.method, c.path, "", c.body)
		wantStatus, wantServer, wantBody := f.siteAnswer(c.method, c.path)
		if status != wantStatus || header.Get("Server") != wantServer || !bytes.Equal(body, wantBody) {
			t.Errorf("%s %s: got %d %q, decoy %d %q", c.method, c.path, status, header.Get("Server"), wantStatus, wantServer)
		}
	}
}

func TestSyncUnknownTokenIs401(t *testing.T) {
	f := newSyncFixture(t)
	status, _, body := f.do(t, "POST", "/"+testPrefix+"/node/sync", "nope", `{"revision":0}`)
	if status != 401 || len(body) != 0 {
		t.Fatalf("%d %q", status, body)
	}
}

type syncReply struct {
	Revision  int64                   `json:"revision"`
	Unchanged bool                    `json:"unchanged"`
	Users     []store.DistributedUser `json:"users"`
	Commands  []store.Command         `json:"commands"`
}

func (f *syncFixture) sync(t *testing.T, token string, revision int64, acked []int64) syncReply {
	t.Helper()
	body, _ := json.Marshal(store.NodeReport{Revision: revision, Acked: acked})
	status, _, data := f.do(t, "POST", "/"+testPrefix+"/node/sync", token, string(body))
	if status != 200 {
		t.Fatalf("sync %d %s", status, data)
	}
	var r syncReply
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestJoinThenSyncReturnsSnapshot(t *testing.T) {
	f := newSyncFixture(t)
	f.appliedUser(t, "alice", store.Policy{Enabled: true, DownloadMbps: 30})
	_, token := f.join(t)
	r := f.sync(t, token, 0, nil)
	if r.Unchanged || r.Revision != 1 || len(r.Users) != 1 || r.Users[0].Password != "alice-password-123" || r.Users[0].DownloadMbps != 30 {
		t.Fatalf("%+v", r)
	}
}

func TestSyncLongPollWakesOnNotify(t *testing.T) {
	old := longPoll
	longPoll = 300 * time.Millisecond
	defer func() { longPoll = old }()
	f := newSyncFixture(t)
	f.appliedUser(t, "alice", store.Policy{Enabled: true})
	_, token := f.join(t)
	if r := f.sync(t, token, 1, nil); !r.Unchanged || r.Revision != 1 {
		t.Fatalf("expected unchanged: %+v", r)
	}
	longPoll = 5 * time.Second
	go func() {
		time.Sleep(100 * time.Millisecond)
		f.appliedUser(t, "bob", store.Policy{Enabled: true})
		f.hub.Notify()
	}()
	start := time.Now()
	r := f.sync(t, token, 1, nil)
	if time.Since(start) > 2*time.Second || r.Revision != 2 || len(r.Users) != 2 {
		t.Fatalf("not woken: %v %+v", time.Since(start), r)
	}
}

func TestSyncReturnsPendingCommandImmediately(t *testing.T) {
	old := longPoll
	longPoll = 5 * time.Second
	defer func() { longPoll = old }()
	f := newSyncFixture(t)
	f.appliedUser(t, "alice", store.Policy{Enabled: true})
	_, token := f.join(t)
	f.db.QueueDisconnect(context.Background(), "alice", f.now)
	start := time.Now()
	r := f.sync(t, token, 1, nil)
	if time.Since(start) > time.Second || len(r.Commands) != 1 || r.Commands[0].Username != "alice" {
		t.Fatalf("%v %+v", time.Since(start), r)
	}
	longPoll = 200 * time.Millisecond
	if r = f.sync(t, token, 1, []int64{r.Commands[0].ID}); len(r.Commands) != 0 {
		t.Fatalf("acked command repeated: %+v", r)
	}
}

func TestSubscriptionLists(t *testing.T) {
	f := newSyncFixture(t)
	ctx := context.Background()
	u := f.appliedUser(t, "alice", store.Policy{Enabled: true})
	nodeID, token := f.join(t)
	f.sync(t, token, 0, nil)
	// A node that has not synced for 3 minutes (offline, or its VPS is gone) is left out.
	stale, staleToken := f.join(t)
	f.now = f.now.Add(-3 * time.Minute)
	f.sync(t, staleToken, 0, nil)
	f.now = f.now.Add(3 * time.Minute)
	_ = stale
	sub, _ := f.db.Subscription(ctx, "admin", u.ID, false)
	status, header, body := f.do(t, "GET", "/"+testPrefix+"/sub/"+sub, "", "")
	if status != 200 || !strings.HasPrefix(header.Get("Content-Type"), "application/json") || header.Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %v", status, header)
	}
	var feed struct {
		Version     int    `json:"version"`
		Name        string `json:"name"`
		UpdateHours int    `json:"update_hours"`
		Servers     []struct{ ID, Name, Country, Link string }
	}
	json.Unmarshal(body, &feed)
	if feed.Version != 1 || feed.Name != "🇳🇱 Амстердам" || feed.UpdateHours != 12 || len(feed.Servers) != 2 {
		t.Fatalf("%s", body)
	}
	if feed.Servers[0].ID != "main" || feed.Servers[1].ID != nodeID || feed.Servers[1].Name != "🇩🇪 Франкфурт" || feed.Servers[1].Country != "DE" {
		t.Fatalf("%s", body)
	}
	if !strings.HasPrefix(feed.Servers[1].Link, "tt://?") || feed.Servers[1].Link == feed.Servers[0].Link {
		t.Fatalf("links %s", body)
	}
	// A disabled user gets an empty list.
	if _, err := f.db.UpdatePolicy(ctx, "admin", u.ID, 1, store.Policy{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	f.db.MarkApplied(ctx, "admin", u.ID, 2)
	_, _, body = f.do(t, "GET", "/"+testPrefix+"/sub/"+sub, "", "")
	if !strings.Contains(string(body), `"servers":[]`) {
		t.Fatalf("disabled user got servers: %s", body)
	}
}

// The endpoint's reverse proxy drops request bodies, so nodes send their data in a header.
func TestSyncAcceptsHeaderPayload(t *testing.T) {
	f := newSyncFixture(t)
	f.appliedUser(t, "alice", store.Policy{Enabled: true})
	code, _, _ := f.db.CreateInvite(context.Background(), "admin", f.now)
	join, _ := EncodePayload([]byte(`{"code":"` + code + `","name":"Берлин","country":"DE","domain":"ber.example.com"}`))
	req, _ := http.NewRequest("POST", f.srv.URL+"/"+testPrefix+"/node/join", nil)
	req.Header.Set("X-Sync-Data", join)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode, err)
	}
	var out struct {
		Token string `json:"node_token"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	report, _ := EncodePayload([]byte(`{"revision":0}`))
	req, _ = http.NewRequest("POST", f.srv.URL+"/"+testPrefix+"/node/sync", nil)
	req.Header.Set("Authorization", "Bearer "+out.Token)
	req.Header.Set("X-Sync-Data", report)
	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode, err)
	}
	var r syncReply
	json.NewDecoder(resp.Body).Decode(&r)
	resp.Body.Close()
	if len(r.Users) != 1 {
		t.Fatalf("%+v", r)
	}
	if _, err := DecodePayload("!!!"); err == nil {
		t.Fatal("garbage payload accepted")
	}
}

// A saved but unapplied edit must not empty the subscription: it follows what nodes have.
func TestSubscriptionFollowsAppliedSnapshot(t *testing.T) {
	f := newSyncFixture(t)
	ctx := context.Background()
	u := f.appliedUser(t, "alice", store.Policy{Enabled: true})
	if _, err := f.db.UpdatePolicy(ctx, "admin", u.ID, 1, store.Policy{Enabled: true, DownloadMbps: 5}); err != nil {
		t.Fatal(err)
	}
	sub, _ := f.db.Subscription(ctx, "admin", u.ID, false)
	_, _, body := f.do(t, "GET", "/"+testPrefix+"/sub/"+sub, "", "")
	if strings.Contains(string(body), `"servers":[]`) || !strings.Contains(string(body), `"id":"main"`) {
		t.Fatalf("pending edit emptied the feed: %s", body)
	}
}

// A node receives only the users whose group covers it, and the subscription feed
// offers the user only the locations of the user's group.
func TestSyncAndSubscriptionFollowGroups(t *testing.T) {
	f := newSyncFixture(t)
	ctx := context.Background()
	alice := f.appliedUser(t, "alice", store.Policy{Enabled: true})
	bob := f.appliedUser(t, "bob", store.Policy{Enabled: true})
	allowedID, allowedToken := f.join(t)
	deniedID, deniedToken := f.join(t)
	f.sync(t, allowedToken, 0, nil)
	f.sync(t, deniedToken, 0, nil)
	work, err := f.db.CreateGroup(ctx, "Работа", []string{allowedID}, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.SetUserGroup(ctx, bob.ID, work.ID); err != nil {
		t.Fatal(err)
	}

	// Alice stays in the default group: both nodes serve her. Bob's group covers
	// Amsterdam only, so Berlin must not receive him.
	ams := f.sync(t, allowedToken, 0, nil)
	berlin := f.sync(t, deniedToken, 0, nil)
	if len(ams.Users) != 2 {
		t.Fatalf("Amsterdam must serve both users: %+v", ams.Users)
	}
	if len(berlin.Users) != 1 || berlin.Users[0].Username != "alice" {
		t.Fatalf("Berlin must serve only Alice: %+v", berlin.Users)
	}

	// Bob's subscription offers the main server and Amsterdam, not Berlin.
	sub, err := f.db.Subscription(ctx, "admin", bob.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	status, _, body := f.do(t, "GET", "/"+testPrefix+"/sub/"+sub, "", "")
	if status != 200 {
		t.Fatalf("subscription %d %s", status, body)
	}
	var feed struct {
		Servers []struct{ ID, Name, Country, Link string }
	}
	json.Unmarshal(body, &feed)
	if len(feed.Servers) != 2 || feed.Servers[0].ID != "main" || feed.Servers[1].ID != allowedID {
		t.Fatalf("feed must hold main and Amsterdam only: %s", body)
	}
	if strings.Contains(string(body), deniedID) {
		t.Fatalf("denied location leaked into the feed: %s", body)
	}
	_ = alice
}

// In polling mode the node asks main not to hold the request.
func TestSyncPollHeaderAnswersAtOnce(t *testing.T) {
	old := longPoll
	longPoll = 5 * time.Second
	defer func() { longPoll = old }()
	f := newSyncFixture(t)
	f.appliedUser(t, "alice", store.Policy{Enabled: true})
	_, token := f.join(t)
	req, _ := http.NewRequest("POST", f.srv.URL+"/"+testPrefix+"/node/sync", strings.NewReader(`{"revision":1}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Sync-Poll", "1")
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 || time.Since(start) > time.Second {
		t.Fatal(err, time.Since(start))
	}
	resp.Body.Close()
}

// A renamed node keeps its location id in the subscription; only the name and flag change.
func TestRenamedNodeKeepsItsLocation(t *testing.T) {
	f := newSyncFixture(t)
	ctx := context.Background()
	u := f.appliedUser(t, "alice", store.Policy{Enabled: true})
	nodeID, token := f.join(t)
	f.sync(t, token, 0, nil)
	if _, err := f.db.UpdateNode(ctx, "admin", nodeID, "Варшава", "PL"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.UpdateNode(ctx, "admin", "missing", "X", "PL"); err == nil {
		t.Fatal("an unknown node was updated")
	}
	sub, _ := f.db.Subscription(ctx, "admin", u.ID, false)
	_, _, body := f.do(t, "GET", "/"+testPrefix+"/sub/"+sub, "", "")
	var feed struct {
		Servers []struct{ ID, Name, Country string }
	}
	json.Unmarshal(body, &feed)
	if len(feed.Servers) != 2 || feed.Servers[1].ID != nodeID || feed.Servers[1].Name != "🇵🇱 Варшава" || feed.Servers[1].Country != "PL" {
		t.Fatalf("%s", body)
	}
}

type fakeQuotas struct {
	state endpoint.ControlState
	err   error
}

func (q fakeQuotas) Status(context.Context) (endpoint.ControlState, error) { return q.state, q.err }

// The subscription tells the client whether the access works, until when and how much of the
// monthly quota is used; without the endpoint it still has the status and the date.
func TestSubscriptionAccount(t *testing.T) {
	f := newSyncFixture(t)
	ctx := context.Background()
	handler := f.srv.Config.Handler.(*syncHandler)
	soon := f.now.Add(72 * time.Hour).UTC().Format(time.RFC3339)
	active := f.appliedUser(t, "alice", store.Policy{Enabled: true, ExpiresAt: soon, QuotaBytes: 50 << 30})
	expired := f.appliedUser(t, "bob", store.Policy{Enabled: true, ExpiresAt: f.now.Add(-time.Hour).UTC().Format(time.RFC3339)})
	disabled := f.appliedUser(t, "carol", store.Policy{Enabled: false})
	pending, err := f.db.Create(ctx, "admin", "dave", "dave-password-123", store.Policy{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	handler.quotas = fakeQuotas{state: endpoint.ControlState{Usage: &endpoint.UsageMeta{Month: "2026-11"}, Users: []endpoint.AppliedUser{{ID: active.ID, QuotaBytes: 50 << 30, QuotaUsedBytes: 12 << 30}}}}
	account := func(u store.User) map[string]any {
		t.Helper()
		sub, err := f.db.Subscription(ctx, "admin", u.ID, false)
		if err != nil {
			t.Fatal(err)
		}
		_, _, body := f.do(t, "GET", "/"+testPrefix+"/sub/"+sub, "", "")
		var feed struct {
			Account map[string]any `json:"account"`
		}
		if err := json.Unmarshal(body, &feed); err != nil || feed.Account == nil {
			t.Fatalf("%s", body)
		}
		if strings.Contains(string(body), u.Username+"-password") && feed.Account["password"] != nil {
			t.Fatal("password in the account block")
		}
		return feed.Account
	}
	a := account(active)
	if a["status"] != "active" || a["expires_at"] != soon || a["quota_bytes"] != float64(50<<30) || a["quota_used_bytes"] != float64(12<<30) || a["quota_month"] != "2026-11" {
		t.Fatalf("active: %v", a)
	}
	if a := account(expired); a["status"] != "expired" || a["expires_at"] == nil || a["quota_bytes"] != nil {
		t.Fatalf("expired: %v", a)
	}
	if a := account(disabled); a["status"] != "disabled" {
		t.Fatalf("disabled: %v", a)
	}
	if a := account(pending); a["status"] != "pending" || len(a) != 1 {
		t.Fatalf("pending: %v", a)
	}
	handler.quotas = fakeQuotas{state: endpoint.ControlState{Users: []endpoint.AppliedUser{{ID: active.ID, QuotaBytes: 50 << 30, QuotaUsedBytes: 50 << 30, QuotaExhausted: true}}}}
	if a := account(active); a["status"] != "exhausted" {
		t.Fatalf("exhausted: %v", a)
	}
	handler.quotas = fakeQuotas{err: errors.New("endpoint is down")}
	if a := account(active); a["status"] != "active" || a["expires_at"] != soon || a["quota_bytes"] != nil {
		t.Fatalf("endpoint down: %v", a)
	}
}
