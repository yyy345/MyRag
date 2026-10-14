// Package ports defines application boundaries; adapters must preserve owner scoping.
package ports

import (
	"context"
	"time"
)

type Scope struct{ UserID, KnowledgeBaseID string }
type Entry struct {
	ID, Title, Content string
	Version            int64
}
type IndexTask struct {
	SchemaVersion int    `json:"schema_version"`
	TaskID        string `json:"task_id"`
	EntryID       string `json:"entry_id"`
	KBID          string `json:"kb_id"`
	EntryVersion  int64  `json:"entry_version"`
	TraceID       string `json:"trace_id"`
}

// Repository must commit the entry, version and outbox row in one transaction.
// Updating an entry must lock the same row as the indexing worker.
type Repository interface {
	Authorize(context.Context, Scope) error
	GetEntry(context.Context, Scope, string) (Entry, error)
	SaveEntryAndTask(context.Context, Scope, Entry, IndexTask) error
}

// Cache is an optimization, never a substitute for an ownership check.
type Cache interface {
	Get(context.Context, string) ([]byte, error)
	Set(context.Context, string, []byte, time.Duration) error
	Delete(context.Context, ...string) error
}

// Publisher returns only after broker confirmation; outbox marks sent afterwards.
type Publisher interface {
	PublishConfirmed(context.Context, IndexTask) error
}

// AI delegates computation only. Callers retain ownership of business writes.
// Readers must be closed on client disconnect, propagating cancellation upstream.
type AI interface {
	DocumentParser
	ImageDescriber
	ChatStreamer
	Extractor
}
