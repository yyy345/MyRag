package ports

import (
	"context"
	"io"
)

type ChatRequest struct {
	RequestID    string        `json:"request_id"`
	UserID       string        `json:"user_id"`
	KBID         string        `json:"kb_id"`
	Query        string        `json:"query"`
	History      []ChatHistory `json:"history"`
	SystemPrompt *string       `json:"system_prompt"`
}
type ChatHistory struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type ChatStreamer interface {
	OpenChat(context.Context, ChatRequest) (io.ReadCloser, error)
}
