package knowledge

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"ai-library/backend/internal/repository"
)

func (h *Handler) DeleteEntry(w http.ResponseWriter, r *http.Request)   { h.deleteEntries(w, r, false) }
func (h *Handler) DeleteEntries(w http.ResponseWriter, r *http.Request) { h.deleteEntries(w, r, true) }
func (h *Handler) deleteEntries(w http.ResponseWriter, r *http.Request, batch bool) {
	scope, ok := h.entryScope(w, r)
	if !ok {
		return
	}
	ids := []string{r.PathValue("eid")}
	if batch {
		body, err := payload(w, r)
		if err != nil {
			failure(w, err)
			return
		}
		if len(body) != 1 || json.Unmarshal(body["ids"], &ids) != nil {
			respond(w, 400, 5000, nil, "请提供条目ID列表")
			return
		}
	}
	err := (&repository.Entries{Pool: h.Pool}).DeleteEntries(r.Context(), scope, ids)
	if errors.Is(err, repository.ErrInvalid) {
		respond(w, 400, 5000, nil, "需提供1至100个有效条目ID")
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
		keys := []string{"go:analytics:overview:" + scope.UserID, "analytics:overview:" + scope.UserID, "go:analytics:kb:" + scope.KnowledgeBaseID, "analytics:kb:" + scope.KnowledgeBaseID}
		for _, id := range ids {
			id = strings.ToLower(id)
			keys = append(keys, "go:entry:detail:"+id, "entry:detail:"+id)
		}
		_ = h.Cache.Delete(r.Context(), keys...)
	}
	respond(w, 200, 0, nil, "ok")
}
