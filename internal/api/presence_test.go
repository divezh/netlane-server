package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/divezh/netlane-server/internal/endpoint"
	"github.com/divezh/netlane-server/internal/store"
)

func TestPresenceSampleRecordsOnlyOpenSessionsWithValidAddresses(t *testing.T) {
	metricsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"username":"alex","ip":"198.51.100.7","sessions":2,"inbound":1,"outbound":1},
 {"username":"kate","ip":null,"sessions":0,"inbound":5,"outbound":5},
 {"username":"mira","ip":"not-an-address","sessions":1,"inbound":0,"outbound":0}]`))
	}))
	defer metricsServer.Close()
	metrics, err := endpoint.NewMetrics(strings.TrimPrefix(metricsServer.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "panel.sqlite"), bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	for _, name := range []string{"alex", "kate", "mira"} {
		if _, err = db.Create(ctx, "admin", name, "password-for-"+name, store.Policy{Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 11, 3, 16, 0, 0, 0, time.UTC)
	if err = samplePresence(ctx, metrics, db, at); err != nil {
		t.Fatal(err)
	}
	users, err := db.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][2]string{}
	for _, u := range users {
		got[u.Username] = [2]string{u.LastSeenAt, u.LastIP}
	}
	if got["alex"] != [2]string{at.Format(time.RFC3339), "198.51.100.7"} {
		t.Fatalf("online user: %v", got["alex"])
	}
	if got["kate"] != [2]string{"", ""} {
		t.Fatalf("a user without sessions is not online: %v", got["kate"])
	}
	if got["mira"] != [2]string{at.Format(time.RFC3339), ""} {
		t.Fatalf("a malformed address must not be stored: %v", got["mira"])
	}
}
