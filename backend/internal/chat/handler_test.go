package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-library/backend/internal/auth"
	"ai-library/backend/internal/cache"
	"ai-library/backend/internal/ports"
	"ai-library/backend/internal/repository"
	"github.com/golang-jwt/jwt/v5"
)

func TestSessionHTTPAuthorizationHistoryAndDeleteIntegration(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" || os.Getenv("RUN_REDIS_TESTS") != "1" {
		t.Skip("requires isolated DB/Redis")
	}
	ctx := context.Background()
	pool, err := repository.Open(ctx, "postgresql://postgres:migration-test-only@127.0.0.1:55432/migration_test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	migrations, err := repository.ReadMigrations("../../../server/prisma/migrations")
	if err != nil {
		t.Fatal(err)
	}
	if err = repository.Apply(ctx, pool, migrations); err != nil {
		t.Fatal(err)
	}
	redis, err := cache.New("redis://127.0.0.1:56380/0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { redis.Close() })
	secret := []byte("chat-local-test-secret-at-least-32-characters")
	h := &Handler{Store: &repository.Entries{Pool: pool}, Auth: &auth.Service{Secret: secret}, Cache: redis}
	var user, otherUser, kb, otherKB, token, otherToken string
	for i := 0; i < 2; i++ {
		var u, k string
		name := fmt.Sprintf("go_chat_%d_%d", time.Now().UnixNano(), i)
		if err = pool.QueryRow(ctx, `INSERT INTO "user"(id,username,email,password_hash,updated_at) VALUES(gen_random_uuid(),$1,$2,'unused',now()) RETURNING id::text`, name, name+"@example.test").Scan(&u); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(ctx, `DELETE FROM "user" WHERE id=$1::uuid`, u)
			_ = redis.DeletePrefix(ctx, "go:chat:")
			_ = redis.Delete(ctx, "chat:sessions:"+k, "go:analytics:overview:"+u, "analytics:overview:"+u)
		})
		if err = pool.QueryRow(ctx, `INSERT INTO knowledge_base(id,user_id,name,updated_at) VALUES(gen_random_uuid(),$1::uuid,'chat',now()) RETURNING id::text`, u).Scan(&k); err != nil {
			t.Fatal(err)
		}
		v, err := jwt.NewWithClaims(jwt.SigningMethodHS256, auth.Claims{ID: u, Username: name, RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}).SignedString(secret)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			user, kb, token = u, k, v
		} else {
			otherUser, otherKB, otherToken = u, k, v
		}
	}
	_ = otherUser
	mux := http.NewServeMux()
	h.Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	request := func(method, path, credential string, want int) json.RawMessage {
		t.Helper()
		req, _ := http.NewRequest(method, server.URL+path, nil)
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var value struct {
			Code    int
			Data    json.RawMessage
			Message string
		}
		if err = json.NewDecoder(response.Body).Decode(&value); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want {
			t.Fatal(method, path, response.StatusCode, want, value.Message)
		}
		return value.Data
	}
	path := "/api/knowledge-bases/" + kb + "/chat/sessions"
	upperPath := "/api/knowledge-bases/" + strings.ToUpper(kb) + "/chat/sessions"
	request("POST", path, "", 401)
	request("POST", path, otherToken, 404)
	if string(request("GET", path, token, 200)) != "[]" {
		t.Fatal("empty list not array")
	}
	request("GET", upperPath, token, 200)
	var session repository.Session
	if json.Unmarshal(request("POST", path, token, 200), &session) != nil || session.Title != "新对话" || session.KBID != kb || session.UserID != user {
		t.Fatal(session)
	}
	var listed []repository.Session
	if err = json.Unmarshal(request("GET", upperPath, token, 200), &listed); err != nil || len(listed) != 1 || listed[0].ID != session.ID {
		t.Fatal("mixed-case session list returned stale cache", listed, err)
	}
	if string(request("GET", path+"/"+session.ID, token, 200)) != "[]" {
		t.Fatal("empty history")
	}
	upperHistory := upperPath + "/" + strings.ToUpper(session.ID)
	request("GET", upperHistory, token, 200)
	scope := ports.Scope{UserID: user, KnowledgeBaseID: kb}
	first := strings.Repeat("中", 49) + "🙂尾"
	if _, err = h.Store.SaveMessage(ctx, scope, session.ID, "USER", first, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = h.Store.SaveMessage(ctx, scope, session.ID, "ASSISTANT", "完整回答", json.RawMessage(`[{"id":"reference","title":"引用"}]`), json.RawMessage(`{"prompt_tokens":2,"completion_tokens":3}`)); err != nil {
		t.Fatal(err)
	}
	h.invalidate(httptest.NewRequest("GET", "/", nil), scope, session.ID)
	var messages []repository.ChatMessage
	if err = json.Unmarshal(request("GET", path+"/"+session.ID, token, 200), &messages); err != nil || len(messages) != 2 || messages[0].Role != "USER" || messages[1].Role != "ASSISTANT" || !bytes.Contains(messages[1].TokenUsage, []byte("completion_tokens")) {
		t.Fatal(messages, err)
	}
	if err = json.Unmarshal(request("GET", upperHistory, token, 200), &messages); err != nil || len(messages) != 2 {
		t.Fatal("mixed-case history returned stale cache", messages, err)
	}
	saved, err := h.Store.AuthorizeSession(ctx, scope, session.ID)
	if err != nil || saved.Title != strings.Repeat("中", 49) {
		t.Fatal("UTF16 title split", saved, err)
	}
	// Cached history still requires both KB and session ownership checks.
	request("GET", path+"/"+session.ID, otherToken, 404)
	request("GET", "/api/knowledge-bases/"+otherKB+"/chat/sessions/"+session.ID, otherToken, 404)
	request("DELETE", "/api/knowledge-bases/"+otherKB+"/chat/sessions/"+session.ID, otherToken, 404)
	// Same owner, different KB is rejected too.
	var secondKB string
	if err = pool.QueryRow(ctx, `INSERT INTO knowledge_base(id,user_id,name,updated_at) VALUES(gen_random_uuid(),$1::uuid,'second',now()) RETURNING id::text`, user).Scan(&secondKB); err != nil {
		t.Fatal(err)
	}
	request("GET", "/api/knowledge-bases/"+secondKB+"/chat/sessions/"+session.ID, token, 404)
	request("DELETE", path+"/"+session.ID, token, 200)
	request("GET", path+"/"+session.ID, token, 404)
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM chat_message WHERE session_id=$1::uuid`, session.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("message cascade", count, err)
	}
	if _, err = h.Store.SaveMessage(ctx, scope, session.ID, "ASSISTANT", "late reply", nil, nil); err == nil {
		t.Fatal("deleted session revived")
	}
	// Concurrent first messages serialize one title update and preserve ordering.
	session, err = h.Store.CreateSession(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := h.Store.SaveMessage(ctx, scope, session.ID, "USER", fmt.Sprintf("first-%d", i), nil, nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	saved, err = h.Store.AuthorizeSession(ctx, scope, session.ID)
	if err != nil || !strings.HasPrefix(saved.Title, "first-") {
		t.Fatal(saved, err)
	}
	messages, err = h.Store.Messages(ctx, scope, session.ID)
	if err != nil || len(messages) != 8 || saved.Title != messages[0].Content {
		t.Fatal("first title/order", saved, messages, err)
	}
}
