package decoy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func get(t *testing.T, h http.Handler, method, path string) (*http.Response, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	res := rec.Result()
	body, _ := io.ReadAll(res.Body)
	return res, string(body)
}

func TestBuiltInSiteLooksLikeAnOrdinaryWebsite(t *testing.T) {
	h := Handler("")
	res, body := get(t, h, "GET", "/")
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") || !strings.Contains(body, "<title>") {
		t.Fatalf("index: %d %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	if res.Header.Get("Server") != "nginx" {
		t.Fatalf("server header: %q", res.Header.Get("Server"))
	}
	for _, word := range []string{"VPN", "TrustTunnel", "proxy", "Proxy"} {
		if strings.Contains(body, word) {
			t.Fatalf("decoy page mentions %q", word)
		}
	}
	if res, _ := get(t, h, "GET", "/robots.txt"); res.StatusCode != 200 {
		t.Fatalf("robots.txt: %d", res.StatusCode)
	}
	res, body = get(t, h, "GET", "/no-such-page")
	if res.StatusCode != 404 || !strings.Contains(body, "404") {
		t.Fatalf("missing page: %d", res.StatusCode)
	}
	if res, _ := get(t, h, "POST", "/"); res.StatusCode != 405 || res.Header.Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST: %d %q", res.StatusCode, res.Header.Get("Allow"))
	}
	if res, _ := get(t, h, "GET", "/../../etc/passwd"); res.StatusCode != 404 && res.StatusCode != 301 {
		t.Fatalf("path escape: %d", res.StatusCode)
	}
}

func TestOwnSiteDirectoryReplacesTheBuiltInOne(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html><title>Мой сайт</title></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, body := get(t, Handler(dir), "GET", "/"); !strings.Contains(body, "Мой сайт") {
		t.Fatalf("own site not served: %q", body)
	}
	if _, body := get(t, Handler(filepath.Join(dir, "missing")), "GET", "/"); !strings.Contains(body, "<title>") {
		t.Fatal("a missing directory must fall back to the built-in site")
	}
}
