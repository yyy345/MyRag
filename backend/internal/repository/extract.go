package repository

import (
	"ai-library/backend/internal/ports"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/jackc/pgx/v5"
	"time"
)

type ExtractedEntry struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}
type ExtractionCheckpoint struct {
	Until time.Time
	Day   string
	Token string
}
type SessionHistory struct {
	ID       string
	Messages []ports.ChatHistory
}

// Latest 500 messages per session retain their original chronological order.
func (s *Entries) ExtractionHistory(ctx context.Context, scope ports.Scope, session string, since, until *time.Time) ([]SessionHistory, error) {
	if err := s.Authorize(ctx, scope); err != nil {
		return nil, err
	}
	if session != "" {
		if _, err := s.AuthorizeSession(ctx, scope, session); err != nil {
			return nil, err
		}
	}
	var selected any
	if session != "" {
		selected = session
	}
	rows, err := s.Pool.Query(ctx, `SELECT session_id::text,role,content FROM (SELECT m.session_id,m.role::text,m.content,m.created_at,m.id,row_number() OVER(PARTITION BY m.session_id ORDER BY m.created_at DESC,m.id DESC) n FROM chat_message m JOIN chat_session s ON s.id=m.session_id JOIN knowledge_base k ON k.id=s.kb_id WHERE s.kb_id=$1::uuid AND s.user_id=$2::uuid AND k.user_id=$2::uuid AND ($3::uuid IS NULL OR s.id=$3::uuid) AND ($4::timestamp IS NULL OR m.created_at>$4::timestamp) AND ($5::timestamp IS NULL OR m.created_at<=$5::timestamp)) recent WHERE n<=500 ORDER BY session_id,created_at,id`, scope.KnowledgeBaseID, scope.UserID, selected, since, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []SessionHistory{}
	for rows.Next() {
		var id, role, content string
		if err = rows.Scan(&id, &role, &content); err != nil {
			return nil, err
		}
		if len(values) == 0 || values[len(values)-1].ID != id {
			if len(values) >= 100 {
				return nil, ErrInvalid
			}
			values = append(values, SessionHistory{ID: id})
		}
		if role == "USER" {
			role = "user"
		} else {
			role = "assistant"
		}
		values[len(values)-1].Messages = append(values[len(values)-1].Messages, ports.ChatHistory{Role: role, Content: content})
	}
	return values, rows.Err()
}

// Optional checkpoint is advanced in the same transaction as entries/outbox.
func (s *Entries) SaveExtracted(ctx context.Context, scope ports.Scope, sections []ports.KnowledgeSection, checkpoint *ExtractionCheckpoint) ([]ExtractedEntry, error) {
	result := []ExtractedEntry{}
	if len(sections) > 2000 {
		return result, ErrInvalid
	}
	for _, item := range sections {
		if !item.Valid() {
			return result, ErrInvalid
		}
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
	if checkpoint != nil {
		var token *string
		err = tx.QueryRow(ctx, `SELECT lease_token::text FROM extract_checkpoint WHERE kb_id=$1::uuid FOR UPDATE`, kb).Scan(&token)
		if err != nil {
			return nil, err
		}
		if token == nil || *token != checkpoint.Token {
			return nil, ErrConflict
		}
	}
	for _, item := range sections {
		hash := sha256.Sum256([]byte(item.Content))
		hashed := hex.EncodeToString(hash[:])
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM knowledge_entry WHERE kb_id=$1::uuid AND (title=$2 OR source_file_hash=$3))`, kb, item.Title, hashed).Scan(&exists); err != nil {
			return nil, err
		}
		if exists {
			continue
		}
		var id, task string
		if err = tx.QueryRow(ctx, `SELECT gen_random_uuid()::text,gen_random_uuid()::text`).Scan(&id, &task); err != nil {
			return nil, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO knowledge_entry(id,kb_id,title,content,type,entry_version,source_file_hash,updated_at) VALUES($1::uuid,$2::uuid,$3,$4,'MANUAL',1,$5,now())`, id, kb, item.Title, item.Content, hashed)
		if err != nil {
			return nil, err
		}
		if err = saveIndexTask(ctx, tx, ports.IndexTask{SchemaVersion: 1, TaskID: task, EntryID: id, KBID: kb, EntryVersion: 1, TraceID: task}); err != nil {
			return nil, err
		}
		result = append(result, ExtractedEntry{ID: id, Title: item.Title})
	}
	if checkpoint != nil {
		_, err = tx.Exec(ctx, `UPDATE extract_checkpoint SET last_message_at=greatest(last_message_at,$2::timestamp),completed_day=$3::date,lease_token=NULL,lease_until=NULL,attempts=0,last_error=NULL,next_attempt_at=$2,updated_at=now() WHERE kb_id=$1::uuid`, kb, checkpoint.Until, checkpoint.Day)
		if err != nil {
			return nil, err
		}
	}
	return result, tx.Commit(ctx)
}
