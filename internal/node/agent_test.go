package node

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/divezh/netlane-server/internal/api"
	"github.com/divezh/netlane-server/internal/config"
	"github.com/divezh/netlane-server/internal/decoy"
	"github.com/divezh/netlane-server/internal/endpoint"
	"github.com/divezh/netlane-server/internal/store"
)

// memControl is an in-memory managed endpoint with generation checks.
type memControl struct {
	mu              sync.Mutex
	state           endpoint.ControlState
	conflicts       int // upserts to fail with generation_conflict
	upserts         []string
	removed         []string
	dropped         []string
	broken          string // upserts of this username always fail
	blockBitTorrent *bool
}

func newMem(users ...endpoint.AppliedUser) *memControl {
	return &memControl{state: endpoint.ControlState{Version: 1, Generation: 1, Capabilities: map[string]bool{"remove_user": true, "expiry": true}, Users: users}}
}

func (m *memControl) Status(context.Context) (endpoint.ControlState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.state
	s.Users = append([]endpoint.AppliedUser(nil), m.state.Users...)
	return s, nil
}

func (m *memControl) Upsert(_ context.Context, gen uint64, u endpoint.ManagedUser) (endpoint.ControlState, error) {
	m.mu.Lock()
	if m.conflicts > 0 {
		m.conflicts--
		m.state.Generation++
		m.mu.Unlock()
		return endpoint.ControlState{}, &endpoint.ControlError{Code: "generation_conflict"}
	}
	if gen != m.state.Generation {
		m.mu.Unlock()
		return endpoint.ControlState{}, &endpoint.ControlError{Code: "generation_conflict"}
	}
	for _, live := range m.state.Users {
		if live.Username == u.Username && live.ID != u.ID {
			m.mu.Unlock()
			return endpoint.ControlState{}, &endpoint.ControlError{Code: "username_conflict"}
		}
		if live.ID == u.ID && live.Revision > u.Revision {
			m.mu.Unlock()
			return endpoint.ControlState{}, &endpoint.ControlError{Code: "revision_conflict"}
		}
	}
	if u.Username == m.broken {
		m.mu.Unlock()
		return endpoint.ControlState{}, &endpoint.ControlError{Code: "invalid_user"}
	}
	m.upserts = append(m.upserts, u.Username)
	next := []endpoint.AppliedUser{}
	for _, live := range m.state.Users {
		if live.ID != u.ID {
			next = append(next, live)
		}
	}
	m.state.Users = append(next, endpoint.AppliedUser{ID: u.ID, Username: u.Username, Enabled: u.Enabled, DownloadMbps: u.DownloadMbps, UploadMbps: u.UploadMbps, ExpiresAt: u.ExpiresAt, Revision: u.Revision})
	m.state.Generation++
	m.mu.Unlock()
	return m.Status(context.Background())
}

func (m *memControl) Remove(_ context.Context, gen uint64, id, username string, rev int64) (endpoint.ControlState, error) {
	m.mu.Lock()
	if gen != m.state.Generation {
		m.mu.Unlock()
		return endpoint.ControlState{}, &endpoint.ControlError{Code: "generation_conflict"}
	}
	m.removed = append(m.removed, username)
	next := []endpoint.AppliedUser{}
	for _, live := range m.state.Users {
		if live.ID != id {
			next = append(next, live)
		}
	}
	m.state.Users = next
	m.state.Generation++
	m.mu.Unlock()
	return m.Status(context.Background())
}

func (m *memControl) SyncBlockBitTorrent(_ context.Context, on bool) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blockBitTorrent = &on
	return true, nil
}

func (m *memControl) Disconnect(_ context.Context, username string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dropped = append(m.dropped, username)
	return 1, nil
}

func (m *memControl) Usage(context.Context, string, string) (*endpoint.UsageReport, error) {
	return nil, &endpoint.ControlError{Code: "usage_unavailable"}
}

func (m *memControl) names() []string {
	s, _ := m.Status(context.Background())
	var out []string
	for _, u := range s.Users {
		out = append(out, u.Username)
	}
	return out
}

func TestApplyAddsUpdatesRemoves(t *testing.T) {
	m := newMem(endpoint.AppliedUser{ID: "a", Username: "alice", Enabled: true, Revision: 1}, endpoint.AppliedUser{ID: "b", Username: "bob", Enabled: true, Revision: 1})
	a := &Agent{Control: m}
	want := []store.DistributedUser{
		{ID: "a", Username: "alice", Password: "alice-password-123", Enabled: true, DownloadMbps: 10, Revision: 2},
		{ID: "c", Username: "carol", Password: "carol-password-123", Enabled: true, Revision: 1},
	}
	acked, err := a.Apply(context.Background(), want, []store.Command{{ID: 7, Kind: "disconnect", Username: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(m.upserts, ",") != "alice,carol" || strings.Join(m.removed, ",") != "bob" || len(acked) != 1 || acked[0] != 7 || m.dropped[0] != "alice" {
		t.Fatalf("upserts %v removed %v acked %v dropped %v", m.upserts, m.removed, acked, m.dropped)
	}
	m.upserts = nil
	if _, err = a.Apply(context.Background(), want, nil); err != nil || len(m.upserts) != 0 {
		t.Fatal("unchanged users upserted again", m.upserts, err)
	}
}

func TestApplyRetriesGenerationConflict(t *testing.T) {
	m := newMem()
	m.conflicts = 1
	a := &Agent{Control: m}
	if _, err := a.Apply(context.Background(), []store.DistributedUser{{ID: "a", Username: "alice", Password: "alice-password-123", Enabled: true, Revision: 1}}, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Join(m.names(), ",") != "alice" {
		t.Fatal(m.names())
	}
}

const prefix = "abcdefghijklmnopqrstuvwxyz"

func TestAgentEndToEnd(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "panel.sqlite"), bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	hub := api.NewHub()
	main := httptest.NewTLSServer(api.SyncHandler(config.Config{Domain: "ams.example.com", SyncPrefix: prefix}, db, hub, decoy.Handler(""), time.Now))
	defer main.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	apply := func(name string) {
		u, err := db.Create(ctx, "admin", name, name+"-password-123", store.Policy{Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		db.MarkApplied(ctx, "admin", u.ID, 1)
		hub.Notify()
	}
	apply("alice")
	code, _, _ := db.CreateInvite(ctx, "admin", time.Now())
	nodeID, token, err := Join(ctx, main.Client(), main.URL+"/"+prefix+"/node/join#"+code, store.NodeJoin{Name: "Берлин", Country: "DE", Domain: "de.example.com"})
	if err != nil || nodeID == "" {
		t.Fatal(nodeID, err)
	}
	m := newMem()
	a := &Agent{MainURL: main.URL + "/" + prefix, Token: token, StatePath: filepath.Join(t.TempDir(), "node.state"), Control: m, HTTP: main.Client()}
	a.Resources = func() store.Resources { cpu := 26.; return store.Resources{CPU: &cpu, Cores: 4} }
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	waitFor(t, func() bool { return strings.Join(m.names(), ",") == "alice" })
	waitFor(t, func() bool {
		report, err := db.LocationStatistics(ctx, nodeID, time.Now(), time.Hour, time.Now().UTC().Format("2006-01-02"))
		return err == nil && report.Latest != nil && report.Latest.CPU != nil && *report.Latest.CPU == 26 && report.Latest.Cores == 4
	})
	// The owner's BitTorrent choice reaches the node with the next sync (on by default).
	blocking := func() *bool { m.mu.Lock(); defer m.mu.Unlock(); return m.blockBitTorrent }
	waitFor(t, func() bool { b := blocking(); return b != nil && *b })
	if err = db.SetBlockBitTorrent(ctx, false); err != nil {
		t.Fatal(err)
	}
	hub.Notify()
	waitFor(t, func() bool { b := blocking(); return b != nil && !*b })
	apply("bob")
	waitFor(t, func() bool { return len(m.names()) == 2 })
	if err = db.DeleteNode(ctx, "admin", nodeID); err != nil {
		t.Fatal(err)
	}
	hub.Notify()
	waitFor(t, func() bool { return len(m.names()) == 0 })
	cancel()
	<-done
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not reached in 3 s")
}

func TestJoinSendsCodeInBodyAndExplainsFailure(t *testing.T) {
	var gotURL, gotBody, gotHeader string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		gotHeader = r.Header.Get("X-Sync-Data")
		b := new(bytes.Buffer)
		b.ReadFrom(r.Body)
		gotBody = b.String()
		http.NotFound(w, r)
	}))
	defer srv.Close()
	_, _, err := Join(context.Background(), srv.Client(), srv.URL+"/p/node/join#secret-code", store.NodeJoin{Name: "X", Domain: "x.example.com"})
	if err == nil || !strings.Contains(err.Error(), "код недействителен") {
		t.Fatal(err)
	}
	payload, err := api.DecodePayload(gotHeader)
	if strings.Contains(gotURL, "secret-code") || gotBody != "" || err != nil || !strings.Contains(string(payload), `"code":"secret-code"`) {
		t.Fatal(gotURL, gotBody, string(payload), err)
	}
	if _, _, err = Join(context.Background(), srv.Client(), "http://x/p/node/join#c", store.NodeJoin{}); err == nil {
		t.Fatal("plain http accepted")
	}
}

// A name deleted and re-created on main while the node was offline: the old id goes first.
func TestApplyRemovesNameHolderFirst(t *testing.T) {
	m := newMem(endpoint.AppliedUser{ID: "old", Username: "alice", Enabled: true, Revision: 3})
	a := &Agent{Control: m}
	if _, err := a.Apply(context.Background(), []store.DistributedUser{{ID: "new", Username: "alice", Password: "alice-password-123", Enabled: true, Revision: 1}}, nil); err != nil {
		t.Fatal(err)
	}
	s, _ := m.Status(context.Background())
	if len(s.Users) != 1 || s.Users[0].ID != "new" {
		t.Fatalf("%+v", s.Users)
	}
}

// One user the endpoint refuses does not stop the others or the commands.
func TestApplyContinuesPastBadUser(t *testing.T) {
	m := newMem(endpoint.AppliedUser{ID: "gone", Username: "gone", Enabled: true, Revision: 1})
	m.broken = "bad"
	a := &Agent{Control: m}
	acked, err := a.Apply(context.Background(), []store.DistributedUser{
		{ID: "b", Username: "bad", Password: "bad-password-1234", Enabled: true, Revision: 1},
		{ID: "c", Username: "carol", Password: "carol-password-123", Enabled: true, Revision: 1},
	}, []store.Command{{ID: 3, Kind: "disconnect", Username: "carol"}})
	if err == nil || !strings.Contains(err.Error(), "bad") {
		t.Fatal("failure not reported", err)
	}
	if strings.Join(m.names(), ",") != "carol" || len(acked) != 1 {
		t.Fatalf("names %v acked %v", m.names(), acked)
	}
}

// main restored from a backup: revisions went back; the user is replaced, not stuck.
func TestApplyRevisionRegression(t *testing.T) {
	m := newMem(endpoint.AppliedUser{ID: "a", Username: "alice", Enabled: true, Revision: 5})
	a := &Agent{Control: m}
	if _, err := a.Apply(context.Background(), []store.DistributedUser{{ID: "a", Username: "alice", Password: "alice-password-123", Enabled: true, DownloadMbps: 7, Revision: 2}}, nil); err != nil {
		t.Fatal(err)
	}
	s, _ := m.Status(context.Background())
	if len(s.Users) != 1 || s.Users[0].Revision != 2 || s.Users[0].DownloadMbps != 7 {
		t.Fatalf("%+v", s.Users)
	}
}

// Long polls cut early (a proxy closing idle requests): the agent switches to polling and
// asks main not to wait.
func TestAgentFallsBackToPolling(t *testing.T) {
	oldBackoff, oldPoll := firstBackoff, pollInterval
	firstBackoff, pollInterval = 10*time.Millisecond, 10*time.Millisecond
	defer func() { firstBackoff, pollInterval = oldBackoff, oldPoll }()
	var mu sync.Mutex
	calls, polled := 0, false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		if r.Header.Get(api.PollHeader) != "" {
			polled = true
		}
		mu.Unlock()
		if n <= 3 {
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		w.Write([]byte(`{"revision":0,"unchanged":true}`))
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	a := &Agent{MainURL: srv.URL, Token: "t", Control: newMem(), HTTP: srv.Client(), Log: func(string) {}}
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	// The agent must stop before the deferred restore of the pause variables runs.
	defer func() { cancel(); <-done }()
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return polled })
}
