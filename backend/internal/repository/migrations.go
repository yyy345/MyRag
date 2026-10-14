package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Migration struct{ Name, SQL, Checksum string }

func checksum(sql string) string {
	sum := sha256.Sum256([]byte(sql))
	return hex.EncodeToString(sum[:])
}
func (m Migration) matches(hash string) bool {
	lf := strings.ReplaceAll(m.SQL, "\r\n", "\n")
	return hash == m.Checksum || hash == checksum(lf) || hash == checksum(strings.ReplaceAll(lf, "\n", "\r\n"))
}

func ReadMigrations(directory string) ([]Migration, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, errors.New("cannot read migration directory")
	}
	var result []Migration
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		body, err := os.ReadFile(filepath.Join(directory, entry.Name(), "migration.sql"))
		if err != nil {
			return nil, fmt.Errorf("missing SQL for migration %s", entry.Name())
		}
		result = append(result, Migration{Name: entry.Name(), SQL: string(body), Checksum: checksum(string(body))})
	}
	if len(result) == 0 {
		return nil, errors.New("migration directory is empty")
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

// Apply preserves Prisma's ledger and SQL history. The API never migrates on startup.
// All checks and pending SQL run under one transaction/advisory lock.
func Apply(ctx context.Context, pool *pgxpool.Pool, migrations []Migration) error {
	if len(migrations) == 0 {
		return errors.New("no migrations supplied")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return errors.New("cannot begin migration transaction")
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(19740927)"); err != nil {
		return errors.New("cannot lock migrations")
	}
	var ledger, populated bool
	err = tx.QueryRow(ctx, `SELECT to_regclass('public._prisma_migrations') IS NOT NULL, EXISTS(SELECT 1 FROM pg_tables WHERE schemaname='public' AND tablename<>'_prisma_migrations')`).Scan(&ledger, &populated)
	if err != nil {
		return errors.New("cannot inspect migration baseline")
	}
	if !ledger && populated {
		return errors.New("unmanaged non-empty database: restore a verified Prisma baseline first")
	}
	_, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS "_prisma_migrations" (
 id VARCHAR(36) PRIMARY KEY NOT NULL, checksum VARCHAR(64) NOT NULL, finished_at TIMESTAMPTZ,
 migration_name VARCHAR(255) NOT NULL, logs TEXT, rolled_back_at TIMESTAMPTZ,
 started_at TIMESTAMPTZ NOT NULL DEFAULT now(), applied_steps_count INTEGER NOT NULL DEFAULT 0)`)
	if err != nil {
		return errors.New("cannot initialize migration ledger")
	}
	known := make(map[string]Migration)
	for _, m := range migrations {
		if _, exists := known[m.Name]; exists {
			return errors.New("duplicate migration name")
		}
		known[m.Name] = m
	}
	rows, err := tx.Query(ctx, `SELECT migration_name,checksum,finished_at IS NOT NULL FROM "_prisma_migrations" WHERE rolled_back_at IS NULL`)
	if err != nil {
		return errors.New("cannot read migration ledger")
	}
	applied := make(map[string]bool)
	for rows.Next() {
		var name, hash string
		var finished bool
		if err = rows.Scan(&name, &hash, &finished); err != nil {
			break
		}
		m, exists := known[name]
		if !finished || !exists || !m.matches(hash) || applied[name] {
			rows.Close()
			return fmt.Errorf("migration history mismatch: %s", name)
		}
		applied[name] = true
	}
	rows.Close()
	if err != nil || rows.Err() != nil {
		return errors.New("cannot read migration ledger")
	}
	for _, m := range migrations {
		if applied[m.Name] {
			continue
		}
		if _, err = tx.Exec(ctx, m.SQL); err != nil {
			return fmt.Errorf("migration %s failed; transaction rolled back", m.Name)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO "_prisma_migrations" (id,checksum,migration_name,finished_at,applied_steps_count) VALUES (gen_random_uuid()::text,$1,$2,now(),1)`, m.Checksum, m.Name); err != nil {
			return errors.New("cannot record migration completion")
		}
	}
	if err = validateSchema(ctx, tx); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return errors.New("cannot commit migrations")
	}
	return nil
}

func validateSchema(ctx context.Context, tx pgx.Tx) error {
	for _, table := range []string{"user", "knowledge_base", "knowledge_entry", "knowledge_chunk", "chat_session", "chat_message"} {
		var idType, timeType string
		var precision int
		err := tx.QueryRow(ctx, `SELECT data_type FROM information_schema.columns WHERE table_schema='public' AND table_name=$1 AND column_name='id'`, table).Scan(&idType)
		if err != nil || idType != "uuid" {
			return fmt.Errorf("invalid UUID schema: %s", table)
		}
		err = tx.QueryRow(ctx, `SELECT data_type,datetime_precision FROM information_schema.columns WHERE table_schema='public' AND table_name=$1 AND column_name='created_at'`, table).Scan(&timeType, &precision)
		if err != nil || timeType != "timestamp without time zone" || precision != 3 {
			return fmt.Errorf("invalid timestamp schema: %s", table)
		}
	}
	var vectorType string
	err := tx.QueryRow(ctx, `SELECT format_type(atttypid,atttypmod) FROM pg_attribute WHERE attrelid='knowledge_chunk'::regclass AND attname='embedding'`).Scan(&vectorType)
	if err != nil || vectorType != "vector(1024)" {
		return errors.New("knowledge_chunk.embedding must be vector(1024)")
	}
	for name, want := range map[string]string{"EntryType": "MANUAL,FILE", "EntryStatus": "ENABLED,DISABLED", "ProcessingStatus": "PENDING,PROCESSING,COMPLETED,FAILED", "MessageRole": "USER,ASSISTANT"} {
		var actual string
		err = tx.QueryRow(ctx, `SELECT string_agg(e.enumlabel,',' ORDER BY e.enumsortorder) FROM pg_enum e JOIN pg_type t ON e.enumtypid=t.oid JOIN pg_namespace n ON t.typnamespace=n.oid WHERE n.nspname='public' AND t.typname=$1`, name).Scan(&actual)
		if err != nil || actual != want {
			return fmt.Errorf("invalid enum schema: %s", name)
		}
	}
	for _, name := range []string{"user_username_key", "user_email_key", "knowledge_entry_kb_id_status_idx", "knowledge_entry_kb_id_title_idx", "knowledge_entry_kb_id_source_file_hash_idx", "knowledge_chunk_entry_id_idx", "chat_session_user_id_kb_id_idx", "chat_message_session_id_created_at_idx"} {
		var exists bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_indexes WHERE schemaname='public' AND indexname=$1)`, name).Scan(&exists)
		if err != nil || !exists {
			return fmt.Errorf("missing required index: %s", name)
		}
	}
	return nil
}
