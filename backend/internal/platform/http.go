// Package platform assembles the business API and health endpoints.
package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

func Handler(installers ...func(*http.ServeMux)) http.Handler {
	return HandlerWithReadiness(nil, installers...)
}
func HandlerWithReadiness(check func(context.Context) error, installers ...func(*http.ServeMux)) http.Handler {
	mux := http.NewServeMux()
	for _, install := range installers {
		install(mux)
	}
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, "alive")
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if check == nil || check(ctx) != nil {
			respond(w, 503, "dependencies_unavailable")
			return
		}
		respond(w, 200, "ready")
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]any{"code": 5000, "data": nil, "message": "接口不存在"})
	})
	return mux
}

func respond(w http.ResponseWriter, status int, state string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"service": "go-api", "status": state})
}
