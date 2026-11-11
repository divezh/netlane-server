package store

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestPresenceRecordsLastSeenAndKeepsLastKnownIP(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "panel.sqlite"), bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	alex, err := s.Create(ctx, "admin", "alex", "unique-vpn-password", Policy{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Create(ctx, "admin", "kate", "another-vpn-password", Policy{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, 11, 3, 15, 0, 0, 0, time.UTC)
	if err = s.RecordPresence(ctx, first, []Seen{{Username: "alex", IP: "198.51.100.7"}, {Username: "ghost", IP: "203.0.113.1"}}); err != nil {
		t.Fatal(err)
	}
	// The endpoint may report a session without an address: time moves on, the known address stays.
	second := first.Add(time.Minute)
	if err = s.RecordPresence(ctx, second, []Seen{{Username: "alex"}}); err != nil {
		t.Fatal(err)
	}
	users, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]User{}
	for _, u := range users {
		byName[u.Username] = u
	}
	if got := byName["alex"]; got.LastSeenAt != second.Format(time.RFC3339) || got.LastIP != "198.51.100.7" {
		t.Fatalf("alex presence: %q %q", got.LastSeenAt, got.LastIP)
	}
	if got := byName["kate"]; got.LastSeenAt != "" || got.LastIP != "" {
		t.Fatalf("never connected user must have no presence: %q %q", got.LastSeenAt, got.LastIP)
	}
	if err = s.Delete(ctx, "admin", alex.ID, alex.DesiredRevision); err != nil {
		t.Fatal(err)
	}
	var left int
	if err = s.db.QueryRow("SELECT count(*) FROM presence").Scan(&left); err != nil || left != 0 {
		t.Fatalf("presence of a deleted user must go with it: %d %v", left, err)
	}
}

func TestPresenceTableDoesNotChangeSchemaVersion(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "panel.sqlite"), bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version int
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatalf("an additive table must keep schema 1 so the previous panel can still open the database: %d %v", version, err)
	}
}
