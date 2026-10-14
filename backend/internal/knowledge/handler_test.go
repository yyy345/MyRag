package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestKnowledgeIntegration(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" || os.Getenv("RUN_REDIS_TESTS") != "1" {
		t.Skip("requires isolated DB and Redis")
	}
	ctx := context.Background()
	pool, err := repository.Open(ctx, "postgresql://postgres:migration-test-only@127.0.0.1:55432/migration_test")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	migrations, err := repository.ReadMigrations("../../../server/prisma/migrations")
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
	defer store.Close()
	secret := []byte("knowledge-local-test-secret-at-least-32")
	h := &Handler{Pool: pool, Cache: store, Auth: &auth.Service{Secret: secret}}
	mux := http.NewServeMux()
	h.Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	var users, tokens [2]string
	for i := range users {
		name := fmt.Sprintf("go_kb_%d_%d", time.Now().UnixNano(), i)
		err = pool.QueryRow(ctx, `INSERT INTO "user" (id,username,email,password_hash,updated_at) VALUES (gen_random_uuid(),$1,$2,'unused',now()) RETURNING id::text`, name, name+"@example.test").Scan(&users[i])
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Exec(ctx, `DELETE FROM "user" WHERE id=$1::uuid`, users[i])
		defer store.DeletePrefix(ctx, prefix(users[i]))
		claims := auth.Claims{ID: users[i], Username: name, RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}
		tokens[i], err = jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
		if err != nil {
			t.Fatal(err)
		}
	}
	idempotencyKey := ""
	request := func(method, path, body, token string, want int) map[string]any {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+"/api/knowledge-bases"+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		if idempotencyKey != "" {
			req.Header.Set("Idempotency-Key", idempotencyKey)
		}
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var envelope map[string]any
		if err = json.NewDecoder(res.Body).Decode(&envelope); err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != want {
			t.Fatalf("%s %s: status %d want %d: %v", method, path, res.StatusCode, want, envelope)
		}
		data, _ := envelope["data"].(map[string]any)
		return data
	}
	request("GET", "", "", "", 401)
	base := request("POST", "", `{"name":"  知识库  ","description":"说明","system_prompt":"回答中文"}`, tokens[0], 200)
	if base["name"] != "知识库" || base["user_id"] != users[0] {
		t.Fatal(base)
	}
	id := base["id"].(string)
	path := "/" + id
	request("GET", path, "", tokens[0], 200) // Warm both cache paths before testing ownership.
	request("GET", "", "", tokens[0], 200)
	if _, err = store.Get(ctx, prefix(users[0])+"detail:"+id); err != nil {
		t.Fatal("detail cache not populated", err)
	}
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		request(method, path, `{"name":"stolen"}`, tokens[1], 404)
	}
	if other := request("GET", "", "", tokens[1], 200); other["total"] != float64(0) {
		t.Fatal("cross-user list leak")
	}
	changed := request("PUT", path, `{"name":"新名称"}`, tokens[0], 200)
	if changed["description"] != "说明" || changed["system_prompt"] != "回答中文" {
		t.Fatal("partial update lost fields")
	}
	if got := request("GET", path, "", tokens[0], 200); got["name"] != "新名称" {
		t.Fatal("stale detail cache")
	}
	list := request("GET", "", "", tokens[0], 200)
	if list["records"].([]any)[0].(map[string]any)["name"] != "新名称" {
		t.Fatal("stale list cache")
	}
	changed = request("PUT", path, `{"description":null,"system_prompt":null}`, tokens[0], 200)
	if changed["description"] != nil || changed["system_prompt"] != nil {
		t.Fatal("explicit null ignored")
	}
	request("POST", "", `{"name":"second"}`, tokens[0], 200)
	list = request("GET", "?page=2&size=1", "", tokens[0], 200)
	if list["total"] != float64(2) || len(list["records"].([]any)) != 1 || list["current"] != float64(2) {
		t.Fatal("pagination", list)
	}
	for _, query := range []string{"?page=0", "?size=101", "?page=bad"} {
		request("GET", query, "", tokens[0], 400)
	}
	for _, body := range []string{`{"name":" "}`, `{"name":123}`, `null`, `{} {}`} {
		request("POST", "", body, tokens[0], 400)
	}
	request("GET", "/invalid-id", "", tokens[0], 404)
	// C12: actual HTTP creates entry/task/outbox together, then reads full text.
	entryPath := path + "/entries"
	request("POST", entryPath, `{"title":"x","content":"local"}`, tokens[1], 404)
	request("POST", entryPath, `{"title":"x","content":"local"}`, "", 401)
	idempotencyKey = "bad"
	request("POST", entryPath, `{"title":"x","content":"local"}`, tokens[0], 400)
	idempotencyKey = ""
	for _, body := range []string{`{}`, `{"title":"x","content":" "}`, `{"title":12,"content":"x"}`, `{"title":"x","content":"\u0000"}`} {
		request("POST", entryPath, body, tokens[0], 400)
	}
	if err = pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&idempotencyKey); err != nil {
		t.Fatal(err)
	}
	input := `{"title":"  中文 Manual  ","content":"# 原文\n\nemoji 😀 100%_ <script>literal</script>"}`
	created := request("POST", entryPath, input, tokens[0], 200)
	createdID := created["id"].(string)
	if createdID != idempotencyKey || created["title"] != "中文 Manual" || created["processing_status"] != "PENDING" || created["source_file_name"] != nil || len(created) != 12 {
		t.Fatal("entry response contract", created)
	}
	repeated := request("POST", entryPath, input, tokens[0], 200)
	if repeated["id"] != createdID {
		t.Fatal("retry created another entry")
	}
	request("POST", entryPath, `{"title":"different","content":"different"}`, tokens[0], 409)
	idempotencyKey = ""
	var tasks, outbox int
	err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM index_task WHERE entry_id=$1::uuid),(SELECT count(*) FROM index_outbox WHERE payload->>'entry_id'=$1::text)`, createdID).Scan(&tasks, &outbox)
	if err != nil || tasks != 1 || outbox != 1 {
		t.Fatal("entry/task/outbox not atomic or deduplicated", tasks, outbox, err)
	}
	preview := request("GET", entryPath+"/"+createdID, "", tokens[0], 200)
	if preview["content"] != created["content"] {
		t.Fatal("preview changed markdown")
	}
	// A planted old cached response must not bypass owner or nested KB scope.
	if err = store.Set(ctx, "entry:detail:"+createdID, []byte(`{"content":"stale"}`), time.Minute); err != nil {
		t.Fatal(err)
	}
	defer store.Delete(ctx, "entry:detail:"+createdID)
	request("GET", entryPath+"/"+createdID, "", tokens[1], 404)
	request("GET", entryPath, "", tokens[1], 404)
	otherKB := request("POST", "", `{"name":"other scope"}`, tokens[0], 200)["id"].(string)
	request("GET", "/"+otherKB+"/entries/"+createdID, "", tokens[0], 404)
	request("DELETE", "/"+otherKB, "", tokens[0], 200)
	request("GET", entryPath+"/invalid", "", tokens[0], 404)
	for _, query := range []string{"?page=0", "?size=101", "?status=UNKNOWN"} {
		request("GET", entryPath+query, "", tokens[0], 400)
	}
	for _, search := range []string{"manual", "emoji", "100%_"} {
		got := request("GET", entryPath+"?search="+url.QueryEscape(search), "", tokens[0], 200)
		if got["total"] != float64(1) {
			t.Fatal("search failed", search, got)
		}
	}
	if got := request("GET", entryPath+"?search=missing", "", tokens[0], 200); len(got["records"].([]any)) != 0 {
		t.Fatal("empty list contract")
	}
	secondEntry := request("POST", entryPath, `{"title":"disabled","content":"second"}`, tokens[0], 200)["id"].(string)
	_, err = pool.Exec(ctx, `UPDATE knowledge_entry SET status='DISABLED',processing_status='FAILED',processing_message='local failure' WHERE id=$1::uuid`, secondEntry)
	if err != nil {
		t.Fatal(err)
	}
	filtered := request("GET", entryPath+"?status=DISABLED", "", tokens[0], 200)
	if filtered["total"] != float64(1) || filtered["records"].([]any)[0].(map[string]any)["processing_status"] != "FAILED" {
		t.Fatal("filter/status polling", filtered)
	}
	if got := request("GET", entryPath+"?page=2&size=1", "", tokens[0], 200); got["total"] != float64(2) || len(got["records"].([]any)) != 1 {
		t.Fatal("entry pagination", got)
	}
	// Failure at final outbox INSERT must leave no visible entry or task.
	constraint := fmt.Sprintf("http_entry_rollback_%d", time.Now().UnixNano())
	_, err = pool.Exec(ctx, `ALTER TABLE index_outbox ADD CONSTRAINT `+constraint+` CHECK (payload->>'kb_id' <> '`+id+`') NOT VALID`)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, `ALTER TABLE index_outbox DROP CONSTRAINT IF EXISTS `+constraint)
	request("POST", entryPath, `{"title":"must rollback","content":"local"}`, tokens[0], 500)
	request("PUT", entryPath+"/"+createdID, `{"content":"must rollback"}`, tokens[0], 500)
	if got := request("GET", entryPath+"/"+createdID, "", tokens[0], 200); got["content"] != created["content"] {
		t.Fatal("failed edit escaped rollback")
	}
	if _, err = pool.Exec(ctx, `ALTER TABLE index_outbox DROP CONSTRAINT `+constraint); err != nil {
		t.Fatal(err)
	}
	if got := request("GET", entryPath, "", tokens[0], 200); got["total"] != float64(2) {
		t.Fatal("failed create persisted")
	}
	// C13: metadata edits preserve index version; body changes queue exactly once.
	detailPath := entryPath + "/" + createdID
	request("PUT", detailPath, `{"title":"rename"}`, tokens[1], 404)
	request("PATCH", detailPath+"/status", `{"status":"DISABLED"}`, tokens[1], 404)
	for _, body := range []string{`{}`, `{"title":null}`, `{"content":" "}`, `{"status":"DISABLED"}`, `{"entry_version":99}`, `{"expected_version":null}`} {
		request("PUT", detailPath, body, tokens[0], 400)
	}
	request("PATCH", detailPath+"/status", `{"status":"INVALID"}`, tokens[0], 400)
	_, err = pool.Exec(ctx, `UPDATE knowledge_entry SET processing_status='COMPLETED' WHERE id=$1::uuid`, createdID)
	if err != nil {
		t.Fatal(err)
	}
	changedEntry := request("PUT", detailPath, `{"title":"renamed"}`, tokens[0], 200)
	if changedEntry["content"] != created["content"] || changedEntry["processing_status"] != "COMPLETED" {
		t.Fatal("rename changed indexing")
	}
	changedEntry = request("PATCH", detailPath+"/status", `{"status":"DISABLED"}`, tokens[0], 200)
	if changedEntry["status"] != "DISABLED" || changedEntry["processing_status"] != "COMPLETED" {
		t.Fatal("status changed indexing")
	}
	var version int64
	checkVersion := func(want int64, wantTasks int) {
		t.Helper()
		err := pool.QueryRow(ctx, `SELECT entry_version,(SELECT count(*) FROM index_task WHERE entry_id=$1::uuid) FROM knowledge_entry WHERE id=$1::uuid`, createdID).Scan(&version, &tasks)
		if err != nil || version != want || tasks != wantTasks {
			t.Fatal("version/tasks", version, tasks, err)
		}
	}
	checkVersion(1, 1)
	changedEntry = request("PUT", detailPath, `{"content":"new text","expected_version":1}`, tokens[0], 200)
	if changedEntry["status"] != "DISABLED" || changedEntry["title"] != "renamed" || changedEntry["processing_status"] != "PENDING" {
		t.Fatal("partial body edit", changedEntry)
	}
	checkVersion(2, 2)
	request("PUT", detailPath, `{"content":"new text"}`, tokens[0], 200)
	checkVersion(2, 2)
	request("PUT", detailPath, `{"content":"stale text","expected_version":1}`, tokens[0], 409)
	// Both callers expect version 2: only one may create version 3.
	repo := &repository.Entries{Pool: pool}
	scope := ports.Scope{UserID: users[0], KnowledgeBaseID: id}
	var group sync.WaitGroup
	results := make(chan error, 2)
	expected := int64(2)
	for _, value := range []string{"writer A", "writer B"} {
		group.Add(1)
		go func(content string) {
			defer group.Done()
			results <- repo.EditEntry(ctx, scope, createdID, repository.EntryPatch{Content: &content, ExpectedVersion: &expected})
		}(value)
	}
	group.Wait()
	close(results)
	wins, conflicts := 0, 0
	for err := range results {
		if err == nil {
			wins++
		} else if errors.Is(err, repository.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatal("concurrent edit lost update", wins, conflicts)
	}
	checkVersion(3, 3)
	_, err = pool.Exec(ctx, `UPDATE knowledge_entry SET type='FILE',source_file_name='original.md' WHERE id=$1::uuid`, secondEntry)
	if err != nil {
		t.Fatal(err)
	}
	request("PUT", entryPath+"/"+secondEntry, `{"title":"missing suffix"}`, tokens[0], 400)
	request("PUT", entryPath+"/"+secondEntry, `{"title":".md"}`, tokens[0], 400)
	request("PUT", entryPath+"/"+secondEntry, `{"title":"renamed.md"}`, tokens[0], 200)
	request("PATCH", detailPath+"/status", `{"status":"ENABLED"}`, tokens[0], 200)
	checkVersion(3, 3)
	// C14: batch scope checks are atomic, including mixed owner/KB IDs.
	foreignKB := request("POST", "", `{"name":"foreign"}`, tokens[1], 200)["id"].(string)
	foreignEntry := request("POST", "/"+foreignKB+"/entries", `{"title":"foreign","content":"local"}`, tokens[1], 200)["id"].(string)
	batch := func(ids ...string) string { body, _ := json.Marshal(map[string]any{"ids": ids}); return string(body) }
	request("DELETE", entryPath, batch(createdID, foreignEntry), tokens[0], 404)
	request("GET", detailPath, "", tokens[0], 200)
	request("GET", "/"+foreignKB+"/entries/"+foreignEntry, "", tokens[1], 200)
	request("DELETE", detailPath, "", tokens[1], 404)
	request("DELETE", "/"+foreignKB+"/entries/"+createdID, "", tokens[1], 404)
	for _, body := range []string{`{"ids":[]}`, `{"ids":null}`, `{"ids":["invalid"]}`, `{"ids":[null]}`, `{"ids":[],"extra":true}`} {
		request("DELETE", entryPath, body, tokens[0], 400)
	}
	tooMany := make([]string, 101)
	for i := range tooMany {
		tooMany[i] = createdID
	}
	request("DELETE", entryPath, batch(tooMany...), tokens[0], 400)
	var missing string
	if err = pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&missing); err != nil {
		t.Fatal(err)
	}
	request("DELETE", entryPath, batch(createdID, missing), tokens[0], 404)
	request("GET", detailPath, "", tokens[0], 200)
	// Real vector rows use fixed local data, never an embedding provider.
	_, err = pool.Exec(ctx, `INSERT INTO knowledge_chunk(id,entry_id,chunk_index,content,embedding) VALUES(gen_random_uuid(),$1::uuid,0,'local',array_fill(0.01::real,ARRAY[1024])::vector)`, createdID)
	if err != nil {
		t.Fatal(err)
	}
	cacheKeys := []string{"go:entry:" + id + ":list", "entry:detail:" + createdID, "go:entry:detail:" + secondEntry, "analytics:overview:" + users[0]}
	for _, key := range cacheKeys {
		if err = store.Set(ctx, key, []byte(`"stale"`), time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	// Force a database failure during DELETE, after all authorization checks.
	guard := fmt.Sprintf("delete_guard_%d", time.Now().UnixNano())
	_, err = pool.Exec(ctx, `CREATE TABLE `+guard+` (entry_id uuid REFERENCES knowledge_entry(id))`)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, `DROP TABLE IF EXISTS `+guard)
	if _, err = pool.Exec(ctx, `INSERT INTO `+guard+` VALUES ($1::uuid)`, secondEntry); err != nil {
		t.Fatal(err)
	}
	request("DELETE", entryPath, batch(createdID, secondEntry), tokens[0], 500)
	request("GET", detailPath, "", tokens[0], 200)
	request("GET", entryPath+"/"+secondEntry, "", tokens[0], 200)
	if _, err = pool.Exec(ctx, `DROP TABLE `+guard); err != nil {
		t.Fatal(err)
	}
	request("DELETE", entryPath, batch(createdID, secondEntry, strings.ToUpper(createdID)), tokens[0], 200)
	request("GET", detailPath, "", tokens[0], 404)
	request("DELETE", detailPath, "", tokens[0], 404)
	for _, key := range cacheKeys {
		if _, err = store.Get(ctx, key); !errors.Is(err, cache.ErrMiss) {
			t.Fatal("delete left cached data", key, err)
		}
	}
	var residual int
	err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM knowledge_chunk WHERE entry_id=$1::uuid)+(SELECT count(*) FROM index_task WHERE entry_id=$1::uuid)+(SELECT count(*) FROM index_outbox WHERE payload->>'entry_id'=$1::text)`, createdID).Scan(&residual)
	if err != nil || residual != 0 {
		t.Fatal("delete left dependent rows", residual, err)
	}
	request("DELETE", "/"+foreignKB+"/entries/"+foreignEntry, "", tokens[1], 200)
	request("DELETE", "/"+foreignKB, "", tokens[1], 200)
	// Edit, deletion, and an in-flight result all take the same entry lock.
	racing := request("POST", entryPath, `{"title":"race","content":"local"}`, tokens[0], 200)["id"].(string)
	result := ports.IndexResult{SchemaVersion: 1, EntryID: racing, EntryVersion: 1, Status: "COMPLETED"}
	if err = pool.QueryRow(ctx, `SELECT id::text,trace_id FROM index_task WHERE entry_id=$1::uuid`, racing).Scan(&result.TaskID, &result.TraceID); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	raceResults := make(chan error, 3)
	newContent := "changed during delete"
	for _, operation := range []func() error{
		func() error {
			err := repo.EditEntry(ctx, scope, racing, repository.EntryPatch{Content: &newContent})
			if errors.Is(err, repository.ErrNotFound) {
				return nil
			}
			return err
		},
		func() error { return repo.DeleteEntries(ctx, scope, []string{racing}) },
		func() error { return repo.ApplyResult(ctx, result, store) },
	} {
		group.Add(1)
		go func(op func() error) { defer group.Done(); <-start; raceResults <- op() }(operation)
	}
	close(start)
	group.Wait()
	close(raceResults)
	for err := range raceResults {
		if err != nil {
			t.Fatal("delete race", err)
		}
	}
	if err = repo.ApplyResult(ctx, result, store); err != nil {
		t.Fatal("late result", err)
	}
	request("GET", entryPath+"/"+racing, "", tokens[0], 404)
	err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM knowledge_entry WHERE id=$1::uuid)+(SELECT count(*) FROM index_task WHERE entry_id=$1::uuid)+(SELECT count(*) FROM index_outbox WHERE payload->>'entry_id'=$1::text)`, racing).Scan(&residual)
	if err != nil || residual != 0 {
		t.Fatal("racing task resurrected data", residual, err)
	}
	var entry, session string
	err = pool.QueryRow(ctx, `INSERT INTO knowledge_entry(id,kb_id,title,content,updated_at) VALUES(gen_random_uuid(),$1::uuid,'test','local',now()) RETURNING id::text`, id).Scan(&entry)
	if err != nil {
		t.Fatal(err)
	}
	err = pool.QueryRow(ctx, `INSERT INTO chat_session(id,kb_id,user_id,title,updated_at) VALUES(gen_random_uuid(),$1::uuid,$2::uuid,'test',now()) RETURNING id::text`, id, users[0]).Scan(&session)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO chat_message(id,session_id,role,content) VALUES(gen_random_uuid(),$1::uuid,'USER','local')`, session)
	if err != nil {
		t.Fatal(err)
	}
	request("DELETE", path, "", tokens[0], 200)
	request("GET", path, "", tokens[0], 404)
	request("DELETE", path, "", tokens[0], 404)
	var count int
	err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM knowledge_entry WHERE id=$1::uuid)+(SELECT count(*) FROM chat_session WHERE id=$2::uuid)+(SELECT count(*) FROM chat_message WHERE session_id=$2::uuid)`, entry, session).Scan(&count)
	if err != nil || count != 0 {
		t.Fatal("cascade failed", count, err)
	}
	list = request("GET", "", "", tokens[0], 200)
	if list["total"] != float64(1) {
		t.Fatal("delete did not invalidate list")
	}
	store.Close() // Business reads and writes must fall back to SQL when Redis fails.
	remaining := list["records"].([]any)[0].(map[string]any)["id"].(string)
	request("POST", "/"+remaining+"/entries", `{"title":"Redis offline","content":"local"}`, tokens[0], 200)
	request("GET", "/"+remaining+"/entries", "", tokens[0], 200)
	request("GET", "/"+remaining, "", tokens[0], 200)
	request("PUT", "/"+remaining, `{"name":"redis down"}`, tokens[0], 200)
	request("GET", "", "", tokens[0], 200)
	request("DELETE", "/"+remaining, "", tokens[0], 200)
}
