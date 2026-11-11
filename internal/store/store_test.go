package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPersistenceEncryptionAndRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.sqlite")
	key := bytes.Repeat([]byte{1}, 32)
	s, err := Open(path, key)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	p := Policy{Enabled: true, QuotaBytes: 1000000000, DownloadMbps: 20, UploadMbps: 10, ExpiresAt: "2026-12-01T00:00:00+03:00"}
	u, err := s.Create(ctx, "admin", "alex", "unique-vpn-password", p)
	if err != nil {
		t.Fatal(err)
	}
	if u.AppliedRevision != 0 {
		t.Fatal("unapplied policy claimed as applied")
	}
	if _, err = s.Create(ctx, "admin", "alex", "other-password-123", p); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict: %v", err)
	}
	p.DownloadMbps = 30
	u, err = s.UpdatePolicy(ctx, "admin", u.ID, 1, p)
	if err != nil || u.DesiredRevision != 2 {
		t.Fatalf("revision: %v", err)
	}
	if _, err = s.UpdatePolicy(ctx, "admin", u.ID, 1, p); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("stale revision accepted")
	}
	var credential string
	if err = s.db.QueryRow("SELECT credential FROM users WHERE id=?", u.ID).Scan(&credential); err != nil {
		t.Fatal(err)
	}
	if credential == "unique-vpn-password" {
		t.Fatal("plaintext password stored")
	}
	var audit int
	if err = s.db.QueryRow("SELECT COUNT(*) FROM audit").Scan(&audit); err != nil || audit != 2 {
		t.Fatal("audit and writes are not consistent")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, key)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	users, err := s.List(ctx)
	if err != nil || len(users) != 1 || users[0].DesiredRevision != 2 {
		t.Fatal("data did not survive reopening")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("unique-vpn-password")) {
		t.Fatal("database leaked password")
	}
}

func TestPolicyValidation(t *testing.T) {
	for _, p := range []Policy{{QuotaBytes: -1}, {DownloadMbps: -1}, {UploadMbps: 1000001}, {ExpiresAt: "tomorrow"}} {
		if p.Validate() == nil {
			t.Fatalf("invalid policy accepted: %+v", p)
		}
	}
}

func TestDeleteIsAtomicAndRetainsAudit(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "panel.sqlite"), bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	u, err := s.Create(ctx, "admin", "alice", "test-password-123", Policy{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Delete(ctx, "admin", u.ID, 2); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("stale delete accepted", err)
	}
	if _, _, err = s.Managed(ctx, u.ID); err != nil {
		t.Fatal("stale delete removed user", err)
	}
	// An audit failure must roll back the credential deletion.
	if _, err = s.db.Exec(`CREATE TRIGGER fail_delete_audit BEFORE INSERT ON audit WHEN NEW.action='user.deleted' BEGIN SELECT RAISE(ABORT,'test audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err = s.Delete(ctx, "admin", u.ID, 1); err == nil {
		t.Fatal("audit failure ignored")
	}
	if _, _, err = s.Managed(ctx, u.ID); err != nil {
		t.Fatal("audit failure removed user", err)
	}
	if _, err = s.db.Exec("DROP TRIGGER fail_delete_audit"); err != nil {
		t.Fatal(err)
	}
	if err = s.Delete(ctx, "admin", u.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Managed(ctx, u.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("credential retained", err)
	}
	var events int
	if err = s.db.QueryRow("SELECT COUNT(*) FROM audit WHERE target=?", u.ID).Scan(&events); err != nil || events != 2 {
		t.Fatal("audit history lost", events, err)
	}
	replacement, err := s.Create(ctx, "admin", "alice", "test-password-123", Policy{})
	if err != nil || replacement.ID == u.ID {
		t.Fatal("name reuse failed", err)
	}
}

func TestAppliedRevisionDoesNotClaimConcurrentEdit(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "panel.sqlite"), bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	p := Policy{Enabled: true, DownloadMbps: 2}
	u, err := s.Create(ctx, "admin", "alice", "test-password-123", p)
	if err != nil {
		t.Fatal(err)
	}
	p.DownloadMbps = 4
	if _, err = s.UpdatePolicy(ctx, "admin", u.ID, 1, p); err != nil {
		t.Fatal(err)
	}
	if err = s.MarkApplied(ctx, "admin", u.ID, 1); err != nil {
		t.Fatal(err)
	}
	users, _ := s.List(ctx)
	if users[0].AppliedRevision != 1 || users[0].DesiredRevision != 2 {
		t.Fatal("concurrent unapplied revision was advertised as applied")
	}
	if err = s.MarkApplied(ctx, "admin", u.ID, 3); err == nil {
		t.Fatal("future revision accepted")
	}
	if err = s.MarkApplied(ctx, "admin", u.ID, 2); err != nil {
		t.Fatal(err)
	}
}

func TestNewerDatabaseIsNotSilentlyDowngraded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "newer.sqlite")
	key := bytes.Repeat([]byte{3}, 32)
	s, err := Open(path, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("PRAGMA user_version=2"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s, err = Open(path, key); err == nil {
		s.Close()
		t.Fatal("newer schema accepted")
	}
}
