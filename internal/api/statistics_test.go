package api

import (
	"context"
	"github.com/divezh/netlane-server/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStatisticsRouteValidation(t *testing.T) {
	f := newSyncFixture(t)
	s := &Server{db: f.db}
	for _, query := range []string{"?period=bad", "?day=not-a-date", "?day=2099-01-01", "?day=2000-01-01"} {
		rec := httptest.NewRecorder()
		s.statistics(rec, httptest.NewRequest("GET", "/api/v1/statistics"+query, nil))
		if rec.Code != 400 {
			t.Fatal(query, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	s.statistics(rec, httptest.NewRequest("GET", "/api/v1/statistics?period=24h", nil))
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
}
func TestSubscriptionCountsOnlyUsableSuccessfulGet(t *testing.T) {
	f := newSyncFixture(t)
	u := f.appliedUser(t, "alex", store.Policy{Enabled: true})
	token, err := f.db.Subscription(context.Background(), "admin", u.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	visit := func(token, ua string) {
		t.Helper()
		req, _ := http.NewRequest("GET", f.srv.URL+"/"+testPrefix+"/sub/"+token, nil)
		req.Header.Set("User-Agent", ua)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	visit("invalid-token", "Netlane/1")
	visit(token, "Netlane/1")
	visit(token, "Netlane/1")
	r, err := f.db.Statistics(context.Background(), f.now, time.Hour, f.now.Format("2006-01-02"))
	if err != nil || len(r.Clients) != 1 || r.Clients[0].Count != 1 || r.Clients[0].Name != "Netlane" {
		t.Fatal(r, err)
	}
	if _, err = f.db.UpdatePolicy(context.Background(), "admin", u.ID, 1, store.Policy{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if err = f.db.MarkApplied(context.Background(), "admin", u.ID, 2); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(24 * time.Hour)
	visit(token, "Netlane/1")
	r, _ = f.db.Statistics(context.Background(), f.now, time.Hour, f.now.Format("2006-01-02"))
	if len(r.Clients) != 0 {
		t.Fatal("disabled account counted")
	}
}

func TestBackgroundStatisticsKnownUsersAndCancellation(t *testing.T) {
	f := newSyncFixture(t)
	f.appliedUser(t, "alex", store.Policy{Enabled: true})
	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"username":"alex","sessions":2,"inbound":100,"outbound":200},{"username":"alex","sessions":1,"inbound":0,"outbound":0},{"username":"removed","sessions":4,"inbound":0,"outbound":0}]`))
	}))
	defer metrics.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		TrackStatistics(ctx, strings.TrimPrefix(metrics.URL, "http://"), t.TempDir()+"/db.sqlite", f.db)
	}()
	deadline := time.Now().Add(3 * time.Second)
	var report store.StatisticsReport
	for time.Now().Before(deadline) {
		report, _ = f.db.Statistics(context.Background(), time.Now().UTC(), time.Hour, time.Now().UTC().Format("2006-01-02"))
		if report.Latest != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("collector failed to stop")
	}
	if report.Latest == nil || report.Latest.Online == nil || *report.Latest.Online != 1 || report.Latest.Download != nil {
		t.Fatal("bad baseline or duplicate users", report)
	}
}
