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

// Failed uploads remain visible without creating a paid indexing task.
// Cleanup reservation remains durable and independent of this record.
func (s *Entries) SaveFailedImage(ctx context.Context, scope ports.Scope, file FileEntry) (UploadResult, error) {
	var result UploadResult
	hash, err := hex.DecodeString(file.Hash)
	if err != nil || len(hash) != 32 || !entryUUID.MatchString(file.ID) || !entryUUID.MatchString(scope.UserID) || !entryUUID.MatchString(scope.KnowledgeBaseID) || strings.TrimSpace(file.Filename) == "" || utf8.RuneCountInString(file.Filename) > 200 || strings.ContainsRune(file.Filename, 0) {
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
	err = tx.QueryRow(ctx, `SELECT id::text,processing_status::text FROM knowledge_entry WHERE kb_id=$1::uuid AND source_file_hash=$2 ORDER BY created_at,id LIMIT 1`, kb, file.Hash).Scan(&result.ID, &result.ProcessingStatus)
	if err == nil {
		return result, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return result, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO knowledge_entry(id,kb_id,title,content,type,processing_status,processing_message,source_file_name,source_file_hash,updated_at) VALUES($1::uuid,$2::uuid,$3,'','FILE','FAILED','图片处理失败，请检查图片服务',$3,$4,now())`, file.ID, kb, file.Filename, file.Hash)
	if err != nil {
		return result, err
	}
	if err = tx.Commit(ctx); err != nil {
		return result, err
	}
	return UploadResult{ID: file.ID, ProcessingStatus: "FAILED", Created: true}, nil
}
