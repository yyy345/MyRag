package repository

import (
	"ai-library/backend/internal/ports"
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"time"
)

type ExtractionLease struct {
	Token string
	Since time.Time
}

func (s *Entries) ExtractionScopes(ctx context.Context) ([]ports.Scope, error) {
	rows, err := s.Pool.Query(ctx, `SELECT k.user_id::text,k.id::text FROM knowledge_base k WHERE EXISTS(SELECT 1 FROM chat_session s WHERE s.kb_id=k.id AND s.user_id=k.user_id) ORDER BY k.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []ports.Scope{}
	for rows.Next() {
		var scope ports.Scope
		if err = rows.Scan(&scope.UserID, &scope.KnowledgeBaseID); err != nil {
			return nil, err
		}
		values = append(values, scope)
	}
	return values, rows.Err()
}
func (s *Entries) ClaimExtraction(ctx context.Context, scope ports.Scope, now time.Time, day string) (ExtractionLease, bool, error) {
	var lease ExtractionLease
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return lease, false, err
	}
	defer tx.Rollback(context.Background())
	var kb string
	// Wait for earlier message transactions before scanning their watermark.
	err = tx.QueryRow(ctx, `SELECT id::text FROM knowledge_base WHERE id=$1::uuid AND user_id=$2::uuid FOR UPDATE`, scope.KnowledgeBaseID, scope.UserID).Scan(&kb)
	if errors.Is(err, pgx.ErrNoRows) {
		return lease, false, nil
	}
	if err != nil {
		return lease, false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO extract_checkpoint(kb_id,last_message_at,next_attempt_at) VALUES($1::uuid,$2::timestamp,$3) ON CONFLICT(kb_id) DO NOTHING`, kb, now.Add(-24*time.Hour), now)
	if err != nil {
		return lease, false, err
	}
	err = tx.QueryRow(ctx, `UPDATE extract_checkpoint SET lease_token=gen_random_uuid(),lease_until=$2::timestamptz+interval '10 minutes',updated_at=now() WHERE kb_id=$1::uuid AND (completed_day IS NULL OR completed_day<$3::date) AND next_attempt_at<=$2 AND (lease_until IS NULL OR lease_until<=$2) RETURNING lease_token::text,last_message_at`, kb, now, day).Scan(&lease.Token, &lease.Since)
	if errors.Is(err, pgx.ErrNoRows) {
		return lease, false, tx.Commit(ctx)
	}
	if err != nil {
		return lease, false, err
	}
	return lease, true, tx.Commit(ctx)
}
func (s *Entries) FailExtraction(ctx context.Context, scope ports.Scope, token string, now time.Time) error {
	_, err := s.Pool.Exec(ctx, `UPDATE extract_checkpoint SET lease_token=NULL,lease_until=NULL,attempts=attempts+1,last_error='extraction_failed',next_attempt_at=$3::timestamptz+make_interval(mins=>least(60,5*(1<<least(attempts,3)))),updated_at=now() WHERE kb_id=$1::uuid AND lease_token=$2::uuid`, scope.KnowledgeBaseID, token, now)
	return err
}
