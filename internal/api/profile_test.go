package api

import (
	"encoding/base64"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/divezh/netlane-server/internal/config"
	"github.com/divezh/netlane-server/internal/store"
)

// linkTags decodes a tt:// link into its TLV fields (one- or two-byte lengths, as appendLength writes).
func linkTags(t *testing.T, link string) map[byte]string {
	t.Helper()
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(link, "tt://?"))
	if err != nil {
		t.Fatal(err)
	}
	tags := map[byte]string{}
	for len(data) > 0 {
		tag := data[0]
		n, skip := int(data[1]), 2
		if n&0x40 != 0 {
			n, skip = (n&0x3f)<<8|int(data[2]), 3
		}
		tags[tag] = string(data[skip : skip+n])
		data = data[skip+n:]
	}
	return tags
}

func TestProfileName(t *testing.T) {
	cases := []struct{ name, country, want string }{
		{"Амстердам", "NL", "\U0001F1F3\U0001F1F1 Амстердам"},
		{"Амстердам", "", "Амстердам"},
		{"", "", ""},
		{"", "NL", ""},
	}
	for _, c := range cases {
		got := profileName(config.Config{ServerName: c.name, ServerCountry: c.country})
		if got != c.want {
			t.Fatalf("profileName(%q,%q) = %q, want %q", c.name, c.country, got, c.want)
		}
		tags := linkTags(t, profileLink("vpn.example.net", "vpn.example.net:443", "alice", "secret-password", "http2", false, got))
		name, has := tags[12]
		if has != (c.want != "") || name != c.want {
			t.Fatalf("tag 12 for %q: present=%v value=%q", c.want, has, name)
		}
		if tags[1] != "vpn.example.net" || tags[5] != "alice" {
			t.Fatalf("link fields: %v", tags)
		}
	}
}

func TestServerIdentityUpdate(t *testing.T) {
	dir := t.TempDir()
	cfg, err := config.New("vpn.example.net", "admin", "administrator-password", dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err = config.SaveNew(path, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg, err = config.Load(path); err != nil {
		t.Fatal(err)
	}
	key, _ := cfg.Key()
	db, err := store.Open(filepath.Join(dir, "db.sqlite"), key)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, err := New(cfg, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	admin := newClient(t, srv.URL)
	status, csrf := admin.login("administrator-password")
	if status != 200 {
		t.Fatal("login failed")
	}
	body := `{"server_name":"Амстердам","server_country":"NL"}`
	if status, _ := admin.call("PUT", "/api/v1/server", body, ""); status != 403 {
		t.Fatalf("server update without CSRF: %d", status)
	}
	for _, bad := range []string{`{"server_name":"x","server_country":"nl1"}`, `{"server_name":"x","server_country":"N"}`, `{"server_name":"` + strings.Repeat("я", 65) + `","server_country":""}`, `{"server_name":"a\u0007b","server_country":""}`} {
		if status, _ := admin.call("PUT", "/api/v1/server", bad, csrf); status != 400 {
			t.Fatalf("invalid server identity accepted: %s → %d", bad, status)
		}
	}
	if status, out := admin.call("PUT", "/api/v1/server", body, csrf); status != 200 || out["server_name"] != "Амстердам" || out["server_country"] != "NL" {
		t.Fatalf("server update: %d %v", status, out)
	}
	saved, err := config.Load(path)
	if err != nil || saved.ServerName != "Амстердам" || saved.ServerCountry != "NL" {
		t.Fatalf("saved config: %+v %v", saved, err)
	}
	if status, out := admin.call("GET", "/api/v1/info", "", ""); status != 200 || out["server_name"] != "Амстердам" || out["server_country"] != "NL" {
		t.Fatalf("info: %d %v", status, out)
	}
	if status, _ := admin.call("PUT", "/api/v1/server", `{"server_name":"","server_country":""}`, csrf); status != 200 {
		t.Fatal("clearing the server identity failed")
	}
}
