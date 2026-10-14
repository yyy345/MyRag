package repository

import (
	"ai-library/backend/internal/ports"
	"context"
	"github.com/jackc/pgx/v5"
)

type HotEntry struct {
	Title  string `json:"entry_title"`
	KBName string `json:"kb_name,omitempty"`
	Hits   int64  `json:"total_hits"`
	Status string `json:"status,omitempty"`
}
type KBActivity struct {
	ID      string `json:"kb_id"`
	Name    string `json:"kb_name"`
	Hits    int64  `json:"total_hits"`
	Entries int64  `json:"entry_count"`
}
type Summary struct {
	KBs      int64 `json:"kbCount"`
	Entries  int64 `json:"entryCount"`
	Sessions int64 `json:"sessionCount"`
}
type Overview struct {
	Hot      []HotEntry   `json:"hotEntries"`
	Activity []KBActivity `json:"kbActivity"`
	Summary  Summary      `json:"summary"`
}
type KBStats struct {
	Name string     `json:"kbName"`
	Hot  []HotEntry `json:"hotEntries"`
}

func (s *Entries) Overview(ctx context.Context, user string) (Overview, error) {
	value := Overview{Hot: []HotEntry{}, Activity: []KBActivity{}}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return value, err
	}
	defer tx.Rollback(context.Background())
	rows, err := tx.Query(ctx, `SELECT e.title,k.name,sum(c.hit_count)::bigint FROM knowledge_chunk c JOIN knowledge_entry e ON e.id=c.entry_id JOIN knowledge_base k ON k.id=e.kb_id WHERE k.user_id=$1::uuid AND e.status='ENABLED' AND c.hit_count>0 GROUP BY e.id,e.title,k.name ORDER BY sum(c.hit_count) DESC,e.id LIMIT 10`, user)
	if err != nil {
		return value, err
	}
	for rows.Next() {
		var item HotEntry
		if err = rows.Scan(&item.Title, &item.KBName, &item.Hits); err != nil {
			rows.Close()
			return value, err
		}
		value.Hot = append(value.Hot, item)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return value, err
	}
	rows, err = tx.Query(ctx, `SELECT k.id::text,k.name,coalesce(sum(c.hit_count),0)::bigint,count(DISTINCT e.id)::bigint FROM knowledge_base k LEFT JOIN knowledge_entry e ON e.kb_id=k.id LEFT JOIN knowledge_chunk c ON c.entry_id=e.id WHERE k.user_id=$1::uuid GROUP BY k.id,k.name ORDER BY coalesce(sum(c.hit_count),0) DESC,k.id`, user)
	if err != nil {
		return value, err
	}
	for rows.Next() {
		var item KBActivity
		if err = rows.Scan(&item.ID, &item.Name, &item.Hits, &item.Entries); err != nil {
			rows.Close()
			return value, err
		}
		value.Activity = append(value.Activity, item)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return value, err
	}
	err = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM knowledge_base WHERE user_id=$1::uuid),(SELECT count(*) FROM knowledge_entry e JOIN knowledge_base k ON k.id=e.kb_id WHERE k.user_id=$1::uuid),(SELECT count(*) FROM chat_session s JOIN knowledge_base k ON k.id=s.kb_id WHERE s.user_id=$1::uuid AND k.user_id=$1::uuid)`, user).Scan(&value.Summary.KBs, &value.Summary.Entries, &value.Summary.Sessions)
	if err != nil {
		return value, err
	}
	return value, tx.Commit(ctx)
}

func (s *Entries) KBStats(ctx context.Context, scope ports.Scope) (KBStats, error) {
	value := KBStats{Hot: []HotEntry{}}
	if err := s.Authorize(ctx, scope); err != nil {
		return value, err
	}
	err := s.Pool.QueryRow(ctx, `SELECT name FROM knowledge_base WHERE id=$1::uuid AND user_id=$2::uuid`, scope.KnowledgeBaseID, scope.UserID).Scan(&value.Name)
	if err == pgx.ErrNoRows {
		return value, ErrNotFound
	}
	if err != nil {
		return value, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT e.title,sum(c.hit_count)::bigint,e.status::text FROM knowledge_chunk c JOIN knowledge_entry e ON e.id=c.entry_id JOIN knowledge_base k ON k.id=e.kb_id WHERE e.kb_id=$1::uuid AND k.user_id=$2::uuid AND c.hit_count>0 GROUP BY e.id,e.title,e.status ORDER BY sum(c.hit_count) DESC,e.id LIMIT 10`, scope.KnowledgeBaseID, scope.UserID)
	if err != nil {
		return value, err
	}
	defer rows.Close()
	for rows.Next() {
		var item HotEntry
		if err = rows.Scan(&item.Title, &item.Hits, &item.Status); err != nil {
			return value, err
		}
		value.Hot = append(value.Hot, item)
	}
	return value, rows.Err()
}
