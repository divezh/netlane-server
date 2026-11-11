package store

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "panel.sqlite"), bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestMigrationGivesEveryUserTheDefaultGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.sqlite")
	key := bytes.Repeat([]byte{1}, 32)
	first, err := Open(path, key)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	user, err := first.Create(context.Background(), "admin", "i_ivanov", "secret-password", Policy{Enabled: true})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err = first.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// Reopening runs the migration again: the user created before groups existed
	// must end up in the default group instead of losing every location.
	second, err := Open(path, key)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = second.Close() }()
	groups, err := second.UserGroups(context.Background())
	if err != nil {
		t.Fatalf("user groups: %v", err)
	}
	if groups[user.ID] != DefaultGroupID {
		t.Fatalf("user must land in %q, got %q", DefaultGroupID, groups[user.ID])
	}
	all, err := second.GroupByID(context.Background(), DefaultGroupID)
	if err != nil {
		t.Fatalf("default group: %v", err)
	}
	if all.Name != DefaultGroupName || all.Members != 1 {
		t.Fatalf("unexpected default group: %+v", all)
	}
	if !all.Covers("any-node") {
		t.Fatal("a group without locations must cover every location")
	}
}

func TestGroupLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now()

	work, err := s.CreateGroup(ctx, "  Работа  ", []string{"ams", "ams", "warsaw"}, now)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if work.Name != "Работа" {
		t.Fatalf("name must be trimmed, got %q", work.Name)
	}
	if len(work.Locations) != 2 {
		t.Fatalf("duplicate locations must collapse, got %v", work.Locations)
	}

	groups, err := s.Groups(ctx)
	if err != nil {
		t.Fatalf("list groups: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("expected the default group and the new one, got %d", len(groups))
	}

	if err = s.RenameGroup(ctx, work.ID, "Офис"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if err = s.SetGroupLocations(ctx, work.ID, []string{"berlin"}); err != nil {
		t.Fatalf("set locations: %v", err)
	}
	renamed, err := s.GroupByID(ctx, work.ID)
	if err != nil {
		t.Fatalf("read group: %v", err)
	}
	if renamed.Name != "Офис" || len(renamed.Locations) != 1 || renamed.Locations[0] != "berlin" {
		t.Fatalf("unexpected group after edits: %+v", renamed)
	}

	if err = s.DeleteGroup(ctx, work.ID); err != nil {
		t.Fatalf("delete empty group: %v", err)
	}
	if _, err = s.GroupByID(ctx, work.ID); !errors.Is(err, ErrGroupUnknown) {
		t.Fatalf("deleted group must be gone, got %v", err)
	}
}

func TestGroupWithMembersIsNotDeleted(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	user, err := s.Create(ctx, "admin", "i_ivanov", "secret-password", Policy{Enabled: true})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	group, err := s.CreateGroup(ctx, "Работа", nil, time.Now())
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err = s.SetUserGroup(ctx, user.ID, group.ID); err != nil {
		t.Fatalf("assign user: %v", err)
	}
	if err = s.DeleteGroup(ctx, group.ID); !errors.Is(err, ErrGroupNotEmpty) {
		t.Fatalf("a group with members must not be deleted, got %v", err)
	}
	if err = s.SetUserGroup(ctx, user.ID, DefaultGroupID); err != nil {
		t.Fatalf("move user back: %v", err)
	}
	if err = s.DeleteGroup(ctx, group.ID); err != nil {
		t.Fatalf("emptied group must be deletable: %v", err)
	}
}

func TestDefaultGroupIsNotDeleted(t *testing.T) {
	s := testStore(t)
	if err := s.DeleteGroup(context.Background(), DefaultGroupID); !errors.Is(err, ErrGroupFixed) {
		t.Fatalf("the default group must stay, got %v", err)
	}
}

func TestLocationGroupsIncludesUnrestrictedGroups(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	work, err := s.CreateGroup(ctx, "Работа", []string{"ams"}, time.Now())
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	ams, err := s.LocationGroups(ctx, "ams")
	if err != nil {
		t.Fatalf("location groups: %v", err)
	}
	if len(ams) != 2 {
		t.Fatalf("Amsterdam must serve the default group and Работа, got %v", ams)
	}
	berlin, err := s.LocationGroups(ctx, "berlin")
	if err != nil {
		t.Fatalf("location groups: %v", err)
	}
	if len(berlin) != 1 || berlin[0] != DefaultGroupID {
		t.Fatalf("Berlin must serve only the unrestricted group, got %v", berlin)
	}
	_ = work
}

func TestSetUsersGroupMovesMany(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	first, err := s.Create(ctx, "admin", "a_one", "secret-password", Policy{Enabled: true})
	if err != nil {
		t.Fatalf("create first user: %v", err)
	}
	second, err := s.Create(ctx, "admin", "b_two", "secret-password", Policy{Enabled: true})
	if err != nil {
		t.Fatalf("create second user: %v", err)
	}
	group, err := s.CreateGroup(ctx, "Европа", []string{"fra"}, time.Now())
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err = s.SetUsersGroup(ctx, []string{first.ID, second.ID}, group.ID); err != nil {
		t.Fatalf("bulk move: %v", err)
	}
	groups, err := s.UserGroups(ctx)
	if err != nil {
		t.Fatalf("user groups: %v", err)
	}
	for _, id := range []string{first.ID, second.ID} {
		if groups[id] != group.ID {
			t.Fatalf("user %s must be in %s, got %s", id, group.ID, groups[id])
		}
	}
}

func TestLocationsForUser(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	user, err := s.Create(ctx, "admin", "i_ivanov", "secret-password", Policy{Enabled: true})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	// The default group has no locations: the user may use every location.
	locations, err := s.LocationsForUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("locations for user: %v", err)
	}
	if len(locations) != 0 {
		t.Fatalf("a user of the default group must get every location, got %v", locations)
	}
	work, err := s.CreateGroup(ctx, "Работа", []string{"ams", "berlin"}, time.Now())
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err = s.SetUserGroup(ctx, user.ID, work.ID); err != nil {
		t.Fatalf("assign user: %v", err)
	}
	locations, err = s.LocationsForUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("locations for user: %v", err)
	}
	if len(locations) != 2 || locations[0] != "ams" || locations[1] != "berlin" {
		t.Fatalf("unexpected locations: %v", locations)
	}
	if !CoversLocation(locations, "ams") || CoversLocation(locations, "fra") {
		t.Fatalf("covers must follow the list, got %v", locations)
	}
	// A user without a group row gets every location rather than none.
	unknown, err := s.Create(ctx, "admin", "b_petrov", "secret-password", Policy{Enabled: true})
	if err != nil {
		t.Fatalf("create second user: %v", err)
	}
	if _, err = s.db.ExecContext(ctx, "DELETE FROM user_groups WHERE user_id=?", unknown.ID); err != nil {
		t.Fatalf("drop group row: %v", err)
	}
	locations, err = s.LocationsForUser(ctx, unknown.ID)
	if err != nil {
		t.Fatalf("locations for unknown group: %v", err)
	}
	if len(locations) != 0 {
		t.Fatalf("a user without a group row must get every location, got %v", locations)
	}
}

func TestGroupNameIsValidated(t *testing.T) {
	s := testStore(t)
	for _, name := range []string{"", "   ", string(make([]rune, 41))} {
		if _, err := s.CreateGroup(context.Background(), name, nil, time.Now()); !errors.Is(err, ErrGroupName) {
			t.Fatalf("name %q must be refused, got %v", name, err)
		}
	}
}

func TestDeletedUserLeavesGroupCount(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	user, err := s.Create(ctx, "admin", "artem", "secret-password", Policy{Enabled: true})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	group, err := s.CreateGroup(ctx, "Работа", []string{"ams"}, time.Now())
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err = s.SetUserGroup(ctx, user.ID, group.ID); err != nil {
		t.Fatalf("set group: %v", err)
	}
	if err = s.Delete(ctx, "admin", user.ID, user.DesiredRevision); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	after, err := s.GroupByID(ctx, group.ID)
	if err != nil {
		t.Fatalf("group after delete: %v", err)
	}
	if after.Members != 0 {
		t.Fatalf("deleted user still counted in group: %+v", after)
	}
	if err = s.DeleteGroup(ctx, group.ID); err != nil {
		t.Fatalf("empty group after deleting its user must be deletable: %v", err)
	}
}
