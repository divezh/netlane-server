package update

import (
	"strings"
	"testing"
)

const validRelease = `{
  "tag_name": "netlane-0.6.0",
  "draft": false,
  "prerelease": false,
  "published_at": "2026-11-11T18:00:00Z",
  "html_url": "https://github.com/divezh/netlane-server/releases/tag/netlane-0.6.0",
  "body": "Что нового:\r\n\r\n- Обновления из **netlane-server**.\n- Без \u0060токена\u0060.\n\nПроверка происхождения: gh attestation verify",
  "assets": [
    {"name": "trusttunnel-dev.tar.gz.sig", "browser_download_url": "https://github.com/divezh/netlane-server/releases/download/netlane-0.6.0/trusttunnel-dev.tar.gz.sig", "digest": "sha256:2222222222222222222222222222222222222222222222222222222222222222"},
    {"name": "trusttunnel-dev.tar.gz.sha256", "browser_download_url": "https://github.com/divezh/netlane-server/releases/download/netlane-0.6.0/trusttunnel-dev.tar.gz.sha256", "digest": "sha256:1111111111111111111111111111111111111111111111111111111111111111"},
    {"name": "trusttunnel-dev.tar.gz", "browser_download_url": "https://github.com/divezh/netlane-server/releases/download/netlane-0.6.0/trusttunnel-dev.tar.gz", "digest": "sha256:9F2C0C1D5B3A4E6F708192A3B4C5D6E7F8091A2B3C4D5E6F708192A3B4C5D6E7"}
  ]
}`

func TestParseAcceptsLatestRelease(t *testing.T) {
	m, err := Parse([]byte(validRelease))
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != "0.6.0" || m.SHA256 != "9f2c0c1d5b3a4e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7" || !strings.HasSuffix(m.URL, "/netlane-0.6.0/trusttunnel-dev.tar.gz") {
		t.Fatalf("unexpected manifest %+v", m)
	}
	if m.Notes != "Обновления из netlane-server. Без токена." || m.NotesURL == "" || m.Released == "" {
		t.Fatalf("notes and page are taken from the release: %+v", m)
	}
	if v, err := Parse([]byte(strings.Replace(validRelease, `"netlane-0.6.0",`, `"v0.6.1",`, 1))); err != nil || v.Version != "0.6.1" {
		t.Fatalf("a vX.Y.Z tag is a release too: %v %+v", err, v)
	}
	if Channel != "https://api.github.com/repos/divezh/netlane-server/releases/latest" {
		t.Fatal("updates come from the latest release of netlane-server only")
	}
}

func TestParseRefusesUnsafeRelease(t *testing.T) {
	cases := map[string]string{
		"foreign host": strings.Replace(validRelease, "https://github.com/divezh/netlane-server/releases/download/netlane-0.6.0/trusttunnel-dev.tar.gz\"", "https://evil.example.com/trusttunnel-dev.tar.gz\"", 1),
		"other repo":   strings.Replace(validRelease, "github.com/divezh/netlane-server/releases/download/netlane-0.6.0/trusttunnel-dev.tar.gz\"", "github.com/someone/else/releases/download/x/trusttunnel-dev.tar.gz\"", 1),
		"plain http":   strings.Replace(validRelease, "\"https://github.com/divezh/netlane-server/releases/download/netlane-0.6.0/trusttunnel-dev.tar.gz\"", "\"http://github.com/divezh/netlane-server/releases/download/netlane-0.6.0/trusttunnel-dev.tar.gz\"", 1),
		"no checksum":  strings.Replace(validRelease, `"sha256:9F2C0C1D5B3A4E6F708192A3B4C5D6E7F8091A2B3C4D5E6F708192A3B4C5D6E7"`, `null`, 1),
		"no bundle":    strings.Replace(validRelease, `"name": "trusttunnel-dev.tar.gz",`, `"name": "other.tar.gz",`, 1),
		"unsigned":     strings.Replace(validRelease, `"name": "trusttunnel-dev.tar.gz.sig",`, `"name": "other.sig",`, 1),
		"dev tag":      strings.Replace(validRelease, `"netlane-0.6.0",`, `"dev-e73bc86",`, 1),
		"pre-release":  strings.Replace(validRelease, `"prerelease": false`, `"prerelease": true`, 1),
		"draft":        strings.Replace(validRelease, `"draft": false`, `"draft": true`, 1),
		"not json":     "{",
		"huge answer":  validRelease + strings.Repeat(" ", maxRelease),
	}
	for name, body := range cases {
		if _, err := Parse([]byte(body)); err == nil {
			t.Fatalf("%s: release must be refused", name)
		}
	}
	if m, err := Parse([]byte(strings.Replace(validRelease, "https://github.com/divezh/netlane-server/releases/tag/netlane-0.6.0", "https://evil.example.com/x", 1))); err != nil || m.NotesURL != "" {
		t.Fatalf("a foreign notes page is dropped, not trusted: %v %+v", err, m)
	}
}

func TestCompareOrdersReleases(t *testing.T) {
	cases := []struct {
		current, latest string
		want            int
	}{
		{"0.5.2", "0.5.3", -1},
		{"0.5.3", "0.5.3", 0},
		{"0.5.4", "0.5.3", 1},
		{"0.5.10", "0.5.9", 1},
		{"0.10.0", "0.9.9", 1},
		{"dev", "0.5.3", -1},
		{"dev-e73bc86", "0.5.3", -1},
		{"dev", "dev", 0},
	}
	for _, c := range cases {
		if got := Compare(c.current, c.latest); got != c.want {
			t.Fatalf("%s → %s: %d, want %d", c.current, c.latest, got, c.want)
		}
	}
}
