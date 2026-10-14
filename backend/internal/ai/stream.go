package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"ai-library/backend/internal/ports"
)

var ErrChatStream = errors.New("回答生成暂不可用，请重试")

type Streamer struct {
	endpoint, token string
	client          *http.Client
}

func NewStreamer(baseURL, token string, timeout time.Duration) (*Streamer, error) {
	p, err := NewParser(baseURL, token, timeout)
	if err != nil {
		return nil, err
	}
	return &Streamer{endpoint: strings.TrimSuffix(p.endpoint, "/parse") + "/chat/stream", token: p.token, client: p.client}, nil
}
func (*Streamer) String() string { return "Streamer{credentials:redacted}" }
func (s *Streamer) OpenChat(ctx context.Context, payload ports.ChatRequest) (io.ReadCloser, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, ErrChatStream
	}
	request, err := http.NewRequestWithContext(ctx, "POST", s.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, ErrChatStream
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+s.token)
	response, err := s.client.Do(request)
	if err != nil {
		return nil, ErrChatStream
	}
	media, _, parseErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != 200 || parseErr != nil || media != "text/event-stream" {
		response.Body.Close()
		return nil, ErrChatStream
	}
	return response.Body, nil
}

type Reference struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Content string `json:"content"`
}
type Usage struct {
	Prompt     int `json:"prompt_tokens"`
	Completion int `json:"completion_tokens"`
}
type ChatEvent struct {
	Type       string
	Content    string
	References []Reference
	Usage      Usage
	Hits       []string
}
type EventReader struct {
	scanner *bufio.Scanner
	id      string
	total   int
}

func NewEventReader(reader io.Reader, id string) *EventReader {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 256*1024)
	return &EventReader{scanner: scanner, id: id}
}
func (r *EventReader) Next() (ChatEvent, error) {
	var data []string
	for r.scanner.Scan() {
		line := r.scanner.Text()
		r.total += len(line) + 1
		if r.total > 16*1024*1024 {
			return ChatEvent{}, ErrChatStream
		}
		if line == "" {
			if len(data) == 0 {
				continue
			}
			return r.decode([]byte(strings.Join(data, "\n")))
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			return ChatEvent{}, ErrChatStream
		}
		data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
	}
	if r.scanner.Err() != nil || len(data) > 0 {
		return ChatEvent{}, ErrChatStream
	}
	return ChatEvent{}, io.EOF
}
func (r *EventReader) decode(body []byte) (ChatEvent, error) {
	var header struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(body, &header) != nil {
		return ChatEvent{}, ErrChatStream
	}
	var required []string
	switch header.Type {
	case "chunk":
		required = []string{"type", "request_id", "content"}
	case "done":
		required = []string{"type", "request_id", "references", "usage", "hit_chunk_ids"}
	case "error":
		return ChatEvent{}, ErrChatStream
	default:
		return ChatEvent{}, ErrChatStream
	}
	fields, err := parseObject(body, required...)
	if err != nil {
		return ChatEvent{}, ErrChatStream
	}
	var id string
	if json.Unmarshal(fields["request_id"], &id) != nil || id != r.id {
		return ChatEvent{}, ErrChatStream
	}
	event := ChatEvent{Type: header.Type}
	if header.Type == "chunk" {
		if json.Unmarshal(fields["content"], &event.Content) != nil || !utf8.ValidString(event.Content) || strings.ContainsRune(event.Content, 0) {
			return event, ErrChatStream
		}
		return event, nil
	}
	usage, err := parseObject(fields["usage"], "prompt_tokens", "completion_tokens")
	if err != nil || json.Unmarshal(usage["prompt_tokens"], &event.Usage.Prompt) != nil || json.Unmarshal(usage["completion_tokens"], &event.Usage.Completion) != nil || event.Usage.Prompt < 0 || event.Usage.Completion < 0 || json.Unmarshal(fields["references"], &event.References) != nil || json.Unmarshal(fields["hit_chunk_ids"], &event.Hits) != nil || len(event.References) > 5 || len(event.Hits) > 5 {
		return event, ErrChatStream
	}
	return event, nil
}
