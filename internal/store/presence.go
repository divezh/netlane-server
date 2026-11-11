package store

import (
	"context"
	"time"
)

// Seen is one user the endpoint reported with an open session; IP is empty when unknown.
type Seen struct {
	Username string
	IP       string
}

// RecordPresence stores when each listed user was last seen online and, when the endpoint
// reported one, the address they connected from. Unknown usernames are ignored.
func (s *Store) RecordPresence(ctx context.Context, at time.Time, seen []Seen) error {
	if len(seen) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stamp := at.UTC().Format(time.RFC3339)
	for _, item := range seen {
		if _, err = tx.ExecContext(ctx, `INSERT INTO presence(user_id,last_seen_at,last_ip) SELECT id,?,? FROM users WHERE username=?
 ON CONFLICT(user_id) DO UPDATE SET last_seen_at=excluded.last_seen_at, last_ip=CASE WHEN excluded.last_ip='' THEN presence.last_ip ELSE excluded.last_ip END`, stamp, item.IP, item.Username); err != nil {
			return err
		}
	}
	return tx.Commit()
}
