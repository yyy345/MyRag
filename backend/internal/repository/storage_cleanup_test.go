package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-library/backend/internal/ports"
	"github.com/jackc/pgx/v5"
)

type imageDeleteFunc func(context.Context, string, string) error

func (f imageDeleteFunc) DeleteImage(ctx context.Context, user, entry string) error {
	return f(ctx, user, entry)
}
func (imageDeleteFunc) PutImage(context.Context, string, string, []byte, string) (string, error) {
	panic("unexpected upload")
}

func TestStorageCleanupTransactionIntegration(t *testing.T) {
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
	name := fmt.Sprintf("migration_storage_test_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
	pool, err := Open(ctx, endpoint+name)
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
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	id := func() string {
		t.Helper()
		var v string
		if err := pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	user, kb, entry := id(), id(), id()
	exec(`INSERT INTO "user"(id,username,email,password_hash,updated_at) VALUES($1::uuid,'storage','storage@example.test','unused',now())`, user)
	exec(`INSERT INTO knowledge_base(id,user_id,name,updated_at) VALUES($1::uuid,$2::uuid,'storage',now())`, kb, user)
	scope := ports.Scope{UserID: user, KnowledgeBaseID: kb}
	repo := &Entries{Pool: pool}
	if err = repo.ReserveImage(ctx, scope, entry); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	success := imageDeleteFunc(func(_ context.Context, u, e string) error {
		calls.Add(1)
		if u != user || e != entry {
			t.Error("wrong scoped key")
		}
		return nil
	})
	if worked, err := repo.CleanupOne(ctx, success); worked || err != nil {
		t.Fatal("active reservation deleted", worked, err)
	}
	if err = repo.AbandonImage(ctx, user, entry); err != nil {
		t.Fatal(err)
	}
	bad := imageDeleteFunc(func(context.Context, string, string) error { return errors.New("private provider response") })
	if worked, err := repo.CleanupOne(ctx, bad); !worked || err == nil {
		t.Fatal("failed delete lost", worked, err)
	}
	var attempts int
	var message string
	var future bool
	if err = pool.QueryRow(ctx, `SELECT attempts,last_error,available_at>now() FROM storage_cleanup`).Scan(&attempts, &message, &future); err != nil || attempts != 1 || message != "storage_delete_failed" || !future {
		t.Fatal("retry state", err, attempts, message, future)
	}
	exec(`UPDATE storage_cleanup SET available_at=now()`)
	entered, release := make(chan struct{}), make(chan struct{})
	blocking := imageDeleteFunc(func(ctx context.Context, u, e string) error { close(entered); <-release; return success(ctx, u, e) })
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if worked, err := repo.CleanupOne(ctx, blocking); !worked || err != nil {
			t.Error(worked, err)
		}
	}()
	<-entered
	if worked, err := repo.CleanupOne(ctx, success); worked || err != nil {
		t.Fatal("concurrent cleaner did not skip locked", worked, err)
	}
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("duplicate cleanup", calls.Load())
	}
	if worked, err := repo.CleanupOne(ctx, success); worked || err != nil {
		t.Fatal("deleted job reappeared", worked, err)
	}
	// An already expired or processed reservation may never be attached.
	tx, _ := pool.Begin(ctx)
	if err = consumeImageReservation(ctx, tx, user, entry); !errors.Is(err, ErrInvalid) {
		t.Fatal("missing reservation consumed", err)
	}
	tx.Rollback(ctx)
	// Cleanup enqueue failure must roll back the business deletion too.
	exec(`INSERT INTO knowledge_entry(id,kb_id,title,content,type,updated_at) VALUES($1::uuid,$2::uuid,'image','description','FILE',now())`, entry, kb)
	exec(`ALTER TABLE storage_cleanup ADD CONSTRAINT local_block CHECK(entry_id<>'` + entry + `'::uuid)`)
	if err = repo.DeleteEntries(ctx, scope, []string{entry}); err == nil {
		t.Fatal("failed enqueue accepted")
	}
	var exists bool
	if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM knowledge_entry WHERE id=$1::uuid)`, entry).Scan(&exists); err != nil || !exists {
		t.Fatal("delete escaped failed enqueue", err)
	}
	exec(`ALTER TABLE storage_cleanup DROP CONSTRAINT local_block`)
	if err = repo.DeleteEntries(ctx, scope, []string{entry}); err != nil {
		t.Fatal(err)
	}
	if worked, err := repo.CleanupOne(ctx, success); !worked || err != nil {
		t.Fatal("single delete did not enqueue", worked, err)
	}
	entry = id()
	exec(`INSERT INTO knowledge_entry(id,kb_id,title,content,type,updated_at) VALUES($1::uuid,$2::uuid,'image','description','FILE',now())`, entry, kb)
	if err = repo.DeleteBase(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if worked, err := repo.CleanupOne(ctx, success); !worked || err != nil {
		t.Fatal("cascade lost cleanup", worked, err)
	}
}
