package repository

import (
	"context"
	"errors"

	"ai-library/backend/internal/ports"
	"github.com/jackc/pgx/v5"
)

type ResultCache interface {
	Delete(context.Context, ...string) error
	DeletePrefix(context.Context, string) error
}

// Database commit precedes cache invalidation. On cache failure the broker must
// redeliver: a duplicate terminal result repeats invalidation before ACK.
func (s *Entries) ApplyResult(ctx context.Context, v ports.IndexResult, cache ResultCache) error {
	if err := v.Validate(); err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var version int64
	var kb, user string
	err = tx.QueryRow(ctx, `SELECT e.entry_version,e.kb_id::text,k.user_id::text FROM knowledge_entry e JOIN knowledge_base k ON k.id=e.kb_id WHERE e.id=$1::uuid FOR UPDATE OF e`, v.EntryID).Scan(&version, &kb, &user)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var taskEntry, taskKB, trace, status string
	var taskVersion int64
	err = tx.QueryRow(ctx, `SELECT entry_id::text,kb_id::text,entry_version,trace_id,status FROM index_task WHERE id=$1::uuid FOR UPDATE`, v.TaskID).Scan(&taskEntry, &taskKB, &taskVersion, &trace, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.ErrInvalidResult
	}
	if err != nil {
		return err
	}
	if taskEntry != v.EntryID || taskKB != kb || taskVersion != v.EntryVersion || trace != v.TraceID || version < v.EntryVersion {
		return ports.ErrInvalidResult
	}
	if version > v.EntryVersion {
		_, err = tx.Exec(ctx, `UPDATE index_task SET status='STALE',updated_at=now() WHERE id=$1::uuid AND status IN ('PENDING','PROCESSING')`, v.TaskID)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	// STALE is meaningful only when a newer version exists; it cannot terminate
	// the current version or overwrite the entry's processing state.
	if v.Status == "STALE" {
		return ports.ErrInvalidResult
	}
	terminal := status == "COMPLETED" || status == "FAILED" || status == "STALE"
	if terminal && status != v.Status {
		return ports.ErrInvalidResult
	}
	if v.Status == "COMPLETED" {
		var count int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM knowledge_chunk WHERE entry_id=$1::uuid`, v.EntryID).Scan(&count); err != nil {
			return err
		}
		if count != v.ChunkCount {
			return ports.ErrInvalidResult
		}
	}
	if !terminal {
		var message any
		if v.Status == "FAILED" {
			message = "索引处理失败，请重试"
		} // Never expose arbitrary provider errors/secrets.
		_, err = tx.Exec(ctx, `UPDATE knowledge_entry SET processing_status=$2::"ProcessingStatus",processing_message=$3,updated_at=now() WHERE id=$1::uuid`, v.EntryID, v.Status, message)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE index_task SET status=$2,updated_at=now() WHERE id=$1::uuid`, v.TaskID, v.Status)
		if err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if cache == nil {
		return errors.New("result cache invalidator is required")
	}
	for _, prefix := range []string{"go:entry:" + kb + ":", "entry:" + kb + ":"} {
		if err = cache.DeletePrefix(ctx, prefix); err != nil {
			return err
		}
	}
	return cache.Delete(ctx, "go:entry:detail:"+v.EntryID, "entry:detail:"+v.EntryID,
		"analytics:overview:"+user, "analytics:kb:"+kb,
		"go:analytics:overview:"+user, "go:analytics:kb:"+kb)
}
