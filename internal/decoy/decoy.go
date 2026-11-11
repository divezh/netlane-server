// Package decoy serves the ordinary website the VPN endpoint shows to visitors and probes
// that come without VPN credentials (the endpoint's reverse_proxy points here).
package decoy

import (
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"

	webassets "github.com/divezh/netlane-server"
)

// Handler serves dir when it holds an index.html (the owner's own site), otherwise the
// built-in one. It answers like a plain static nginx site: GET and HEAD only, a 404 page.
func Handler(dir string) http.Handler {
	site := webassets.Decoy()
	if dir != "" {
		if info, err := os.Stat(path.Join(dir, "index.html")); err == nil && !info.IsDir() {
			site = os.DirFS(dir)
		}
	}
	notFound, _ := fs.ReadFile(site, "404.html")
	files := http.FileServer(http.FS(site))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "nginx")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "405 Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		if info, err := fs.Stat(site, name); err != nil || info.IsDir() || name == "404.html" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			if r.Method == http.MethodGet {
				w.Write(notFound)
			}
			return
		}
		files.ServeHTTP(w, r)
	})
}
