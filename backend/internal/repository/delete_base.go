package repository

import (
	"context"
	"errors"

	"ai-library/backend/internal/ports"
	"github.com/jackc/pgx/v5"
)

func (s *Entries) DeleteBase(ctx context.Context, scope ports.Scope) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var kb string
	err = tx.QueryRow(ctx, `SELECT id::text FROM knowledge_base WHERE id=$1::uuid AND user_id=$2::uuid FOR UPDATE`, scope.KnowledgeBaseID, scope.UserID).Scan(&kb)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err = enqueueImageDeletes(ctx, tx, scope.UserID, kb, nil); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM knowledge_base WHERE id=$1::uuid`, kb); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
