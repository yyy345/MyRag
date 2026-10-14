package knowledge

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ai-library/backend/internal/auth"
	"ai-library/backend/internal/cache"
	"ai-library/backend/internal/ports"
	"ai-library/backend/internal/repository"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Base struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Description  *string `json:"description"`
	SystemPrompt *string `json:"system_prompt"`
	UserID       string  `json:"user_id"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
}
type Handler struct {
	Pool   *pgxpool.Pool
	Cache  *cache.Store
	Auth   *auth.Service
	Parser ports.DocumentParser
	Images ports.ImageStorage
	Vision ports.ImageDescriber
}

const fields = `id::text,name,description,system_prompt,user_id::text,created_at,updated_at`

var idPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func respond(w http.ResponseWriter, status, code int, data any, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "data": data, "message": message})
}
func failure(w http.ResponseWriter, err error) {
	var app *auth.Error
	if errors.As(err, &app) {
		respond(w, app.Status, app.Code, nil, app.Message)
	} else if errors.Is(err, pgx.ErrNoRows) {
		respond(w, 404, 2001, nil, "知识库不存在")
	} else {
		respond(w, 500, 5000, nil, "服务器内部错误")
	}
}
func scan(row pgx.Row) (Base, error) {
	var base Base
	var created, updated time.Time
	err := row.Scan(&base.ID, &base.Name, &base.Description, &base.SystemPrompt, &base.UserID, &created, &updated)
	base.CreatedAt = created.UTC().Format("2006-01-02T15:04:05.000Z")
	base.UpdatedAt = updated.UTC().Format("2006-01-02T15:04:05.000Z")
	return base, err
}
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/knowledge-bases/{id}/entries/upload", h.UploadEntry)
	mux.HandleFunc("POST /api/knowledge-bases/{id}/entries/upload-images", h.UploadImages)
	mux.HandleFunc("DELETE /api/knowledge-bases/{id}/entries/{eid}", h.DeleteEntry)
	mux.HandleFunc("DELETE /api/knowledge-bases/{id}/entries", h.DeleteEntries)
	mux.HandleFunc("PUT /api/knowledge-bases/{id}/entries/{eid}", h.UpdateEntry)
	mux.HandleFunc("PATCH /api/knowledge-bases/{id}/entries/{eid}/status", h.SetEntryStatus)
	mux.HandleFunc("POST /api/knowledge-bases/{id}/entries", h.CreateEntry)
	mux.HandleFunc("GET /api/knowledge-bases/{id}/entries", h.ListEntries)
	mux.HandleFunc("GET /api/knowledge-bases/{id}/entries/{eid}", h.GetEntry)
	mux.HandleFunc("POST /api/knowledge-bases", h.Create)
	mux.HandleFunc("GET /api/knowledge-bases", h.List)
	mux.HandleFunc("GET /api/knowledge-bases/{id}", h.Get)
	mux.HandleFunc("PUT /api/knowledge-bases/{id}", h.Update)
	mux.HandleFunc("DELETE /api/knowledge-bases/{id}", h.Delete)
}
func (h *Handler) owner(w http.ResponseWriter, r *http.Request) (string, bool) {
	claims, err := h.Auth.Authenticate(r)
	if err != nil {
		failure(w, err)
		return "", false
	}
	if id := r.PathValue("id"); id != "" && !idPattern.MatchString(id) {
		failure(w, pgx.ErrNoRows)
		return "", false
	}
	return claims.ID, true
}
func prefix(user string) string { return "go:kb:" + user + ":" }
func (h *Handler) invalidate(r *http.Request, user string) {
	if h.Cache != nil {
		_ = h.Cache.DeletePrefix(r.Context(), prefix(user))
	}
}
func (h *Handler) cached(r *http.Request, key string, target any) bool {
	if h.Cache == nil {
		return false
	}
	value, ok := h.Cache.Lookup(r.Context(), key)
	return ok && json.Unmarshal(value, target) == nil
}
func (h *Handler) remember(r *http.Request, key string, value any) {
	if h.Cache == nil {
		return
	}
	data, err := json.Marshal(value)
	if err == nil {
		_ = h.Cache.Set(r.Context(), key, data, 5*time.Minute)
	}
}
func payload(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, error) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	var value map[string]json.RawMessage
	if err := d.Decode(&value); err != nil {
		return nil, &auth.Error{Status: 400, Code: 5000, Message: "请求参数格式不正确"}
	}
	if err := d.Decode(new(any)); err != io.EOF || value == nil {
		return nil, &auth.Error{Status: 400, Code: 5000, Message: "请求参数格式不正确"}
	}
	return value, nil
}
func textField(body map[string]json.RawMessage, key string) (*string, bool, error) {
	raw, exists := body[key]
	if !exists {
		return nil, false, nil
	}
	var value *string
	if json.Unmarshal(raw, &value) != nil {
		return nil, true, &auth.Error{Status: 400, Code: 5000, Message: "字段格式不正确"}
	}
	return value, true, nil
}
func input(w http.ResponseWriter, r *http.Request, create bool) (*string, *string, *string, bool, bool, error) {
	body, err := payload(w, r)
	if err != nil {
		return nil, nil, nil, false, false, err
	}
	name, nameSet, err := textField(body, "name")
	if err != nil {
		return nil, nil, nil, false, false, err
	}
	if create || nameSet {
		if name == nil || strings.TrimSpace(*name) == "" || len([]rune(*name)) > 200 {
			return nil, nil, nil, false, false, &auth.Error{Status: 400, Code: 5000, Message: "知识库名称不能为空且不能超过200个字符"}
		}
		trimmed := strings.TrimSpace(*name)
		name = &trimmed
	}
	description, descSet, err := textField(body, "description")
	if err != nil {
		return nil, nil, nil, false, false, err
	}
	prompt, promptSet, err := textField(body, "system_prompt")
	if err != nil {
		return nil, nil, nil, false, false, err
	}
	if (description != nil && len(*description) > 20000) || (prompt != nil && len(*prompt) > 20000) {
		return nil, nil, nil, false, false, &auth.Error{Status: 400, Code: 5000, Message: "知识库说明过长"}
	}
	return name, description, prompt, descSet, promptSet, nil
}
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	user, ok := h.owner(w, r)
	if !ok {
		return
	}
	name, description, prompt, _, _, err := input(w, r, true)
	if err != nil {
		failure(w, err)
		return
	}
	base, err := scan(h.Pool.QueryRow(r.Context(), `INSERT INTO knowledge_base(id,user_id,name,description,system_prompt,updated_at) VALUES (gen_random_uuid(),$1::uuid,$2,$3,$4,now()) RETURNING `+fields, user, name, description, prompt))
	if err != nil {
		failure(w, err)
		return
	}
	h.invalidate(r, user)
	respond(w, 200, 0, base, "ok")
}
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	user, ok := h.owner(w, r)
	if !ok {
		return
	}
	page, size := 1, 10
	for key, target := range map[string]*int{"page": &page, "size": &size} {
		if raw := r.URL.Query().Get(key); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 1 || value > 1000000 {
				failure(w, &auth.Error{Status: 400, Code: 5000, Message: "分页参数不正确"})
				return
			}
			*target = value
		}
	}
	if size > 100 {
		failure(w, &auth.Error{Status: 400, Code: 5000, Message: "每页最多100条"})
		return
	}
	key := prefix(user) + "list:" + strconv.Itoa(page) + ":" + strconv.Itoa(size)
	var result struct {
		Total   int    `json:"total"`
		Records []Base `json:"records"`
		Current int    `json:"current"`
		Size    int    `json:"size"`
	}
	if h.cached(r, key, &result) {
		respond(w, 200, 0, result, "ok")
		return
	}
	result.Current = page
	result.Size = size
	result.Records = []Base{}
	if err := h.Pool.QueryRow(r.Context(), `SELECT count(*) FROM knowledge_base WHERE user_id=$1::uuid`, user).Scan(&result.Total); err != nil {
		failure(w, err)
		return
	}
	rows, err := h.Pool.Query(r.Context(), `SELECT `+fields+` FROM knowledge_base WHERE user_id=$1::uuid ORDER BY updated_at DESC,id DESC LIMIT $2 OFFSET $3`, user, size, (page-1)*size)
	if err != nil {
		failure(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		base, err := scan(rows)
		if err != nil {
			failure(w, err)
			return
		}
		result.Records = append(result.Records, base)
	}
	if rows.Err() != nil {
		failure(w, rows.Err())
		return
	}
	if result.Total > 0 {
		h.remember(r, key, result)
	}
	respond(w, 200, 0, result, "ok")
}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	user, ok := h.owner(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	// Authorization always hits the database before any cached content is read.
	var authorized bool
	err := h.Pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM knowledge_base WHERE id=$1::uuid AND user_id=$2::uuid)`, id, user).Scan(&authorized)
	if err != nil {
		failure(w, err)
		return
	}
	if !authorized {
		failure(w, pgx.ErrNoRows)
		return
	}
	key := prefix(user) + "detail:" + id
	var base Base
	if h.cached(r, key, &base) {
		respond(w, 200, 0, base, "ok")
		return
	}
	base, err = scan(h.Pool.QueryRow(r.Context(), `SELECT `+fields+` FROM knowledge_base WHERE id=$1::uuid AND user_id=$2::uuid`, id, user))
	if err != nil {
		failure(w, err)
		return
	}
	h.remember(r, key, base)
	respond(w, 200, 0, base, "ok")
}
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	user, ok := h.owner(w, r)
	if !ok {
		return
	}
	name, description, prompt, descSet, promptSet, err := input(w, r, false)
	if err != nil {
		failure(w, err)
		return
	}
	base, err := scan(h.Pool.QueryRow(r.Context(), `UPDATE knowledge_base SET name=COALESCE($3,name),description=CASE WHEN $4 THEN $5::text ELSE description END,system_prompt=CASE WHEN $6 THEN $7::text ELSE system_prompt END,updated_at=now() WHERE id=$1::uuid AND user_id=$2::uuid RETURNING `+fields, r.PathValue("id"), user, name, descSet, description, promptSet, prompt))
	if err != nil {
		failure(w, err)
		return
	}
	h.invalidate(r, user)
	respond(w, 200, 0, base, "ok")
}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	user, ok := h.owner(w, r)
	if !ok {
		return
	}
	err := (&repository.Entries{Pool: h.Pool}).DeleteBase(r.Context(), ports.Scope{UserID: user, KnowledgeBaseID: r.PathValue("id")})
	if err != nil {
		entryFailure(w, err)
		return
	}
	h.invalidate(r, user)
	respond(w, 200, 0, nil, "ok")
}
