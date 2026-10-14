package ai

import (
	"ai-library/backend/internal/ports"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type smallReader struct{ io.Reader }

func (s smallReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return s.Reader.Read(p)
}

func TestChatEventReaderBoundariesAndFailures(t *testing.T) {
	const id = "fcd0d385-7a1a-47b9-a8d4-038d9b1508b2"
	chunk := `{"type":"chunk","request_id":"` + id + `","content":"中文🙂"}`
	done := `{"type":"done","request_id":"` + id + `","references":[],"usage":{"prompt_tokens":12,"completion_tokens":4},"hit_chunk_ids":[]}`
	reader := NewEventReader(smallReader{strings.NewReader(": heartbeat\r\ndata: " + chunk + "\r\n\r\ndata: " + done + "\n\n")}, id)
	event, err := reader.Next()
	if err != nil || event.Content != "中文🙂" {
		t.Fatal(event, err)
	}
	event, err = reader.Next()
	if err != nil || event.Type != "done" || event.Usage.Prompt != 12 {
		t.Fatal(event, err)
	}
	if _, err = reader.Next(); err != io.EOF {
		t.Fatal(err)
	}
	for _, bad := range []string{chunk, strings.Replace(chunk, id, "wrong", 1) + "\n\n", strings.Replace(chunk, `"content"`, `"extra":1,"content"`, 1) + "\n\n", strings.Replace(chunk, `"content"`, `"content":"duplicate","content"`, 1) + "\n\n", strings.Replace(done, `"prompt_tokens":12`, `"prompt_tokens":-1`, 1) + "\n\n", strings.Replace(done, `"prompt_tokens":12`, `"prompt_tokens":true`, 1) + "\n\n", `{"type":"error","code":"secret"}` + "\n\n", strings.Repeat("x", 300*1024) + "\n\n"} {
		if _, err := NewEventReader(strings.NewReader("data: "+bad), id).Next(); !errors.Is(err, ErrChatStream) {
			t.Fatalf("invalid stream accepted: %v", err)
		}
	}
}

func TestChatAdapterAuthenticationAndCancellation(t *testing.T) {
	token := "local-chat-token-at-least-32-characters"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/chat/stream" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("request contract")
		}
		var payload ports.ChatRequest
		if json.NewDecoder(r.Body).Decode(&payload) != nil {
			t.Error("payload")
		}
		if payload.Query == "reject" {
			w.WriteHeader(429)
			io.WriteString(w, "private provider failure")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	adapter, err := NewStreamer(server.URL, token, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = adapter.OpenChat(context.Background(), ports.ChatRequest{Query: "reject"}); !errors.Is(err, ErrChatStream) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	body, err := adapter.OpenChat(ctx, ports.ChatRequest{Query: "cancel"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	defer body.Close()
	if _, err = io.ReadAll(body); err == nil {
		t.Fatal("cancellation not propagated")
	}
}
