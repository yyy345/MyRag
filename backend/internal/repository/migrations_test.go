package repository

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigrationChecksumsAcceptLineEndingsOnly(t *testing.T) {
	m := Migration{SQL: "SELECT 1;\r\n"}
	m.Checksum = checksum(m.SQL)
	if !m.matches(checksum("SELECT 1;\n")) || m.matches(checksum("SELECT 2;\n")) {
		t.Fatal("checksum validation failed")
	}
	if _, err := ReadMigrations(t.TempDir()); err == nil {
		t.Fatal("empty directory accepted")
	}
}

func TestDatabaseMigrationIntegration(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 for isolated PostgreSQL tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	// Deliberately fixed isolated endpoint; never derive destructive targets from .env.
	const base = "postgresql://postgres:migration-test-only@127.0.0.1:55432/"
	admin, err := Open(ctx, base+"postgres")
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	migrations, err := ReadMigrations(filepath.Join(root, "server/prisma/migrations"))
	if err != nil {
		t.Fatal(err)
	}
	newDB := func(label string) (*pgxpool.Pool, string, string) {
		name := fmt.Sprintf("migration_go_test_%s_%d", label, time.Now().UnixNano())
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Fatal(err)
		}
		pool, err := Open(ctx, base+name)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			pool.Close()
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, err := pgx.Connect(cleanup, base+"postgres")
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close(cleanup)
			if _, err = conn.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
				t.Error(err)
			}
		})
		return pool, name, base + name
	}
	goDB, _, _ := newDB("empty")
	// Upgrade an actual pre-outbox database with an existing entry. The new
	// version defaults to 1, but migration must not enqueue paid indexing work.
	oldDB, _, _ := newDB("pre_outbox")
	var historical []Migration
	for _, migration := range migrations {
		if migration.Name == "20260928000000_index_tasks_outbox" {
			break
		}
		historical = append(historical, migration)
	}
	if err = Apply(ctx, oldDB, historical); err != nil {
		t.Fatal(err)
	}
	if err = CheckSchema(ctx, oldDB); err == nil || !strings.Contains(err.Error(), "knowledge_entry.entry_version") {
		t.Fatal("runtime accepted a pre-outbox database", err)
	}
	_, err = oldDB.Exec(ctx, `INSERT INTO "user"(id,username,email,password_hash,updated_at) VALUES ('11111111-1111-4111-8111-111111111111','old','old@example.test','hash',now());
INSERT INTO knowledge_base(id,user_id,name,updated_at) VALUES ('22222222-2222-4222-8222-222222222222','11111111-1111-4111-8111-111111111111','old',now());
INSERT INTO knowledge_entry(id,kb_id,title,content,updated_at) VALUES ('33333333-3333-4333-8333-333333333333','22222222-2222-4222-8222-222222222222','old','preserve text',now())`)
	if err != nil {
		t.Fatal(err)
	}
	if err = Apply(ctx, oldDB, migrations); err != nil {
		t.Fatal(err)
	}
	var preserved bool
	err = oldDB.QueryRow(ctx, `SELECT entry_version=1 AND content='preserve text' AND NOT EXISTS(SELECT 1 FROM index_outbox) FROM knowledge_entry WHERE id='33333333-3333-4333-8333-333333333333'`).Scan(&preserved)
	if err != nil || !preserved {
		t.Fatal("outbox upgrade changed existing content or queued work", err)
	}
	if err = Apply(ctx, goDB, migrations); err != nil {
		t.Fatal(err)
	}
	if err = Apply(ctx, goDB, migrations); err != nil {
		t.Fatalf("idempotence: %v", err)
	}
	if err = CheckSchema(ctx, goDB); err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct{ name, sql, want string }{
		{"task_table", "DROP TABLE index_task CASCADE", "index_task.id"},
		{"outbox_table", "DROP TABLE index_outbox", "index_outbox.task_id"},
		{"version_type", "ALTER TABLE knowledge_entry ALTER COLUMN entry_version TYPE integer", "knowledge_entry.entry_version"},
		{"payload_nullable", "ALTER TABLE index_outbox ALTER COLUMN payload DROP NOT NULL", "index_outbox.payload"},
		{"task_unique", "ALTER TABLE index_task DROP CONSTRAINT index_task_entry_id_entry_version_key", "index_task_entry_id_entry_version_key"},
		{"outbox_fk", "ALTER TABLE index_outbox DROP CONSTRAINT index_outbox_task_id_fkey", "index_outbox_task_id_fkey"},
		{"pending_index", "DROP INDEX index_outbox_pending_idx", "index_outbox_pending_idx"},
		{"cleanup_table", "DROP TABLE storage_cleanup", "storage_cleanup.user_id"},
		{"cleanup_index", "DROP INDEX storage_cleanup_pending_idx", "storage_cleanup_pending_idx"},
		{"receipt_table", "DROP TABLE index_receipt", "index_receipt.task_id"},
		{"receipt_fk", "ALTER TABLE index_receipt DROP CONSTRAINT index_receipt_task_id_fkey", "index_receipt_task_id_fkey"},
		{"chat_receipt", "DROP TABLE chat_receipt", "chat_receipt.request_id"},
		{"chat_receipt_fk", "ALTER TABLE chat_receipt DROP CONSTRAINT chat_receipt_session_id_fkey", "chat_receipt_session_id_fkey"},
		{"checkpoint", "DROP TABLE extract_checkpoint", "extract_checkpoint.kb_id"},
	} {
		t.Run("runtime_schema_"+change.name, func(t *testing.T) {
			tx, err := goDB.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, err = tx.Exec(ctx, change.sql); err != nil {
				t.Fatal(err)
			}
			if err = validateRuntimeSchema(ctx, tx); err == nil || !strings.Contains(err.Error(), change.want) {
				t.Fatal("runtime accepted incompatible indexing schema", err)
			}
		})
	}
	var count int
	if err = goDB.QueryRow(ctx, `SELECT count(*) FROM "_prisma_migrations"`).Scan(&count); err != nil || count != len(migrations) {
		t.Fatal("unexpected ledger count")
	}
	legacy, sourceName, legacyDSN := newDB("prisma")
	command := exec.CommandContext(ctx, "node", filepath.Join(root, "node_modules/prisma/build/index.js"), "migrate", "deploy", "--schema", filepath.Join(root, "server/prisma/schema.prisma"))
	command.Dir = root
	command.Env = append(os.Environ(), "DATABASE_URL="+legacyDSN)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Prisma baseline failed: %s", output)
	}
	if schemaSnapshot(t, ctx, goDB) != schemaSnapshot(t, ctx, legacy) {
		t.Fatal("Go initialization differs from Prisma schema")
	}
	_, err = legacy.Exec(ctx, `INSERT INTO "user" (id,username,email,password_hash,updated_at) VALUES ('11111111-1111-4111-8111-111111111111','restore-fixture','restore@example.test','local-only-hash',now())`)
	if err != nil {
		t.Fatal(err)
	}
	if err = Apply(ctx, legacy, migrations); err != nil {
		t.Fatalf("existing Prisma baseline: %v", err)
	}
	if err = legacy.QueryRow(ctx, `SELECT count(*) FROM "user" WHERE username='restore-fixture'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("existing data changed")
	}
	restored, restoreName, _ := newDB("restore")
	args := []string{"compose", "-f", filepath.Join(root, "compose.migration-test.yml"), "exec", "-T", "postgres"}
	dump := exec.CommandContext(ctx, "docker", append(append([]string{}, args...), "pg_dump", "-U", "postgres", "--no-owner", "--no-privileges", sourceName)...)
	backup, err := dump.Output()
	if err != nil {
		t.Fatal("isolated backup failed", err)
	}
	restore := exec.CommandContext(ctx, "docker", append(append([]string{}, args...), "psql", "-U", "postgres", "-d", restoreName, "-v", "ON_ERROR_STOP=1")...)
	restore.Stdin = bytes.NewReader(backup)
	if output, err := restore.CombinedOutput(); err != nil {
		t.Fatalf("isolated restore failed: %s", output)
	}
	if err = Apply(ctx, restored, migrations); err != nil {
		t.Fatalf("restored baseline: %v", err)
	}
	if schemaSnapshot(t, ctx, restored) != schemaSnapshot(t, ctx, legacy) {
		t.Fatal("restored schema differs")
	}
	if err = restored.QueryRow(ctx, `SELECT count(*) FROM "user" WHERE username='restore-fixture' AND password_hash='local-only-hash'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("restored data differs")
	}
	changed := append([]Migration(nil), migrations...)
	changed[0].SQL += "\nSELECT 2;"
	changed[0].Checksum = checksum(changed[0].SQL)
	if err = Apply(ctx, goDB, changed); err == nil || !strings.Contains(err.Error(), "history mismatch") {
		t.Fatal("modified historical SQL accepted")
	}
	bad := Migration{Name: "20990101_failure", SQL: "CREATE TABLE rollback_probe(id int); SELECT * FROM nonexistent_migration_table;"}
	bad.Checksum = checksum(bad.SQL)
	if err = Apply(ctx, goDB, append(migrations, bad)); err == nil {
		t.Fatal("failing SQL accepted")
	}
	var absent bool
	if err = goDB.QueryRow(ctx, `SELECT to_regclass('rollback_probe') IS NULL`).Scan(&absent); err != nil || !absent {
		t.Fatal("failed migration did not roll back")
	}
	// A legacy 1536-vector containing data cannot be blindly cast to 1024.
	_, err = goDB.Exec(ctx, `ALTER TABLE knowledge_chunk ALTER COLUMN embedding TYPE vector(1536)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = goDB.Exec(ctx, `DELETE FROM "_prisma_migrations" WHERE migration_name=$1`, "20260921083000_align_embedding_dimension")
	if err != nil {
		t.Fatal(err)
	}
	_, err = goDB.Exec(ctx, `INSERT INTO "user" (id,username,email,password_hash,updated_at) VALUES ('22222222-2222-4222-8222-222222222222','vector-fixture','vector@example.test','hash',now());
INSERT INTO knowledge_base (id,name,user_id,updated_at) VALUES ('33333333-3333-4333-8333-333333333333','vector','22222222-2222-4222-8222-222222222222',now());
INSERT INTO knowledge_entry (id,kb_id,title,content,updated_at) VALUES ('44444444-4444-4444-8444-444444444444','33333333-3333-4333-8333-333333333333','fixture','fixture',now());
INSERT INTO knowledge_chunk (id,entry_id,chunk_index,content,embedding) VALUES ('55555555-5555-4555-8555-555555555555','44444444-4444-4444-8444-444444444444',0,'fixture',array_fill(0.01::real,ARRAY[1536])::vector)`)
	if err != nil {
		t.Fatal(err)
	}
	if err = Apply(ctx, goDB, migrations); err == nil {
		t.Fatal("incompatible stored vectors were accepted")
	}
	if err = goDB.QueryRow(ctx, `SELECT vector_dims(embedding) FROM knowledge_chunk`).Scan(&count); err != nil || count != 1536 {
		t.Fatal("incompatible stored vector was modified")
	}
}

func schemaSnapshot(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	var columns, indexes string
	err := pool.QueryRow(ctx, `SELECT jsonb_agg(jsonb_build_array(table_name,column_name,data_type,udt_name,is_nullable,column_default,datetime_precision) ORDER BY table_name,ordinal_position)::text FROM information_schema.columns WHERE table_schema='public' AND table_name<>'_prisma_migrations'`).Scan(&columns)
	if err != nil {
		t.Fatal(err)
	}
	err = pool.QueryRow(ctx, `SELECT jsonb_agg(jsonb_build_array(tablename,indexname,indexdef) ORDER BY indexname)::text FROM pg_indexes WHERE schemaname='public' AND tablename<>'_prisma_migrations'`).Scan(&indexes)
	if err != nil {
		t.Fatal(err)
	}
	return columns + indexes
}
