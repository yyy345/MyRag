package scheduler

import (
	"ai-library/backend/internal/ports"
	"ai-library/backend/internal/repository"
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type extractionFunc func(context.Context, ports.ExtractRequest) ([]ports.KnowledgeSection, error)

func (f extractionFunc) Extract(ctx context.Context, req ports.ExtractRequest) ([]ports.KnowledgeSection, error) {
	return f(ctx, req)
}
func TestDueDayShanghaiBoundaries(t *testing.T) {
	for _, value := range []struct {
		raw, day string
		due      bool
	}{{"2026-10-08T20:29:59Z", "2026-10-09", false}, {"2026-10-08T20:30:00Z", "2026-10-09", true}, {"2026-10-09T15:59:59Z", "2026-10-09", true}, {"2026-10-09T16:00:00Z", "2026-10-10", false}} {
		now, _ := time.Parse(time.RFC3339, value.raw)
		day, due := DueDay(now)
		if day != value.day || due != value.due {
			t.Fatal(value, day, due)
		}
	}
}
func TestScheduledExtractionLeasesCheckpointRetryAndRollbackIntegration(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("requires isolated PostgreSQL")
	}
	ctx := context.Background()
	const endpoint = "postgresql://postgres:migration-test-only@127.0.0.1:55432/"
	admin, err := repository.Open(ctx, endpoint+"postgres")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	name := fmt.Sprintf("migration_schedule_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)") })
	pool, err := repository.Open(ctx, endpoint+name)
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
	store := &repository.Entries{Pool: pool}
	var user, kb string
	if err = pool.QueryRow(ctx, `INSERT INTO "user"(id,username,email,password_hash,updated_at) VALUES(gen_random_uuid(),'schedule','schedule@example.test','unused',now()) RETURNING id::text`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO knowledge_base(id,user_id,name,updated_at) VALUES(gen_random_uuid(),$1::uuid,'schedule',now()) RETURNING id::text`, user).Scan(&kb); err != nil {
		t.Fatal(err)
	}
	scope := ports.Scope{UserID: user, KnowledgeBaseID: kb}
	session, err := store.CreateSession(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	now, _ := time.Parse(time.RFC3339, "2026-10-08T20:30:00Z")
	add := func(at time.Time) {
		t.Helper()
		for i := range 2 {
			if _, err := pool.Exec(ctx, `INSERT INTO chat_message(id,session_id,role,content,created_at) VALUES(gen_random_uuid(),$1::uuid,'USER',$2,$3)`, session.ID, fmt.Sprintf("question-%d", i), at.Add(time.Duration(i)*time.Millisecond)); err != nil {
				t.Fatal(err)
			}
		}
	}
	add(now.Add(-time.Hour))
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	extractor := extractionFunc(func(ctx context.Context, req ports.ExtractRequest) ([]ports.KnowledgeSection, error) {
		calls.Add(1)
		if req.Mode != "scheduled" || len(req.History) != 2 {
			t.Error("history/mode", req)
		}
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return []ports.KnowledgeSection{{Title: "知识", Content: strings.Repeat("知识", 120)}}, nil
	})
	runner := &Runner{Store: store, Extractor: extractor}
	if err = runner.RunDue(ctx, now.Add(-time.Second)); err != nil || calls.Load() != 0 {
		t.Fatal("before 04:30", err)
	}
	finished := make(chan error, 1)
	go func() { finished <- runner.RunDue(ctx, now) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("not started")
	}
	if err = runner.RunDue(ctx, now); err != nil || calls.Load() != 1 {
		t.Fatal("multi-instance lease", err, calls.Load())
	}
	close(release)
	if err = <-finished; err != nil {
		t.Fatal(err)
	}
	if err = runner.RunDue(ctx, now.Add(time.Hour)); err != nil || calls.Load() != 1 {
		t.Fatal("same day repeated", err)
	}
	var entries, tasks int
	var checkpoint time.Time
	if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM knowledge_entry),(SELECT count(*) FROM index_outbox),(SELECT last_message_at FROM extract_checkpoint WHERE kb_id=$1::uuid)`, kb).Scan(&entries, &tasks, &checkpoint); err != nil || entries != 1 || tasks != 1 || !checkpoint.Equal(now.Add(-time.Millisecond)) {
		t.Fatal(entries, tasks, checkpoint, err)
	}
	next := now.Add(24 * time.Hour)
	add(next.Add(-time.Hour))
	runner.Extractor = extractionFunc(func(ctx context.Context, req ports.ExtractRequest) ([]ports.KnowledgeSection, error) {
		calls.Add(1)
		return nil, errors.New("private mock error")
	})
	if err = runner.RunDue(ctx, next); err == nil {
		t.Fatal("failure not reported")
	}
	var code string
	var attempts int
	if err = pool.QueryRow(ctx, `SELECT last_message_at,last_error,attempts FROM extract_checkpoint WHERE kb_id=$1::uuid`, kb).Scan(&checkpoint, &code, &attempts); err != nil || !checkpoint.Equal(now.Add(-time.Millisecond)) || code != "extraction_failed" || attempts != 1 {
		t.Fatal("failure checkpoint", checkpoint, code, attempts, err)
	}
	before := calls.Load()
	if err = runner.RunDue(ctx, next.Add(4*time.Minute)); err != nil || calls.Load() != before {
		t.Fatal("retry backoff", err)
	}
	runner.Extractor = extractionFunc(func(ctx context.Context, req ports.ExtractRequest) ([]ports.KnowledgeSection, error) {
		calls.Add(1)
		return []ports.KnowledgeSection{{Title: "same hash different title", Content: strings.Repeat("知识", 120)}, {Title: "知识", Content: strings.Repeat("其他", 120)}}, nil
	})
	if err = runner.RunDue(ctx, next.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM knowledge_entry`).Scan(&entries); err != nil || entries != 1 {
		t.Fatal("title/hash dedup", entries, err)
	}
	// Expired owner cannot commit after another instance has claimed the lease.
	later := next.Add(24 * time.Hour)
	old, claimed, err := store.ClaimExtraction(ctx, scope, later, "2026-10-11")
	if err != nil || !claimed {
		t.Fatal(old, claimed, err)
	}
	fresh, claimed, err := store.ClaimExtraction(ctx, scope, later.Add(11*time.Minute), "2026-10-11")
	if err != nil || !claimed {
		t.Fatal(fresh, claimed, err)
	}
	if _, err = store.SaveExtracted(ctx, scope, []ports.KnowledgeSection{{Title: "stale", Content: strings.Repeat("new", 100)}}, &repository.ExtractionCheckpoint{Until: later, Day: "2026-10-11", Token: old.Token}); !errors.Is(err, repository.ErrConflict) {
		t.Fatal("stale lease committed", err)
	}
	// Failure in outbox insertion rolls back both entry and checkpoint advancement.
	if _, err = pool.Exec(ctx, `CREATE FUNCTION reject_extracted_task() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'local fault'; END; $$; CREATE TRIGGER reject_extracted_task BEFORE INSERT ON index_outbox FOR EACH ROW EXECUTE FUNCTION reject_extracted_task()`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SaveExtracted(ctx, scope, []ports.KnowledgeSection{{Title: "new", Content: strings.Repeat("新", 240)}}, &repository.ExtractionCheckpoint{Until: later, Day: "2026-10-11", Token: fresh.Token}); err == nil {
		t.Fatal("fault accepted")
	}
	var token string
	if err = pool.QueryRow(ctx, `SELECT lease_token::text FROM extract_checkpoint WHERE kb_id=$1::uuid`, kb).Scan(&token); err != nil || token != fresh.Token {
		t.Fatal("checkpoint advanced after SQL failure", err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM knowledge_entry`).Scan(&entries); err != nil || entries != 1 {
		t.Fatal("entry not rolled back", entries, err)
	}
	if _, claimed, err = store.ClaimExtraction(ctx, ports.Scope{UserID: "11111111-1111-4111-8111-111111111111", KnowledgeBaseID: kb}, later, "2026-10-11"); err != nil || claimed {
		t.Fatal("cross-owner lease", claimed, err)
	}
}
