package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/divezh/netlane-server/internal/security"
)

// Groups bind users to locations. A user belongs to exactly one group and may use
// every location of that group; a node serves the users of the groups it is
// attached to. The main server is not restricted: it hosts the panel and is the
// entry point, so it serves everyone.
//
// A group without locations means every location, including nodes added later. That
// is how the panel keeps working for installations that never touch groups: the
// default group created by the migration has no locations and holds every user.
const (
	DefaultGroupID   = "all"
	DefaultGroupName = "Все локации"
)

var (
	ErrGroupUnknown  = errors.New("group does not exist")
	ErrGroupNotEmpty = errors.New("group still has members")
	ErrGroupName     = errors.New("group name must be 1 to 40 characters")
	ErrGroupFixed    = errors.New("the default group cannot be deleted")
)

// Group is a named set of locations with its members counted.
type Group struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	CreatedAt string   `json:"created_at"`
	Locations []string `json:"locations"`
	Members   int      `json:"members"`
}

// CoversLocation reports whether a location list makes a location available. An
// empty list covers everything, including nodes added later.
func CoversLocation(locations []string, locationID string) bool {
	if len(locations) == 0 {
		return true
	}
	for _, id := range locations {
		if id == locationID {
			return true
		}
	}
	return false
}

// Covers reports whether the group makes a location available. An empty list of
// locations covers everything.
func (g Group) Covers(locationID string) bool { return CoversLocation(g.Locations, locationID) }

func validGroupName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 40 {
		return false
	}
	return strings.IndexFunc(name, unicode.IsControl) < 0
}

// Groups lists the groups with their locations and member counts, by name.
func (s *Store) Groups(ctx context.Context) ([]Group, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT g.id,g.name,g.created_at,
  (SELECT COUNT(*) FROM user_groups ug JOIN users u ON u.id=ug.user_id WHERE ug.group_id=g.id),
  (SELECT COUNT(*) FROM group_locations gl WHERE gl.group_id=g.id)
 FROM groups g ORDER BY g.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := []Group{}
	for rows.Next() {
		var g Group
		var locations int
		if err = rows.Scan(&g.ID, &g.Name, &g.CreatedAt, &g.Members, &locations); err != nil {
			return nil, err
		}
		g.Locations = []string{}
		groups = append(groups, g)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for i := range groups {
		if groups[i].Locations, err = s.groupLocations(ctx, groups[i].ID); err != nil {
			return nil, err
		}
	}
	return groups, nil
}

func (s *Store) groupLocations(ctx context.Context, groupID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT location_id FROM group_locations WHERE group_id=? ORDER BY location_id", groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	locations := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		locations = append(locations, id)
	}
	return locations, rows.Err()
}

// GroupByID reads one group; ErrGroupUnknown when it is gone.
func (s *Store) GroupByID(ctx context.Context, id string) (Group, error) {
	var g Group
	err := s.db.QueryRowContext(ctx, `SELECT g.id,g.name,g.created_at,
  (SELECT COUNT(*) FROM user_groups ug JOIN users u ON u.id=ug.user_id WHERE ug.group_id=g.id)
 FROM groups g WHERE g.id=?`, id).Scan(&g.ID, &g.Name, &g.CreatedAt, &g.Members)
	if errors.Is(err, sql.ErrNoRows) {
		return Group{}, ErrGroupUnknown
	}
	if err != nil {
		return Group{}, err
	}
	if g.Locations, err = s.groupLocations(ctx, g.ID); err != nil {
		return Group{}, err
	}
	return g, nil
}

// CreateGroup adds a group with the given locations.
func (s *Store) CreateGroup(ctx context.Context, name string, locations []string, now time.Time) (Group, error) {
	name = strings.TrimSpace(name)
	if !validGroupName(name) {
		return Group{}, ErrGroupName
	}
	id := security.Token(9)
	created := now.UTC().Format(time.RFC3339)
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO groups(id,name,created_at) VALUES(?,?,?)", id, name, created); err != nil {
			return err
		}
		return setGroupLocations(ctx, tx, id, locations)
	})
	if err != nil {
		return Group{}, err
	}
	return Group{ID: id, Name: name, CreatedAt: created, Locations: dedupe(locations)}, nil
}

// RenameGroup changes the name; the default group may be renamed as well.
func (s *Store) RenameGroup(ctx context.Context, id, name string) error {
	name = strings.TrimSpace(name)
	if !validGroupName(name) {
		return ErrGroupName
	}
	result, err := s.db.ExecContext(ctx, "UPDATE groups SET name=? WHERE id=?", name, id)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err == nil && changed == 0 {
		return ErrGroupUnknown
	}
	return nil
}

// SetGroupLocations replaces the locations of a group. An empty list means every
// location, including nodes added later. Members' access changes with the list, so
// the users revision moves: otherwise nodes behind on sync would keep serving the
// old set until some unrelated edit woke them up.
func (s *Store) SetGroupLocations(ctx context.Context, id string, locations []string) error {
	if _, err := s.GroupByID(ctx, id); err != nil {
		return err
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := setGroupLocations(ctx, tx, id, locations); err != nil {
			return err
		}
		return bumpRevision(ctx, tx)
	})
}

func setGroupLocations(ctx context.Context, tx *sql.Tx, id string, locations []string) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM group_locations WHERE group_id=?", id); err != nil {
		return err
	}
	for _, location := range dedupe(locations) {
		if _, err := tx.ExecContext(ctx, "INSERT INTO group_locations(group_id,location_id) VALUES(?,?)", id, location); err != nil {
			return err
		}
	}
	return nil
}

// DeleteGroup removes an empty group. A group with members is refused: deleting it
// would silently take away access, so the members have to be moved first. The
// default group is never deleted: it is where users land when nothing else is set.
func (s *Store) DeleteGroup(ctx context.Context, id string) error {
	group, err := s.GroupByID(ctx, id)
	if err != nil {
		return err
	}
	if id == DefaultGroupID {
		return ErrGroupFixed
	}
	if group.Members > 0 {
		return ErrGroupNotEmpty
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM group_locations WHERE group_id=?", id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM groups WHERE id=?", id)
		return err
	})
}

// SetUserGroup moves one user. Moving changes which nodes serve the user, so the
// users revision moves with it.
func (s *Store) SetUserGroup(ctx context.Context, userID, groupID string) error {
	if _, err := s.GroupByID(ctx, groupID); err != nil {
		return err
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO user_groups(user_id,group_id) VALUES(?,?) ON CONFLICT(user_id) DO UPDATE SET group_id=excluded.group_id", userID, groupID); err != nil {
			return err
		}
		return bumpRevision(ctx, tx)
	})
}

// SetUsersGroup moves every listed user; all of them must exist.
func (s *Store) SetUsersGroup(ctx context.Context, userIDs []string, groupID string) error {
	if _, err := s.GroupByID(ctx, groupID); err != nil {
		return err
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, userID := range userIDs {
			result, err := tx.ExecContext(ctx, "INSERT INTO user_groups(user_id,group_id) VALUES(?,?) ON CONFLICT(user_id) DO UPDATE SET group_id=excluded.group_id", userID, groupID)
			if err != nil {
				return err
			}
			if changed, err := result.RowsAffected(); err == nil && changed == 0 {
				if _, err := tx.ExecContext(ctx, "UPDATE user_groups SET group_id=? WHERE user_id=?", groupID, userID); err != nil {
					return err
				}
			}
		}
		return bumpRevision(ctx, tx)
	})
}

// UserGroups maps every user to a group id.
func (s *Store) UserGroups(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT user_id,group_id FROM user_groups")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := map[string]string{}
	for rows.Next() {
		var userID, groupID string
		if err = rows.Scan(&userID, &groupID); err != nil {
			return nil, err
		}
		groups[userID] = groupID
	}
	return groups, rows.Err()
}

// LocationsForUser lists the locations a user may use: the locations of the user's
// group. An empty list means every location, including nodes added later; a user
// without a group row counts as the default group and therefore gets everything.
func (s *Store) LocationsForUser(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT gl.location_id FROM user_groups ug
 JOIN group_locations gl ON gl.group_id=ug.group_id WHERE ug.user_id=? ORDER BY gl.location_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	locations := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		locations = append(locations, id)
	}
	return locations, rows.Err()
}

// LocationGroups lists the groups attached to a location, including the groups that
// cover every location.
func (s *Store) LocationGroups(ctx context.Context, locationID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT g.id FROM groups g
 WHERE NOT EXISTS(SELECT 1 FROM group_locations gl WHERE gl.group_id=g.id)
    OR EXISTS(SELECT 1 FROM group_locations gl WHERE gl.group_id=g.id AND gl.location_id=?)
 ORDER BY g.id`, locationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func dedupe(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
