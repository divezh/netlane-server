package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/divezh/netlane-server/internal/security"
)

// DistributedUser is a user as nodes receive it: the state of the last successful apply.
type DistributedUser struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	Password     string `json:"password"`
	Enabled      bool   `json:"enabled"`
	DownloadMbps int    `json:"download_mbps"`
	UploadMbps   int    `json:"upload_mbps"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	Revision     int64  `json:"revision"`
}

type Command struct {
	ID       int64  `json:"id"`
	Kind     string `json:"kind"`
	Username string `json:"username"`
}

type UserRevision struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
}

type UsageRow struct {
	UserID        string `json:"user_id"`
	Month         string `json:"month"`
	UploadBytes   uint64 `json:"upload_bytes"`
	DownloadBytes uint64 `json:"download_bytes"`
}

type PresenceRow struct {
	UserID     string `json:"user_id"`
	LastSeenAt string `json:"last_seen_at"`
	LastIP     string `json:"last_ip,omitempty"`
}

// NodeReport is what a node sends with every sync.
type NodeReport struct {
	Statistics      *StatisticsObservation `json:"statistics,omitempty"`
	Revision        int64                  `json:"revision"`
	Users           []UserRevision         `json:"users"`
	Usage           []UsageRow             `json:"usage"`
	Presence        []PresenceRow          `json:"presence"`
	Acked           []int64                `json:"acked_commands"`
	EndpointVersion string                 `json:"endpoint_version"`
}

// Location is one node's confirmed revision of a user.
type Location struct {
	NodeID   string
	Revision int64
}

func bumpRevision(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, "UPDATE meta SET value=value+1 WHERE key='users_revision'")
	return err
}

// snapshot stores the user's current password and policy as what nodes receive.
func (s *Store) snapshot(ctx context.Context, tx *sql.Tx, id string, revision int64) error {
	var username, sealed, policy string
	if err := tx.QueryRowContext(ctx, "SELECT username,credential,policy FROM users WHERE id=?", id).Scan(&username, &sealed, &policy); err != nil {
		return err
	}
	var p Policy
	if err := json.Unmarshal([]byte(policy), &p); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO distributed_users(user_id,username,credential,enabled,download_mbps,upload_mbps,expires_at,revision) VALUES(?,?,?,?,?,?,?,?)
 ON CONFLICT(user_id) DO UPDATE SET username=excluded.username,credential=excluded.credential,enabled=excluded.enabled,download_mbps=excluded.download_mbps,upload_mbps=excluded.upload_mbps,expires_at=excluded.expires_at,revision=excluded.revision`,
		id, username, sealed, p.Enabled, p.DownloadMbps, p.UploadMbps, p.ExpiresAt, revision)
	if err != nil {
		return err
	}
	return bumpRevision(ctx, tx)
}

func (s *Store) UsersRevision(ctx context.Context) (int64, error) {
	var v int64
	err := s.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='users_revision'").Scan(&v)
	return v, err
}

// Distributed lists the users nodes must have, with their passwords.
func (s *Store) Distributed(ctx context.Context) ([]DistributedUser, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT user_id,username,credential,enabled,download_mbps,upload_mbps,expires_at,revision FROM distributed_users ORDER BY username")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return s.scanDistributed(rows)
}

// DistributedForNode lists the users a node must serve: those whose group covers the
// node. A group without locations covers every node and a user without a group row
// counts as the default group, so installations that never touch groups keep sending
// every user to every node.
func (s *Store) DistributedForNode(ctx context.Context, nodeID string) ([]DistributedUser, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.user_id,d.username,d.credential,d.enabled,d.download_mbps,d.upload_mbps,d.expires_at,d.revision
 FROM distributed_users d
 LEFT JOIN user_groups ug ON ug.user_id=d.user_id
 WHERE ug.user_id IS NULL
    OR NOT EXISTS(SELECT 1 FROM group_locations gl WHERE gl.group_id=ug.group_id)
    OR EXISTS(SELECT 1 FROM group_locations gl WHERE gl.group_id=ug.group_id AND gl.location_id=?)
 ORDER BY d.username`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users, err := s.scanDistributed(rows)
	if err != nil {
		return nil, err
	}
	// A node gets only its own password for each user (security.NodePassword).
	for i := range users {
		users[i].Password = security.NodePassword(users[i].Password, nodeID)
	}
	return users, nil
}

func (s *Store) scanDistributed(rows *sql.Rows) ([]DistributedUser, error) {
	users := []DistributedUser{}
	for rows.Next() {
		var u DistributedUser
		var sealed string
		if err := rows.Scan(&u.ID, &u.Username, &sealed, &u.Enabled, &u.DownloadMbps, &u.UploadMbps, &u.ExpiresAt, &u.Revision); err != nil {
			return nil, err
		}
		password, err := security.Open(s.key, u.ID, sealed)
		if err != nil {
			return nil, err
		}
		u.Password = password
		users = append(users, u)
	}
	return users, rows.Err()
}

// DistributedUserByID is one user's snapshot as nodes have it; sql.ErrNoRows when none.
func (s *Store) DistributedUserByID(ctx context.Context, id string) (DistributedUser, error) {
	var u DistributedUser
	var sealed string
	err := s.db.QueryRowContext(ctx, "SELECT user_id,username,credential,enabled,download_mbps,upload_mbps,expires_at,revision FROM distributed_users WHERE user_id=?", id).Scan(&u.ID, &u.Username, &sealed, &u.Enabled, &u.DownloadMbps, &u.UploadMbps, &u.ExpiresAt, &u.Revision)
	if err != nil {
		return DistributedUser{}, err
	}
	u.Password, err = security.Open(s.key, u.ID, sealed)
	return u, err
}

// QueueDisconnect asks every node to drop the user's sessions.
func (s *Store) QueueDisconnect(ctx context.Context, username string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO node_commands(node_id,kind,username,created_at) SELECT id,'disconnect',?,? FROM nodes", username, now.UTC().Format(time.RFC3339))
	return err
}

func (s *Store) PendingCommands(ctx context.Context, nodeID string) ([]Command, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,kind,username FROM node_commands WHERE node_id=? AND acked_at='' ORDER BY id", nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cmds := []Command{}
	for rows.Next() {
		var c Command
		if err = rows.Scan(&c.ID, &c.Kind, &c.Username); err != nil {
			return nil, err
		}
		cmds = append(cmds, c)
	}
	return cmds, rows.Err()
}

func normalTime(value string) (string, bool) {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return "", false
	}
	return t.UTC().Format(time.RFC3339), true
}

// RecordNodeReport stores a node's sync report in one transaction.
func (s *Store) RecordNodeReport(ctx context.Context, nodeID string, r NodeReport, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stamp := now.UTC().Format(time.RFC3339)
	result, err := tx.ExecContext(ctx, "UPDATE nodes SET last_seen_at=?,applied_revision=?,endpoint_version=CASE WHEN ?='' THEN endpoint_version ELSE ? END WHERE id=?", stamp, r.Revision, r.EndpointVersion, r.EndpointVersion, nodeID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return sql.ErrNoRows
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM node_user_state WHERE node_id=?", nodeID); err != nil {
		return err
	}
	for _, u := range r.Users {
		if _, err = tx.ExecContext(ctx, "INSERT OR REPLACE INTO node_user_state(node_id,user_id,revision) VALUES(?,?,?)", nodeID, u.ID, u.Revision); err != nil {
			return err
		}
	}
	for _, u := range r.Usage {
		if _, err = tx.ExecContext(ctx, `INSERT INTO node_usage(node_id,user_id,month,upload_bytes,download_bytes) VALUES(?,?,?,?,?)
 ON CONFLICT(node_id,user_id,month) DO UPDATE SET upload_bytes=excluded.upload_bytes,download_bytes=excluded.download_bytes`, nodeID, u.UserID, u.Month, int64(u.UploadBytes), int64(u.DownloadBytes)); err != nil {
			return err
		}
	}
	for _, p := range r.Presence {
		seen, ok := normalTime(p.LastSeenAt)
		if !ok {
			continue
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO presence(user_id,last_seen_at,last_ip) SELECT id,?,? FROM users WHERE id=?
 ON CONFLICT(user_id) DO UPDATE SET last_seen_at=excluded.last_seen_at, last_ip=CASE WHEN excluded.last_ip='' THEN presence.last_ip ELSE excluded.last_ip END
 WHERE excluded.last_seen_at > presence.last_seen_at`, seen, p.LastIP, p.UserID); err != nil {
			return err
		}
	}
	for _, id := range r.Acked {
		if _, err = tx.ExecContext(ctx, "UPDATE node_commands SET acked_at=? WHERE id=? AND node_id=? AND acked_at=''", stamp, id, nodeID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// NodeUserStates maps each user id to the revisions nodes confirmed for it.
func (s *Store) NodeUserStates(ctx context.Context) (map[string][]Location, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT user_id,node_id,revision FROM node_user_state ORDER BY node_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := map[string][]Location{}
	for rows.Next() {
		var user string
		var l Location
		if err = rows.Scan(&user, &l.NodeID, &l.Revision); err != nil {
			return nil, err
		}
		states[user] = append(states[user], l)
	}
	return states, rows.Err()
}

// NodeUsage sums what all nodes reported for the month, per user.
func (s *Store) NodeUsage(ctx context.Context, month string) (map[string]UsageRow, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT user_id,SUM(upload_bytes),SUM(download_bytes) FROM node_usage WHERE month=? GROUP BY user_id", month)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	usage := map[string]UsageRow{}
	for rows.Next() {
		r := UsageRow{Month: month}
		var up, down int64
		if err = rows.Scan(&r.UserID, &up, &down); err != nil {
			return nil, err
		}
		r.UploadBytes, r.DownloadBytes = uint64(up), uint64(down)
		usage[r.UserID] = r
	}
	return usage, rows.Err()
}

// UsageByNode returns the reported transfer totals for each registered node in a month.
func (s *Store) UsageByNode(ctx context.Context, month string) (map[string]uint64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT node_id, SUM(upload_bytes + download_bytes)
        FROM node_usage WHERE month=? GROUP BY node_id`, month)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	totals := make(map[string]uint64)
	for rows.Next() {
		var id string
		var total uint64
		if err := rows.Scan(&id, &total); err != nil {
			return nil, err
		}
		totals[id] = total
	}
	return totals, rows.Err()
}
