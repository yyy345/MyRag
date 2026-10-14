package knowledge

import (
	"ai-library/backend/internal/ai"
	"ai-library/backend/internal/chat"
	"ai-library/backend/internal/ports"
	"ai-library/backend/internal/repository"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestChatStreamFailureCancellationHeatAndAnalyticsIntegration(t *testing.T) {
	f := uploadSetup(t, nil)
	ctx := context.Background()
	store := &repository.Entries{Pool: f.h.Pool}
	scope := ports.Scope{UserID: f.user, KnowledgeBaseID: f.kb}
	var entry, chunk, foreignChunk string
	for _, kb := range []string{f.kb, f.otherKB} {
		var e, c string
		if err := f.h.Pool.QueryRow(ctx, `INSERT INTO knowledge_entry(id,kb_id,title,content,processing_status,updated_at) VALUES(gen_random_uuid(),$1::uuid,'资料','content','COMPLETED',now()) RETURNING id::text`, kb).Scan(&e); err != nil {
			t.Fatal(err)
		}
		if err := f.h.Pool.QueryRow(ctx, `INSERT INTO knowledge_chunk(id,entry_id,chunk_index,content,embedding) VALUES(gen_random_uuid(),$1::uuid,0,'content',array_fill(0.01::real,ARRAY[1024])::vector) RETURNING id::text`, e).Scan(&c); err != nil {
			t.Fatal(err)
		}
		if kb == f.kb {
			entry, chunk = e, c
		} else {
			foreignChunk = c
		}
	}
	cancelled := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload ports.ChatRequest
		if json.NewDecoder(r.Body).Decode(&payload) != nil || payload.UserID != f.user || payload.KBID != f.kb {
			t.Error("identity not forwarded")
			return
		}
		if payload.Query == "reject" {
			w.WriteHeader(429)
			io.WriteString(w, "private secret")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(value any) {
			data, _ := json.Marshal(value)
			fmt.Fprintf(w, "data: %s\n\n", data)
			w.(http.Flusher).Flush()
		}
		send(map[string]any{"type": "chunk", "request_id": payload.RequestID, "content": "中文🙂"})
		if payload.Query == "cancel" {
			<-r.Context().Done()
			cancelled <- struct{}{}
			return
		}
		if payload.Query == "error" {
			send(map[string]any{"type": "error", "request_id": payload.RequestID, "code": "private_failure"})
			return
		}
		if payload.Query == "eof" {
			return
		}
		hits := []string{chunk, chunk, foreignChunk}
		if payload.Query == "invalid" {
			hits = []string{"invalid-chunk-id"}
		}
		send(map[string]any{"type": "done", "request_id": payload.RequestID, "references": []ai.Reference{{ID: entry, Title: "资料", Content: "content"}}, "usage": ai.Usage{Prompt: 12, Completion: 4}, "hit_chunk_ids": hits})
		if payload.Query == "extra" {
			send(map[string]any{"type": "chunk", "request_id": payload.RequestID, "content": "late"})
		}
	}))
	defer upstream.Close()
	streamer, err := ai.NewStreamer(upstream.URL, uploadTestToken, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	handler := &chat.Handler{Store: store, Auth: f.h.Auth, Cache: f.h.Cache, Streamer: streamer}
	handler.Register(f.server.Config.Handler.(*http.ServeMux))
	var successSession repository.Session
	for _, query := range []string{"success", "error", "eof", "invalid", "extra", "reject", "cancel"} {
		t.Run(query, func(t *testing.T) {
			session, err := store.CreateSession(ctx, scope)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(map[string]string{"content": query})
			requestCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			kbID, sessionID := f.kb, session.ID
			if query == "success" {
				kbID, sessionID = strings.ToUpper(kbID), strings.ToUpper(sessionID)
			}
			req, _ := http.NewRequestWithContext(requestCtx, "POST", f.server.URL+"/api/knowledge-bases/"+kbID+"/chat/sessions/"+sessionID+"/messages", strings.NewReader(string(body)))
			req.Header.Set("Authorization", "Bearer "+f.token)
			response, err := f.server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			if query == "cancel" {
				buf := make([]byte, 1024)
				n, err := response.Body.Read(buf)
				if err != nil || n == 0 {
					t.Fatal("first frame", err)
				}
				cancel()
				response.Body.Close()
				select {
				case <-cancelled:
				case <-time.After(2 * time.Second):
					t.Fatal("upstream not cancelled")
				}
			} else {
				raw, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(raw), "private") {
					t.Fatal("private error leaked")
				}
				if query == "success" {
					if !strings.Contains(string(raw), `"references"`) {
						t.Fatal(string(raw))
					}
					successSession = session
				} else if query == "reject" {
					if response.StatusCode != 503 {
						t.Fatal(response.StatusCode)
					}
				} else if strings.Contains(string(raw), `"references"`) {
					t.Fatal("failed stream sent successful done")
				}
			}
			messages, err := store.Messages(ctx, scope, session.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if query == "success" {
				want = 2
			}
			if len(messages) != want {
				t.Fatal(query, len(messages), want)
			}
		})
	}
	var requestID, messageID string
	if err = f.h.Pool.QueryRow(ctx, `SELECT request_id::text,message_id::text FROM chat_receipt WHERE session_id=$1::uuid`, successSession.ID).Scan(&requestID, &messageID); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		id, err := store.SaveAnswer(ctx, scope, strings.ToUpper(successSession.ID), "duplicate", json.RawMessage(`[]`), json.RawMessage(`{}`), requestID, []string{chunk})
		if err != nil || id != messageID {
			t.Fatal("idempotency", id, err)
		}
	}
	var ownHits, foreignHits int
	if err = f.h.Pool.QueryRow(ctx, `SELECT (SELECT hit_count FROM knowledge_chunk WHERE id=$1::uuid),(SELECT hit_count FROM knowledge_chunk WHERE id=$2::uuid)`, chunk, foreignChunk).Scan(&ownHits, &foreignHits); err != nil || ownHits != 1 || foreignHits != 0 {
		t.Fatal("scoped heat", ownHits, foreignHits, err)
	}
	get := func(path, token string, want int) json.RawMessage {
		t.Helper()
		req, _ := http.NewRequest("GET", f.server.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := f.server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var value struct{ Data json.RawMessage }
		if json.NewDecoder(res.Body).Decode(&value) != nil || res.StatusCode != want {
			t.Fatal(path, res.StatusCode, want)
		}
		return value.Data
	}
	var overview repository.Overview
	if err = json.Unmarshal(get("/api/analytics/overview", f.token, 200), &overview); err != nil || overview.Summary.KBs != 1 || overview.Summary.Entries != 1 || overview.Summary.Sessions != 7 || len(overview.Hot) != 1 || overview.Hot[0].Hits != 1 {
		t.Fatal(overview, err)
	}
	var stats repository.KBStats
	if err = json.Unmarshal(get("/api/analytics/kb/"+f.kb, f.token, 200), &stats); err != nil || len(stats.Hot) != 1 || stats.Hot[0].Hits != 1 {
		t.Fatal(stats, err)
	}
	get("/api/analytics/kb/"+f.kb, f.otherToken, 404)
	get("/api/analytics/kb/invalid", f.token, 404)
	get("/api/analytics/overview", "", 401)
	if f.h.Cache != nil {
		t.Cleanup(func() {
			_ = f.h.Cache.Delete(ctx, "go:analytics:overview:"+f.user, "go:analytics:kb:"+f.kb, "go:chat:sessions:"+f.kb)
		})
	}
}
