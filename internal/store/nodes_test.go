package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "panel.sqlite")
	s, err := Open(path, bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

const schemaV1 = `BEGIN;
 CREATE TABLE users(id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, credential TEXT NOT NULL, policy TEXT NOT NULL, desired_revision INTEGER NOT NULL DEFAULT 1, applied_revision INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL);
 CREATE TABLE audit(id INTEGER PRIMARY KEY, happened_at TEXT NOT NULL, actor TEXT NOT NULL, action TEXT NOT NULL, target TEXT NOT NULL);
 CREATE TABLE presence(user_id TEXT PRIMARY KEY, last_seen_at TEXT NOT NULL, last_ip TEXT NOT NULL DEFAULT '');
 INSERT INTO users(id,username,credential,policy,created_at) VALUES('u1','alex','x','{"enabled":true}','2026-01-01T00:00:00Z');
 PRAGMA user_version=1; COMMIT;`

func TestNodeTablesAddedToV1Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.sqlite")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(schemaV1); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	s, err := Open(path, bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version int
	s.db.QueryRow("PRAGMA user_version").Scan(&version)
	if version != 1 {
		t.Fatalf("schema %d", version)
	}
	users, err := s.List(context.Background())
	if err != nil || len(users) != 1 || users[0].Username != "alex" {
		t.Fatalf("users lost: %v %v", users, err)
	}
	if ok, err := s.HasNodes(context.Background()); err != nil || ok {
		t.Fatal("fresh nodes table not empty")
	}
}

func TestRefuseNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.sqlite")
	raw, _ := sql.Open("sqlite", path)
	raw.Exec("PRAGMA user_version=3")
	raw.Close()
	if _, err := Open(path, bytes.Repeat([]byte{1}, 32)); err == nil {
		t.Fatal("newer schema opened")
	}
}

func TestInviteSingleUseAndExpiry(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	code, expires, err := s.CreateInvite(ctx, "admin", now)
	if err != nil || !expires.Equal(now.Add(24*time.Hour)) {
		t.Fatal(expires, err)
	}
	join := NodeJoin{Name: "Франкфурт", Country: "DE", Domain: "de.example.com", EndpointVersion: "1.0"}
	node, token, err := s.JoinNode(ctx, code, join, now.Add(time.Hour))
	if err != nil || node.ID == "" || token == "" || node.Domain != "de.example.com" {
		t.Fatal(node, err)
	}
	if _, _, err = s.JoinNode(ctx, code, join, now.Add(time.Hour)); !errors.Is(err, ErrInvalidInvite) {
		t.Fatal("invite reused", err)
	}
	late, _, _ := s.CreateInvite(ctx, "admin", now)
	if _, _, err = s.JoinNode(ctx, late, join, now.Add(25*time.Hour)); !errors.Is(err, ErrInvalidInvite) {
		t.Fatal("expired invite accepted", err)
	}
	if _, _, err = s.JoinNode(ctx, "nope", join, now); !errors.Is(err, ErrInvalidInvite) {
		t.Fatal("unknown invite accepted", err)
	}
}

func TestNodeTokenLookupAndDelete(t *testing.T) {
	s, path := openTest(t)
	ctx := context.Background()
	now := time.Now().UTC()
	code, _, _ := s.CreateInvite(ctx, "admin", now)
	node, token, err := s.JoinNode(ctx, code, NodeJoin{Name: "X", Domain: "x.example.com"}, now)
	if err != nil {
		t.Fatal(err)
	}
	found, err := s.NodeByToken(ctx, token)
	if err != nil || found.ID != node.ID {
		t.Fatal(found, err)
	}
	if nodes, _ := s.ListNodes(ctx); len(nodes) != 1 {
		t.Fatal(nodes)
	}
	if err = s.DeleteNode(ctx, "admin", node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.NodeByToken(ctx, token); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("revoked token still works", err)
	}
	s.Close()
	data, _ := readAll(path)
	if strings.Contains(data, token) || strings.Contains(data, code) {
		t.Fatal("raw token or code stored")
	}
}

func readAll(path string) (string, error) {
	var out strings.Builder
	for _, p := range []string{path, path + "-wal"} {
		b, err := os.ReadFile(p)
		if err == nil {
			out.Write(b)
		}
	}
	return out.String(), nil
}
