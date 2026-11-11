package store

import (
	"context"
	"testing"
	"time"
)

func joinTestNode(t *testing.T, s *Store, name string) Node {
	t.Helper()
	ctx := context.Background()
	code, _, err := s.CreateInvite(ctx, "admin", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	n, _, err := s.JoinNode(ctx, code, NodeJoin{Name: name, Domain: name + ".example.com"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestApplySnapshotsAndBumpsRevision(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	u, err := s.Create(ctx, "admin", "alice", "alice-password-123", Policy{Enabled: true, DownloadMbps: 50})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.MarkApplied(ctx, "admin", u.ID, 1); err != nil {
		t.Fatal(err)
	}
	users, err := s.Distributed(ctx)
	if err != nil || len(users) != 1 || users[0].Password != "alice-password-123" || users[0].DownloadMbps != 50 || !users[0].Enabled || users[0].Revision != 1 {
		t.Fatalf("snapshot %+v %v", users, err)
	}
	if rev, _ := s.UsersRevision(ctx); rev != 1 {
		t.Fatal("revision", rev)
	}
	// An edit that is not applied yet must not reach the nodes.
	if _, err = s.UpdatePolicy(ctx, "admin", u.ID, 1, Policy{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	users, _ = s.Distributed(ctx)
	if len(users) != 1 || !users[0].Enabled || users[0].DownloadMbps != 50 {
		t.Fatalf("unapplied edit leaked: %+v", users)
	}
	if rev, _ := s.UsersRevision(ctx); rev != 1 {
		t.Fatal("unapplied edit bumped revision", rev)
	}
	if err = s.Delete(ctx, "admin", u.ID, 2); err != nil {
		t.Fatal(err)
	}
	if users, _ = s.Distributed(ctx); len(users) != 0 {
		t.Fatal("deleted user still distributed")
	}
	if rev, _ := s.UsersRevision(ctx); rev != 2 {
		t.Fatal("delete did not bump revision", rev)
	}
}

func TestDistributedForNodeFollowsGroups(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	ams := joinTestNode(t, s, "ams")
	berlin := joinTestNode(t, s, "berlin")
	alice, err := s.Create(ctx, "admin", "alice", "alice-password-123", Policy{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := s.Create(ctx, "admin", "bob", "bob-password-123", Policy{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	work, err := s.CreateGroup(ctx, "Работа", []string{ams.ID}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetUserGroup(ctx, bob.ID, work.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{alice.ID, bob.ID} {
		if err = s.MarkApplied(ctx, "admin", id, 1); err != nil {
			t.Fatal(err)
		}
	}

	// Alice stays in the default group: its empty location list covers every node.
	amsUsers, err := s.DistributedForNode(ctx, ams.ID)
	if err != nil {
		t.Fatal(err)
	}
	berlinUsers, err := s.DistributedForNode(ctx, berlin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(amsUsers) != 2 || amsUsers[0].Username != "alice" || amsUsers[1].Username != "bob" {
		t.Fatalf("Amsterdam must serve both users, got %+v", amsUsers)
	}
	if len(berlinUsers) != 1 || berlinUsers[0].Username != "alice" {
		t.Fatalf("Berlin must serve only Alice, got %+v", berlinUsers)
	}

	// A user without a group row counts as the default group, so nothing vanishes
	// on an installation where the row is missing.
	if _, err = s.db.ExecContext(ctx, "DELETE FROM user_groups WHERE user_id=?", bob.ID); err != nil {
		t.Fatal(err)
	}
	berlinUsers, err = s.DistributedForNode(ctx, berlin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(berlinUsers) != 2 {
		t.Fatalf("a user without a group row must keep every node, got %+v", berlinUsers)
	}
}

func TestGroupChangesBumpUsersRevision(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	alice, err := s.Create(ctx, "admin", "alice", "alice-password-123", Policy{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.MarkApplied(ctx, "admin", alice.ID, 1); err != nil {
		t.Fatal(err)
	}
	before, _ := s.UsersRevision(ctx)
	work, err := s.CreateGroup(ctx, "Работа", []string{"ams"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rev, _ := s.UsersRevision(ctx); rev != before {
		t.Fatalf("creating an empty group must not bump revision: %d -> %d", before, rev)
	}
	if err = s.SetUserGroup(ctx, alice.ID, work.ID); err != nil {
		t.Fatal(err)
	}
	moved, _ := s.UsersRevision(ctx)
	if moved <= before {
		t.Fatalf("moving a user must bump revision: %d -> %d", before, moved)
	}
	if err = s.SetGroupLocations(ctx, work.ID, []string{"berlin"}); err != nil {
		t.Fatal(err)
	}
	if rev, _ := s.UsersRevision(ctx); rev <= moved {
		t.Fatalf("changing group locations must bump revision: %d -> %d", moved, rev)
	}
}

func TestDisconnectQueuedPerNodeAndAcked(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	n1, n2 := joinTestNode(t, s, "one"), joinTestNode(t, s, "two")
	if err := s.QueueDisconnect(ctx, "alice", time.Now()); err != nil {
		t.Fatal(err)
	}
	c1, _ := s.PendingCommands(ctx, n1.ID)
	c2, _ := s.PendingCommands(ctx, n2.ID)
	if len(c1) != 1 || len(c2) != 1 || c1[0].Kind != "disconnect" || c1[0].Username != "alice" {
		t.Fatal(c1, c2)
	}
	report := NodeReport{Acked: []int64{c1[0].ID}}
	if err := s.RecordNodeReport(ctx, n1.ID, report, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordNodeReport(ctx, n1.ID, report, time.Now()); err != nil {
		t.Fatal("repeated ack", err)
	}
	c1, _ = s.PendingCommands(ctx, n1.ID)
	c2, _ = s.PendingCommands(ctx, n2.ID)
	if len(c1) != 0 || len(c2) != 1 {
		t.Fatal("ack crossed nodes", c1, c2)
	}
	// A node cannot acknowledge another node's command.
	s.RecordNodeReport(ctx, n1.ID, NodeReport{Acked: []int64{c2[0].ID}}, time.Now())
	if c2, _ = s.PendingCommands(ctx, n2.ID); len(c2) != 1 {
		t.Fatal("foreign ack accepted")
	}
}

func TestNodeReportStatesUsagePresence(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	u, _ := s.Create(ctx, "admin", "alice", "alice-password-123", Policy{Enabled: true})
	n1, n2 := joinTestNode(t, s, "one"), joinTestNode(t, s, "two")
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	r1 := NodeReport{Revision: 3, EndpointVersion: "1.2", Users: []UserRevision{{ID: u.ID, Revision: 1}},
		Usage:    []UsageRow{{UserID: u.ID, Month: "2026-11", UploadBytes: 10, DownloadBytes: 100}},
		Presence: []PresenceRow{{UserID: u.ID, LastSeenAt: "2026-11-04T11:59:00Z", LastIP: "203.0.113.5"}}}
	if err := s.RecordNodeReport(ctx, n1.ID, r1, now); err != nil {
		t.Fatal(err)
	}
	r2 := NodeReport{Usage: []UsageRow{{UserID: u.ID, Month: "2026-11", UploadBytes: 1, DownloadBytes: 2}},
		Presence: []PresenceRow{{UserID: u.ID, LastSeenAt: "2026-11-04T10:00:00Z", LastIP: "198.51.100.9"}}}
	if err := s.RecordNodeReport(ctx, n2.ID, r2, now); err != nil {
		t.Fatal(err)
	}
	usage, _ := s.NodeUsage(ctx, "2026-11")
	if got := usage[u.ID]; got.UploadBytes != 11 || got.DownloadBytes != 102 {
		t.Fatalf("usage %+v", got)
	}
	node, _, details, err := s.NodeDetails(ctx, n1.ID, "2026-11")
	if err != nil {
		t.Fatal(err)
	}
	if node.ID != n1.ID || len(details) != 1 {
		t.Fatalf("node details: %+v, users %+v", node, details)
	}
	got := details[0]
	if got.Username != "alice" || got.ExpectedRevision != nil || got.AppliedRevision == nil ||
		*got.AppliedRevision != 1 || got.UploadBytes != 10 || got.DownloadBytes != 100 {
		t.Fatalf("node user details: %+v", got)
	}
	users, _ := s.List(ctx)
	if users[0].LastSeenAt != "2026-11-04T11:59:00Z" || users[0].LastIP != "203.0.113.5" {
		t.Fatalf("older presence overwrote newer: %+v", users[0])
	}
	states, _ := s.NodeUserStates(ctx)
	if len(states[u.ID]) != 1 || states[u.ID][0].NodeID != n1.ID || states[u.ID][0].Revision != 1 {
		t.Fatalf("states %+v", states)
	}
	nodes, _ := s.ListNodes(ctx)
	if nodes[0].ID != n1.ID {
		nodes[0] = nodes[1]
	}
	if nodes[0].LastSeenAt != "2026-11-04T12:00:00Z" || nodes[0].AppliedRevision != 3 || nodes[0].EndpointVersion != "1.2" {
		t.Fatalf("node %+v", nodes[0])
	}
}
