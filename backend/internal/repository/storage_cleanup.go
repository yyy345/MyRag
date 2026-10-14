package repository

import (
	"context"
	"errors"
	"time"

	"ai-library/backend/internal/ports"
	"github.com/jackc/pgx/v5"
)

// Reserve before sending any bytes to storage. A crashed request leaves a
// durable cleanup job. Image attachment must consume it in its SQL transaction.
func (s *Entries) ReserveImage(ctx context.Context, scope ports.Scope, entry string) error {
	if !entryUUID.MatchString(scope.UserID) || !entryUUID.MatchString(scope.KnowledgeBaseID) || !entryUUID.MatchString(entry) {
		return ErrInvalid
	}
	result, err := s.Pool.Exec(ctx, `INSERT INTO storage_cleanup(user_id,entry_id,available_at)
SELECT $1::uuid,$3::uuid,now()+interval '10 minutes' WHERE EXISTS(SELECT 1 FROM knowledge_base WHERE id=$2::uuid AND user_id=$1::uuid)`, scope.UserID, scope.KnowledgeBaseID, entry)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

// Request cancellation must not discard cleanup; callers use a bounded detached
// context. If rescheduling fails, the original ten-minute job still survives.
func (s *Entries) AbandonImage(ctx context.Context, user, entry string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE storage_cleanup SET available_at=now() WHERE user_id=$1::uuid AND entry_id=$2::uuid`, user, entry)
	return err
}

func consumeImageReservation(ctx context.Context, tx pgx.Tx, user, entry string) error {
	result, err := tx.Exec(ctx, `DELETE FROM storage_cleanup WHERE user_id=$1::uuid AND entry_id=$2::uuid AND available_at>now()`, user, entry)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrInvalid
	}
	return nil
}

func enqueueImageDeletes(ctx context.Context, tx pgx.Tx, user, kb string, ids []string) error {
	_, err := tx.Exec(ctx, `INSERT INTO storage_cleanup(user_id,entry_id)
SELECT $1::uuid,id FROM knowledge_entry WHERE kb_id=$2::uuid AND type='FILE' AND ($3::uuid[] IS NULL OR id=ANY($3::uuid[]))
ON CONFLICT(user_id,entry_id) DO UPDATE SET available_at=LEAST(storage_cleanup.available_at,now())`, user, kb, ids)
	return err
}

// Lock one durable job while deleting, serializing against reservation consume.
// A crash after OSS DELETE but before commit retries the idempotent deletion.
func (s *Entries) CleanupOne(ctx context.Context, storage ports.ImageStorage) (bool, error) {
	if storage == nil {
		return false, ports.ErrStorageUnavailable
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(context.Background())
	var user, entry string
	var attempts int
	err = tx.QueryRow(ctx, `SELECT user_id::text,entry_id::text,attempts FROM storage_cleanup WHERE available_at<=now() ORDER BY available_at,created_at FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&user, &entry, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	call, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	deleteErr := storage.DeleteImage(call, user, entry)
	if deleteErr == nil {
		_, err = tx.Exec(ctx, `DELETE FROM storage_cleanup WHERE user_id=$1::uuid AND entry_id=$2::uuid`, user, entry)
	} else {
		// Store only a stable error code, never provider responses or credentials.
		delay := time.Minute * time.Duration(1<<min(attempts, 6))
		_, err = tx.Exec(ctx, `UPDATE storage_cleanup SET attempts=attempts+1,last_error='storage_delete_failed',available_at=now()+($3::double precision*interval '1 second') WHERE user_id=$1::uuid AND entry_id=$2::uuid`, user, entry, delay.Seconds())
	}
	if err != nil {
		return true, err
	}
	if err = tx.Commit(ctx); err != nil {
		return true, err
	}
	return true, deleteErr
}
