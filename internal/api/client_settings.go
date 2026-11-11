package api

import (
	"errors"
	"net/http"

	"github.com/divezh/netlane-server/internal/store"
)

func (s *Server) getClientSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.db.ClientSettings(r.Context())
	if err != nil {
		problem(w, 500, "storage_error")
		return
	}
	respond(w, 200, settings)
}
func (s *Server) putClientSettings(w http.ResponseWriter, r *http.Request) {
	var input store.ClientSettings
	if !decodeLimit(w, r, &input, 128<<10) {
		return
	}
	settings, err := s.db.SaveClientSettings(r.Context(), s.cfg.Admin, input)
	if errors.Is(err, store.ErrClientSettings) {
		problem(w, 400, "invalid_client_settings")
		return
	}
	if errors.Is(err, store.ErrSettingsConflict) {
		problem(w, 409, "client_settings_conflict")
		return
	}
	if err != nil {
		problem(w, 500, "storage_error")
		return
	}
	respond(w, 200, settings)
}
