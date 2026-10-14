package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"ai-library/backend/internal/ai"
	"ai-library/backend/internal/cache"
	"ai-library/backend/internal/chat"
	"ai-library/backend/internal/ports"
	"ai-library/backend/internal/queue"
	"ai-library/backend/internal/repository"
	"github.com/jackc/pgx/v5"
	amqp "github.com/rabbitmq/amqp091-go"
)

func TestIndexFlowGoPythonBrokerIntegration(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" || os.Getenv("RUN_REDIS_TESTS") != "1" || os.Getenv("RUN_RABBITMQ_TESTS") != "1" || os.Getenv("RUN_PYTHON_TESTS") != "1" {
		t.Skip("requires all isolated services and Python")
	}
	ctx := context.Background()
	const endpoint = "postgresql://postgres:migration-test-only@127.0.0.1:55432/"
	const broker = "amqp://guest:guest@127.0.0.1:55673/"
	admin, err := repository.Open(ctx, endpoint+"postgres")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	name := fmt.Sprintf("migration_c26_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)") })
	f := uploadSetupWithDatabase(t, nil, endpoint+name)
	root, _ := filepath.Abs("../../..")
	python := filepath.Join(root, "ai-service/.venv/bin/python")
	if runtime.GOOS == "windows" {
		python = filepath.Join(root, "ai-service/.venv/Scripts/python.exe")
	}
	command := exec.Command(python, "-u", "tests/serve_index_fixtures.py", endpoint+name)
	command.Dir = filepath.Join(root, "ai-service")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { command.Process.Kill(); command.Wait() })
	var fixture struct {
		URL     string `json:"url"`
		Tasks   string `json:"task_queue"`
		Results string `json:"result_queue"`
		Image   []byte `json:"image"`
		Files   []struct {
			Name string `json:"filename"`
			Data []byte `json:"content"`
		} `json:"files"`
	}
	decoded := make(chan error, 1)
	go func() { decoded <- json.NewDecoder(stdout).Decode(&fixture) }()
	select {
	case err = <-decoded:
		if err != nil {
			t.Fatal("index harness startup", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("index harness timeout")
	}
	ready := false
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		response, err := http.Get(fixture.URL + "/health/live")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("local index harness not ready", stderr.String())
	}
	t.Cleanup(func() {
		conn, err := amqp.Dial(broker)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		ch, _ := conn.Channel()
		defer ch.Close()
		for _, name := range []string{fixture.Tasks, fixture.Tasks + ".retry", fixture.Tasks + ".dead", fixture.Results, fixture.Results + ".dead"} {
			_, _ = ch.QueueDelete(name, false, false, false)
		}
	})
	f.h.Parser, err = ai.NewParser(fixture.URL, uploadTestToken, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	f.h.Vision, err = ai.NewVision(fixture.URL, uploadTestToken, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	f.h.Images = &mockImages{}
	var ids []string
	for _, file := range fixture.Files {
		result := f.postOK(t, f.kb, f.token, file.Name, file.Data, 200)
		ids = append(ids, result.Data.ID)
	}
	// Manual entry follows the same outbox/indexing route.
	request, _ := http.NewRequest("POST", f.server.URL+"/api/knowledge-bases/"+f.kb+"/entries", strings.NewReader(`{"title":"手动文本","content":"手动知识内容🙂"}`))
	request.Header.Set("Authorization", "Bearer "+f.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := f.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var created struct{ Data struct{ ID string } }
	err = json.NewDecoder(response.Body).Decode(&created)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || created.Data.ID == "" {
		t.Fatal("manual entry", err, response.StatusCode)
	}
	ids = append(ids, created.Data.ID)
	status, images, err := f.images(ctx, f.kb, f.token, []uploadedImage{{"green.png", "image/png", fixture.Image}})
	if err != nil || status != 200 || len(images) != 1 {
		t.Fatal("image", status, err)
	}
	ids = append(ids, images[0]["entry_id"])
	failed := f.postOK(t, f.kb, f.token, "failure.txt", []byte("MOCK_EMBEDDING_FAILURE"), 200).Data.ID
	store := &repository.Entries{Pool: f.h.Pool}
	consumer := queue.NewResults(broker, fixture.Results)
	consumeCtx, cancel := context.WithCancel(ctx)
	finished := make(chan error, 1)
	go func() {
		finished <- consumer.Run(consumeCtx, func(ctx context.Context, result ports.IndexResult) error {
			return store.ApplyResult(ctx, result, f.h.Cache)
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("result consumer did not stop")
		}
	})
	for _, id := range ids {
		_ = f.h.Cache.Set(ctx, "go:entry:detail:"+id, []byte("stale"), time.Minute)
	}
	publisher := queue.NewPublisher(broker, fixture.Tasks)
	for range 100 {
		worked, err := store.DispatchOne(ctx, publisher)
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			break
		}
	}
	for deadline := time.Now().Add(20 * time.Second); ; {
		var complete, fail int
		err = f.h.Pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE processing_status='COMPLETED'),count(*) FILTER(WHERE processing_status='FAILED') FROM knowledge_entry WHERE kb_id=$1::uuid`, f.kb).Scan(&complete, &fail)
		if err != nil {
			t.Fatal(err)
		}
		if complete == len(ids) && fail == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("indexing did not reach terminal status", complete, fail)
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, id := range ids {
		var chunks int
		if err = f.h.Pool.QueryRow(ctx, `SELECT count(*) FROM knowledge_chunk WHERE entry_id=$1::uuid AND vector_dims(embedding)=1024`, id).Scan(&chunks); err != nil || chunks < 1 {
			t.Fatal("indexed chunks", id, chunks, err)
		}
		cached, err := f.h.Cache.Get(ctx, "go:entry:detail:"+id)
		if !errors.Is(err, cache.ErrMiss) || len(cached) != 0 {
			t.Fatal("result cache invalidation", err)
		}
	}
	var tasks, outbox, receipts, failedChunks int
	err = f.h.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM index_task WHERE status IN ('COMPLETED','FAILED')),(SELECT count(*) FROM index_outbox WHERE published_at IS NOT NULL),(SELECT count(*) FROM index_receipt),(SELECT count(*) FROM knowledge_chunk WHERE entry_id=$1::uuid)`, failed).Scan(&tasks, &outbox, &receipts, &failedChunks)
	if err != nil || tasks != 9 || outbox != 9 || receipts != 9 || failedChunks != 0 {
		t.Fatal("task final state", tasks, outbox, receipts, failedChunks, err)
	}
	// Editing a failed entry creates a new version/task and provides a concrete
	// retry path without reusing the failed receipt or invoking a real model.
	request, _ = http.NewRequest("PUT", f.server.URL+"/api/knowledge-bases/"+f.kb+"/entries/"+failed, strings.NewReader(`{"content":"修正后重新索引"}`))
	request.Header.Set("Authorization", "Bearer "+f.token)
	request.Header.Set("Content-Type", "application/json")
	response, err = f.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("failed entry retry edit", response.StatusCode)
	}
	if worked, err := store.DispatchOne(ctx, publisher); !worked || err != nil {
		t.Fatal("retry outbox", worked, err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; {
		var state string
		var version int
		err = f.h.Pool.QueryRow(ctx, `SELECT processing_status::text,entry_version FROM knowledge_entry WHERE id=$1::uuid`, failed).Scan(&state, &version)
		if err != nil {
			t.Fatal(err)
		}
		if state == "COMPLETED" && version == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retry did not complete", state, version)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Real Go -> Python -> framework SDK SSE, with generated model responses.
	streamer, err := ai.NewStreamer(fixture.URL, uploadTestToken, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	chatHandler := &chat.Handler{Store: store, Auth: f.h.Auth, Cache: f.h.Cache, Streamer: streamer}
	chatHandler.Extractor, err = ai.NewExtractor(fixture.URL, uploadTestToken, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	chatHandler.Register(f.server.Config.Handler.(*http.ServeMux))
	scope := ports.Scope{UserID: f.user, KnowledgeBaseID: f.kb}
	session, err := store.CreateSession(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	request, _ = http.NewRequest("POST", f.server.URL+"/api/knowledge-bases/"+f.kb+"/chat/sessions/"+session.ID+"/messages", strings.NewReader(`{"content":"知识库问题"}`))
	request.Header.Set("Authorization", "Bearer "+f.token)
	response, err = f.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || !bytes.Contains(stream, []byte(`"content":"中文"`)) || !bytes.Contains(stream, []byte(`"content":"🙂回答"`)) || !bytes.Contains(stream, []byte(`"completion_tokens":4`)) {
		t.Fatal("SSE", response.StatusCode, string(stream), err)
	}
	messages, err := store.Messages(ctx, scope, session.ID)
	if err != nil || len(messages) != 2 || messages[1].Content != "中文🙂回答" || !bytes.Contains(messages[1].TokenUsage, []byte(`"prompt_tokens": 12`)) {
		t.Fatal("chat persistence", messages, err)
	}
	var heat, chatReceipts int
	if err = f.h.Pool.QueryRow(ctx, `SELECT (SELECT sum(c.hit_count) FROM knowledge_chunk c JOIN knowledge_entry e ON e.id=c.entry_id WHERE e.kb_id=$1::uuid),(SELECT count(*) FROM chat_receipt WHERE session_id=$2::uuid)`, f.kb, session.ID).Scan(&heat, &chatReceipts); err != nil || heat != 5 || chatReceipts != 1 {
		t.Fatal("heat/receipt", heat, chatReceipts, err)
	}
	var extracted string
	for attempt := range 2 {
		body, _ := json.Marshal(map[string]string{"sessionId": session.ID})
		request, _ = http.NewRequest("POST", f.server.URL+"/api/knowledge-bases/"+f.kb+"/chat/extract", bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+f.token)
		response, err = f.server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		var value struct {
			Data struct {
				Entries []repository.ExtractedEntry
				Count   int
			}
		}
		err = json.NewDecoder(response.Body).Decode(&value)
		response.Body.Close()
		want := 1
		if attempt == 1 {
			want = 0
		}
		if err != nil || response.StatusCode != 200 || value.Data.Count != want || len(value.Data.Entries) != want {
			t.Fatal("extraction/dedup", response.StatusCode, value, err)
		}
		if attempt == 0 {
			extracted = value.Data.Entries[0].ID
		}
	}
	if worked, err := store.DispatchOne(ctx, publisher); !worked || err != nil {
		t.Fatal("extraction outbox", worked, err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; {
		var state string
		err = f.h.Pool.QueryRow(ctx, `SELECT processing_status::text FROM knowledge_entry WHERE id=$1::uuid`, extracted).Scan(&state)
		if err != nil {
			t.Fatal(err)
		}
		if state == "COMPLETED" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("extracted entry not indexed", state)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
