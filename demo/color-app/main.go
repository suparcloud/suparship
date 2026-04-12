package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
)

var version = "dev"

func main() {
	color := os.Getenv("COLOR")
	if color == "" {
		color = "blue"
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head><title>color-app</title></head>
<body style="margin:0;display:flex;align-items:center;justify-content:center;min-height:100vh;background:%s;font-family:sans-serif;color:#fff">
<div style="text-align:center">
  <h1 style="font-size:4rem">%s</h1>
  <p style="font-size:1.2rem;opacity:.8">version %s</p>
</div>
</body>
</html>`, color, color, version)
	})

	mux.HandleFunc("/api/color", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"color":   color,
			"version": version,
		})
	})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	})

	addr := ":" + port
	log.Printf("color-app starting: color=%s version=%s addr=%s", color, version, addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
