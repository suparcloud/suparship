package server

import (
	"net/http"
	"os"
	"path/filepath"
)

// spaHandler serves static files from dir. If the requested path does not
// exist on disk, it falls back to index.html for client-side routing.
func spaHandler(dir string) http.Handler {
	fs := http.Dir(dir)
	fileServer := http.FileServer(fs)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check whether the file exists on disk.
		path := filepath.Join(dir, filepath.Clean(r.URL.Path))
		if _, err := os.Stat(path); os.IsNotExist(err) {
			// SPA fallback: serve index.html so the client router can handle it.
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}

		fileServer.ServeHTTP(w, r)
	})
}
