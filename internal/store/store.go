package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/divezh/netlane-server/internal/security"
	_ "modernc.org/sqlite"
)

type Policy struct {
	Enabled      bool   `json:"enabled"`
	QuotaBytes   int64  `json:"quota_bytes"`
	DownloadMbps int    `json:"download_mbps"`
	UploadMbps   int    `json:"upload_mbps"`
	ExpiresAt    string `json:"expires_at,omitempty"`
}

func (p Policy) Validate() error {
	if p.QuotaBytes < 0 || p.QuotaBytes > 1<<50 || p.DownloadMbps < 0 || p.DownloadMbps > 1000000 || p.UploadMbps < 0 || p.UploadMbps > 1000000 {
		return errors.New("invalid quota or speed limit")
	}
	if p.ExpiresAt != "" {
		if _, err := time.Parse(time.RFC3339, p.ExpiresAt); err != nil {
			return errors.New("expires_at must be an RFC3339 timestamp")
		}
	}
	return nil
}

type User struct {
	ID              string `json:"id"`
	Username        string `json:"username"`
	Policy          Policy `json:"policy"`
	DesiredRevision int64  `json:"desired_revision"`
	AppliedRevision int64  `json:"applied_revision"`
	CreatedAt       string `json:"created_at"`
	// LastSeenAt and LastIP come from the panel's presence sampler; empty until the user connects.
	LastSeenAt string `json:"last_seen_at,omitempty"`
	LastIP     string `json:"last_ip,omitempty"`
	// Client and ClientDay: the application that last fetched the user's subscription (last 31 days).
	Client    string `json:"client,omitempty"`
	ClientDay string `json:"client_day,omitempty"`
}
type Store struct {
	db  *sql.DB
	key []byte
}

var ErrConflict = errors.New("username already exists")
var validUsername = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func Open(path string, key []byte) (*Store, error) {
	if len(key) != 32 {
		return nil, errors.New("invalid encryption key")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	f.Close()
	if err = os.Chmod(path, 0600); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var schema int
	if err = db.QueryRow("PRAGMA user_version").Scan(&schema); err != nil {
		db.Close()
		return nil, err
	}
	if schema > 1 {
		db.Close()
		return nil, errors.New("database belongs to a newer version; refusing to downgrade")
	}
	if _, err = db.Exec("PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}
	if schema < 1 {
		_, err = db.Exec(`BEGIN;
 CREATE TABLE IF NOT EXISTS users(id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, credential TEXT NOT NULL, policy TEXT NOT NULL, desired_revision INTEGER NOT NULL DEFAULT 1, applied_revision INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS audit(id INTEGER PRIMARY KEY, happened_at TEXT NOT NULL, actor TEXT NOT NULL, action TEXT NOT NULL, target TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS presence(user_id TEXT PRIMARY KEY, last_seen_at TEXT NOT NULL, last_ip TEXT NOT NULL DEFAULT '');
 PRAGMA user_version=1; COMMIT;`)
		if err != nil {
			db.Close()
			return nil, err
		}
	}
	if err = migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, key: key}, nil
}
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Delete(ctx context.Context, actor, id string, expected int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "DELETE FROM users WHERE id=? AND desired_revision=?", id, expected)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return sql.ErrNoRows
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM presence WHERE user_id=?", id); err != nil {
		return err
	}
	for _, table := range []string{"distributed_users", "node_user_state", "node_usage", "subscriptions", "user_groups"} {
		if _, err = tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE user_id=?", id); err != nil {
			return err
		}
	}
	if err = bumpRevision(ctx, tx); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO audit(happened_at,actor,action,target) VALUES(?,?,?,?)", time.Now().UTC().Format(time.RFC3339), actor, "user.deleted", id); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) Managed(ctx context.Context, id string) (User, string, error) {
	var u User
	var policy, sealed string
	err := s.db.QueryRowContext(ctx, "SELECT id,username,credential,policy,desired_revision,applied_revision,created_at FROM users WHERE id=?", id).Scan(&u.ID, &u.Username, &sealed, &policy, &u.DesiredRevision, &u.AppliedRevision, &u.CreatedAt)
	if err != nil {
		return u, "", err
	}
	if err = json.Unmarshal([]byte(policy), &u.Policy); err != nil {
		return u, "", err
	}
	password, err := security.Open(s.key, u.ID, sealed)
	return u, password, err
}
func (s *Store) Audit(ctx context.Context, actor, action, target string) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO audit(happened_at,actor,action,target) VALUES(?,?,?,?)", time.Now().UTC().Format(time.RFC3339), actor, action, target)
	return err
}
func (s *Store) MarkApplied(ctx context.Context, actor, id string, revision int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "UPDATE users SET applied_revision=? WHERE id=? AND desired_revision>=? AND applied_revision<=?", revision, id, revision, revision)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return ErrConflict
	}
	if err = s.snapshot(ctx, tx, id, revision); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO audit(happened_at,actor,action,target) VALUES(?,?,?,?)", time.Now().UTC().Format(time.RFC3339), actor, "user.policy_applied", id); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) List(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT u.id,u.username,u.policy,u.desired_revision,u.applied_revision,u.created_at,COALESCE(p.last_seen_at,''),COALESCE(p.last_ip,''),COALESCE((SELECT v.client||'|'||v.day FROM subscription_visits v JOIN subscriptions sub ON sub.token_hash=v.token_hash WHERE sub.user_id=u.id ORDER BY v.day DESC LIMIT 1),'') FROM users u LEFT JOIN presence p ON p.user_id=u.id ORDER BY u.username")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []User{}
	for rows.Next() {
		var u User
		var p, visit string
		if err = rows.Scan(&u.ID, &u.Username, &p, &u.DesiredRevision, &u.AppliedRevision, &u.CreatedAt, &u.LastSeenAt, &u.LastIP, &visit); err != nil {
			return nil, err
		}
		if client, day, ok := strings.Cut(visit, "|"); ok {
			u.Client, u.ClientDay = client, day
		}
		if err = json.Unmarshal([]byte(p), &u.Policy); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}
func (s *Store) Create(ctx context.Context, actor, username, password string, p Policy) (User, error) {
	if !validUsername.MatchString(username) {
		return User{}, errors.New("username must contain 1–64 ASCII letters, digits, underscores or hyphens")
	}
	if len(password) < 12 || len(password) > 1024 {
		return User{}, errors.New("VPN password must contain 12–1024 bytes")
	}
	if err := p.Validate(); err != nil {
		return User{}, err
	}
	u := User{ID: security.Token(16), Username: username, Policy: p, DesiredRevision: 1, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	sealed, err := security.Seal(s.key, u.ID, password)
	if err != nil {
		return User{}, err
	}
	data, _ := json.Marshal(p)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, "INSERT INTO users(id,username,credential,policy,created_at) VALUES(?,?,?,?,?)", u.ID, username, sealed, string(data), u.CreatedAt)
	if err != nil {
		var count int
		if tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE username=?", username).Scan(&count) == nil && count > 0 {
			return User{}, ErrConflict
		}
		return User{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO audit(happened_at,actor,action,target) VALUES(?,?,?,?)", u.CreatedAt, actor, "user.created", u.ID); err != nil {
		return User{}, err
	}
	return u, tx.Commit()
}
func (s *Store) UpdatePolicy(ctx context.Context, actor, id string, expected int64, p Policy) (User, error) {
	if err := p.Validate(); err != nil {
		return User{}, err
	}
	data, _ := json.Marshal(p)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "UPDATE users SET policy=?,desired_revision=desired_revision+1 WHERE id=? AND desired_revision=?", string(data), id, expected)
	if err != nil {
		return User{}, err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return User{}, sql.ErrNoRows
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err = tx.ExecContext(ctx, "INSERT INTO audit(happened_at,actor,action,target) VALUES(?,?,?,?)", now, actor, "user.policy_updated", id); err != nil {
		return User{}, err
	}
	var u User
	u.Policy = p
	err = tx.QueryRowContext(ctx, "SELECT id,username,desired_revision,applied_revision,created_at FROM users WHERE id=?", id).Scan(&u.ID, &u.Username, &u.DesiredRevision, &u.AppliedRevision, &u.CreatedAt)
	if err != nil {
		return User{}, err
	}
	return u, tx.Commit()
}
