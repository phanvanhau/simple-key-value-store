package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// NewServer wires the generated handlers into a router and exposes a health check.
// Endpoint implementations return 501 until the real database logic is connected.
func NewServer() http.Handler {
	r := chi.NewRouter()

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	return HandlerWithOptions(Unimplemented{}, ChiServerOptions{
		BaseRouter: r,
	})
}
