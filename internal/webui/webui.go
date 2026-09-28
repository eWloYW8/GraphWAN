// Package webui serves the production React application embedded in the binary.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed dist
var assets embed.FS

func Handler() http.Handler {
	root, _ := fs.Sub(assets, "dist")
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Static assets do not bypass API authentication, and missing asset/API
		// paths never receive an HTML fallback masquerading as a successful API.
		if r.URL.Path != "/" && r.URL.Path != "/THIRD_PARTY_LICENSES.txt" && !strings.HasPrefix(r.URL.Path, "/assets/") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != "/" {
			info, err := fs.Stat(root, strings.TrimPrefix(r.URL.Path, "/"))
			if err != nil || info.IsDir() {
				http.NotFound(w, r)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		files.ServeHTTP(w, r)
	})
}
