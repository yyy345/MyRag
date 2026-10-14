package ai

import (
	"ai-library/backend/internal/ports"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExtractionAdapterStrictResponseAndSafeErrors(t *testing.T) {
	const id = "5c9359e7-546f-4f3d-b6cc-7da1b422d364"
	value := ports.KnowledgeSection{Title: "知识", Content: strings.Repeat("🙂", 100)}
	for _, mode := range []string{"valid", "none", "wrong-id", "short", "duplicate", "unknown", "rate-limit"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req ports.ExtractRequest
				if r.URL.Path != "/internal/v1/extract" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("t", 32) || json.NewDecoder(r.Body).Decode(&req) != nil || req.Mode != "manual" {
					t.Error("request contract")
				}
				if mode == "rate-limit" {
					w.WriteHeader(429)
					fmt.Fprint(w, "private key")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				sections := []ports.KnowledgeSection{value}
				request := id
				switch mode {
				case "none":
					sections = []ports.KnowledgeSection{}
				case "wrong-id":
					request = "wrong"
				case "short":
					sections[0].Content = "short"
				case "duplicate":
					fmt.Fprintf(w, `{"request_id":%q,"request_id":%q,"sections":[]}`, id, id)
					return
				case "unknown":
					fmt.Fprintf(w, `{"request_id":%q,"sections":[],"extra":true}`, id)
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"request_id": request, "sections": sections})
			}))
			defer server.Close()
			adapter, err := NewExtractor(server.URL, strings.Repeat("t", 32), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			sections, err := adapter.Extract(context.Background(), ports.ExtractRequest{RequestID: id, Mode: "manual", History: []ports.ChatHistory{{Role: "user", Content: "question"}}})
			if mode == "valid" || mode == "none" {
				want := 1
				if mode == "none" {
					want = 0
				}
				if err != nil || len(sections) != want {
					t.Fatal(sections, err)
				}
			} else if !errors.Is(err, ErrExtraction) {
				t.Fatal("invalid response accepted", err)
			}
		})
	}
}
