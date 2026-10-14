package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
)

func TestLoginIdentityLimits(t *testing.T) {
	for _, identity := range []string{strings.Repeat("中", 200), strings.Repeat("🙂", 200), strings.Repeat("a", 320)} {
		if !validLoginIdentity(identity) {
			t.Fatal("valid username or email rejected")
		}
	}
	for _, identity := range []string{"", strings.Repeat("中", 201), strings.Repeat("🙂", 201), strings.Repeat("a", 321)} {
		if validLoginIdentity(identity) {
			t.Fatal("oversized or empty identity accepted")
		}
	}
}

func TestUnicodeUsernameLoginIntegration(t *testing.T) {
	s, server, prefix := testService(t)
	for i, character := range []string{"中", "🙂"} {
		username := prefix + strings.Repeat(character, maxUsernameRunes-utf8.RuneCountInString(prefix))
		input := credentials{Username: username, Email: fmt.Sprintf("%sunicode%d@example.test", prefix, i), Password: "test-password-2026"}
		if status, body := postCredentials(t, s, server, "/api/auth/register", input); status != 200 {
			t.Fatalf("Unicode registration failed: %v", body)
		}
		for _, identity := range []string{username, input.Email} {
			if status, body := postCredentials(t, s, server, "/api/auth/login", credentials{Username: identity, Password: input.Password}); status != 200 {
				t.Fatalf("Unicode account login failed: %v", body)
			}
		}
	}
}

func TestLegacyLoginAndJWTIntegration(t *testing.T) {
	s, server, prefix := testService(t)
	input := credentials{Username: prefix + "legacy", Email: prefix + "legacy@example.test", Password: "registered-password"}
	status, body := postCredentials(t, s, server, "/api/auth/register", input)
	if status != 200 {
		t.Fatal(body)
	}
	data := body["data"].(map[string]any)
	user := data["user"].(map[string]any)
	id := user["id"].(string)
	var goHash string
	if err := s.Pool.QueryRow(context.Background(), `SELECT password_hash FROM "user" WHERE id=$1::uuid`, id).Scan(&goHash); err != nil {
		t.Fatal(err)
	}
	legacyPassword := strings.Repeat("密", 30)
	payload, _ := json.Marshal(map[string]string{"id": id, "username": input.Username, "secret": string(s.Secret), "goToken": data["token"].(string), "goHash": goHash, "password": input.Password, "legacyPassword": legacyPassword})
	script := `const fs=require('node:fs'),b=require('bcryptjs'),j=require('jsonwebtoken');const d=JSON.parse(fs.readFileSync(0,'utf8'));console.log(JSON.stringify({hash:b.hashSync(d.legacyPassword,10),token:j.sign({id:d.id,username:d.username},d.secret,{expiresIn:'7d'}),verified:j.verify(d.goToken,d.secret),hashValid:b.compareSync(d.password,d.goHash)}));`
	command := exec.Command("node", "-e", script)
	command.Dir, _ = filepath.Abs("../../..")
	command.Stdin = strings.NewReader(string(payload))
	output, err := command.Output()
	if err != nil {
		t.Fatal("Node compatibility fixture failed", err)
	}
	var legacy struct {
		Hash, Token string
		Verified    struct{ ID, Username string }
		HashValid   bool
	}
	if json.Unmarshal(output, &legacy) != nil || legacy.Verified.ID != id || !legacy.HashValid {
		t.Fatal("Node rejected Go hash or JWT")
	}
	if _, err = s.Pool.Exec(context.Background(), `UPDATE "user" SET password_hash=$1 WHERE id=$2::uuid`, legacy.Hash, id); err != nil {
		t.Fatal(err)
	}
	for _, identity := range []string{input.Username, input.Email} {
		status, body = postCredentials(t, s, server, "/api/auth/login", credentials{Username: identity, Password: legacyPassword})
		if status != 200 {
			t.Fatalf("legacy bcrypt login failed: %v", body)
		}
	}
	for _, invalid := range []credentials{{Username: input.Username, Password: "wrong"}, {Username: prefix + "missing", Password: "wrong"}} {
		status, body = postCredentials(t, s, server, "/api/auth/login", invalid)
		if status != 401 || body["code"] != float64(1002) {
			t.Fatal("wrong credentials accepted")
		}
	}
	getMe := func(token string) (int, map[string]any) {
		r, _ := http.NewRequest("GET", server.URL+"/api/auth/me", nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var body map[string]any
		if json.NewDecoder(response.Body).Decode(&body) != nil {
			t.Fatal("invalid me response")
		}
		return response.StatusCode, body
	}
	status, body = getMe(legacy.Token)
	if status != 200 || body["data"].(map[string]any)["id"] != id {
		t.Fatal("Go rejected Node JWT")
	}
	expired, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{ID: id, Username: input.Username, RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Minute))}}).SignedString(s.Secret)
	wrongAlgorithm, _ := jwt.NewWithClaims(jwt.SigningMethodHS384, Claims{ID: id, Username: input.Username, RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}).SignedString(s.Secret)
	for _, token := range []string{"", "forged", expired, wrongAlgorithm} {
		status, body = getMe(token)
		if status != 401 || body["code"] != float64(1001) {
			t.Fatal("invalid token accepted")
		}
	}
	if _, err = s.Pool.Exec(context.Background(), `DELETE FROM "user" WHERE id=$1::uuid`, id); err != nil {
		t.Fatal(err)
	}
	status, _ = getMe(legacy.Token)
	if status != 404 {
		t.Fatal("deleted user remained accessible")
	}
}
