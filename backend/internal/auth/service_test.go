package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-library/backend/internal/cache"
	"ai-library/backend/internal/repository"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

func testService(t *testing.T) (*Service, *httptest.Server, string) {
	t.Helper()
	if os.Getenv("RUN_DATABASE_TESTS") != "1" || os.Getenv("RUN_REDIS_TESTS") != "1" {
		t.Skip("enable isolated DB and Redis integration tests")
	}
	ctx := context.Background()
	pool, err := repository.Open(ctx, "postgresql://postgres:migration-test-only@127.0.0.1:55432/migration_test")
	if err != nil {
		t.Fatal(err)
	}
	root, _ := filepath.Abs("../../..")
	migrations, err := repository.ReadMigrations(filepath.Join(root, "server/prisma/migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if err = repository.Apply(ctx, pool, migrations); err != nil {
		t.Fatal(err)
	}
	store, err := cache.New("redis://127.0.0.1:56380/0")
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{Pool: pool, Captcha: Captcha{Store: store}, Secret: []byte("go-auth-test-only-secret-32-characters")}
	mux := http.NewServeMux()
	service.Captcha.Register(mux)
	service.RegisterRoutes(mux)
	server := httptest.NewServer(mux)
	prefix := fmt.Sprintf("go_auth_%d_", time.Now().UnixNano())
	t.Cleanup(func() {
		server.Close()
		_, err := pool.Exec(ctx, `DELETE FROM "user" WHERE left(username,length($1))=$1`, prefix)
		if err != nil {
			t.Error(err)
		}
		store.Close()
		pool.Close()
	})
	return service, server, prefix
}
func postCredentials(t *testing.T, s *Service, server *httptest.Server, path string, input credentials) (int, map[string]any) {
	t.Helper()
	id, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	input.CaptchaID = id
	input.CaptchaCode = "12"
	if err = s.Captcha.Store.Set(context.Background(), "captcha:"+id, []byte(`"12"`), time.Minute); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(input)
	r, err := http.Post(server.URL+path, "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var result map[string]any
	if json.NewDecoder(r.Body).Decode(&result) != nil {
		t.Fatal("invalid response")
	}
	return r.StatusCode, result
}
func TestRegistrationIntegration(t *testing.T) {
	s, server, prefix := testService(t)
	input := credentials{Username: prefix + "user", Email: prefix + "user@example.test", Password: "测试-password-2026"}
	status, body := postCredentials(t, s, server, "/api/auth/register", input)
	if status != 200 {
		t.Fatalf("registration: %v", body)
	}
	data := body["data"].(map[string]any)
	user := data["user"].(map[string]any)
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), "password") || len(user) != 4 {
		t.Fatal("response leaked password fields")
	}
	var hash string
	if err := s.Pool.QueryRow(context.Background(), `SELECT password_hash FROM "user" WHERE username=$1`, input.Username).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash == input.Password || bcrypt.CompareHashAndPassword([]byte(hash), []byte(input.Password)) != nil {
		t.Fatal("invalid password hash")
	}
	if cost, _ := bcrypt.Cost([]byte(hash)); cost != 10 {
		t.Fatal("bcrypt cost differs from legacy")
	}
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(data["token"].(string), claims, func(*jwt.Token) (any, error) { return s.Secret, nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())
	if err != nil || !token.Valid || claims.ID != user["id"] || claims.Username != input.Username || claims.ExpiresAt.Sub(claims.IssuedAt.Time) != 7*24*time.Hour {
		t.Fatal("JWT contract differs")
	}
	for _, duplicate := range []credentials{input, {Username: prefix + "different", Email: input.Email, Password: input.Password}} {
		status, body = postCredentials(t, s, server, "/api/auth/register", duplicate)
		if status != 409 || body["code"] != float64(1003) {
			t.Fatal("duplicate account accepted")
		}
	}
	for _, invalid := range []credentials{{Username: " ", Email: "bad", Password: "short"}, {Username: prefix + "invalid", Email: prefix + "invalid@example.test", Password: strings.Repeat("密", 25)}} {
		status, _ = postCredentials(t, s, server, "/api/auth/register", invalid)
		if status != 400 {
			t.Fatal("invalid registration accepted")
		}
	}
	var group sync.WaitGroup
	statuses := make(chan int, 2)
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			status, _ := postCredentials(t, s, server, "/api/auth/register", credentials{Username: prefix + "race", Email: prefix + "race@example.test", Password: "password-2026"})
			statuses <- status
		}()
	}
	group.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal("concurrent duplicates not rejected", counts)
	}
}
