package knowledge

import (
	"encoding/json"
	"errors"
	"net/http"

	"ai-library/backend/internal/repository"
)

func (h *Handler) UpdateEntry(w http.ResponseWriter, r *http.Request)    { h.editEntry(w, r, false) }
func (h *Handler) SetEntryStatus(w http.ResponseWriter, r *http.Request) { h.editEntry(w, r, true) }
func (h *Handler) editEntry(w http.ResponseWriter, r *http.Request, statusOnly bool) {
	scope, ok := h.entryScope(w, r)
	if !ok {
		return
	}
	body, err := payload(w, r)
	if err != nil {
		failure(w, err)
		return
	}
	p := repository.EntryPatch{}
	for key, raw := range body {
		if key == "expected_version" {
			var v *int64
			if json.Unmarshal(raw, &v) != nil || v == nil || *v < 1 {
				respond(w, 400, 5000, nil, "版本参数不正确")
				return
			}
			p.ExpectedVersion = v
			continue
		}
		if (statusOnly && key != "status") || (!statusOnly && key != "title" && key != "content") {
			respond(w, 400, 5000, nil, "不支持修改此字段")
			return
		}
		value, _, err := textField(body, key)
		if err != nil || value == nil {
			respond(w, 400, 5000, nil, "修改字段必须为字符串")
			return
		}
		switch key {
		case "title":
			p.Title = value
		case "content":
			p.Content = value
		case "status":
			p.Status = value
		}
	}
	err = (&repository.Entries{Pool: h.Pool}).EditEntry(r.Context(), scope, r.PathValue("eid"), p)
	if errors.Is(err, repository.ErrInvalid) {
		respond(w, 400, 5000, nil, "标题、正文或状态无效，文件标题须保留扩展名")
		return
	}
	if errors.Is(err, repository.ErrConflict) {
		respond(w, 409, 5000, nil, "条目已更新，请刷新后重试")
		return
	}
	if err != nil {
		entryFailure(w, err)
		return
	}
	if h.Cache != nil {
		for _, prefix := range []string{"go:entry:" + scope.KnowledgeBaseID + ":", "entry:" + scope.KnowledgeBaseID + ":"} {
			_ = h.Cache.DeletePrefix(r.Context(), prefix)
		}
		_ = h.Cache.Delete(r.Context(), "go:entry:detail:"+r.PathValue("eid"), "entry:detail:"+r.PathValue("eid"), "go:analytics:overview:"+scope.UserID, "analytics:overview:"+scope.UserID, "go:analytics:kb:"+scope.KnowledgeBaseID, "analytics:kb:"+scope.KnowledgeBaseID)
	}
	entry, err := h.readEntry(r, scope, r.PathValue("eid"))
	if err != nil {
		entryFailure(w, err)
		return
	}
	respond(w, 200, 0, entry, "ok")
}
