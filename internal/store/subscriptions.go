package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/divezh/netlane-server/internal/security"
)

// Subscription returns the user's subscription token, creating one when there is none or
// when rotate asks for a new one (the old token stops working at once).
func (s *Store) Subscription(ctx context.Context, actor, userID string, rotate bool) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var exists int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE id=?", userID).Scan(&exists); err != nil {
		return "", err
	}
	if exists == 0 {
		return "", sql.ErrNoRows
	}
	var sealed string
	err = tx.QueryRowContext(ctx, "SELECT token_sealed FROM subscriptions WHERE user_id=?", userID).Scan(&sealed)
	if err == nil && !rotate {
		return security.Open(s.key, "sub:"+userID, sealed)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	action := "subscription.created"
	if err == nil {
		action = "subscription.rotated"
	}
	token := security.Token(32)
	if sealed, err = security.Seal(s.key, "sub:"+userID, token); err != nil {
		return "", err
	}
	stamp := time.Now().UTC().Format(time.RFC3339)
	if _, err = tx.ExecContext(ctx, `INSERT INTO subscriptions(user_id,token_hash,token_sealed,created_at) VALUES(?,?,?,?)
 ON CONFLICT(user_id) DO UPDATE SET token_hash=excluded.token_hash,token_sealed=excluded.token_sealed,created_at=excluded.created_at`, userID, secretHash(token), sealed, stamp); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO audit(happened_at,actor,action,target) VALUES(?,?,?,?)", stamp, actor, action, userID); err != nil {
		return "", err
	}
	return token, tx.Commit()
}

// UserBySubscription finds the user a subscription token belongs to; sql.ErrNoRows when none.
func (s *Store) UserBySubscription(ctx context.Context, token string) (User, error) {
	var u User
	var policy string
	err := s.db.QueryRowContext(ctx, "SELECT u.id,u.username,u.policy,u.desired_revision,u.applied_revision,u.created_at FROM subscriptions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=?", secretHash(token)).Scan(&u.ID, &u.Username, &policy, &u.DesiredRevision, &u.AppliedRevision, &u.CreatedAt)
	if err != nil {
		return User{}, err
	}
	return u, json.Unmarshal([]byte(policy), &u.Policy)
}
