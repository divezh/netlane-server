package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/divezh/netlane-server/internal/config"
	"github.com/divezh/netlane-server/internal/store"
	"github.com/divezh/netlane-server/internal/update"
)

func newUpdateServer(t *testing.T, current string, manifest update.Manifest, manifestErr error) (*Server, *int) {
	t.Helper()
	f := newSyncFixture(t)
	previous := Version
	Version = current
	t.Cleanup(func() { Version = previous })
	started := 0
	s := &Server{db: f.db, cfg: config.Config{Admin: "admin"}}
	s.fetchManifest = func(context.Context) (update.Manifest, error) { return manifest, manifestErr }
	s.requestUpdate = func(string) error { started++; return nil }
	s.timerInstalled = func() bool { return true }
	return s, &started
}

func TestUpdatesReportsPublishedVersion(t *testing.T) {
	s, _ := newUpdateServer(t, "0.5.2", update.Manifest{Version: "0.5.3", Notes: "График потребления", NotesURL: "https://github.com/divezh/netlane-server/releases/tag/netlane-0.5.3"}, nil)
	rec := httptest.NewRecorder()
	s.updates(rec, httptest.NewRequest("GET", "/api/v1/updates", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["current"] != "0.5.2" || body["latest"] != "0.5.3" || body["available"] != true || body["reachable"] != true {
		t.Fatalf("unexpected answer %+v", body)
	}
	if body["notes"] != "График потребления" {
		t.Fatalf("release notes are missing: %+v", body)
	}

	// The same version installed: nothing to offer.
	s2, _ := newUpdateServer(t, "0.5.3", update.Manifest{Version: "0.5.3"}, nil)
	rec = httptest.NewRecorder()
	s2.updates(rec, httptest.NewRequest("GET", "/api/v1/updates", nil))
	body = map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["available"] != false {
		t.Fatalf("installed release must not be offered again: %+v", body)
	}
}

func TestUpdatesSurvivesUnreachableChannel(t *testing.T) {
	s, _ := newUpdateServer(t, "0.5.2", update.Manifest{}, update.ErrChannel)
	rec := httptest.NewRecorder()
	s.updates(rec, httptest.NewRequest("GET", "/api/v1/updates", nil))
	if rec.Code != 200 {
		t.Fatalf("an unreachable channel must not break the page: %d", rec.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["reachable"] != false || body["available"] == true || body["current"] != "0.5.2" {
		t.Fatalf("unexpected answer %+v", body)
	}
}

func TestApplyUpdateStartsUpdaterOnce(t *testing.T) {
	s, started := newUpdateServer(t, "0.5.2", update.Manifest{Version: "0.5.3"}, nil)
	rec := httptest.NewRecorder()
	s.applyUpdate(rec, httptest.NewRequest("POST", "/api/v1/updates/apply", nil))
	if rec.Code != 202 || *started != 1 {
		t.Fatalf("status %d, started %d", rec.Code, *started)
	}
	events, err := s.db.RecentAudit(context.Background(), 0)
	if err != nil || len(events) == 0 || events[0].Action != "panel.update_started" {
		t.Fatalf("update must be audited: %v %+v", err, events)
	}
}

func TestApplyUpdateRefusals(t *testing.T) {
	// Nothing newer: the updater is not started.
	s, started := newUpdateServer(t, "0.5.3", update.Manifest{Version: "0.5.3"}, nil)
	rec := httptest.NewRecorder()
	s.applyUpdate(rec, httptest.NewRequest("POST", "/api/v1/updates/apply", nil))
	if rec.Code != 409 || *started != 0 {
		t.Fatalf("status %d, started %d", rec.Code, *started)
	}
	// Channel down: cannot decide what to install.
	s, started = newUpdateServer(t, "0.5.2", update.Manifest{}, update.ErrChannel)
	rec = httptest.NewRecorder()
	s.applyUpdate(rec, httptest.NewRequest("POST", "/api/v1/updates/apply", nil))
	if rec.Code != 503 || *started != 0 {
		t.Fatalf("status %d, started %d", rec.Code, *started)
	}
	// No privileged updater on this server.
	s = &Server{db: newSyncFixture(t).db, cfg: config.Config{Admin: "admin"}}
	s.fetchManifest = func(context.Context) (update.Manifest, error) { return update.Manifest{Version: "0.5.3"}, nil }
	rec = httptest.NewRecorder()
	s.applyUpdate(rec, httptest.NewRequest("POST", "/api/v1/updates/apply", nil))
	if rec.Code != 503 {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestUpdateRequestsNeedSession(t *testing.T) {
	dir := t.TempDir()
	cfg, err := config.New("vpn.example.net", "admin", "administrator-password", dir)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := cfg.Key()
	db, err := store.Open(filepath.Join(dir, "db.sqlite"), key)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler, err := New(cfg, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []struct{ method, path string }{{"GET", "/api/v1/updates"}, {"POST", "/api/v1/updates/apply"}} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(request.method, request.path, nil))
		if rec.Code != 401 && rec.Code != 403 {
			t.Fatalf("%s %s without a session: %d", request.method, request.path, rec.Code)
		}
	}
}

// The card follows the updater step by step: a waiting request, the report with its steps,
// a finished run kept for a while, and a new request that is not hidden by an old failure.
func TestUpdateProgressReport(t *testing.T) {
	s, _ := newUpdateServer(t, "0.5.18", update.Manifest{Version: "0.5.19"}, nil)
	dir := t.TempDir()
	s.cfg.Database = filepath.Join(dir, "panel.sqlite")
	s.reportDir = t.TempDir()
	now := time.Now().Unix()
	write := func(name, body string) {
		t.Helper()
		target := dir
		if name == update.ReportFile {
			target = s.reportDir
		}
		if err := os.WriteFile(filepath.Join(target, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	progress := func() map[string]any {
		t.Helper()
		raw, _ := json.Marshal(s.updateStatus())
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return out
	}
	if p := progress(); p != nil {
		t.Fatalf("idle: %v", p)
	}
	write(update.RequestFile, fmt.Sprintf(`{"version":"0.5.19","requested_at":%d}`, now-40))
	if p := progress(); p["stage"] != "requested" || p["waiting"].(float64) < 40 {
		t.Fatalf("waiting request: %v", p)
	}
	write(update.ReportFile, fmt.Sprintf(`{"stage":"download","percent":31,"message":"Скачиваю комплект","detail":"4,2 из 12,7 МБ","at":%d,"steps":[{"stage":"accepted","message":"Запрос из панели принят","at":%d},{"stage":"download","message":"Скачиваю комплект","detail":"4,2 из 12,7 МБ","at":%d}]}`, now, now-2, now-1))
	if p := progress(); p["stage"] != "download" || p["detail"] != "4,2 из 12,7 МБ" || len(p["steps"].([]any)) != 2 {
		t.Fatalf("running: %v", p)
	}
	os.Remove(filepath.Join(dir, update.RequestFile))
	write(update.ReportFile, fmt.Sprintf(`{"stage":"done","percent":100,"message":"Готово: 0.5.19 установлена","at":%d}`, now-60))
	if p := progress(); p["stage"] != "done" {
		t.Fatalf("a fresh report must stay on the card: %v", p)
	}
	write(update.ReportFile, fmt.Sprintf(`{"stage":"done","percent":100,"message":"Готово","at":%d}`, now-3600))
	if p := progress(); p != nil {
		t.Fatalf("an old report must be cleared: %v", p)
	}
	write(update.ReportFile, fmt.Sprintf(`{"stage":"failed","percent":null,"message":"контрольная сумма не совпала","failed_stage":"verify","at":%d}`, now-30))
	write(update.RequestFile, fmt.Sprintf(`{"version":"0.5.19","requested_at":%d}`, now))
	if p := progress(); p["stage"] != "requested" {
		t.Fatalf("a retry must not be hidden by the old failure: %v", p)
	}
}

// A server updated by the former updater has its report in the panel data directory: the
// panel still shows it, and the newer of the two reports wins.
func TestUpdateProgressLegacyReport(t *testing.T) {
	s, _ := newUpdateServer(t, "0.6.0", update.Manifest{Version: "0.6.1"}, nil)
	dir := t.TempDir()
	s.cfg.Database = filepath.Join(dir, "panel.sqlite")
	s.reportDir = t.TempDir()
	now := time.Now().Unix()
	if err := os.WriteFile(filepath.Join(dir, update.StatusFile), []byte(fmt.Sprintf(`{"stage":"install","percent":75,"message":"old","at":%d}`, now-5)), 0o600); err != nil {
		t.Fatal(err)
	}
	status, ok := update.ReadStatus(s.reportDir, dir)
	if !ok || status.Message != "old" {
		t.Fatalf("legacy report: %+v %v", status, ok)
	}
	if err := os.WriteFile(filepath.Join(s.reportDir, update.ReportFile), []byte(fmt.Sprintf(`{"stage":"done","percent":100,"message":"new","at":%d}`, now)), 0o644); err != nil {
		t.Fatal(err)
	}
	if status, _ = update.ReadStatus(s.reportDir, dir); status.Message != "new" {
		t.Fatalf("the newer report must win: %+v", status)
	}
}
