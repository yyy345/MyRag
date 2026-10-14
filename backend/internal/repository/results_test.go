package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-library/backend/internal/cache"
	"ai-library/backend/internal/ports"
)

func TestResultStateIntegration(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" || os.Getenv("RUN_REDIS_TESTS") != "1" {
		t.Skip("requires isolated PostgreSQL/Redis")
	}
	ctx := context.Background()
	pool, err := Open(ctx, "postgresql://postgres:migration-test-only@127.0.0.1:55432/migration_test")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	migrations, err := ReadMigrations("../../../server/prisma/migrations")
	if err != nil {
		t.Fatal(err)
	}
	if err = Apply(ctx, pool, migrations); err != nil {
		t.Fatal(err)
	}
	store, err := cache.New("redis://127.0.0.1:56380/0")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	id := func() string {
		t.Helper()
		var value string
		if err := pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	user, kb := id(), id()
	name := fmt.Sprintf("results_%d", time.Now().UnixNano())
	_, err = pool.Exec(ctx, `INSERT INTO "user"(id,username,email,password_hash,updated_at) VALUES($1::uuid,$2,$3,'unused',now())`, user, name, name+"@example.test")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, `DELETE FROM "user" WHERE id=$1::uuid`, user)
	_, err = pool.Exec(ctx, `INSERT INTO knowledge_base(id,user_id,name,updated_at) VALUES($1::uuid,$2::uuid,'results',now())`, kb, user)
	if err != nil {
		t.Fatal(err)
	}
	repo := &Entries{Pool: pool}
	scope := ports.Scope{UserID: user, KnowledgeBaseID: kb}
	entry := ports.Entry{ID: id(), Title: "local", Content: "local"}
	task := ports.IndexTask{SchemaVersion: 1, TaskID: id(), EntryID: entry.ID, KBID: kb, EntryVersion: 1, TraceID: "result-test"}
	if err = repo.SaveEntryAndTask(ctx, scope, entry, task); err != nil {
		t.Fatal(err)
	}
	v := ports.IndexResult{SchemaVersion: 1, TaskID: task.TaskID, EntryID: entry.ID, EntryVersion: 1, TraceID: task.TraceID, Status: "COMPLETED"}
	keys := []string{"go:entry:" + kb + ":list", "entry:" + kb + ":1", "entry:detail:" + entry.ID, "go:entry:detail:" + entry.ID, "analytics:overview:" + user, "analytics:kb:" + kb, "go:analytics:overview:" + user, "go:analytics:kb:" + kb}
	unrelated := "go:entry:" + id() + ":list"
	defer store.Delete(ctx, unrelated)
	warm := func() {
		t.Helper()
		for _, key := range append(keys, unrelated) {
			if err := store.Set(ctx, key, []byte(`{"status":"PENDING"}`), time.Minute); err != nil {
				t.Fatal(err)
			}
		}
	}
	state := func(want string) {
		t.Helper()
		var status string
		if err := pool.QueryRow(ctx, `SELECT processing_status::text FROM knowledge_entry WHERE id=$1::uuid`, entry.ID).Scan(&status); err != nil || status != want {
			t.Fatal("unexpected status", status, err)
		}
	}
	invalid := v
	invalid.TraceID = "wrong"
	if err = repo.ApplyResult(ctx, invalid, store); !errors.Is(err, ports.ErrInvalidResult) {
		t.Fatal("trace mismatch accepted", err)
	}
	invalid = v
	invalid.TaskID = id()
	if err = repo.ApplyResult(ctx, invalid, store); !errors.Is(err, ports.ErrInvalidResult) {
		t.Fatal("unknown task accepted", err)
	}
	invalid = v
	invalid.ChunkCount = 1
	if err = repo.ApplyResult(ctx, invalid, store); !errors.Is(err, ports.ErrInvalidResult) {
		t.Fatal("missing chunks accepted", err)
	}
	state("PENDING")
	// Force the task write to fail after the entry write: both must roll back.
	constraint := fmt.Sprintf("result_rollback_%d", time.Now().UnixNano())
	_, err = pool.Exec(ctx, `ALTER TABLE index_task ADD CONSTRAINT `+constraint+` CHECK (id <> '`+task.TaskID+`'::uuid OR status <> 'COMPLETED')`)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, `ALTER TABLE index_task DROP CONSTRAINT IF EXISTS `+constraint)
	if err = repo.ApplyResult(ctx, v, store); err == nil {
		t.Fatal("task write failure ignored")
	}
	state("PENDING")
	if _, err = pool.Exec(ctx, `ALTER TABLE index_task DROP CONSTRAINT `+constraint); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE index_task SET status='PROCESSING' WHERE id=$1::uuid`, task.TaskID); err != nil {
		t.Fatal(err)
	}
	warm()
	// Simulate cache outage AFTER database commit, then retry the same result.
	broken, err := cache.New("redis://127.0.0.1:56380/0")
	if err != nil {
		t.Fatal(err)
	}
	broken.Close()
	if err = repo.ApplyResult(ctx, v, broken); !errors.Is(err, cache.ErrUnavailable) {
		t.Fatal("cache failure swallowed", err)
	}
	state("COMPLETED")
	if err = repo.ApplyResult(ctx, v, store); err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		if _, err = store.Get(ctx, key); !errors.Is(err, cache.ErrMiss) {
			t.Fatal("stale cache survived", key, err)
		}
	}
	if _, err = store.Get(ctx, unrelated); err != nil {
		t.Fatal("unrelated cache removed", err)
	}
	var stamp time.Time
	if err = pool.QueryRow(ctx, `SELECT updated_at FROM knowledge_entry WHERE id=$1::uuid`, entry.ID).Scan(&stamp); err != nil {
		t.Fatal(err)
	}
	if err = repo.ApplyResult(ctx, v, store); err != nil {
		t.Fatal(err)
	}
	var stampAfter time.Time
	_ = pool.QueryRow(ctx, `SELECT updated_at FROM knowledge_entry WHERE id=$1::uuid`, entry.ID).Scan(&stampAfter)
	if !stamp.Equal(stampAfter) {
		t.Fatal("duplicate rewrote state")
	}
	var group sync.WaitGroup
	failures := make(chan error, 8)
	for range 8 {
		group.Add(1)
		go func() { defer group.Done(); failures <- repo.ApplyResult(ctx, v, store) }()
	}
	group.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal("concurrent duplicate", err)
		}
	}
	errorCode := "provider secret must not leak"
	invalid = v
	invalid.Status = "FAILED"
	invalid.ErrorCode = &errorCode
	if err = repo.ApplyResult(ctx, invalid, store); !errors.Is(err, ports.ErrInvalidResult) {
		t.Fatal("terminal state overwritten", err)
	}
	// New edit resets PENDING. Old completion must not finish the new version.
	entry.Version = 1
	task.EntryVersion = 2
	task.TaskID = id()
	if err = repo.SaveEntryAndTask(ctx, scope, entry, task); err != nil {
		t.Fatal(err)
	}
	if err = repo.ApplyResult(ctx, v, store); err != nil {
		t.Fatal(err)
	}
	state("PENDING")
	next := v
	next.TaskID = task.TaskID
	next.EntryVersion = 2
	next.Status = "STALE"
	if err = repo.ApplyResult(ctx, next, store); !errors.Is(err, ports.ErrInvalidResult) {
		t.Fatal("current version stale accepted", err)
	}
	next.Status = "FAILED"
	next.ErrorCode = &errorCode
	if err = repo.ApplyResult(ctx, next, store); err != nil {
		t.Fatal(err)
	}
	state("FAILED")
	var message string
	_ = pool.QueryRow(ctx, `SELECT processing_message FROM knowledge_entry WHERE id=$1::uuid`, entry.ID).Scan(&message)
	if message == "" || strings.Contains(message, "secret") {
		t.Fatal("unsafe failure message")
	}
	// A pending obsolete version is marked STALE without touching version 4.
	entry.Version = 2
	task.EntryVersion = 3
	task.TaskID = id()
	if err = repo.SaveEntryAndTask(ctx, scope, entry, task); err != nil {
		t.Fatal(err)
	}
	obsolete := v
	obsolete.TaskID = task.TaskID
	obsolete.EntryVersion = 3
	entry.Version = 3
	task.EntryVersion = 4
	task.TaskID = id()
	if err = repo.SaveEntryAndTask(ctx, scope, entry, task); err != nil {
		t.Fatal(err)
	}
	if err = repo.ApplyResult(ctx, obsolete, store); err != nil {
		t.Fatal(err)
	}
	state("PENDING")
	var taskStatus string
	_ = pool.QueryRow(ctx, `SELECT status FROM index_task WHERE id=$1::uuid`, obsolete.TaskID).Scan(&taskStatus)
	if taskStatus != "STALE" {
		t.Fatal("obsolete task not stale")
	}
	if _, err = pool.Exec(ctx, `DELETE FROM knowledge_entry WHERE id=$1::uuid`, entry.ID); err != nil {
		t.Fatal(err)
	}
	if err = repo.ApplyResult(ctx, obsolete, store); err != nil {
		t.Fatal("deleted result should be discarded", err)
	}
}
