package api

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/divezh/netlane-server/internal/endpoint"
)

type policyView struct {
	BlockBitTorrent bool `json:"block_bittorrent"`
	// Supported is false when the main endpoint predates the switch (or does not answer).
	Supported bool `json:"supported"`
	// Applied is what the main endpoint does now; it differs from the choice only for a moment.
	Applied          *bool  `json:"applied,omitempty"`
	BlockedStreams   uint64 `json:"blocked_streams"`
	BlockedDatagrams uint64 `json:"blocked_datagrams"`
}

// policy shows and changes BitTorrent blocking. The choice is stored in the panel database, applied to
// the main endpoint at once and taken by nodes with their next sync.
func (s *Server) policy(w http.ResponseWriter, r *http.Request) {
	if r.Method == "PUT" {
		var in struct {
			BlockBitTorrent *bool `json:"block_bittorrent"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.BlockBitTorrent == nil {
			problem(w, 400, "invalid_policy")
			return
		}
		if err := s.db.SetBlockBitTorrent(r.Context(), *in.BlockBitTorrent); err != nil {
			problem(w, 500, "storage_error")
			return
		}
		action := "policy.bittorrent_allowed"
		if *in.BlockBitTorrent {
			action = "policy.bittorrent_blocked"
		}
		if err := s.db.Audit(r.Context(), s.cfg.Admin, action, ""); err != nil {
			problem(w, 500, "audit_error")
			return
		}
		if _, err := s.control.SyncBlockBitTorrent(r.Context(), *in.BlockBitTorrent); err != nil {
			log.Printf("policy: main endpoint did not take the BitTorrent switch: %v", err)
		}
		if s.hub != nil {
			s.hub.Notify()
		}
	}
	on, err := s.db.BlockBitTorrent(r.Context())
	if err != nil {
		problem(w, 500, "storage_error")
		return
	}
	view := policyView{BlockBitTorrent: on}
	if state, err := s.control.Status(r.Context()); err == nil && state.Capabilities["block_bittorrent"] && state.Policy != nil {
		applied := state.Policy.BlockBitTorrent
		view.Supported, view.Applied = true, &applied
		view.BlockedStreams, view.BlockedDatagrams = state.Policy.BlockedStreams, state.Policy.BlockedDatagrams
	}
	respond(w, 200, view)
}

// KeepPolicy applies the stored BitTorrent choice to the main endpoint at start and then every minute,
// so a reinstalled or restored endpoint follows the panel.
func KeepPolicy(ctx context.Context, control *endpoint.Control, desired func(context.Context) (bool, error)) {
	for {
		if on, err := desired(ctx); err == nil {
			step, cancel := context.WithTimeout(ctx, 10*time.Second)
			_, _ = control.SyncBlockBitTorrent(step, on)
			cancel()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}
