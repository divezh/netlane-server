package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestSubscriptionStableAndRotate(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	u, _ := s.Create(ctx, "admin", "alice", "alice-password-123", Policy{Enabled: true})
	first, err := s.Subscription(ctx, "admin", u.ID, false)
	if err != nil || len(first) < 40 {
		t.Fatal(first, err)
	}
	again, _ := s.Subscription(ctx, "admin", u.ID, false)
	if again != first {
		t.Fatal("token changed without rotate")
	}
	rotated, _ := s.Subscription(ctx, "admin", u.ID, true)
	if rotated == first {
		t.Fatal("rotate kept token")
	}
	if _, err = s.UserBySubscription(ctx, first); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("old token still valid", err)
	}
	found, err := s.UserBySubscription(ctx, rotated)
	if err != nil || found.ID != u.ID || !found.Policy.Enabled {
		t.Fatal(found, err)
	}
	if _, err = s.Subscription(ctx, "admin", "missing", false); err == nil {
		t.Fatal("subscription for unknown user")
	}
}

func TestSubscriptionRemovedWithUser(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	u, _ := s.Create(ctx, "admin", "alice", "alice-password-123", Policy{Enabled: true})
	token, _ := s.Subscription(ctx, "admin", u.ID, false)
	if err := s.Delete(ctx, "admin", u.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserBySubscription(ctx, token); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("token outlived user", err)
	}
}
