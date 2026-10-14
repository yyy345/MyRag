package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Apply can validate intermediate historical migrations. Running services need
// the complete indexing schema as well, and must fail before accepting work.
func validateRuntimeSchema(ctx context.Context, tx pgx.Tx) error {
	if err := validateSchema(ctx, tx); err != nil {
		return err
	}
	columns := []struct {
		table, name, kind string
		nullable          bool
	}{
		{"knowledge_entry", "entry_version", "int8", false},
		{"index_task", "id", "uuid", false},
		{"index_task", "entry_id", "uuid", false},
		{"index_task", "kb_id", "uuid", false},
		{"index_task", "entry_version", "int8", false},
		{"index_task", "trace_id", "varchar", false},
		{"index_task", "status", "text", false},
		{"index_task", "created_at", "timestamptz", false},
		{"index_task", "updated_at", "timestamptz", false},
		{"index_outbox", "task_id", "uuid", false},
		{"index_outbox", "payload", "jsonb", false},
		{"index_outbox", "created_at", "timestamptz", false},
		{"index_outbox", "published_at", "timestamptz", true},
		{"index_outbox", "attempts", "int4", false},
		{"index_outbox", "available_at", "timestamptz", false},
		{"index_outbox", "last_error", "text", true},
		{"storage_cleanup", "user_id", "uuid", false},
		{"storage_cleanup", "entry_id", "uuid", false},
		{"storage_cleanup", "available_at", "timestamptz", false},
		{"storage_cleanup", "attempts", "int4", false},
		{"storage_cleanup", "last_error", "text", true},
		{"storage_cleanup", "created_at", "timestamptz", false},
		{"index_receipt", "task_id", "uuid", false},
		{"index_receipt", "result", "jsonb", false},
		{"index_receipt", "created_at", "timestamptz", false},
		{"chat_receipt", "request_id", "uuid", false},
		{"chat_receipt", "session_id", "uuid", false},
		{"chat_receipt", "message_id", "uuid", false},
		{"chat_receipt", "created_at", "timestamptz", false},
		{"extract_checkpoint", "kb_id", "uuid", false},
		{"extract_checkpoint", "last_message_at", "timestamp", false},
		{"extract_checkpoint", "completed_day", "date", true},
		{"extract_checkpoint", "lease_token", "uuid", true},
		{"extract_checkpoint", "lease_until", "timestamptz", true},
		{"extract_checkpoint", "next_attempt_at", "timestamptz", false},
		{"extract_checkpoint", "attempts", "int4", false},
		{"extract_checkpoint", "last_error", "text", true},
		{"extract_checkpoint", "updated_at", "timestamptz", false},
	}
	for _, column := range columns {
		var kind string
		var nullable bool
		err := tx.QueryRow(ctx, `SELECT udt_name,is_nullable='YES' FROM information_schema.columns
WHERE table_schema='public' AND table_name=$1 AND column_name=$2`, column.table, column.name).Scan(&kind, &nullable)
		if err != nil || kind != column.kind || nullable != column.nullable {
			return fmt.Errorf("missing or incompatible indexing column: %s.%s", column.table, column.name)
		}
	}
	constraints := []struct{ table, name, kind, target string }{
		{"knowledge_entry", "knowledge_entry_entry_version_check", "c", ""},
		{"knowledge_entry", "knowledge_entry_id_kb_unique", "u", ""},
		{"index_task", "index_task_pkey", "p", ""},
		{"index_task", "index_task_entry_version_check", "c", ""},
		{"index_task", "index_task_trace_id_check", "c", ""},
		{"index_task", "index_task_status_check", "c", ""},
		{"index_task", "index_task_entry_id_entry_version_key", "u", ""},
		{"index_task", "index_task_entry_id_kb_id_fkey", "f", "public.knowledge_entry"},
		{"index_outbox", "index_outbox_pkey", "p", ""},
		{"index_outbox", "index_outbox_task_id_fkey", "f", "public.index_task"},
		{"index_outbox", "index_outbox_payload_check", "c", ""},
		{"index_outbox", "index_outbox_attempts_check", "c", ""},
		{"storage_cleanup", "storage_cleanup_pkey", "p", ""},
		{"storage_cleanup", "storage_cleanup_attempts_check", "c", ""},
		{"index_receipt", "index_receipt_pkey", "p", ""},
		{"index_receipt", "index_receipt_task_id_fkey", "f", "public.index_task"},
		{"index_receipt", "index_receipt_result_check", "c", ""},
		{"chat_receipt", "chat_receipt_pkey", "p", ""},
		{"chat_receipt", "chat_receipt_message_id_key", "u", ""},
		{"chat_receipt", "chat_receipt_session_id_fkey", "f", "public.chat_session"},
		{"chat_receipt", "chat_receipt_message_id_fkey", "f", "public.chat_message"},
		{"extract_checkpoint", "extract_checkpoint_pkey", "p", ""},
		{"extract_checkpoint", "extract_checkpoint_kb_id_fkey", "f", "public.knowledge_base"},
		{"extract_checkpoint", "extract_checkpoint_attempts_check", "c", ""},
		{"extract_checkpoint", "extract_checkpoint_lease_check", "c", ""},
	}
	for _, constraint := range constraints {
		var exists bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_constraint
WHERE conrelid=to_regclass($1) AND conname=$2 AND contype::text=$3 AND convalidated
AND ($3<>'f' OR (confrelid=to_regclass(NULLIF($4,'')) AND confdeltype='c')))`, "public."+constraint.table, constraint.name, constraint.kind, constraint.target).Scan(&exists)
		if err != nil || !exists {
			return fmt.Errorf("missing or incompatible indexing constraint: %s", constraint.name)
		}
	}
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid
WHERE i.indrelid=to_regclass('public.index_outbox') AND c.relname='index_outbox_pending_idx' AND i.indisvalid)`).Scan(&exists)
	if err != nil || !exists {
		return fmt.Errorf("missing required index: index_outbox_pending_idx")
	}
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid
WHERE i.indrelid=to_regclass('public.storage_cleanup') AND c.relname='storage_cleanup_pending_idx' AND i.indisvalid)`).Scan(&exists)
	if err != nil || !exists {
		return fmt.Errorf("missing required index: storage_cleanup_pending_idx")
	}
	return nil
}
