package repository

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"unicode/utf8"

	"ai-library/backend/internal/ports"
	"github.com/jackc/pgx/v5"
)

type FileEntry struct {
	ID, Filename, Hash, Content string
	Image                       bool
}
type UploadResult struct {
	ID               string `json:"entry_id"`
	ProcessingStatus string `json:"processing_status"`
	Created          bool   `json:"-"`
}

func (s *Entries) FindFile(ctx context.Context, scope ports.Scope, hash string) (UploadResult, error) {
	var result UploadResult
	err := s.Pool.QueryRow(ctx, `SELECT e.id::text,e.processing_status::text FROM knowledge_entry e
JOIN knowledge_base k ON k.id=e.kb_id WHERE e.kb_id=$1::uuid AND k.user_id=$2::uuid AND e.source_file_hash=$3 ORDER BY e.created_at,e.id LIMIT 1`, scope.KnowledgeBaseID, scope.UserID, hash).Scan(&result.ID, &result.ProcessingStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	return result, err
}

// Parsing happens before this transaction. A brief exclusive parent lock
// serializes file deduplication for this KB, without locking during HTTP/AI work.
// Existing hashes are not globally unique and no historical data is removed.
func (s *Entries) SaveFileAndTask(ctx context.Context, scope ports.Scope, file FileEntry, task ports.IndexTask) (UploadResult, error) {
	result := UploadResult{}
	hash, err := hex.DecodeString(file.Hash)
	if err != nil || len(hash) != 32 || !entryUUID.MatchString(scope.UserID) || !entryUUID.MatchString(scope.KnowledgeBaseID) || !entryUUID.MatchString(file.ID) || !entryUUID.MatchString(task.TaskID) ||
		task.SchemaVersion != 1 || task.EntryID != file.ID || task.KBID != scope.KnowledgeBaseID || task.EntryVersion != 1 || strings.TrimSpace(task.TraceID) == "" || utf8.RuneCountInString(task.TraceID) > 128 ||
		strings.TrimSpace(file.Filename) == "" || utf8.RuneCountInString(file.Filename) > 200 || strings.ContainsRune(file.Filename, 0) || strings.TrimSpace(strings.Trim(file.Content, "\ufeff")) == "" || strings.ContainsRune(file.Content, 0) || !utf8.ValidString(file.Content) || utf8.RuneCountInString(file.Content) > ports.MaxParsedCharacters {
		return result, ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(context.Background())
	var kb string
	err = tx.QueryRow(ctx, `SELECT id::text FROM knowledge_base WHERE id=$1::uuid AND user_id=$2::uuid FOR UPDATE`, scope.KnowledgeBaseID, scope.UserID).Scan(&kb)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, ErrNotFound
	}
	if err != nil {
		return result, err
	}
	err = tx.QueryRow(ctx, `SELECT id::text,processing_status::text FROM knowledge_entry WHERE kb_id=$1::uuid AND source_file_hash=$2 ORDER BY created_at,id LIMIT 1`, kb, strings.ToLower(file.Hash)).Scan(&result.ID, &result.ProcessingStatus)
	if err == nil {
		return result, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return result, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO knowledge_entry(id,kb_id,title,content,type,entry_version,source_file_name,source_file_hash,updated_at)
VALUES($1::uuid,$2::uuid,$3,$4,'FILE',1,$3,$5,now())`, file.ID, kb, file.Filename, file.Content, strings.ToLower(file.Hash))
	if err != nil {
		return result, err
	}
	if err = saveIndexTask(ctx, tx, task); err != nil {
		return result, err
	}
	if file.Image {
		if err = consumeImageReservation(ctx, tx, scope.UserID, file.ID); err != nil {
			return result, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return result, err
	}
	return UploadResult{ID: file.ID, ProcessingStatus: "PENDING", Created: true}, nil
}
