package store

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestStatisticsBaselineRestartResetAndGaps(t *testing.T) {
	s, path := openTest(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	n := 1
	record := func(at time.Time, down uint64) {
		t.Helper()
		if err := s.RecordStatistics(ctx, StatisticsSample{At: at.Unix(), Online: &n}, map[string]Counter{"alex": {Download: down, Upload: down / 2}}); err != nil {
			t.Fatal(err)
		}
	}
	record(now.Add(-time.Minute), 1000)
	// A second connection to the same database simulates a panel restart.
	other, err := Open(path, bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err = other.RecordStatistics(ctx, StatisticsSample{At: now.Unix(), Online: &n}, map[string]Counter{"alex": {Download: 1600, Upload: 800}}); err != nil {
		t.Fatal(err)
	}
	r, err := s.Statistics(ctx, now, time.Hour, now.Format("2006-01-02"))
	if err != nil || r.Download24 != 600 || r.Upload24 != 300 || r.Coverage24 != 60 {
		t.Fatalf("wrong deltas after restart: %+v %v", r, err)
	}
	record(now.Add(time.Minute), 10)
	record(now.Add(4*time.Minute), 1000)
	record(now.Add(5*time.Minute), 1100)
	r, err = s.Statistics(ctx, now.Add(5*time.Minute), time.Hour, now.Format("2006-01-02"))
	if err != nil || r.Download24 != 700 || r.Coverage24 != 120 {
		t.Fatalf("reset or gap inflated traffic: %+v %v", r, err)
	}
	if err = s.RecordStatistics(ctx, StatisticsSample{At: now.Add(6 * time.Minute).Unix()}, nil); err != nil {
		t.Fatal(err)
	}
	record(now.Add(7*time.Minute), 2000)
	r, _ = s.Statistics(ctx, now.Add(7*time.Minute), time.Hour, now.Format("2006-01-02"))
	if r.Download24 != 700 {
		t.Fatal("missing data was extrapolated")
	}
	// Rows before the rolling 24h window must not leak into its total.
	r, _ = s.Statistics(ctx, now.Add(25*time.Hour), 30*24*time.Hour, now.Format("2006-01-02"))
	if r.Download24 != 0 {
		t.Fatal("24h window contains old traffic")
	}
}
func TestDailyClientsUniqueTokenLastAgentAndPrivacy(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.Now().UTC()
	token := "random-private-subscription-token"
	for _, ua := range []string{"Netlane/0.4.6", "Netlane/0.4.6", "TrustTunnel/1.1"} {
		if err := s.RecordSubscriptionVisit(ctx, now, token, ua); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordSubscriptionVisit(ctx, now, "another-token", "Unknown/1"); err != nil {
		t.Fatal(err)
	}
	r, err := s.Statistics(ctx, now, time.Hour, now.Format("2006-01-02"))
	if err != nil || len(r.Clients) != 2 {
		t.Fatal(r, err)
	}
	total := 0
	for _, c := range r.Clients {
		total += c.Count
		if c.Name == "Netlane" {
			t.Fatal("same token counted in two apps")
		}
	}
	if total != 2 {
		t.Fatal(total)
	}
	var stored string
	if err = s.db.QueryRow("SELECT token_hash FROM subscription_visits WHERE client='TrustTunnel'").Scan(&stored); err != nil || stored == token || len(stored) != 64 {
		t.Fatal("raw token stored", err)
	}
	s.RecordSubscriptionVisit(ctx, now.Add(24*time.Hour), token, "Netlane/1")
	r, _ = s.Statistics(ctx, now.Add(24*time.Hour), time.Hour, now.Add(24*time.Hour).Format("2006-01-02"))
	if len(r.Clients) != 1 || r.Clients[0].Count != 1 {
		t.Fatal("UTC days not separated", r)
	}
}
func TestStatisticsRetentionAndBoundedAggregation(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.Now().UTC()
	s.RecordStatistics(ctx, StatisticsSample{At: now.Add(-32 * 24 * time.Hour).Unix()}, nil)
	s.RecordSubscriptionVisit(ctx, now.Add(-32*24*time.Hour), "old", "x")
	s.RecordStatistics(ctx, StatisticsSample{At: now.Unix()}, nil)
	r, err := s.Statistics(ctx, now, 30*24*time.Hour, now.Add(-32*24*time.Hour).Format("2006-01-02"))
	if err != nil || len(r.Clients) != 0 || r.StartedAt == nil || *r.StartedAt != now.Unix() {
		t.Fatal("retention", r, err)
	}
}

// The Windows client's version is kept, and the user list shows the last application per user.
func TestUserListShowsLastClientVersion(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	u, err := s.Create(ctx, "admin", "alice", "vpn-password-123", Policy{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.Subscription(ctx, "admin", u.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	s.RecordSubscriptionVisit(ctx, day, token, "Netlane/0.6.1")
	s.RecordSubscriptionVisit(ctx, day.Add(24*time.Hour), token, "Netlane/0.6.2")
	s.RecordSubscriptionVisit(ctx, day, "someone-else", "Netlane/0.6.2 (Windows; x"+strings.Repeat("9", 600)+")")
	users, err := s.List(ctx)
	if err != nil || len(users) != 1 || users[0].Client != "Netlane 0.6.2" || users[0].ClientDay != "2026-11-10" {
		t.Fatalf("%+v %v", users, err)
	}
	for ua, want := range map[string]string{"Netlane/0.6.2": "Netlane 0.6.2", "Netlane/1": "Netlane", "Netlane/0.6.2.1": "Netlane", "TrustTunnel/1.1": "TrustTunnel"} {
		s.RecordSubscriptionVisit(ctx, day, "probe", ua)
		var got string
		s.db.QueryRow("SELECT client FROM subscription_visits WHERE token_hash=? AND day=?", secretHash("probe"), "2026-11-09").Scan(&got)
		if got != want {
			t.Fatalf("%q → %q, want %q", ua, got, want)
		}
	}
}
