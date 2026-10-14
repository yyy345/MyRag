package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"ai-library/backend/internal/ports"
	"github.com/jackc/pgx/v5"
)

func TestEntryTaskTransactionIntegration(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("requires isolated PostgreSQL")
	}
	ctx := context.Background()
	const endpoint = "postgresql://postgres:migration-test-only@127.0.0.1:55432/"
	admin, err := Open(ctx, endpoint+"postgres")
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	dbName := fmt.Sprintf("migration_outbox_test_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{dbName}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{dbName}.Sanitize()+" WITH (FORCE)")
	pool, err := Open(ctx, endpoint+dbName)
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
	id := func() string {
		t.Helper()
		var value string
		if err := pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	user, kb := id(), id()
	name := fmt.Sprintf("outbox_%d", time.Now().UnixNano())
	_, err = pool.Exec(ctx, `INSERT INTO "user"(id,username,email,password_hash,updated_at) VALUES($1::uuid,$2,$3,'unused',now())`, user, name, name+"@example.test")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, `DELETE FROM "user" WHERE id=$1::uuid`, user)
	_, err = pool.Exec(ctx, `INSERT INTO knowledge_base(id,user_id,name,updated_at) VALUES($1::uuid,$2::uuid,'outbox test',now())`, kb, user)
	if err != nil {
		t.Fatal(err)
	}
	repo := &Entries{Pool: pool}
	scope := ports.Scope{UserID: user, KnowledgeBaseID: kb}
	entry := ports.Entry{ID: id(), Title: "first", Content: "local text", Version: 0}
	task := ports.IndexTask{SchemaVersion: 1, TaskID: id(), EntryID: entry.ID, KBID: kb, EntryVersion: 1, TraceID: "local-c09"}
	if err = repo.SaveEntryAndTask(ctx, scope, entry, task); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	var published bool
	if err = pool.QueryRow(ctx, `SELECT payload,published_at IS NOT NULL FROM index_outbox WHERE task_id=$1::uuid`, task.TaskID).Scan(&raw, &published); err != nil {
		t.Fatal(err)
	}
	var event ports.IndexTask
	if json.Unmarshal(raw, &event) != nil || event != task || published {
		t.Fatal("outbox event mismatch")
	}
	// Task uniqueness failure occurs after the entry UPDATE and must roll it back.
	entry.Version = 1
	entry.Content = "must roll back"
	task.EntryVersion = 2
	if err = repo.SaveEntryAndTask(ctx, scope, entry, task); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate task accepted", err)
	}
	got, err := repo.GetEntry(ctx, scope, entry.ID)
	if err != nil || got.Version != 1 || got.Content != "local text" {
		t.Fatal("entry update escaped failed transaction", got, err)
	}
	// Inject a scoped database failure at the final outbox write to verify the
	// preceding entry INSERT and task INSERT both disappear on rollback.
	blocked := id()
	constraint := fmt.Sprintf("test_outbox_%d", time.Now().UnixNano())
	_, err = pool.Exec(ctx, `ALTER TABLE index_outbox ADD CONSTRAINT `+constraint+` CHECK(task_id <> '`+blocked+`'::uuid)`)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, `ALTER TABLE index_outbox DROP CONSTRAINT IF EXISTS `+constraint)
	failedEntry := ports.Entry{ID: id(), Title: "rollback", Content: "local"}
	failedTask := ports.IndexTask{SchemaVersion: 1, TaskID: blocked, EntryID: failedEntry.ID, KBID: kb, EntryVersion: 1, TraceID: "rollback"}
	if err = repo.SaveEntryAndTask(ctx, scope, failedEntry, failedTask); err == nil {
		t.Fatal("outbox failure ignored")
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM knowledge_entry WHERE id=$1::uuid)+(SELECT count(*) FROM index_task WHERE id=$2::uuid)`, failedEntry.ID, blocked).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial commit", count, err)
	}
	_, err = pool.Exec(ctx, `ALTER TABLE index_outbox DROP CONSTRAINT `+constraint)
	if err != nil {
		t.Fatal(err)
	}
	// Two writers holding version 1 race; exactly one can create version 2.
	tasks := []ports.IndexTask{task, task}
	tasks[0].TaskID = id()
	tasks[1].TaskID = id()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, candidate := range tasks {
		wg.Add(1)
		go func(v ports.IndexTask) { defer wg.Done(); results <- repo.SaveEntryAndTask(ctx, scope, entry, v) }(candidate)
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatal("version race", success, conflicts)
	}
	got, err = repo.GetEntry(ctx, scope, entry.ID)
	if err != nil || got.Version != 2 {
		t.Fatal("version not advanced", got, err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM index_task WHERE entry_id=$1::uuid`, entry.ID).Scan(&count); err != nil || count != 2 {
		t.Fatal("duplicate version tasks", count, err)
	}
	alien := ports.Scope{UserID: id(), KnowledgeBaseID: kb}
	if err = repo.Authorize(ctx, alien); !errors.Is(err, ErrNotFound) {
		t.Fatal("unauthorized scope", err)
	}
	if _, err = repo.GetEntry(ctx, alien, entry.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("unauthorized read", err)
	}
	entry.Version = 2
	task.EntryVersion = 3
	task.TaskID = id()
	if err = repo.SaveEntryAndTask(ctx, alien, entry, task); !errors.Is(err, ErrNotFound) {
		t.Fatal("unauthorized write", err)
	}
	task.EntryVersion = 1
	if err = repo.SaveEntryAndTask(ctx, scope, entry, task); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid event version", err)
	}
	// Failed delivery remains pending, stores only a safe error code and backs off.
	failedPublisher := publishFunc(func(context.Context, ports.IndexTask) error { return errors.New("secret broker URL") })
	if worked, err := repo.DispatchOne(ctx, failedPublisher); !worked || err == nil {
		t.Fatal("failed dispatch not reported")
	}
	var safe bool
	err = pool.QueryRow(ctx, `SELECT count(*)=1 FROM index_outbox WHERE attempts=1 AND published_at IS NULL AND available_at>now() AND last_error='publish_unconfirmed'`).Scan(&safe)
	if err != nil || !safe {
		t.Fatal("retry bookkeeping", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE index_outbox SET available_at=now()`); err != nil {
		t.Fatal(err)
	}
	// Concurrent dispatchers claim disjoint rows while waiting for confirmation.
	entered := make(chan string, 2)
	release := make(chan struct{})
	finished := make(chan error, 2)
	publisher := publishFunc(func(_ context.Context, v ports.IndexTask) error { entered <- v.TaskID; <-release; return nil })
	for range 2 {
		go func() {
			worked, err := repo.DispatchOne(ctx, publisher)
			if !worked && err == nil {
				err = errors.New("no task claimed")
			}
			finished <- err
		}()
	}
	var claimed []string
	for range 2 {
		select {
		case value := <-entered:
			claimed = append(claimed, value)
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("dispatch lock blocked unrelated task")
		}
	}
	close(release)
	for range 2 {
		if err := <-finished; err != nil {
			t.Fatal(err)
		}
	}
	if claimed[0] == claimed[1] {
		t.Fatal("duplicate concurrent claim")
	}
	if worked, err := repo.DispatchOne(ctx, failedPublisher); worked || err != nil {
		t.Fatal("confirmed task republished", err)
	}
	_, err = pool.Exec(ctx, `DELETE FROM knowledge_entry WHERE id=$1::uuid`, entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM index_outbox WHERE payload->>'entry_id'=$1`, entry.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("orphan outbox", count, err)
	}
	task.EntryVersion = 3
	if err = repo.SaveEntryAndTask(ctx, scope, entry, task); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted entry resurrected", err)
	}
}

type publishFunc func(context.Context, ports.IndexTask) error

func (f publishFunc) PublishConfirmed(ctx context.Context, v ports.IndexTask) error { return f(ctx, v) }
