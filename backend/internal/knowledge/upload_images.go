package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"ai-library/backend/internal/ports"
	"ai-library/backend/internal/repository"
	"ai-library/backend/internal/storage"
)

type uploadedImage struct {
	name, media string
	data        []byte
}

func readImages(w http.ResponseWriter, r *http.Request) ([]uploadedImage, error) {
	const totalLimit = 10*ports.MaxImageBytes + 1024*1024
	if r.ContentLength > totalLimit {
		return nil, ports.ErrInvalidImage
	}
	r.Body = http.MaxBytesReader(w, r.Body, totalLimit)
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, ports.ErrInvalidImage
	}
	var images []uploadedImage
	for count := 0; ; count++ {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil || count >= 16 {
			return nil, ports.ErrInvalidImage
		}
		if part.FileName() == "" {
			bytes, err := io.Copy(io.Discard, io.LimitReader(part, 65537))
			part.Close()
			if err != nil || bytes > 65536 {
				return nil, ports.ErrInvalidImage
			}
			continue
		}
		if part.FormName() != "images" || len(images) >= 10 {
			part.Close()
			return nil, ports.ErrInvalidImage
		}
		name := path.Base(strings.ReplaceAll(part.FileName(), "\\", "/"))
		if !utf8.ValidString(name) || strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > 200 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
			part.Close()
			return nil, ports.ErrInvalidImage
		}
		data, err := io.ReadAll(io.LimitReader(part, ports.MaxImageBytes+1))
		part.Close()
		media := part.Header.Get("Content-Type")
		if err != nil || storage.ValidateImage(data, media) != nil {
			return nil, ports.ErrInvalidImage
		}
		images = append(images, uploadedImage{name, media, data})
	}
	if _, err = io.Copy(io.Discard, r.Body); err != nil || len(images) == 0 {
		return nil, ports.ErrInvalidImage
	}
	return images, nil
}

func (h *Handler) UploadImages(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.entryScope(w, r)
	if !ok {
		return
	}
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(time.Minute))
	defer controller.SetReadDeadline(time.Time{})
	images, err := readImages(w, r)
	if err != nil {
		respond(w, 400, 2003, nil, ports.ErrInvalidImage.Error())
		return
	}
	if h.Images == nil || h.Vision == nil {
		respond(w, 400, 2003, nil, "图片服务尚未配置")
		return
	}
	store := &repository.Entries{Pool: h.Pool}
	results := make([]map[string]string, 0, len(images))
	for _, image := range images {
		if r.Context().Err() != nil {
			return
		}
		sum := sha256.Sum256(image.data)
		hash := hex.EncodeToString(sum[:])
		existing, err := store.FindFile(r.Context(), scope, hash)
		if err != nil {
			failure(w, err)
			return
		}
		if existing.ID != "" {
			results = append(results, map[string]string{"entry_id": existing.ID, "title": image.name})
			continue
		}
		var entry, task string
		if err = h.Pool.QueryRow(r.Context(), `SELECT gen_random_uuid()::text,gen_random_uuid()::text`).Scan(&entry, &task); err != nil {
			failure(w, err)
			return
		}
		if err = store.ReserveImage(r.Context(), scope, entry); err != nil {
			entryFailure(w, err)
			return
		}
		process, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		link, processErr := h.Images.PutImage(process, scope.UserID, entry, image.data, image.media)
		var description string
		if processErr == nil {
			description, processErr = h.Vision.DescribeImage(process, task, image.data, image.media)
		}
		cancel()
		file := repository.FileEntry{ID: entry, Filename: image.name, Hash: hash, Content: description + "\n\n![](" + link + ")", Image: true}
		var result repository.UploadResult
		if processErr == nil && r.Context().Err() == nil {
			event := ports.IndexTask{SchemaVersion: 1, TaskID: task, EntryID: entry, KBID: scope.KnowledgeBaseID, EntryVersion: 1, TraceID: task}
			result, err = store.SaveFileAndTask(r.Context(), scope, file, event)
		} else if r.Context().Err() == nil {
			result, err = store.SaveFailedImage(r.Context(), scope, file)
		}
		// Even ambiguous SQL commit outcomes are safe: a successful attachment
		// consumed the job atomically. Otherwise the orphan is still scheduled.
		if processErr != nil || err != nil || !result.Created || r.Context().Err() != nil {
			cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
			_ = store.AbandonImage(cleanup, scope.UserID, entry)
			done()
		}
		if r.Context().Err() != nil {
			return
		}
		if errors.Is(err, repository.ErrNotFound) {
			entryFailure(w, err)
			return
		}
		if err != nil {
			failure(w, err)
			return
		}
		h.invalidateUpload(r, scope)
		results = append(results, map[string]string{"entry_id": result.ID, "title": image.name})
	}
	respond(w, 200, 0, map[string]any{"entries": results}, "ok")
}
