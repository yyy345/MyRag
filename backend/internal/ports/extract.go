package ports

import (
	"context"
	"strings"
	"unicode/utf8"
)

type KnowledgeSection struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

func (s KnowledgeSection) Valid() bool {
	if !utf8.ValidString(s.Title+s.Content) || strings.ContainsRune(s.Title+s.Content, 0) || strings.TrimSpace(s.Title) == "" || utf8.RuneCountInString(s.Title) > 200 || utf8.RuneCountInString(s.Content) > 200_000 {
		return false
	}
	units := 0
	for _, r := range strings.TrimSpace(s.Content) {
		units++
		if r > 0xffff {
			units++
		}
	}
	return units >= 200
}

type ExtractRequest struct {
	RequestID string        `json:"request_id"`
	History   []ChatHistory `json:"history"`
	Mode      string        `json:"mode"`
}
type Extractor interface {
	Extract(context.Context, ExtractRequest) ([]KnowledgeSection, error)
}
