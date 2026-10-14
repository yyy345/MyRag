package repository

import (
	"context"
	"encoding/json"
	"errors"

	"ai-library/backend/internal/ports"
	"github.com/jackc/pgx/v5"
)

// DispatchOne locks one pending row until broker confirmation and bookkeeping
// commit. A crash between confirmation and commit permits duplicate delivery;
// consumers must deduplicate using task_id/version. SKIP LOCKED shares work.
func (s *Entries) DispatchOne(ctx context.Context, publisher ports.Publisher) (bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(context.Background())
	var id string
	var payload []byte
	err = tx.QueryRow(ctx, `SELECT task_id::text,payload FROM index_outbox WHERE published_at IS NULL AND available_at<=now() ORDER BY available_at,created_at,task_id FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&id, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var task ports.IndexTask
	if err = json.Unmarshal(payload, &task); err != nil {
		return true, err
	}
	publishErr := publisher.PublishConfirmed(ctx, task)
	if publishErr == nil {
		_, err = tx.Exec(ctx, `UPDATE index_outbox SET published_at=now(),attempts=attempts+1,last_error=NULL WHERE task_id=$1::uuid`, id)
	} else {
		// Safe error code only: broker URLs and credentials must never reach storage.
		_, err = tx.Exec(ctx, `UPDATE index_outbox SET attempts=attempts+1,last_error='publish_unconfirmed',available_at=now()+make_interval(secs=>LEAST(60,power(2,LEAST(attempts,6)))::int) WHERE task_id=$1::uuid`, id)
	}
	if err != nil {
		return true, err
	}
	if err = tx.Commit(ctx); err != nil {
		return true, err
	}
	return true, publishErr
}
