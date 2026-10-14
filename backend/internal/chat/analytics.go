package chat

import (
	"ai-library/backend/internal/repository"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

func (h *Handler) rememberStats(r *http.Request, key string, value any) {
	if h.Cache != nil {
		data, err := json.Marshal(value)
		if err == nil {
			_ = h.Cache.Set(r.Context(), key, data, 5*time.Minute)
		}
	}
}
func (h *Handler) AnalyticsOverview(w http.ResponseWriter, r *http.Request) {
	claims, err := h.Auth.Authenticate(r)
	if err != nil {
		failure(w, err, false)
		return
	}
	claims.ID = strings.ToLower(claims.ID)
	key := "go:analytics:overview:" + claims.ID
	var value repository.Overview
	if !h.cached(r, key, &value) {
		value, err = h.Store.Overview(r.Context(), claims.ID)
		if err != nil {
			failure(w, err, false)
			return
		}
		h.rememberStats(r, key, value)
	}
	respond(w, 200, 0, value, "ok")
}
func (h *Handler) AnalyticsKB(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.scope(w, r, false)
	if !ok {
		return
	}
	key := "go:analytics:kb:" + scope.KnowledgeBaseID
	var value repository.KBStats
	if !h.cached(r, key, &value) {
		var err error
		value, err = h.Store.KBStats(r.Context(), scope)
		if err != nil {
			failure(w, err, false)
			return
		}
		h.rememberStats(r, key, value)
	}
	respond(w, 200, 0, value, "ok")
}
