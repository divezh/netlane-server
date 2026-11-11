package node

import (
	"context"
	"github.com/divezh/netlane-server/internal/endpoint"
	"github.com/divezh/netlane-server/internal/store"
	"testing"
	"time"
)

func TestStatisticsReportsMinuteSamplesAndUniqueUsers(t *testing.T) {
	now := time.Now().UTC()
	calls := 0
	a := &Agent{Now: func() time.Time { return now }, Control: newMem(endpoint.AppliedUser{ID: "u1", Username: "alex"}), Resources: func() store.Resources { calls++; return store.Resources{Cores: 2} }, Clients: func(context.Context) ([]endpoint.Client, error) {
		return []endpoint.Client{{Username: "alex", Sessions: 2, Outbound: 100}, {Username: "alex", Sessions: 1, Outbound: 200}, {Username: "deleted", Sessions: 5}}, nil
	}}
	first := a.report(context.Background()).Statistics
	if first == nil || *first.Sample.Online != 1 || *first.Sample.Connections != 3 || first.Counters["alex"].Download != 300 {
		t.Fatal(first)
	}
	now = now.Add(25 * time.Second)
	if second := a.report(context.Background()).Statistics; second.Sample.At != first.Sample.At || calls != 1 {
		t.Fatal(second, calls)
	}
	now = now.Add(40 * time.Second)
	if next := a.report(context.Background()).Statistics; next.Sample.At == first.Sample.At || calls != 2 {
		t.Fatal(next, calls)
	}
}
