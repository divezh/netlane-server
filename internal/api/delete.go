package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
)

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/users/")
	if id == "" || strings.Contains(id, "/") {
		problem(w, 404, "not_found")
		return
	}
	var input struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.ExpectedRevision < 1 {
		problem(w, 400, "invalid_revision")
		return
	}
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	u, _, err := s.db.Managed(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, 404, "user_not_found")
		return
	}
	if err != nil {
		problem(w, 500, "storage_error")
		return
	}
	if u.DesiredRevision != input.ExpectedRevision {
		problem(w, 409, "revision_conflict_or_missing_user")
		return
	}
	state, err := s.control.Status(r.Context())
	if err != nil {
		controlProblem(w, err)
		return
	}
	if !state.Capabilities["remove_user"] {
		problem(w, 409, "endpoint_remove_not_supported")
		return
	}
	if err = s.db.Audit(r.Context(), s.cfg.Admin, "user.delete_requested", id); err != nil {
		problem(w, 500, "audit_error")
		return
	}
	revision := u.DesiredRevision
	for _, live := range state.Users {
		if live.ID == id {
			if live.Username != u.Username || live.Revision > u.DesiredRevision {
				problem(w, 409, "revision_conflict")
				return
			}
			revision = live.Revision
		}
	}
	applied, err := s.control.Remove(r.Context(), state.Generation, id, u.Username, revision)
	if err != nil {
		controlProblem(w, err)
		return
	}
	for _, live := range applied.Users {
		if live.ID == id || live.Username == u.Username {
			problem(w, 502, "endpoint_acknowledgement_mismatch")
			return
		}
	}
	if err = s.db.Delete(r.Context(), s.cfg.Admin, id, input.ExpectedRevision); err != nil {
		problem(w, 500, "removed_but_reconciliation_required")
		return
	}
	s.hub.Notify()
	respond(w, 200, map[string]any{"deleted": true, "generation": applied.Generation})
}
