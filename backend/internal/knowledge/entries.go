package knowledge

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-library/backend/internal/ports"
	"ai-library/backend/internal/repository"
	"github.com/jackc/pgx/v5"
)

type Entry struct {
	ID                string  `json:"id"`
	KBID              string  `json:"kb_id"`
	Title             string  `json:"title"`
	Content           string  `json:"content"`
	Type              string  `json:"type"`
	Status            string  `json:"status"`
	ProcessingStatus  string  `json:"processing_status"`
	ProcessingMessage *string `json:"processing_message"`
	SourceFileName    *string `json:"source_file_name"`
	SourceFileHash    *string `json:"source_file_hash"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
}

const entryFields = `e.id::text,e.kb_id::text,e.title,e.content,e.type::text,e.status::text,e.processing_status::text,e.processing_message,e.source_file_name,e.source_file_hash,e.created_at,e.updated_at`

func scanEntry(row pgx.Row) (Entry, error) {
	var e Entry
	var created, updated time.Time
	err := row.Scan(&e.ID, &e.KBID, &e.Title, &e.Content, &e.Type, &e.Status, &e.ProcessingStatus, &e.ProcessingMessage, &e.SourceFileName, &e.SourceFileHash, &created, &updated)
	e.CreatedAt = created.UTC().Format("2006-01-02T15:04:05.000Z")
	e.UpdatedAt = updated.UTC().Format("2006-01-02T15:04:05.000Z")
	return e, err
}
func (h *Handler) entryScope(w http.ResponseWriter, r *http.Request) (ports.Scope, bool) {
	user, ok := h.owner(w, r)
	if !ok {
		return ports.Scope{}, false
	}
	scope := ports.Scope{UserID: user, KnowledgeBaseID: strings.ToLower(r.PathValue("id"))}
	err := (&repository.Entries{Pool: h.Pool}).Authorize(r.Context(), scope)
	if errors.Is(err, repository.ErrNotFound) {
		failure(w, pgx.ErrNoRows)
		return scope, false
	}
	if err != nil {
		failure(w, err)
		return scope, false
	}
	return scope, true
}
func (h *Handler) readEntry(r *http.Request, scope ports.Scope, id string) (Entry, error) {
	return scanEntry(h.Pool.QueryRow(r.Context(), `SELECT `+entryFields+` FROM knowledge_entry e JOIN knowledge_base k ON k.id=e.kb_id WHERE e.id=$1::uuid AND e.kb_id=$2::uuid AND k.user_id=$3::uuid`, id, scope.KnowledgeBaseID, scope.UserID))
}
func entryFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, repository.ErrNotFound) {
		respond(w, 404, 2002, nil, "知识条目不存在")
		return
	}
	failure(w, err)
}
func (h *Handler) CreateEntry(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.entryScope(w, r)
	if !ok {
		return
	}
	body, err := payload(w, r)
	if err != nil {
		failure(w, err)
		return
	}
	title, _, err := textField(body, "title")
	if err != nil {
		failure(w, err)
		return
	}
	content, _, err := textField(body, "content")
	if err != nil {
		failure(w, err)
		return
	}
	if title == nil || content == nil || strings.TrimSpace(*title) == "" || strings.TrimSpace(*content) == "" || len([]rune(*title)) > 200 || strings.ContainsRune(*title, 0) || strings.ContainsRune(*content, 0) {
		respond(w, 400, 5000, nil, "标题和正文不能为空，标题最多200字符，不能包含NUL字符")
		return
	}
	// Optional UUID idempotency key prevents duplicate tasks on retried POSTs.
	// Ordinary manual entries retain legacy behavior: identical text may be saved.
	id := strings.ToLower(r.Header.Get("Idempotency-Key"))
	if id != "" && !idPattern.MatchString(id) {
		respond(w, 400, 5000, nil, "Idempotency-Key 必须为UUID")
		return
	}
	var taskID string
	if id == "" {
		err = h.Pool.QueryRow(r.Context(), `SELECT gen_random_uuid()::text`).Scan(&id)
		if err != nil {
			failure(w, err)
			return
		}
	}
	if err = h.Pool.QueryRow(r.Context(), `SELECT gen_random_uuid()::text`).Scan(&taskID); err != nil {
		failure(w, err)
		return
	}
	entry := ports.Entry{ID: id, Title: strings.TrimSpace(*title), Content: *content}
	task := ports.IndexTask{SchemaVersion: 1, TaskID: taskID, EntryID: id, KBID: scope.KnowledgeBaseID, EntryVersion: 1, TraceID: taskID}
	err = (&repository.Entries{Pool: h.Pool}).SaveEntryAndTask(r.Context(), scope, entry, task)
	if errors.Is(err, repository.ErrConflict) && r.Header.Get("Idempotency-Key") != "" {
		existing, readErr := h.readEntry(r, scope, id)
		if readErr == nil && existing.Type == "MANUAL" && existing.Title == entry.Title && existing.Content == entry.Content {
			respond(w, 200, 0, existing, "ok")
			return
		}
		respond(w, 409, 5000, nil, "请求标识已使用，请勿用于不同条目")
		return
	}
	if err != nil {
		entryFailure(w, err)
		return
	}
	// Reads below intentionally go directly to SQL so processing-state polling
	// remains correct even when Redis invalidation is unavailable.
	if h.Cache != nil {
		_ = h.Cache.DeletePrefix(r.Context(), "go:entry:"+scope.KnowledgeBaseID+":")
		_ = h.Cache.DeletePrefix(r.Context(), "entry:"+scope.KnowledgeBaseID+":")
		_ = h.Cache.Delete(r.Context(), "analytics:overview:"+scope.UserID, "analytics:kb:"+scope.KnowledgeBaseID, "go:analytics:overview:"+scope.UserID, "go:analytics:kb:"+scope.KnowledgeBaseID)
	}
	saved, err := h.readEntry(r, scope, id)
	if err != nil {
		entryFailure(w, err)
		return
	}
	respond(w, 200, 0, saved, "ok")
}
func (h *Handler) GetEntry(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.entryScope(w, r)
	if !ok {
		return
	}
	id := r.PathValue("eid")
	if !idPattern.MatchString(id) {
		entryFailure(w, pgx.ErrNoRows)
		return
	}
	entry, err := h.readEntry(r, scope, id)
	if err != nil {
		entryFailure(w, err)
		return
	}
	respond(w, 200, 0, entry, "ok")
}
func (h *Handler) ListEntries(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.entryScope(w, r)
	if !ok {
		return
	}
	page, size := 1, 10
	for key, target := range map[string]*int{"page": &page, "size": &size} {
		if raw := r.URL.Query().Get(key); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 1 || value > 1000000 {
				respond(w, 400, 5000, nil, "分页参数不正确")
				return
			}
			*target = value
		}
	}
	status, search := r.URL.Query().Get("status"), r.URL.Query().Get("search")
	if size > 100 || (status != "" && status != "ENABLED" && status != "DISABLED") || len(search) > 1000 || strings.ContainsRune(search, 0) {
		respond(w, 400, 5000, nil, "分页或筛选参数不正确")
		return
	}
	var result struct {
		Total   int     `json:"total"`
		Records []Entry `json:"records"`
		Current int     `json:"current"`
		Size    int     `json:"size"`
	}
	result.Records = []Entry{}
	result.Current = page
	result.Size = size
	// strpos implements literal case-insensitive substring matching, including %/_.
	const where = ` FROM knowledge_entry e JOIN knowledge_base k ON k.id=e.kb_id WHERE e.kb_id=$1::uuid AND k.user_id=$2::uuid AND ($3='' OR e.status::text=$3) AND ($4='' OR strpos(lower(e.title),lower($4))>0 OR strpos(lower(e.content),lower($4))>0)`
	args := []any{scope.KnowledgeBaseID, scope.UserID, status, search}
	if err := h.Pool.QueryRow(r.Context(), `SELECT count(*)`+where, args...).Scan(&result.Total); err != nil {
		failure(w, err)
		return
	}
	rows, err := h.Pool.Query(r.Context(), `SELECT `+entryFields+where+` ORDER BY e.updated_at DESC,e.id DESC LIMIT $5 OFFSET $6`, append(args, size, (page-1)*size)...)
	if err != nil {
		failure(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		entry, err := scanEntry(rows)
		if err != nil {
			failure(w, err)
			return
		}
		result.Records = append(result.Records, entry)
	}
	if err = rows.Err(); err != nil {
		failure(w, err)
		return
	}
	respond(w, 200, 0, result, "ok")
}
