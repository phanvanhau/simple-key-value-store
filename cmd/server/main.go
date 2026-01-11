package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"simplekv/internal/api"
)

func main() {
	addr := envOrDefault("KV_HTTP_ADDR", "127.0.0.1:8080")

	srv := &http.Server{
		Addr:              addr,
		Handler:           api.NewServer(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("starting server on %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server failed: %v", err)
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
