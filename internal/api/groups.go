package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/divezh/netlane-server/internal/store"
)

// groupRef is the short form a user list carries: enough to show a badge and to
// filter on the client without shipping the whole group.
type groupRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// groupView is a group as the panel lists it.
type groupView struct {
	store.Group
	Default bool `json:"default"`
}

func (s *Server) listGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.db.Groups(r.Context())
	if err != nil {
		problem(w, 500, "storage_error")
		return
	}
	views := make([]groupView, 0, len(groups))
	for _, g := range groups {
		views = append(views, groupView{Group: g, Default: g.ID == store.DefaultGroupID})
	}
	respond(w, 200, map[string]any{"groups": views, "default_id": store.DefaultGroupID})
}

func (s *Server) createGroup(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name      string   `json:"name"`
		Locations []string `json:"locations"`
	}
	if !decode(w, r, &input) {
		return
	}
	if !s.locationsExist(w, r, input.Locations) {
		return
	}
	group, err := s.db.CreateGroup(r.Context(), input.Name, input.Locations, time.Now())
	if err != nil {
		s.groupProblem(w, err)
		return
	}
	_ = s.db.Audit(r.Context(), s.cfg.Admin, "group.create", group.Name)
	respond(w, 201, map[string]any{"group": group})
}

func (s *Server) updateGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var input struct {
		Name      *string   `json:"name"`
		Locations *[]string `json:"locations"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Name != nil {
		if err := s.db.RenameGroup(r.Context(), id, *input.Name); err != nil {
			s.groupProblem(w, err)
			return
		}
		_ = s.db.Audit(r.Context(), s.cfg.Admin, "group.rename", id)
	}
	if input.Locations != nil {
		if !s.locationsExist(w, r, *input.Locations) {
			return
		}
		if err := s.db.SetGroupLocations(r.Context(), id, *input.Locations); err != nil {
			s.groupProblem(w, err)
			return
		}
		_ = s.db.Audit(r.Context(), s.cfg.Admin, "group.locations", id)
	}
	group, err := s.db.GroupByID(r.Context(), id)
	if err != nil {
		s.groupProblem(w, err)
		return
	}
	respond(w, 200, map[string]any{"group": group})
}

func (s *Server) deleteGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.db.DeleteGroup(r.Context(), id); err != nil {
		s.groupProblem(w, err)
		return
	}
	_ = s.db.Audit(r.Context(), s.cfg.Admin, "group.delete", id)
	w.WriteHeader(204)
}

// setUserGroup moves one user; setUsersGroup moves the selection from the list.
func (s *Server) setUserGroup(w http.ResponseWriter, r *http.Request) {
	var input struct {
		GroupID string `json:"group_id"`
	}
	if !decode(w, r, &input) {
		return
	}
	id := r.PathValue("id")
	if err := s.db.SetUserGroup(r.Context(), id, input.GroupID); err != nil {
		s.groupProblem(w, err)
		return
	}
	_ = s.db.Audit(r.Context(), s.cfg.Admin, "user.group", id)
	w.WriteHeader(204)
}

func (s *Server) setUsersGroup(w http.ResponseWriter, r *http.Request) {
	var input struct {
		UserIDs []string `json:"user_ids"`
		GroupID string   `json:"group_id"`
	}
	if !decode(w, r, &input) {
		return
	}
	if err := s.db.SetUsersGroup(r.Context(), input.UserIDs, input.GroupID); err != nil {
		s.groupProblem(w, err)
		return
	}
	for _, id := range input.UserIDs {
		_ = s.db.Audit(r.Context(), s.cfg.Admin, "user.group", id)
	}
	respond(w, 200, map[string]any{"moved": len(input.UserIDs)})
}

// locationsExist rejects ids of nodes that are gone, so a typo does not create a
// group nobody can use. An empty list means every location and needs no check.
func (s *Server) locationsExist(w http.ResponseWriter, r *http.Request, locations []string) bool {
	if len(locations) == 0 {
		return true
	}
	nodes, err := s.db.ListNodes(r.Context())
	if err != nil {
		problem(w, 500, "storage_error")
		return false
	}
	known := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		known[n.ID] = true
	}
	for _, id := range locations {
		if id != "" && !known[id] {
			problem(w, 400, "unknown_location")
			return false
		}
	}
	return true
}

func (s *Server) groupProblem(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrGroupName):
		problem(w, 400, "invalid_group")
	case errors.Is(err, store.ErrGroupNotEmpty):
		problem(w, 409, "group_not_empty")
	case errors.Is(err, store.ErrGroupFixed):
		problem(w, 400, "group_fixed")
	case errors.Is(err, store.ErrGroupUnknown):
		problem(w, 404, "group_not_found")
	default:
		problem(w, 500, "storage_error")
	}
}
