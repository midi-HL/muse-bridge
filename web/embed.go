// Package web serves the embedded Muse Bridge administration interface.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// FS contains every browser asset. No runtime frontend toolchain is required.
//
//go:embed static/*
var FS embed.FS

// Handler serves the administration UI at / or beneath /admin/.
// The host must register /admin/api/ separately, before this catch-all.
func Handler() http.Handler {
	root, err := fs.Sub(FS, "static")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/admin")
		if name == "" || name == "/" {
			name = "/index.html"
		}
		name = path.Clean("/" + name)
		if strings.HasPrefix(name, "/api/") || name == "/api" {
			http.NotFound(w, r)
			return
		}
		if name == "/index.html" {
			// FileServer redirects index.html to ./; serve its bytes directly.
			body, readErr := fs.ReadFile(root, "index.html")
			if readErr != nil {
				http.Error(w, "interface unavailable", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Referrer-Policy", "same-origin")
			w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' blob: data:; media-src 'self' blob:; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
			if r.Method != http.MethodHead {
				_, _ = w.Write(body)
			}
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		r2 := r.Clone(r.Context())
		r2.URL.Path = name
		files.ServeHTTP(w, r2)
	})
}
