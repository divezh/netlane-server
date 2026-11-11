package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/divezh/netlane-server/internal/security"
)

// Node is a VPN location that syncs users from main.
type Node struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Country         string `json:"country"`
	Domain          string `json:"domain"`
	CreatedAt       string `json:"created_at"`
	LastSeenAt      string `json:"last_seen_at"`
	AppliedRevision int64  `json:"applied_revision"`
	EndpointVersion string `json:"endpoint_version"`
}

// NodeJoin is what a node tells main when it registers.
type NodeJoin struct {
	Name            string `json:"name"`
	Country         string `json:"country"`
	Domain          string `json:"domain"`
	EndpointVersion string `json:"endpoint_version"`
}

var ErrInvalidInvite = errors.New("invalid or used invite")

// InviteLifetime is how long a node invitation code stays valid.
const InviteLifetime = 24 * time.Hour

func secretHash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// CreateInvite issues a single-use node invitation code.
func (s *Store) CreateInvite(ctx context.Context, actor string, now time.Time) (string, time.Time, error) {
	code := security.Token(32)
	expires := now.UTC().Add(InviteLifetime)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "INSERT INTO node_invites(code_hash,expires_at) VALUES(?,?)", secretHash(code), expires.Format(time.RFC3339)); err != nil {
		return "", time.Time{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO audit(happened_at,actor,action,target) VALUES(?,?,?,?)", now.UTC().Format(time.RFC3339), actor, "node.invite_created", ""); err != nil {
		return "", time.Time{}, err
	}
	return code, expires, tx.Commit()
}

// JoinNode spends an invitation code and registers a node; it returns the node token.
func (s *Store) JoinNode(ctx context.Context, code string, j NodeJoin, now time.Time) (Node, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Node{}, "", err
	}
	defer tx.Rollback()
	stamp := now.UTC().Format(time.RFC3339)
	result, err := tx.ExecContext(ctx, "UPDATE node_invites SET used_at=? WHERE code_hash=? AND used_at='' AND expires_at>?", stamp, secretHash(code), stamp)
	if err != nil {
		return Node{}, "", err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return Node{}, "", ErrInvalidInvite
	}
	token := security.Token(32)
	node := Node{ID: security.Token(16), Name: j.Name, Country: j.Country, Domain: j.Domain, CreatedAt: stamp, EndpointVersion: j.EndpointVersion}
	if _, err = tx.ExecContext(ctx, "INSERT INTO nodes(id,name,country,domain,token_hash,created_at,endpoint_version) VALUES(?,?,?,?,?,?,?)", node.ID, node.Name, node.Country, node.Domain, secretHash(token), stamp, node.EndpointVersion); err != nil {
		return Node{}, "", err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO audit(happened_at,actor,action,target) VALUES(?,?,?,?)", stamp, "node", "node.joined", node.ID); err != nil {
		return Node{}, "", err
	}
	return node, token, tx.Commit()
}

const nodeColumns = "id,name,country,domain,created_at,last_seen_at,applied_revision,endpoint_version"

func scanNode(row interface{ Scan(...any) error }) (Node, error) {
	var n Node
	err := row.Scan(&n.ID, &n.Name, &n.Country, &n.Domain, &n.CreatedAt, &n.LastSeenAt, &n.AppliedRevision, &n.EndpointVersion)
	return n, err
}

// NodeByToken finds the node a token belongs to; sql.ErrNoRows when none does.
func (s *Store) NodeByToken(ctx context.Context, token string) (Node, error) {
	return scanNode(s.db.QueryRowContext(ctx, "SELECT "+nodeColumns+" FROM nodes WHERE token_hash=?", secretHash(token)))
}

func (s *Store) ListNodes(ctx context.Context) ([]Node, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+nodeColumns+" FROM nodes ORDER BY created_at,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes := []Node{}
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

type NodeUserDetail struct {
	Username         string `json:"username"`
	ExpectedRevision *int64 `json:"expected_revision"`
	AppliedRevision  *int64 `json:"applied_revision"`
	Enabled          bool   `json:"enabled"`
	ExpiresAt        string `json:"expires_at"`
	UploadBytes      uint64 `json:"upload_bytes"`
	DownloadBytes    uint64 `json:"download_bytes"`
}

// NodeDetails reads the node, current revisions and monthly traffic from one database snapshot.
func (s *Store) NodeDetails(ctx context.Context, id, month string) (Node, int64, []NodeUserDetail, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Node{}, 0, nil, err
	}
	defer tx.Rollback()
	node, err := scanNode(tx.QueryRowContext(ctx, "SELECT "+nodeColumns+" FROM nodes WHERE id=?", id))
	if err != nil {
		return Node{}, 0, nil, err
	}
	var revision int64
	if err := tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='users_revision'").Scan(&revision); err != nil {
		return Node{}, 0, nil, err
	}
	rows, err := tx.QueryContext(ctx, `
        WITH relevant AS (
            SELECT user_id FROM distributed_users
            UNION SELECT user_id FROM node_user_state WHERE node_id=?1
            UNION SELECT user_id FROM node_usage WHERE node_id=?1 AND month=?2
        )
        SELECT COALESCE(u.username, 'Удалённый пользователь (' || r.user_id || ')'),
               d.revision, a.revision, COALESCE(d.enabled,0), COALESCE(d.expires_at,''),
               COALESCE(t.upload_bytes,0), COALESCE(t.download_bytes,0)
        FROM relevant r
        LEFT JOIN users u ON u.id=r.user_id
        LEFT JOIN distributed_users d ON d.user_id=r.user_id
        LEFT JOIN node_user_state a ON a.user_id=r.user_id AND a.node_id=?1
        LEFT JOIN node_usage t ON t.user_id=r.user_id AND t.node_id=?1 AND t.month=?2
        ORDER BY 1, r.user_id`, id, month)
	if err != nil {
		return Node{}, 0, nil, err
	}
	defer rows.Close()
	users := []NodeUserDetail{}
	for rows.Next() {
		var u NodeUserDetail
		if err := rows.Scan(&u.Username, &u.ExpectedRevision, &u.AppliedRevision, &u.Enabled, &u.ExpiresAt, &u.UploadBytes, &u.DownloadBytes); err != nil {
			return Node{}, 0, nil, err
		}
		users = append(users, u)
	}
	return node, revision, users, rows.Err()
}

// UpdateNode renames a node and changes its country. Clients pick it up with the next
// subscription refresh: the location keeps its id, only the name and flag change.
func (s *Store) UpdateNode(ctx context.Context, actor, id, name, country string) (Node, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Node{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "UPDATE nodes SET name=?, country=? WHERE id=?", name, country, id)
	if err != nil {
		return Node{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return Node{}, sql.ErrNoRows
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO audit(happened_at,actor,action,target) VALUES(?,?,?,?)", time.Now().UTC().Format(time.RFC3339), actor, "node.updated", id); err != nil {
		return Node{}, err
	}
	node, err := scanNode(tx.QueryRowContext(ctx, "SELECT "+nodeColumns+" FROM nodes WHERE id=?", id))
	if err != nil {
		return Node{}, err
	}
	return node, tx.Commit()
}

// DeleteNode revokes a node's token and forgets its sync state.
func (s *Store) DeleteNode(ctx context.Context, actor, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "DELETE FROM nodes WHERE id=?", id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return sql.ErrNoRows
	}
	for _, table := range []string{"node_user_state", "node_commands", "node_usage"} {
		if _, err = tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE node_id=?", id); err != nil {
			return err
		}
	}
	for _, table := range []string{"location_statistics_samples", "location_statistics_baseline"} {
		if _, err = tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE source=?", id); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO audit(happened_at,actor,action,target) VALUES(?,?,?,?)", time.Now().UTC().Format(time.RFC3339), actor, "node.deleted", id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) HasNodes(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM nodes").Scan(&n)
	return n > 0, err
}
