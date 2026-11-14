package api

import (
	"context"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/divezh/netlane-server/internal/update"
)

// Version is the panel build. The linker sets it; a development build stays "dev".
var Version = "dev"

// updateCache holds the channel answer: the manifest changes rarely, and a server without
// outbound HTTPS must not retry it on every page refresh.
type updateResult struct {
	at       time.Time
	manifest update.Manifest
	err      error
}

const updateTTL = 2 * time.Minute

// doneShown is how long the card keeps the report of a finished update.
const doneShown = 15 * time.Minute

// channel returns the published manifest, cached briefly.
func (s *Server) channel(ctx context.Context) (update.Manifest, error) {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	if !s.update.at.IsZero() && time.Since(s.update.at) < updateTTL {
		return s.update.manifest, s.update.err
	}
	manifest, err := s.fetchManifest(ctx)
	if err != nil {
		log.Printf("update: %v", err)
	}
	s.update = updateResult{at: time.Now(), manifest: manifest, err: err}
	return manifest, err
}

// updates answers whether a newer version is published. An unreachable channel is not an
// error of the panel: the page stays usable and simply says the check failed.
func (s *Server) updates(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("force") == "1" {
		s.updateMu.Lock()
		s.update = updateResult{}
		s.updateMu.Unlock()
	}
	manifest, err := s.channel(r.Context())
	out := map[string]any{"current": Version, "can_apply": s.ready(), "instant_start": s.instantStart != nil && s.instantStart()}
	if err != nil {
		out["reachable"] = false
		respond(w, 200, out)
		return
	}
	out["reachable"] = true
	out["latest"] = manifest.Version
	out["released_at"] = manifest.Released
	out["notes"] = manifest.Notes
	out["notes_url"] = manifest.NotesURL
	out["available"] = update.Compare(Version, manifest.Version) < 0
	out["progress"] = s.updateStatus()
	respond(w, 200, out)
}

// applyUpdate leaves the request the root timer reads. The console is started with
// NoNewPrivileges and cannot install anything by design: the updater downloads the bundle
// from the channel, checks its checksum, makes a backup and restarts the services.
func (s *Server) applyUpdate(w http.ResponseWriter, r *http.Request) {
	manifest, err := s.channel(r.Context())
	if err != nil {
		problem(w, 503, "update_channel_unavailable")
		return
	}
	if update.Compare(Version, manifest.Version) >= 0 {
		problem(w, 409, "update_current")
		return
	}
	if !s.ready() {
		problem(w, 503, "update_unavailable")
		return
	}
	// A report of an earlier run must not stand in for this one: the request below is newer
	// than any report, so updateStatus shows the new run until the updater writes again.
	update.ClearStatus(filepath.Dir(s.cfg.Database))
	if err = s.requestUpdate(manifest.Version); err != nil {
		log.Printf("update: cannot leave the request: %v", err)
		problem(w, 503, "update_unavailable")
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if err = s.db.Audit(r.Context(), s.cfg.Admin, "panel.update_started", ip); err != nil {
		log.Printf("update: cannot write the audit record: %v", err)
	}
	respond(w, 202, map[string]any{"started": true, "version": manifest.Version})
}

// updateStatus reports where the updater is, so the console can draw progress instead of a
// message that never changes: the panel writes a request and has no other window into root's work.
func (s *Server) updateStatus() any {
	dir := filepath.Dir(s.cfg.Database)
	now := time.Now().Unix()
	status, reported := update.ReadStatus(s.reportDir, dir)
	pending, requested := update.ReadRequest(dir)
	// A request newer than the last report is a new run the updater has not picked up yet.
	if requested && (!reported || status.At < pending.RequestedAt) {
		return map[string]any{"stage": "requested", "percent": 3, "message": "Запрос отправлен, ждём запуска обновления", "version": pending.Version, "at": pending.RequestedAt, "waiting": now - pending.RequestedAt}
	}
	if !reported {
		return nil
	}
	// The finished run stays on the card for a while as a report, then the card is clean.
	if status.Stage == "done" && now-status.At > int64(doneShown/time.Second) {
		update.Clear(dir)
		return nil
	}
	return status
}

// ready reports whether this server can install a requested update at all.
func (s *Server) ready() bool {
	return s.requestUpdate != nil && s.timerInstalled != nil && s.timerInstalled()
}

// requestVersion asks for the published update from the panel data directory.
func requestVersion(dir string) func(string) error {
	return func(version string) error { return update.Request(dir, version) }
}
