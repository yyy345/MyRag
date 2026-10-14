package ai

import (
	"ai-library/backend/internal/ports"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

var ErrExtraction = errors.New("知识提炼暂不可用，请重试")

type Extractor struct {
	endpoint, token string
	client          *http.Client
}

func NewExtractor(baseURL, token string, timeout time.Duration) (*Extractor, error) {
	p, err := NewParser(baseURL, token, timeout)
	if err != nil {
		return nil, err
	}
	return &Extractor{strings.TrimSuffix(p.endpoint, "/parse") + "/extract", p.token, p.client}, nil
}
func (*Extractor) String() string { return "Extractor{credentials:redacted}" }
func (e *Extractor) Extract(ctx context.Context, payload ports.ExtractRequest) ([]ports.KnowledgeSection, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, ErrExtraction
	}
	req, err := http.NewRequestWithContext(ctx, "POST", e.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, ErrExtraction
	}
	req.Header.Set("Authorization", "Bearer "+e.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := e.client.Do(req)
	if err != nil {
		return nil, ErrExtraction
	}
	defer res.Body.Close()
	media, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || res.StatusCode != 200 {
		return nil, ErrExtraction
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4*1024*1024+1))
	if err != nil || len(raw) > 4*1024*1024 {
		return nil, ErrExtraction
	}
	fields, err := parseObject(raw, "request_id", "sections")
	if err != nil {
		return nil, ErrExtraction
	}
	var id string
	var sections []json.RawMessage
	if json.Unmarshal(fields["request_id"], &id) != nil || id != payload.RequestID || json.Unmarshal(fields["sections"], &sections) != nil || len(sections) > 20 {
		return nil, ErrExtraction
	}
	values := []ports.KnowledgeSection{}
	for _, section := range sections {
		fields, err := parseObject(section, "title", "content")
		var item ports.KnowledgeSection
		if err != nil || json.Unmarshal(fields["title"], &item.Title) != nil || json.Unmarshal(fields["content"], &item.Content) != nil || !item.Valid() {
			return nil, ErrExtraction
		}
		values = append(values, item)
	}
	return values, nil
}
