package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestLocationStatisticsIsolationReplayAndDeletion(t *testing.T) {
	s, _ := openTest(t)
	defer s.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	code, _, _ := s.CreateInvite(ctx, "admin", now)
	node, _, err := s.JoinNode(ctx, code, NodeJoin{Domain: "wars.example.com"}, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"main", node.ID} {
		for i := 0; i < 2; i++ {
			n := 1
			value := uint64(100 + i*100)
			if source == "main" {
				value *= 10
			}
			err = s.RecordLocationStatistics(ctx, source, StatisticsSample{At: now.Add(time.Duration(i-1) * time.Minute).Unix(), Online: &n}, map[string]Counter{"alex": {Download: value}})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	r, err := s.LocationStatistics(ctx, node.ID, now, time.Hour, now.Format("2006-01-02"))
	if err != nil || r.Download24 != 100 || r.Scope != node.ID {
		t.Fatal(r, err)
	}
	// Retry of a cached report must not charge its interval twice or replace the sample.
	if err = s.RecordLocationStatistics(ctx, node.ID, StatisticsSample{At: now.Unix()}, map[string]Counter{}); err != nil {
		t.Fatal(err)
	}
	r, _ = s.LocationStatistics(ctx, node.ID, now, time.Hour, now.Format("2006-01-02"))
	if r.Download24 != 100 || len(r.Points) != 2 {
		t.Fatal(r)
	}
	main, _ := s.Statistics(ctx, now, time.Hour, now.Format("2006-01-02"))
	if main.Download24 != 1000 {
		t.Fatal(main)
	}
	if err = s.DeleteNode(ctx, "admin", node.ID); err != nil {
		t.Fatal(err)
	}
	r, _ = s.LocationStatistics(ctx, node.ID, now, time.Hour, now.Format("2006-01-02"))
	if r.Latest != nil || len(r.Points) != 0 {
		t.Fatal("deleted node history retained", r)
	}
	if err = s.RecordLocationStatistics(ctx, node.ID, StatisticsSample{At: now.Unix() + 60}, nil); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("deleted node accepted", err)
	}
}

func TestLocationStatisticsMigratesExistingMainHistory(t *testing.T) {
	s, path := openTest(t)
	now := time.Now().UTC().Truncate(time.Second)
	sample := StatisticsSample{At: now.Unix()}
	data, _ := json.Marshal(sample)
	if _, err := s.db.Exec("INSERT INTO statistics_samples(at,data) VALUES(?,?); DELETE FROM meta WHERE key='location_statistics_migrated'", sample.At, string(data)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err := Open(path, bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r, err := s.Statistics(context.Background(), now, time.Hour, now.Format("2006-01-02"))
	if err != nil || r.Latest == nil || r.Latest.At != sample.At {
		t.Fatal("lost pre-upgrade history", r, err)
	}
}
