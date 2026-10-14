package chat

import (
	"ai-library/backend/internal/ai"
	"ai-library/backend/internal/ports"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func (h *Handler) Extract(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.scope(w, r, false)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var payload struct {
		Session string `json:"sessionId"`
	}
	err := decoder.Decode(&payload)
	if (err != nil && err != io.EOF) || (err == nil && decoder.Decode(new(any)) != io.EOF) {
		respond(w, 400, 5000, nil, "提炼参数无效")
		return
	}
	payload.Session = strings.TrimSpace(payload.Session)
	history, err := h.Store.ExtractionHistory(r.Context(), scope, payload.Session, nil, nil)
	if err != nil {
		failure(w, err, payload.Session != "")
		return
	}
	if len(history) == 0 {
		respond(w, 200, 0, map[string]any{"entries": []any{}, "count": 0}, "没有对话记录可提炼")
		return
	}
	if h.Extractor == nil {
		respond(w, 503, 5000, nil, ai.ErrExtraction.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	var sections []ports.KnowledgeSection
	for _, session := range history {
		var id string
		if err = h.Store.Pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
			failure(w, err, false)
			return
		}
		items, err := h.Extractor.Extract(ctx, ports.ExtractRequest{RequestID: id, History: session.Messages, Mode: "manual"})
		if err != nil {
			respond(w, 503, 5000, nil, ai.ErrExtraction.Error())
			return
		}
		sections = append(sections, items...)
	}
	entries, err := h.Store.SaveExtracted(ctx, scope, sections, nil)
	if err != nil {
		failure(w, err, false)
		return
	}
	h.invalidateEntries(ctx, scope)
	message := "未提炼到新知识"
	if len(entries) > 0 {
		message = fmt.Sprintf("成功提炼 %d 条知识", len(entries))
	}
	respond(w, 200, 0, map[string]any{"entries": entries, "count": len(entries)}, message)
}
func (h *Handler) invalidateEntries(ctx context.Context, scope ports.Scope) {
	if h.Cache != nil {
		_ = h.Cache.DeletePrefix(ctx, "go:entry:"+scope.KnowledgeBaseID+":")
		_ = h.Cache.DeletePrefix(ctx, "entry:"+scope.KnowledgeBaseID+":")
		_ = h.Cache.Delete(ctx, "analytics:overview:"+scope.UserID, "analytics:kb:"+scope.KnowledgeBaseID, "go:analytics:overview:"+scope.UserID, "go:analytics:kb:"+scope.KnowledgeBaseID)
	}
}
