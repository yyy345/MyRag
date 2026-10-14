package repository

import (
	"context"
	"errors"
	"strings"

	"ai-library/backend/internal/ports"
	"github.com/jackc/pgx/v5"
)

// DeleteEntries is all-or-nothing. Lock entries in UUID order, using the same
// row locks as edits and result commits. Foreign keys remove chunks/tasks/outbox.
func (s *Entries) DeleteEntries(ctx context.Context, scope ports.Scope, ids []string) error {
	if len(ids) == 0 || len(ids) > 100 {
		return ErrInvalid
	}
	if !entryUUID.MatchString(scope.UserID) || !entryUUID.MatchString(scope.KnowledgeBaseID) {
		return ErrNotFound
	}
	unique := make(map[string]bool)
	targets := make([]string, 0, len(ids))
	for _, id := range ids {
		if !entryUUID.MatchString(id) {
			return ErrInvalid
		}
		id = strings.ToLower(id)
		if !unique[id] {
			unique[id] = true
			targets = append(targets, id)
		}
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
	rows, err := tx.Query(ctx, `SELECT id::text FROM knowledge_entry WHERE kb_id=$1::uuid AND id=ANY($2::uuid[]) ORDER BY id FOR UPDATE`, kb, targets)
	if err != nil {
		return err
	}
	count := 0
	for rows.Next() {
		count++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if count != len(targets) {
		return ErrNotFound
	}
	if err = enqueueImageDeletes(ctx, tx, scope.UserID, kb, targets); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM knowledge_entry WHERE kb_id=$1::uuid AND id=ANY($2::uuid[])`, kb, targets)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
