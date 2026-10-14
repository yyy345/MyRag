package repository

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"ai-library/backend/internal/ports"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound = errors.New("resource not found")
	ErrConflict = errors.New("entry version or task conflict")
	ErrInvalid  = errors.New("invalid entry or task")
	entryUUID   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

type Entries struct{ Pool *pgxpool.Pool }

var _ ports.Repository = (*Entries)(nil)

func (s *Entries) Authorize(ctx context.Context, scope ports.Scope) error {
	if !entryUUID.MatchString(scope.UserID) || !entryUUID.MatchString(scope.KnowledgeBaseID) {
		return ErrNotFound
	}
	var exists bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM knowledge_base WHERE id=$1::uuid AND user_id=$2::uuid)`, scope.KnowledgeBaseID, scope.UserID).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

func (s *Entries) GetEntry(ctx context.Context, scope ports.Scope, id string) (ports.Entry, error) {
	var entry ports.Entry
	if !entryUUID.MatchString(id) || !entryUUID.MatchString(scope.UserID) || !entryUUID.MatchString(scope.KnowledgeBaseID) {
		return entry, ErrNotFound
	}
	err := s.Pool.QueryRow(ctx, `SELECT e.id::text,e.title,e.content,e.entry_version FROM knowledge_entry e JOIN knowledge_base k ON k.id=e.kb_id WHERE e.id=$1::uuid AND k.id=$2::uuid AND k.user_id=$3::uuid`, id, scope.KnowledgeBaseID, scope.UserID).Scan(&entry.ID, &entry.Title, &entry.Content, &entry.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return entry, err
}

// Entry.Version is the expected current version (0 creates). Task.EntryVersion
// must be current+1. An optimistic conflict never overwrites a newer edit.
// The entry row lock also coordinates with the Python indexing worker.
func (s *Entries) SaveEntryAndTask(ctx context.Context, scope ports.Scope, entry ports.Entry, task ports.IndexTask) error {
	if !entryUUID.MatchString(scope.UserID) || !entryUUID.MatchString(scope.KnowledgeBaseID) ||
		!entryUUID.MatchString(entry.ID) || !entryUUID.MatchString(task.TaskID) ||
		task.EntryID != entry.ID || task.KBID != scope.KnowledgeBaseID || task.SchemaVersion != 1 ||
		entry.Version < 0 || task.EntryVersion < 1 || task.EntryVersion-1 != entry.Version ||
		strings.TrimSpace(entry.Title) == "" || strings.TrimSpace(entry.Content) == "" ||
		strings.TrimSpace(task.TraceID) == "" || utf8.RuneCountInString(task.TraceID) > 128 {
		return ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	// Hold parent ownership stable until commit; concurrent edits to other entries
	// remain possible because SHARE locks are compatible.
	var kb string
	err = tx.QueryRow(ctx, `SELECT id::text FROM knowledge_base WHERE id=$1::uuid AND user_id=$2::uuid FOR SHARE`, scope.KnowledgeBaseID, scope.UserID).Scan(&kb)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if entry.Version == 0 {
		_, err = tx.Exec(ctx, `INSERT INTO knowledge_entry(id,kb_id,title,content,entry_version,updated_at) VALUES($1::uuid,$2::uuid,$3,$4,1,now())`, entry.ID, kb, entry.Title, entry.Content)
	} else {
		var current int64
		err = tx.QueryRow(ctx, `SELECT entry_version FROM knowledge_entry WHERE id=$1::uuid AND kb_id=$2::uuid FOR UPDATE`, entry.ID, kb).Scan(&current)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if current != entry.Version {
			return ErrConflict
		}
		_, err = tx.Exec(ctx, `UPDATE knowledge_entry SET title=$2,content=$3,entry_version=$4,processing_status='PENDING',processing_message=NULL,updated_at=now() WHERE id=$1::uuid`, entry.ID, entry.Title, entry.Content, task.EntryVersion)
	}
	if err != nil {
		return taskError(err)
	}
	if err = saveIndexTask(ctx, tx, task); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func saveIndexTask(ctx context.Context, tx pgx.Tx, task ports.IndexTask) error {
	_, err := tx.Exec(ctx, `INSERT INTO index_task(id,entry_id,kb_id,entry_version,trace_id) VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5)`, task.TaskID, task.EntryID, task.KBID, task.EntryVersion, task.TraceID)
	if err != nil {
		return taskError(err)
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO index_outbox(task_id,payload) VALUES($1::uuid,$2::jsonb)`, task.TaskID, string(payload))
	if err != nil {
		return taskError(err)
	}
	return nil
}

func taskError(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return ErrConflict
	}
	return err
}
