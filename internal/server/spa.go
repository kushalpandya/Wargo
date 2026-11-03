// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

func init() {
	// Go doesn't know the web app manifest type; browsers expect it.
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
	_ = mime.AddExtensionType(".ico", "image/x-icon")
}

// spaHandler serves static files and falls back to index.html for
// client-side routes.
func spaHandler(files fs.FS) http.Handler {
	fsrv := http.FileServerFS(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if st, err := fs.Stat(files, p); err == nil && !st.IsDir() {
			if strings.HasPrefix(p, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			fsrv.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(p, "assets/") || strings.Contains(path.Base(p), ".") {
			http.NotFound(w, r)
			return
		}
		f, err := files.Open("index.html")
		if err != nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, "Wargo frontend is not built. Run `make web`.\n")
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		io.Copy(w, f)
	})
}
