package chat

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"ai-library/backend/internal/ai"
	"ai-library/backend/internal/ports"
)

func (h *Handler) SendMessage(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.scope(w, r, true)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var payload struct {
		Content string `json:"content"`
	}
	if decoder.Decode(&payload) != nil || decoder.Decode(new(any)) != io.EOF || strings.TrimSpace(payload.Content) == "" || strings.ContainsRune(payload.Content, 0) || utf8.RuneCountInString(payload.Content) > 10_000 {
		respond(w, 400, 5000, nil, "请输入不超过10000字符的问题")
		return
	}
	if h.Streamer == nil {
		respond(w, 503, 5000, nil, ai.ErrChatStream.Error())
		return
	}
	id := r.PathValue("sid")
	var requestID string
	var prompt *string
	if err := h.Store.Pool.QueryRow(r.Context(), `SELECT gen_random_uuid()::text,system_prompt FROM knowledge_base WHERE id=$1::uuid AND user_id=$2::uuid`, scope.KnowledgeBaseID, scope.UserID).Scan(&requestID, &prompt); err != nil {
		failure(w, err, false)
		return
	}
	history, err := h.Store.Messages(r.Context(), scope, id)
	if err != nil {
		failure(w, err, true)
		return
	}
	if len(history) > 500 {
		history = history[len(history)-500:]
	}
	previous := make([]ports.ChatHistory, 0, len(history))
	for _, message := range history {
		role := "user"
		if message.Role == "ASSISTANT" {
			role = "assistant"
		}
		previous = append(previous, ports.ChatHistory{Role: role, Content: message.Content})
	}
	if _, err = h.Store.SaveMessage(r.Context(), scope, id, "USER", payload.Content, nil, nil); err != nil {
		failure(w, err, true)
		return
	}
	h.invalidate(r, scope, id)
	body, err := h.Streamer.OpenChat(r.Context(), ports.ChatRequest{RequestID: requestID, UserID: scope.UserID, KBID: scope.KnowledgeBaseID, Query: payload.Content, History: previous, SystemPrompt: prompt})
	if err != nil {
		respond(w, 503, 5000, nil, ai.ErrChatStream.Error())
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", "text/event-stream;charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	send := func(value any) bool {
		_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
		data, _ := json.Marshal(value)
		if _, err := w.Write(append(append([]byte("data: "), data...), []byte("\n\n")...)); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	defer controller.SetWriteDeadline(time.Time{})
	reader := ai.NewEventReader(body, requestID)
	var answer strings.Builder
	characters := 0
	for {
		event, err := reader.Next()
		if err != nil {
			if r.Context().Err() == nil {
				send(map[string]any{"type": "done", "is_end": true, "content": ai.ErrChatStream.Error()})
			}
			return
		}
		if event.Type == "chunk" {
			characters += utf8.RuneCountInString(event.Content)
			answer.WriteString(event.Content)
			if characters > 200_000 {
				send(map[string]any{"type": "done", "is_end": true, "content": ai.ErrChatStream.Error()})
				return
			}
			if !send(map[string]any{"type": "chunk", "content": event.Content, "is_end": false}) {
				return
			}
			continue
		}
		if strings.TrimSpace(answer.String()) == "" {
			send(map[string]any{"type": "done", "is_end": true, "content": ai.ErrChatStream.Error()})
			return
		}
		// No extra frames or unclean EOF may follow the terminal event.
		if _, err = reader.Next(); err != io.EOF || r.Context().Err() != nil {
			if r.Context().Err() == nil {
				send(map[string]any{"type": "done", "is_end": true, "content": ai.ErrChatStream.Error()})
			}
			return
		}
		for _, ref := range event.References {
			if !uuidPattern.MatchString(ref.ID) || utf8.RuneCountInString(ref.Title) > 200 || utf8.RuneCountInString(ref.Content) > 200 {
				send(map[string]any{"type": "done", "is_end": true, "content": ai.ErrChatStream.Error()})
				return
			}
		}
		refs, _ := json.Marshal(event.References)
		usage, _ := json.Marshal(event.Usage)
		if _, err = h.Store.SaveAnswer(r.Context(), scope, id, answer.String(), refs, usage, requestID, event.Hits); err != nil {
			send(map[string]any{"type": "done", "is_end": true, "content": ai.ErrChatStream.Error()})
			return
		}
		h.invalidate(r, scope, id)
		if h.Cache != nil {
			_ = h.Cache.Delete(r.Context(), "go:analytics:kb:"+scope.KnowledgeBaseID, "analytics:kb:"+scope.KnowledgeBaseID)
		}
		send(map[string]any{"type": "done", "is_end": true, "references": event.References, "usage": event.Usage})
		return
	}
}
