package main

import (
	"crypto/tls"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
)

func main() {
	upstream := os.Getenv("UPSTREAM_URL")
	if upstream == "" {
		upstream = "http://gitea-http.gitea.svc.cluster.local:3000"
	}
	certFile := os.Getenv("TLS_CERT")
	if certFile == "" {
		certFile = "/etc/tls/tls.crt"
	}
	keyFile := os.Getenv("TLS_KEY")
	if keyFile == "" {
		keyFile = "/etc/tls/tls.key"
	}
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":3000"
	}

	target, err := url.Parse(upstream)
	if err != nil {
		log.Fatalf("invalid UPSTREAM_URL: %v", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}

	log.Printf("TLS proxy %s -> %s", addr, upstream)
	log.Fatal(http.ListenAndServeTLS(addr, certFile, keyFile, proxy))
}
