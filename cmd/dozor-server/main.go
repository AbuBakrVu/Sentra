package main

import (
	"crypto/tls"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"dozor/internal/server"
	"dozor/web"
)

var version = "0.1.0-dev"

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	cfg := server.Config{
		Listen:    env("DOZOR_LISTEN", ":443"),
		DataDir:   env("DOZOR_DATA", "/var/lib/dozor"),
		VMURL:     env("DOZOR_VM_URL", "http://127.0.0.1:8428"),
		PublicURL: os.Getenv("DOZOR_PUBLIC_URL"),
		AgentsDir: env("DOZOR_AGENTS_DIR", "/opt/dozor/agents"),
		AdminPass: os.Getenv("DOZOR_ADMIN_PASSWORD"),
		Version:   version,
	}
	if h := os.Getenv("DOZOR_CERT_HOSTS"); h != "" {
		for _, x := range strings.Split(h, ",") {
			cfg.CertHosts = append(cfg.CertHosts, strings.TrimSpace(x))
		}
	}
	s, cert, err := server.New(cfg, web.FS())
	if err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           s.Handler(),
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{*cert}, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	log.Printf("Дозор %s слушает %s, отпечаток ключа sha256//%s", version, cfg.Listen, s.Pin())
	log.Fatal(srv.ListenAndServeTLS("", ""))
}
