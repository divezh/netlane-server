package store

import (
	"context"
	"math"
	"time"
)

type AuditEvent struct {
	ID         int64  `json:"id"`
	HappenedAt string `json:"happened_at"`
	Actor      string `json:"actor"`
	Action     string `json:"action"`
	Target     string `json:"target"`
}

// AuditPage is how many events one request returns.
const AuditPage = 100

// RecentAudit returns the newest events; with before > 0, the page of events older than that id.
func (s *Store) RecentAudit(ctx context.Context, before int64) ([]AuditEvent, error) {
	if before <= 0 {
		before = math.MaxInt64
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id,happened_at,actor,action,target FROM audit WHERE id<? ORDER BY id DESC LIMIT ?", before, AuditPage)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []AuditEvent{}
	for rows.Next() {
		var event AuditEvent
		if err := rows.Scan(&event.ID, &event.HappenedAt, &event.Actor, &event.Action, &event.Target); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

// AuditRetention is how long the journal keeps events; older ones are removed once a day.
const AuditRetention = 365 * 24 * time.Hour

// PruneAudit removes events older than before and reports how many went.
func (s *Store) PruneAudit(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, "DELETE FROM audit WHERE happened_at<?", before.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
