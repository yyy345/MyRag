package ports

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"unicode/utf8"
)

var ErrInvalidResult = errors.New("invalid index result")
var resultUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type IndexResult struct {
	SchemaVersion int     `json:"schema_version"`
	TaskID        string  `json:"task_id"`
	EntryID       string  `json:"entry_id"`
	EntryVersion  int64   `json:"entry_version"`
	TraceID       string  `json:"trace_id"`
	Status        string  `json:"status"`
	ChunkCount    int     `json:"chunk_count"`
	ErrorCode     *string `json:"error_code"`
}

func (v IndexResult) Validate() error {
	if v.SchemaVersion != 1 || !resultUUID.MatchString(v.TaskID) || !resultUUID.MatchString(v.EntryID) || v.EntryVersion < 1 || utf8.RuneCountInString(v.TraceID) < 1 || utf8.RuneCountInString(v.TraceID) > 128 || v.ChunkCount < 0 {
		return ErrInvalidResult
	}
	switch v.Status {
	case "FAILED":
		if v.ErrorCode == nil || *v.ErrorCode == "" || v.ChunkCount != 0 {
			return ErrInvalidResult
		}
	case "COMPLETED", "STALE":
		if v.ErrorCode != nil {
			return ErrInvalidResult
		}
	default:
		return ErrInvalidResult
	}
	return nil
}

func DecodeIndexResult(body []byte) (IndexResult, error) {
	var value IndexResult
	if len(body) > 65536 || !utf8.Valid(body) {
		return value, ErrInvalidResult
	}
	// Check required fields and reject duplicate keys before decoding typed values.
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return value, ErrInvalidResult
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return value, ErrInvalidResult
		}
		name, ok := key.(string)
		if !ok {
			return value, ErrInvalidResult
		}
		if _, ok = fields[name]; ok {
			return value, ErrInvalidResult
		}
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil {
			return value, ErrInvalidResult
		}
		fields[name] = raw
	}
	if _, err = decoder.Token(); err != nil {
		return value, ErrInvalidResult
	}
	if decoder.Decode(new(any)) != io.EOF {
		return value, ErrInvalidResult
	}
	for _, key := range []string{"schema_version", "task_id", "entry_id", "entry_version", "trace_id", "status", "chunk_count", "error_code"} {
		raw, ok := fields[key]
		if !ok || (key != "error_code" && bytes.Equal(bytes.TrimSpace(raw), []byte("null"))) {
			return value, ErrInvalidResult
		}
	}
	decoder = json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil {
		return value, ErrInvalidResult
	}
	return value, value.Validate()
}
