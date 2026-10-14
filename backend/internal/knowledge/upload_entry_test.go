package knowledge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"ai-library/backend/internal/ai"
	"ai-library/backend/internal/auth"
	"ai-library/backend/internal/cache"
	"ai-library/backend/internal/ports"
	"ai-library/backend/internal/repository"
	"github.com/golang-jwt/jwt/v5"
)

const uploadTestToken = "parse-local-test-abcdefghijklmnopqrstuvwxyz0123456789"

func TestReadUploadRejectsDeclaredAndChunkedEpilogueOversize(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("file", "valid.txt")
	part.Write([]byte("local"))
	writer.Close()
	body.Write(bytes.Repeat([]byte("x"), ports.MaxDocumentBytes+1024*1024))
	for _, declared := range []bool{true, false} {
		req := httptest.NewRequest("POST", "/upload", bytes.NewReader(body.Bytes()))
		if !declared {
			req.ContentLength = -1
		}
		req.Header.Set("Content-Type", writer.FormDataContentType())
		if _, _, err := readUpload(httptest.NewRecorder(), req); err == nil {
			t.Fatal("oversized request epilogue accepted")
		}
	}
}

type parserFunc func(context.Context, string, string, io.Reader) (string, error)

func (f parserFunc) Parse(ctx context.Context, id, name string, r io.Reader) (string, error) {
	return f(ctx, id, name, r)
}

type uploadFixture struct {
	h                                    *Handler
	server                               *httptest.Server
	user, kb, otherKB, token, otherToken string
}

func uploadSetup(t *testing.T, parser ports.DocumentParser) *uploadFixture {
	return uploadSetupWithDatabase(t, parser, "postgresql://postgres:migration-test-only@127.0.0.1:55432/migration_test")
}

func uploadSetupWithDatabase(t *testing.T, parser ports.DocumentParser, database string) *uploadFixture {
	t.Helper()
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("requires isolated PostgreSQL")
	}
	ctx := context.Background()
	pool, err := repository.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	migrations, err := repository.ReadMigrations("../../../server/prisma/migrations")
	if err != nil {
		t.Fatal(err)
	}
	if err = repository.Apply(ctx, pool, migrations); err != nil {
		t.Fatal(err)
	}
	secret := []byte("upload-local-only-secret-at-least-32-characters")
	f := &uploadFixture{h: &Handler{Pool: pool, Auth: &auth.Service{Secret: secret}, Parser: parser}}
	if os.Getenv("RUN_REDIS_TESTS") == "1" {
		f.h.Cache, err = cache.New("redis://127.0.0.1:56380/0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.h.Cache.Close() })
	}
	for i := 0; i < 2; i++ {
		name := fmt.Sprintf("go_upload_%d_%d", time.Now().UnixNano(), i)
		var user, kb string
		if err = pool.QueryRow(ctx, `INSERT INTO "user"(id,username,email,password_hash,updated_at) VALUES(gen_random_uuid(),$1,$2,'unused',now()) RETURNING id::text`, name, name+"@example.test").Scan(&user); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM storage_cleanup WHERE user_id=$1::uuid`, user)
			_, err := pool.Exec(context.Background(), `DELETE FROM "user" WHERE id=$1::uuid`, user)
			if err != nil {
				t.Error(err)
			}
		})
		if err = pool.QueryRow(ctx, `INSERT INTO knowledge_base(id,user_id,name,updated_at) VALUES(gen_random_uuid(),$1::uuid,'upload test',now()) RETURNING id::text`, user).Scan(&kb); err != nil {
			t.Fatal(err)
		}
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, auth.Claims{ID: user, Username: name, RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}).SignedString(secret)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			f.user, f.kb, f.token = user, kb, token
		} else {
			f.otherKB, f.otherToken = kb, token
		}
	}
	mux := http.NewServeMux()
	f.h.Register(mux)
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

type uploadEnvelope struct {
	Code    int                     `json:"code"`
	Message string                  `json:"message"`
	Data    repository.UploadResult `json:"data"`
}

func (f *uploadFixture) post(ctx context.Context, kb, token, name string, content []byte) (int, uploadEnvelope, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if name != "" {
		part, err := writer.CreateFormFile("file", name)
		if err != nil {
			return 0, uploadEnvelope{}, err
		}
		part.Write(content)
	}
	writer.Close()
	request, err := http.NewRequestWithContext(ctx, "POST", f.server.URL+"/api/knowledge-bases/"+kb+"/entries/upload", &body)
	if err != nil {
		return 0, uploadEnvelope{}, err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := f.server.Client().Do(request)
	if err != nil {
		return 0, uploadEnvelope{}, err
	}
	defer response.Body.Close()
	var envelope uploadEnvelope
	err = json.NewDecoder(response.Body).Decode(&envelope)
	return response.StatusCode, envelope, err
}
func (f *uploadFixture) postOK(t *testing.T, kb, token, name string, content []byte, want int) uploadEnvelope {
	t.Helper()
	status, result, err := f.post(context.Background(), kb, token, name, content)
	if err != nil || status != want {
		t.Fatalf("upload %s: status %d want %d, %v, %v", name, status, want, result, err)
	}
	return result
}
func (f *uploadFixture) counts(t *testing.T) (int, int, int) {
	t.Helper()
	var entries, tasks, outbox int
	err := f.h.Pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM knowledge_entry WHERE kb_id=$1::uuid),(SELECT count(*) FROM index_task WHERE kb_id=$1::uuid),(SELECT count(*) FROM index_outbox WHERE payload->>'kb_id'=$1::text)`, f.kb).Scan(&entries, &tasks, &outbox)
	if err != nil {
		t.Fatal(err)
	}
	return entries, tasks, outbox
}

func TestUploadAtomicDeduplicationIntegration(t *testing.T) {
	var calls atomic.Int32
	var lastTrace string
	f := uploadSetup(t, parserFunc(func(ctx context.Context, id, name string, r io.Reader) (string, error) {
		calls.Add(1)
		lastTrace = id
		data, err := io.ReadAll(r)
		return "# 原文🙂\n" + string(data), err
	}))
	body := []byte("local source bytes")
	for _, token := range []string{"", f.otherToken} {
		want := 401
		if token != "" {
			want = 404
		}
		f.postOK(t, f.kb, token, "bad.exe", body, want)
	}
	for _, name := range []string{"", "bad.exe", strings.Repeat("中", 201) + ".txt", "blank.txt"} {
		input := body
		if name == "blank.txt" {
			input = nil
		}
		result := f.postOK(t, f.kb, f.token, name, input, 400)
		if result.Code != 2003 {
			t.Fatal("wrong upload error code")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid or unauthorized upload reached parser")
	}
	if f.h.Cache != nil {
		for _, key := range []string{"entry:" + f.kb + ":1:10", "go:entry:" + f.kb + ":1:10", "analytics:overview:" + f.user, "go:analytics:kb:" + f.kb} {
			f.h.Cache.Set(context.Background(), key, []byte("stale"), time.Minute)
			t.Cleanup(func() { f.h.Cache.Delete(context.Background(), key) })
		}
	}
	created := f.postOK(t, f.kb, f.token, "中文🙂.TXT", body, 200)
	if created.Code != 0 || created.Message != "ok" || created.Data.ProcessingStatus != "PENDING" || created.Data.ID == "" {
		t.Fatal("upload contract", created)
	}
	var typ, name, hash, content, trace string
	var version int64
	err := f.h.Pool.QueryRow(context.Background(), `SELECT e.type::text,e.source_file_name,e.source_file_hash,e.content,e.entry_version,t.trace_id FROM knowledge_entry e JOIN index_task t ON t.entry_id=e.id WHERE e.id=$1::uuid`, created.Data.ID).Scan(&typ, &name, &hash, &content, &version, &trace)
	sum := sha256.Sum256(body)
	if err != nil || typ != "FILE" || name != "中文🙂.TXT" || hash != hex.EncodeToString(sum[:]) || content != "# 原文🙂\n"+string(body) || version != 1 || trace != lastTrace {
		t.Fatal("file metadata or task trace changed", err)
	}
	if f.h.Cache != nil {
		if _, err := f.h.Cache.Get(context.Background(), "entry:"+f.kb+":1:10"); err == nil {
			t.Fatal("upload left stale cache")
		}
	}
	_, err = f.h.Pool.Exec(context.Background(), `UPDATE knowledge_entry SET processing_status='FAILED' WHERE id=$1::uuid`, created.Data.ID)
	if err != nil {
		t.Fatal(err)
	}
	repeated := f.postOK(t, f.kb, f.token, "different.txt", body, 200)
	if repeated.Data.ID != created.Data.ID || repeated.Data.ProcessingStatus != "FAILED" || repeated.Message != "文件已存在" || calls.Load() != 1 {
		t.Fatal("duplicate re-parsed or lost real status", repeated)
	}
	if e, tasks, o := f.counts(t); e != 1 || tasks != 1 || o != 1 {
		t.Fatal("duplicate created work", e, tasks, o)
	}
	other := f.postOK(t, f.otherKB, f.otherToken, "same.txt", body, 200)
	if other.Data.ID == created.Data.ID || other.Message != "ok" {
		t.Fatal("hash incorrectly deduplicated across KBs")
	}
	f.postOK(t, f.kb, f.token, "same_name.txt", []byte("different bytes"), 200)
	if e, tasks, o := f.counts(t); e != 2 || tasks != 2 || o != 2 {
		t.Fatal("new bytes not independently stored", e, tasks, o)
	}
}

func TestUploadParallelDeduplicationIntegration(t *testing.T) {
	f := uploadSetup(t, parserFunc(func(context.Context, string, string, io.Reader) (string, error) {
		time.Sleep(20 * time.Millisecond)
		return "local parallel text", nil
	}))
	var group sync.WaitGroup
	results := make(chan uploadEnvelope, 8)
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			status, value, err := f.post(context.Background(), f.kb, f.token, "parallel.txt", []byte("parallel bytes"))
			if err != nil || status != 200 {
				failures <- fmt.Errorf("status %d: %v", status, err)
				return
			}
			results <- value
		}()
	}
	group.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	id := ""
	created := 0
	for result := range results {
		if id == "" {
			id = result.Data.ID
		}
		if result.Data.ID != id {
			t.Fatal("concurrent duplicate IDs")
		}
		if result.Message == "ok" {
			created++
		}
	}
	if created != 1 {
		t.Fatal("duplicate creation responses", created)
	}
	if e, tasks, o := f.counts(t); e != 1 || tasks != 1 || o != 1 {
		t.Fatal("concurrent duplicates persisted", e, tasks, o)
	}
}

func TestUploadFailureRollbackAndCancellationIntegration(t *testing.T) {
	f := uploadSetup(t, nil)
	f.postOK(t, f.kb, f.token, "absent.txt", []byte("new"), 400)
	for _, failure := range []error{ports.ErrParseRejected, ports.ErrParserTimeout, ports.ErrParserUnavailable, errors.New("private-document provider-key")} {
		f.h.Parser = parserFunc(func(context.Context, string, string, io.Reader) (string, error) { return "", failure })
		result := f.postOK(t, f.kb, f.token, "failed.txt", []byte("new"), 400)
		if result.Code != 2003 || strings.Contains(result.Message, "private") {
			t.Fatal("unsafe parser failure")
		}
		if e, tasks, o := f.counts(t); e+tasks+o != 0 {
			t.Fatal("parser failure left records")
		}
	}
	constraint := fmt.Sprintf("upload_rollback_%d", time.Now().UnixNano())
	f.h.Parser = parserFunc(func(ctx context.Context, id, name string, r io.Reader) (string, error) {
		_, err := f.h.Pool.Exec(ctx, `ALTER TABLE index_outbox ADD CONSTRAINT `+constraint+` CHECK(task_id <> '`+id+`'::uuid)`)
		return "local text", err
	})
	t.Cleanup(func() {
		f.h.Pool.Exec(context.Background(), `ALTER TABLE index_outbox DROP CONSTRAINT IF EXISTS `+constraint)
	})
	f.postOK(t, f.kb, f.token, "rollback.txt", []byte("new"), 500)
	if e, tasks, o := f.counts(t); e+tasks+o != 0 {
		t.Fatal("failed outbox write partially committed")
	}
	started, stopped := make(chan struct{}), make(chan struct{})
	f.h.Parser = parserFunc(func(ctx context.Context, _, _ string, _ io.Reader) (string, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return "", ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-started; cancel() }()
	_, _, err := f.post(ctx, f.kb, f.token, "cancel.txt", []byte("new"))
	if err == nil {
		t.Fatal("cancelled request succeeded")
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("parse not cancelled")
	}
	if e, tasks, o := f.counts(t); e+tasks+o != 0 {
		t.Fatal("cancelled parse persisted work")
	}
	// Delete the parent while parsing. The commit path rechecks ownership.
	f.h.Parser = parserFunc(func(ctx context.Context, _, _ string, _ io.Reader) (string, error) {
		_, err := f.h.Pool.Exec(ctx, `DELETE FROM knowledge_base WHERE id=$1::uuid`, f.kb)
		return "local text", err
	})
	f.postOK(t, f.kb, f.token, "deleted.txt", []byte("new"), 404)
	if e, tasks, o := f.counts(t); e+tasks+o != 0 {
		t.Fatal("deleted parent resurrected records")
	}
}

func TestUploadByteAndFormLimitsIntegration(t *testing.T) {
	var calls atomic.Int32
	f := uploadSetup(t, parserFunc(func(context.Context, string, string, io.Reader) (string, error) {
		calls.Add(1)
		return "short parsed text", nil
	}))
	f.postOK(t, f.kb, f.token, "limit.txt", bytes.Repeat([]byte("a"), ports.MaxDocumentBytes), 200)
	f.postOK(t, f.kb, f.token, "oversize.txt", bytes.Repeat([]byte("a"), ports.MaxDocumentBytes+1), 400)
	if calls.Load() != 1 {
		t.Fatal("oversized file reached parser")
	}
	for _, kind := range []string{"two_files", "other_file", "large_field", "malformed"} {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		if kind == "large_field" {
			part, _ := writer.CreateFormField("extra")
			part.Write(bytes.Repeat([]byte("x"), 65537))
		} else {
			field := "file"
			if kind == "other_file" {
				field = "images"
			}
			part, _ := writer.CreateFormFile(field, "valid.txt")
			part.Write([]byte("local"))
			if kind == "two_files" {
				part, _ = writer.CreateFormFile("file", "other.txt")
				part.Write([]byte("local"))
			}
		}
		writer.Close()
		raw := body.Bytes()
		if kind == "malformed" {
			raw = raw[:len(raw)/2]
		}
		req, _ := http.NewRequest("POST", f.server.URL+"/api/knowledge-bases/"+f.kb+"/entries/upload", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+f.token)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		res, err := f.server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 400 {
			t.Fatal("invalid multipart accepted", kind, res.StatusCode)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("invalid form reached parser")
	}
}

func TestGoPythonUploadHTTPIntegration(t *testing.T) {
	if os.Getenv("RUN_PYTHON_TESTS") != "1" || os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("enable isolated PostgreSQL and Python HTTP integration")
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	python := filepath.Join(root, "ai-service/.venv/bin/python")
	if runtime.GOOS == "windows" {
		python = filepath.Join(root, "ai-service/.venv/Scripts/python.exe")
	}
	command := exec.Command(python, "-u", "tests/serve_upload_fixtures.py")
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
		URL   string `json:"url"`
		Files []struct {
			Filename string `json:"filename"`
			Content  []byte `json:"content"`
			Expected string `json:"expected"`
			Status   int    `json:"status"`
		} `json:"files"`
		Images []struct {
			Filename string `json:"filename"`
			Content  []byte `json:"content"`
			Media    string `json:"media_type"`
		} `json:"images"`
	}
	decoded := make(chan error, 1)
	go func() { decoded <- json.NewDecoder(stdout).Decode(&fixture) }()
	select {
	case err = <-decoded:
		if err != nil {
			t.Fatal("Python harness failed", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Python harness startup timed out")
	}
	ready := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
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
		t.Fatal("Python parser did not start")
	}
	parser, err := ai.NewParser(fixture.URL, uploadTestToken, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	f := uploadSetup(t, parser)
	for _, file := range fixture.Files {
		t.Run(file.Filename, func(t *testing.T) {
			result := f.postOK(t, f.kb, f.token, file.Filename, file.Content, file.Status)
			if file.Status != 200 {
				if result.Code != 2003 {
					t.Fatal("parse error code changed")
				}
				return
			}
			var content, name, hash string
			err := f.h.Pool.QueryRow(context.Background(), `SELECT content,source_file_name,source_file_hash FROM knowledge_entry WHERE id=$1::uuid`, result.Data.ID).Scan(&content, &name, &hash)
			sum := sha256.Sum256(file.Content)
			if err != nil || content != file.Expected || name != file.Filename || hash != hex.EncodeToString(sum[:]) || utf8.RuneCountInString(content) == 0 {
				t.Fatal("Go/Python bytes or metadata changed", err)
			}
			if result.Data.ProcessingStatus != "PENDING" {
				t.Fatal("upload falsely advertised indexing completion")
			}
		})
	}
	if e, tasks, o := f.counts(t); e != 6 || tasks != 6 || o != 6 {
		t.Fatal("cross-service successes/errors not atomic", e, tasks, o)
	}
	vision, err := ai.NewVision(fixture.URL, uploadTestToken, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	f.h.Vision = vision
	f.h.Images = &mockImages{}
	var images []uploadedImage
	for _, image := range fixture.Images {
		images = append(images, uploadedImage{image.Filename, image.Media, image.Content})
	}
	status, entries, err := f.images(context.Background(), f.kb, f.token, images)
	if err != nil || status != 200 || len(entries) != 2 {
		t.Fatal("Go/Python images", status, entries, err)
	}
	for i, entry := range entries {
		var state, content string
		err = f.h.Pool.QueryRow(context.Background(), `SELECT processing_status::text,content FROM knowledge_entry WHERE id=$1::uuid`, entry["entry_id"]).Scan(&state, &content)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && (state != "PENDING" || !strings.HasPrefix(content, "红色方块🙂\n\n![](")) {
			t.Fatal("real HTTP vision failed", state, content)
		}
		if i == 1 && (state != "FAILED" || content != "") {
			t.Fatal("model failure not visible", state, content)
		}
	}
	if e, tasks, o := f.counts(t); e != 8 || tasks != 7 || o != 7 {
		t.Fatal("cross-service image tasks", e, tasks, o)
	}
}
