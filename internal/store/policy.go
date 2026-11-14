package store

import (
	"context"
	"database/sql"
	"errors"
)

// BlockBitTorrent reports whether VPN users may not use BitTorrent (on unless the owner turned it off).
// It is the desired value: the panel applies it to the main endpoint and nodes take it with each sync.
func (s *Store) BlockBitTorrent(ctx context.Context) (bool, error) {
	var v int64
	err := s.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='block_bittorrent'").Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	return v != 0, err
}

// SetBlockBitTorrent stores the owner's choice.
func (s *Store) SetBlockBitTorrent(ctx context.Context, on bool) error {
	v := 0
	if on {
		v = 1
	}
	_, err := s.db.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES('block_bittorrent',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", v)
	return err
}
