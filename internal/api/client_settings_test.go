package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/divezh/netlane-server/internal/store"
)

func TestClientSettingsSubscription(t *testing.T) {
	f := newSyncFixture(t)
	ctx := context.Background()
	original, err := f.db.ClientSettings(ctx)
	if err != nil || original.Version != 1 || original.Revision != 0 {
		t.Fatal("initial settings", original, err)
	}
	original.DNSServers = []string{"1.1.1.1", "tls://8.8.8.8"}
	original.RuleSets = []store.SharedRuleSet{{ID: "work", Name: "Рабочие", Routing: store.SharedRouting{
		Mode: "selective", Destinations: []string{"example.com", "203.0.113.0/24"},
		Applications: []store.SharedAppRule{{Name: "telegram.exe", Action: "vpn"}},
	}}}
	saved, err := f.db.SaveClientSettings(ctx, "admin", original)
	if err != nil || saved.Revision != 1 {
		t.Fatal("save", err)
	}
	if _, err = f.db.SaveClientSettings(ctx, "admin", original); !errors.Is(err, store.ErrSettingsConflict) {
		t.Fatal("stale save accepted", err)
	}
	user := f.appliedUser(t, "alice", store.Policy{Enabled: true})
	token, err := f.db.Subscription(ctx, "admin", user.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	status, _, body := f.do(t, "GET", "/"+testPrefix+"/sub/"+token, "", "")
	var feed struct {
		Settings store.ClientSettings `json:"client_settings"`
	}
	if status != 200 || json.Unmarshal(body, &feed) != nil || feed.Settings.Revision != 1 || len(feed.Settings.RuleSets) != 1 || len(feed.Settings.DNSServers) != 2 {
		t.Fatal("subscription settings missing", status, string(body))
	}
	for _, dns := range []string{"192.168.1.1", "tls://127.0.0.1", "https://10.0.0.1/dns-query", "https://dns.example.com:0/dns-query"} {
		bad := saved
		bad.DNSServers = []string{dns}
		if _, err = f.db.SaveClientSettings(ctx, "admin", bad); !errors.Is(err, store.ErrClientSettings) {
			t.Fatal("invalid DNS accepted", dns, err)
		}
	}
	if _, err = f.db.UpdatePolicy(ctx, "admin", user.ID, 1, store.Policy{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if err = f.db.MarkApplied(ctx, "admin", user.ID, 2); err != nil {
		t.Fatal(err)
	}
	_, _, body = f.do(t, "GET", "/"+testPrefix+"/sub/"+token, "", "")
	if strings.Contains(string(body), "client_settings") {
		t.Fatal("disabled user received settings")
	}
}
