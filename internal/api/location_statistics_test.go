package api

import (
	"context"
	"github.com/divezh/netlane-server/internal/store"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNodeStatisticsSelectionAndValidation(t *testing.T) {
	f := newSyncFixture(t)
	s := &Server{db: f.db}
	ctx := context.Background()
	now := time.Now().UTC()
	code, _, _ := f.db.CreateInvite(ctx, "admin", now)
	n, _, err := f.db.JoinNode(ctx, code, store.NodeJoin{Domain: "wars.example.com", Name: "Warsaw"}, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		id   string
		code int
	}{{n.ID, 200}, {"missing", 404}, {"' OR 1=1", 404}} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/v1/statistics", nil)
		q := req.URL.Query()
		q.Set("node", test.id)
		req.URL.RawQuery = q.Encode()
		s.statistics(rec, req)
		if rec.Code != test.code {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	o := &store.StatisticsObservation{Sample: store.StatisticsSample{At: now.Unix()}}
	if !validNodeStatistics(o, now) {
		t.Fatal("valid rejected")
	}
	cpu := 101.
	o.Sample.CPU = &cpu
	if validNodeStatistics(o, now) {
		t.Fatal("invalid CPU accepted")
	}
	o.Sample.CPU = nil
	o.Sample.At = now.Add(-4 * time.Minute).Unix()
	if validNodeStatistics(o, now) {
		t.Fatal("stale report accepted")
	}
}
