package chat

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"ai-library/backend/internal/auth"
	"ai-library/backend/internal/cache"
	"ai-library/backend/internal/ports"
	"ai-library/backend/internal/repository"
)

type Handler struct {
	Store     *repository.Entries
	Auth      *auth.Service
	Cache     *cache.Store
	Streamer  ports.ChatStreamer
	Extractor ports.Extractor
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func respond(w http.ResponseWriter, status, code int, data any, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "data": data, "message": message})
}
func failure(w http.ResponseWriter, err error, session bool) {
	var app *auth.Error
	if errors.As(err, &app) {
		respond(w, app.Status, app.Code, nil, app.Message)
		return
	}
	if errors.Is(err, repository.ErrNotFound) {
		if session {
			respond(w, 404, 3001, nil, "对话不存在")
		} else {
			respond(w, 404, 2001, nil, "知识库不存在")
		}
		return
	}
	respond(w, 500, 5000, nil, "服务器内部错误")
}
func (h *Handler) scope(w http.ResponseWriter, r *http.Request, session bool) (ports.Scope, bool) {
	claims, err := h.Auth.Authenticate(r)
	if err != nil {
		failure(w, err, false)
		return ports.Scope{}, false
	}
	scope := ports.Scope{UserID: strings.ToLower(claims.ID), KnowledgeBaseID: strings.ToLower(r.PathValue("id"))}
	if !uuidPattern.MatchString(scope.KnowledgeBaseID) {
		failure(w, repository.ErrNotFound, false)
		return scope, false
	}
	if err = h.Store.Authorize(r.Context(), scope); err != nil {
		failure(w, err, false)
		return scope, false
	}
	r.SetPathValue("id", scope.KnowledgeBaseID)
	if session {
		value, err := h.Store.AuthorizeSession(r.Context(), scope, r.PathValue("sid"))
		if err != nil {
			failure(w, err, true)
			return scope, false
		}
		r.SetPathValue("sid", value.ID)
	}
	return scope, true
}
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/knowledge-bases/{id}/chat/extract", h.Extract)
	mux.HandleFunc("GET /api/analytics/overview", h.AnalyticsOverview)
	mux.HandleFunc("GET /api/analytics/kb/{id}", h.AnalyticsKB)
	mux.HandleFunc("POST /api/knowledge-bases/{id}/chat/sessions/{sid}/messages", h.SendMessage)
	mux.HandleFunc("POST /api/knowledge-bases/{id}/chat/sessions", h.CreateSession)
	mux.HandleFunc("GET /api/knowledge-bases/{id}/chat/sessions", h.ListSessions)
	mux.HandleFunc("GET /api/knowledge-bases/{id}/chat/sessions/{sid}", h.Messages)
	mux.HandleFunc("DELETE /api/knowledge-bases/{id}/chat/sessions/{sid}", h.DeleteSession)
}
func (h *Handler) invalidate(r *http.Request, scope ports.Scope, id string) {
	if h.Cache == nil {
		return
	}
	keys := []string{"go:chat:sessions:" + scope.KnowledgeBaseID, "chat:sessions:" + scope.KnowledgeBaseID, "go:analytics:overview:" + scope.UserID, "analytics:overview:" + scope.UserID}
	if id != "" {
		keys = append(keys, "go:chat:messages:"+id, "chat:messages:"+id)
	}
	_ = h.Cache.Delete(r.Context(), keys...)
}
func (h *Handler) cached(r *http.Request, key string, target any) bool {
	if h.Cache == nil {
		return false
	}
	data, ok := h.Cache.Lookup(r.Context(), key)
	return ok && json.Unmarshal(data, target) == nil
}
func (h *Handler) remember(r *http.Request, key string, value any) {
	if h.Cache != nil {
		data, err := json.Marshal(value)
		if err == nil {
			_ = h.Cache.Set(r.Context(), key, data, 2*time.Minute)
		}
	}
}
func (h *Handler) CreateSession(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.scope(w, r, false)
	if !ok {
		return
	}
	session, err := h.Store.CreateSession(r.Context(), scope)
	if err != nil {
		failure(w, err, false)
		return
	}
	h.invalidate(r, scope, "")
	respond(w, 200, 0, session, "ok")
}
func (h *Handler) ListSessions(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.scope(w, r, false)
	if !ok {
		return
	}
	key := "go:chat:sessions:" + scope.KnowledgeBaseID
	var sessions []repository.Session
	if h.cached(r, key, &sessions) {
		respond(w, 200, 0, sessions, "ok")
		return
	}
	sessions, err := h.Store.ListSessions(r.Context(), scope)
	if err != nil {
		failure(w, err, false)
		return
	}
	h.remember(r, key, sessions)
	respond(w, 200, 0, sessions, "ok")
}
func (h *Handler) Messages(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.scope(w, r, true)
	if !ok {
		return
	}
	id := r.PathValue("sid")
	key := "go:chat:messages:" + id
	var messages []repository.ChatMessage
	if h.cached(r, key, &messages) {
		respond(w, 200, 0, messages, "ok")
		return
	}
	messages, err := h.Store.Messages(r.Context(), scope, id)
	if err != nil {
		failure(w, err, true)
		return
	}
	h.remember(r, key, messages)
	respond(w, 200, 0, messages, "ok")
}
func (h *Handler) DeleteSession(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.scope(w, r, true)
	if !ok {
		return
	}
	id := r.PathValue("sid")
	if err := h.Store.DeleteSession(r.Context(), scope, id); err != nil {
		failure(w, err, true)
		return
	}
	h.invalidate(r, scope, id)
	respond(w, 200, 0, nil, "ok")
}
