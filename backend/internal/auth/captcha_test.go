package auth

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-library/backend/internal/cache"
	"github.com/redis/go-redis/v9"
)

func TestCaptchaStoreFailureDoesNotIssueChallenge(t *testing.T) {
	store, err := cache.New("redis://127.0.0.1:56380")
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	c := Captcha{Store: store}
	r := httptest.NewRecorder()
	c.Issue(r, httptest.NewRequest("GET", "/api/auth/captcha", nil))
	if r.Code != 503 || strings.Contains(r.Body.String(), "svg") {
		t.Fatal("challenge issued without persistence")
	}
}

func TestCaptchaIntegration(t *testing.T) {
	if os.Getenv("RUN_REDIS_TESTS") != "1" {
		t.Skip("set RUN_REDIS_TESTS=1 for isolated captcha tests")
	}
	ctx := context.Background()
	store, err := cache.New("redis://127.0.0.1:56380/0")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	c := Captcha{Store: store}
	mux := http.NewServeMux()
	c.Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:56380"})
	defer client.Close()
	issue := func() (string, string) {
		r, err := http.Get(server.URL + "/api/auth/captcha")
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		if r.StatusCode != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), "image/svg+xml") {
			t.Fatal("legacy captcha envelope changed")
		}
		var body struct {
			Code    int
			Data    struct{ ID, SVG string }
			Message string
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Code != 0 || !uuidPattern.MatchString(body.Data.ID) {
			t.Fatal("invalid captcha response")
		}
		if strings.Contains(body.Data.SVG, "<text") {
			t.Fatal("equation exposed as plaintext SVG")
		}
		decoder := xml.NewDecoder(strings.NewReader(body.Data.SVG))
		for {
			_, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal("invalid SVG", err)
			}
		}
		id := body.Data.ID
		t.Cleanup(func() { store.Delete(ctx, "captcha:"+id) })
		value, err := store.Get(ctx, "captcha:"+id)
		if err != nil {
			t.Fatal(err)
		}
		var answer string
		if json.Unmarshal(value, &answer) != nil {
			t.Fatal("answer not legacy JSON string")
		}
		ttl, err := client.TTL(ctx, "captcha:"+id).Result()
		if err != nil || ttl < 295*time.Second || ttl > 300*time.Second {
			t.Fatal("wrong captcha TTL")
		}
		return id, answer
	}
	id, answer := issue()
	if err = c.Verify(ctx, id, answer); err != nil {
		t.Fatal(err)
	}
	if c.Verify(ctx, id, answer) == nil {
		t.Fatal("replay accepted")
	}
	id, answer = issue()
	if c.Verify(ctx, id, "wrong") == nil {
		t.Fatal("wrong answer accepted")
	}
	if c.Verify(ctx, id, answer) == nil {
		t.Fatal("failed attempt did not consume challenge")
	}
	id, answer = issue()
	if err = client.Expire(ctx, "captcha:"+id, 0).Err(); err != nil {
		t.Fatal(err)
	}
	if c.Verify(ctx, id, answer) == nil {
		t.Fatal("expired challenge accepted")
	}
	if c.Verify(ctx, "", "") == nil || c.Verify(ctx, "invalid", "1") == nil {
		t.Fatal("invalid input accepted")
	}
	id, answer = issue()
	var accepted atomic.Int32
	var group sync.WaitGroup
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			if c.Verify(ctx, id, answer) == nil {
				accepted.Add(1)
			}
		}()
	}
	group.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("concurrent challenge accepted %d times", accepted.Load())
	}
}
