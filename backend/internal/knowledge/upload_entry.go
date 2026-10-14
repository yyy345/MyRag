package knowledge

import (
	"bytes"
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
)

var uploadExtensions = map[string]bool{".txt": true, ".md": true, ".pdf": true, ".docx": true, ".csv": true, ".xlsx": true}

func readUpload(w http.ResponseWriter, r *http.Request) (string, []byte, error) {
	const maxUploadBytes = ports.MaxDocumentBytes + 1024*1024
	if r.ContentLength > maxUploadBytes {
		return "", nil, errors.New("上传请求过大，文件最大20MB")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	reader, err := r.MultipartReader()
	if err != nil {
		return "", nil, errors.New("请选择文件，使用 multipart/form-data 上传")
	}
	var filename string
	var content []byte
	for count := 0; ; count++ {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", nil, uploadReadError(err)
		}
		if count >= 16 {
			return "", nil, errors.New("上传表单字段过多")
		}
		if part.FileName() == "" {
			if part.FormName() == "file" {
				return "", nil, errors.New("请选择文件")
			}
			size, err := io.Copy(io.Discard, io.LimitReader(part, 65537))
			if err != nil {
				return "", nil, uploadReadError(err)
			}
			if size > 65536 {
				return "", nil, errors.New("上传表单字段过长")
			}
			continue
		}
		if part.FormName() != "file" || filename != "" {
			return "", nil, errors.New("请仅上传一个 file 文件")
		}
		filename = path.Base(strings.ReplaceAll(part.FileName(), "\\", "/"))
		extension := strings.ToLower(path.Ext(filename))
		if !utf8.ValidString(filename) || utf8.RuneCountInString(filename) > 200 || strings.IndexFunc(filename, unicode.IsControl) >= 0 || strings.TrimSpace(strings.TrimSuffix(filename, path.Ext(filename))) == "" {
			return "", nil, errors.New("文件名无效或超过200字符")
		}
		if !uploadExtensions[extension] {
			return "", nil, errors.New("仅支持 PDF、DOCX、Markdown、TXT、CSV、XLSX 文件")
		}
		content, err = io.ReadAll(io.LimitReader(part, ports.MaxDocumentBytes+1))
		if err != nil {
			return "", nil, uploadReadError(err)
		}
		if len(content) > ports.MaxDocumentBytes {
			return "", nil, ports.ErrDocumentTooLarge
		}
	}
	// MultipartReader stops at the final boundary. Count trailing request bytes
	// so chunked epilogues cannot bypass the total-body limit.
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		return "", nil, uploadReadError(err)
	}
	if filename == "" || len(content) == 0 {
		return "", nil, errors.New("请选择非空文件")
	}
	return filename, content, nil
}

func uploadReadError(err error) error {
	var large *http.MaxBytesError
	if errors.As(err, &large) {
		return errors.New("上传请求过大，文件最大20MB")
	}
	return errors.New("文件上传不完整或表单格式不正确")
}

func (h *Handler) UploadEntry(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.entryScope(w, r)
	if !ok {
		return
	}
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(time.Minute))
	defer controller.SetReadDeadline(time.Time{})
	filename, content, err := readUpload(w, r)
	if err != nil {
		respond(w, 400, 2003, nil, err.Error())
		return
	}
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	store := &repository.Entries{Pool: h.Pool}
	existing, err := store.FindFile(r.Context(), scope, hash)
	if err != nil {
		failure(w, err)
		return
	}
	if existing.ID != "" {
		h.uploadResponse(w, r, scope, existing)
		return
	}
	if h.Parser == nil {
		respond(w, 400, 2003, nil, ports.ErrParserUnavailable.Error())
		return
	}
	var entryID, taskID string
	if err = h.Pool.QueryRow(r.Context(), `SELECT gen_random_uuid()::text,gen_random_uuid()::text`).Scan(&entryID, &taskID); err != nil {
		failure(w, err)
		return
	}
	text, err := h.Parser.Parse(r.Context(), taskID, filename, bytes.NewReader(content))
	if err != nil {
		message := ports.ErrParseRejected.Error()
		switch {
		case errors.Is(err, context.Canceled):
			return
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, ports.ErrParserTimeout):
			message = ports.ErrParserTimeout.Error()
		case errors.Is(err, ports.ErrParserUnavailable):
			message = ports.ErrParserUnavailable.Error()
		case errors.Is(err, ports.ErrDocumentTooLarge):
			message = ports.ErrDocumentTooLarge.Error()
		case errors.Is(err, ports.ErrParsedTextTooLarge):
			message = ports.ErrParsedTextTooLarge.Error()
		}
		respond(w, 400, 2003, nil, message)
		return
	}
	if r.Context().Err() != nil {
		return
	}
	file := repository.FileEntry{ID: entryID, Filename: filename, Hash: hash, Content: text}
	task := ports.IndexTask{SchemaVersion: 1, TaskID: taskID, EntryID: entryID, KBID: scope.KnowledgeBaseID, EntryVersion: 1, TraceID: taskID}
	result, err := store.SaveFileAndTask(r.Context(), scope, file, task)
	if errors.Is(err, repository.ErrNotFound) {
		respond(w, 404, 2001, nil, "知识库不存在")
		return
	}
	if err != nil {
		failure(w, err)
		return
	}
	h.uploadResponse(w, r, scope, result)
}

func (h *Handler) uploadResponse(w http.ResponseWriter, r *http.Request, scope ports.Scope, result repository.UploadResult) {
	h.invalidateUpload(r, scope)
	message := "文件已存在"
	if result.Created {
		message = "ok"
	}
	respond(w, 200, 0, result, message)
}

func (h *Handler) invalidateUpload(r *http.Request, scope ports.Scope) {
	if h.Cache != nil {
		_ = h.Cache.DeletePrefix(r.Context(), "go:entry:"+scope.KnowledgeBaseID+":")
		_ = h.Cache.DeletePrefix(r.Context(), "entry:"+scope.KnowledgeBaseID+":")
		_ = h.Cache.Delete(r.Context(), "analytics:overview:"+scope.UserID, "analytics:kb:"+scope.KnowledgeBaseID, "go:analytics:overview:"+scope.UserID, "go:analytics:kb:"+scope.KnowledgeBaseID)
	}
}
