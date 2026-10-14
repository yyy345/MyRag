package repository

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"strings"

	"ai-library/backend/internal/ports"
	"github.com/jackc/pgx/v5"
)

// Nil fields are omitted; metadata-only changes preserve the index version.
type EntryPatch struct {
	Title, Content, Status *string
	ExpectedVersion        *int64
}

func (s *Entries) EditEntry(ctx context.Context, scope ports.Scope, id string, p EntryPatch) error {
	if !entryUUID.MatchString(id) || !entryUUID.MatchString(scope.UserID) || !entryUUID.MatchString(scope.KnowledgeBaseID) {
		return ErrNotFound
	}
	if p.Title == nil && p.Content == nil && p.Status == nil {
		return ErrInvalid
	}
	if p.Title != nil && (strings.TrimSpace(*p.Title) == "" || len([]rune(*p.Title)) > 200 || strings.ContainsRune(*p.Title, 0)) {
		return ErrInvalid
	}
	if p.Content != nil && (strings.TrimSpace(*p.Content) == "" || strings.ContainsRune(*p.Content, 0)) {
		return ErrInvalid
	}
	if p.Status != nil && *p.Status != "ENABLED" && *p.Status != "DISABLED" {
		return ErrInvalid
	}
	if p.ExpectedVersion != nil && *p.ExpectedVersion < 1 {
		return ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var kb string
	err = tx.QueryRow(ctx, `SELECT id::text FROM knowledge_base WHERE id=$1::uuid AND user_id=$2::uuid FOR SHARE`, scope.KnowledgeBaseID, scope.UserID).Scan(&kb)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	var title, content, status string
	var filename *string
	var version int64
	err = tx.QueryRow(ctx, `SELECT title,content,status::text,source_file_name,entry_version FROM knowledge_entry WHERE id=$1::uuid AND kb_id=$2::uuid FOR UPDATE`, id, kb).Scan(&title, &content, &status, &filename, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if p.ExpectedVersion != nil && *p.ExpectedVersion != version {
		return ErrConflict
	}
	oldTitle, oldContent, oldStatus := title, content, status
	if p.Title != nil {
		title = strings.TrimSpace(*p.Title)
		if filename != nil {
			suffix := path.Ext(*filename)
			if suffix != "" && (!strings.EqualFold(path.Ext(title), suffix) || strings.TrimSpace(strings.TrimSuffix(title, path.Ext(title))) == "") {
				return ErrInvalid
			}
		}
	}
	if p.Content != nil {
		content = *p.Content
	}
	if p.Status != nil {
		status = *p.Status
	}
	if title == oldTitle && content == oldContent && status == oldStatus {
		return tx.Commit(ctx)
	}
	changed := content != oldContent
	if changed {
		if version == 9223372036854775807 {
			return ErrConflict
		}
		version++
	}
	_, err = tx.Exec(ctx, `UPDATE knowledge_entry SET title=$2,content=$3,status=$4::"EntryStatus",entry_version=$5,updated_at=now(),processing_status=CASE WHEN $6 THEN 'PENDING'::"ProcessingStatus" ELSE processing_status END,processing_message=CASE WHEN $6 THEN NULL ELSE processing_message END WHERE id=$1::uuid`, id, title, content, status, version, changed)
	if err != nil {
		return err
	}
	if changed {
		var taskID string
		if err = tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&taskID); err != nil {
			return err
		}
		task := ports.IndexTask{SchemaVersion: 1, TaskID: taskID, EntryID: strings.ToLower(id), KBID: kb, EntryVersion: version, TraceID: taskID}
		_, err = tx.Exec(ctx, `INSERT INTO index_task(id,entry_id,kb_id,entry_version,trace_id) VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5)`, taskID, id, kb, version, taskID)
		if err != nil {
			return taskError(err)
		}
		body, err := json.Marshal(task)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO index_outbox(task_id,payload) VALUES($1::uuid,$2::jsonb)`, taskID, string(body))
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
