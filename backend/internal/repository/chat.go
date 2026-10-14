package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"ai-library/backend/internal/ports"
	"github.com/jackc/pgx/v5"
)

type Session struct {
	ID        string `json:"id"`
	KBID      string `json:"kb_id"`
	UserID    string `json:"user_id"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}
type ChatMessage struct {
	ID         string          `json:"id"`
	SessionID  string          `json:"session_id"`
	Role       string          `json:"role"`
	Content    string          `json:"content"`
	References json.RawMessage `json:"references"`
	TokenUsage json.RawMessage `json:"token_usage"`
	CreatedAt  string          `json:"created_at"`
}

const sessionFields = `s.id::text,s.kb_id::text,s.user_id::text,s.title,s.created_at,s.updated_at`

func scanSession(row pgx.Row) (Session, error) {
	var value Session
	var created, updated time.Time
	err := row.Scan(&value.ID, &value.KBID, &value.UserID, &value.Title, &created, &updated)
	value.CreatedAt = created.UTC().Format("2006-01-02T15:04:05.000Z")
	value.UpdatedAt = updated.UTC().Format("2006-01-02T15:04:05.000Z")
	if errors.Is(err, pgx.ErrNoRows) {
		return value, ErrNotFound
	}
	return value, err
}

func (s *Entries) CreateSession(ctx context.Context, scope ports.Scope) (Session, error) {
	return scanSession(s.Pool.QueryRow(ctx, `INSERT INTO chat_session AS s(id,kb_id,user_id,title,updated_at)
SELECT gen_random_uuid(),id,user_id,'新对话',now() FROM knowledge_base WHERE id=$1::uuid AND user_id=$2::uuid RETURNING `+sessionFields, scope.KnowledgeBaseID, scope.UserID))
}

func (s *Entries) ListSessions(ctx context.Context, scope ports.Scope) ([]Session, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+sessionFields+` FROM chat_session s JOIN knowledge_base k ON k.id=s.kb_id WHERE s.kb_id=$1::uuid AND s.user_id=$2::uuid AND k.user_id=$2::uuid ORDER BY s.updated_at DESC,s.id DESC`, scope.KnowledgeBaseID, scope.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []Session{}
	for rows.Next() {
		value, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Entries) AuthorizeSession(ctx context.Context, scope ports.Scope, id string) (Session, error) {
	if !entryUUID.MatchString(id) {
		return Session{}, ErrNotFound
	}
	return scanSession(s.Pool.QueryRow(ctx, `SELECT `+sessionFields+` FROM chat_session s JOIN knowledge_base k ON k.id=s.kb_id WHERE s.id=$1::uuid AND s.kb_id=$2::uuid AND s.user_id=$3::uuid AND k.user_id=$3::uuid`, id, scope.KnowledgeBaseID, scope.UserID))
}

func (s *Entries) Messages(ctx context.Context, scope ports.Scope, id string) ([]ChatMessage, error) {
	if _, err := s.AuthorizeSession(ctx, scope, id); err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT m.id::text,m.session_id::text,m.role::text,m.content,m."references",m.token_usage,m.created_at FROM chat_message m JOIN chat_session s ON s.id=m.session_id JOIN knowledge_base k ON k.id=s.kb_id WHERE s.id=$1::uuid AND s.kb_id=$2::uuid AND s.user_id=$3::uuid AND k.user_id=$3::uuid ORDER BY m.created_at,m.id`, id, scope.KnowledgeBaseID, scope.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []ChatMessage{}
	for rows.Next() {
		var value ChatMessage
		var created time.Time
		if err = rows.Scan(&value.ID, &value.SessionID, &value.Role, &value.Content, &value.References, &value.TokenUsage, &created); err != nil {
			return nil, err
		}
		value.CreatedAt = created.UTC().Format("2006-01-02T15:04:05.000Z")
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Entries) DeleteSession(ctx context.Context, scope ports.Scope, id string) error {
	if !entryUUID.MatchString(id) {
		return ErrNotFound
	}
	result, err := s.Pool.Exec(ctx, `DELETE FROM chat_session s USING knowledge_base k WHERE s.kb_id=k.id AND s.id=$1::uuid AND s.kb_id=$2::uuid AND s.user_id=$3::uuid AND k.user_id=$3::uuid`, id, scope.KnowledgeBaseID, scope.UserID)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

// SaveMessage serializes first-title and writes under the authorized session
// lock. A deleted/cross-owner session cannot be recreated by a delayed stream.
func (s *Entries) SaveMessage(ctx context.Context, scope ports.Scope, id, role, content string, references, usage json.RawMessage) (string, error) {
	return s.saveChatMessage(ctx, scope, id, role, content, references, usage, "", nil)
}

// Assistant content, idempotency receipt and scoped heat counters commit together.
func (s *Entries) SaveAnswer(ctx context.Context, scope ports.Scope, id, content string, references, usage json.RawMessage, request string, hits []string) (string, error) {
	if !entryUUID.MatchString(request) || len(hits) > 5 {
		return "", ErrInvalid
	}
	for _, hit := range hits {
		if !entryUUID.MatchString(hit) {
			return "", ErrInvalid
		}
	}
	return s.saveChatMessage(ctx, scope, id, "ASSISTANT", content, references, usage, request, hits)
}

func (s *Entries) saveChatMessage(ctx context.Context, scope ports.Scope, id, role, content string, references, usage json.RawMessage, request string, hits []string) (string, error) {
	if !entryUUID.MatchString(id) || (role != "USER" && role != "ASSISTANT") || !utf8.ValidString(content) || strings.TrimSpace(content) == "" || strings.ContainsRune(content, 0) || utf8.RuneCountInString(content) > 200_000 || (references != nil && !json.Valid(references)) || (usage != nil && !json.Valid(usage)) {
		return "", ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(context.Background())
	var kb string
	err = tx.QueryRow(ctx, `SELECT id::text FROM knowledge_base WHERE id=$1::uuid AND user_id=$2::uuid FOR SHARE`, scope.KnowledgeBaseID, scope.UserID).Scan(&kb)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	var session string
	err = tx.QueryRow(ctx, `SELECT id::text FROM chat_session WHERE id=$1::uuid AND kb_id=$2::uuid AND user_id=$3::uuid FOR UPDATE`, id, kb, scope.UserID).Scan(&session)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if request != "" {
		var previous, previousSession string
		err = tx.QueryRow(ctx, `SELECT message_id::text,session_id::text FROM chat_receipt WHERE request_id=$1::uuid`, request).Scan(&previous, &previousSession)
		if err == nil {
			if previousSession != session {
				return "", ErrConflict
			}
			return previous, tx.Commit(ctx)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
	}
	var first bool
	if role == "USER" {
		err = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM chat_message WHERE session_id=$1::uuid AND role='USER')`, id).Scan(&first)
		if err != nil {
			return "", err
		}
	}
	var message string
	err = tx.QueryRow(ctx, `INSERT INTO chat_message(id,session_id,role,content,"references",token_usage,created_at)
VALUES(gen_random_uuid(),$1::uuid,$2::"MessageRole",$3,$4::jsonb,$5::jsonb,
GREATEST(clock_timestamp()::timestamp(3),COALESCE((SELECT max(created_at)+interval '1 millisecond' FROM chat_message WHERE session_id=$1::uuid),clock_timestamp()::timestamp(3)))) RETURNING id::text`, id, role, content, references, usage).Scan(&message)
	if err != nil {
		return "", err
	}
	title := utf16Prefix(content, 50)
	_, err = tx.Exec(ctx, `UPDATE chat_session SET title=CASE WHEN $2 THEN $3 ELSE title END,updated_at=now() WHERE id=$1::uuid`, id, first, title)
	if err != nil {
		return "", err
	}
	if request != "" {
		_, err = tx.Exec(ctx, `UPDATE knowledge_chunk c SET hit_count=hit_count+1 FROM knowledge_entry e JOIN knowledge_base k ON k.id=e.kb_id WHERE c.entry_id=e.id AND e.kb_id=$1::uuid AND k.user_id=$2::uuid AND c.id=ANY($3::uuid[]) AND e.status='ENABLED' AND e.processing_status='COMPLETED'`, scope.KnowledgeBaseID, scope.UserID, hits)
		if err != nil {
			return "", err
		}
		_, err = tx.Exec(ctx, `INSERT INTO chat_receipt(request_id,session_id,message_id) VALUES($1::uuid,$2::uuid,$3::uuid)`, request, id, message)
		if err != nil {
			return "", err
		}
	}
	return message, tx.Commit(ctx)
}

func utf16Prefix(text string, limit int) string {
	units := 0
	for i, r := range text {
		size := 1
		if r > 0xffff {
			size = 2
		}
		if units+size > limit {
			return text[:i]
		}
		units += size
	}
	return text
}
