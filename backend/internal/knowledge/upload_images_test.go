package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-library/backend/internal/repository"

	"ai-library/backend/internal/ports"
)

type mockImages struct {
	mu      sync.Mutex
	objects map[string][]byte
	calls   int
	fail    bool
}

func (s *mockImages) PutImage(ctx context.Context, user, entry string, data []byte, media string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.fail {
		return "", errors.New("private storage secret")
	}
	if s.objects == nil {
		s.objects = map[string][]byte{}
	}
	s.objects[entry] = data
	return "https://local.invalid/ai-library/images/" + user + "/" + entry + ".png", nil
}
func (s *mockImages) DeleteImage(ctx context.Context, user, entry string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, entry)
	return nil
}

type visionFunc func(context.Context, string, []byte, string) (string, error)

func (f visionFunc) DescribeImage(ctx context.Context, id string, data []byte, media string) (string, error) {
	return f(ctx, id, data, media)
}

func pngData(c color.Color) []byte {
	var b bytes.Buffer
	im := image.NewRGBA(image.Rect(0, 0, 2, 2))
	im.Set(0, 0, c)
	_ = png.Encode(&b, im)
	return b.Bytes()
}

func (f *uploadFixture) images(ctx context.Context, kb, token string, files []uploadedImage) (int, []map[string]string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, file := range files {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", `form-data; name="images"; filename="`+file.name+`"`)
		header.Set("Content-Type", file.media)
		part, err := writer.CreatePart(header)
		if err != nil {
			return 0, nil, err
		}
		part.Write(file.data)
	}
	writer.Close()
	req, err := http.NewRequestWithContext(ctx, "POST", f.server.URL+"/api/knowledge-bases/"+kb+"/entries/upload-images", &body)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := f.server.Client().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	var result struct {
		Code int
		Data struct{ Entries []map[string]string }
	}
	err = json.NewDecoder(resp.Body).Decode(&result)
	return resp.StatusCode, result.Data.Entries, err
}

func TestImageBatchPartialFailureAndDeduplicationIntegration(t *testing.T) {
	f := uploadSetup(t, nil)
	objects := &mockImages{}
	f.h.Images = objects
	var calls atomic.Int32
	f.h.Vision = visionFunc(func(ctx context.Context, id string, data []byte, media string) (string, error) {
		if calls.Add(1) == 2 {
			return "", errors.New("private model key")
		}
		return "图片描述🙂", nil
	})
	files := []uploadedImage{{"红.png", "image/png", pngData(color.RGBA{R: 255, A: 255})}, {"蓝.png", "image/png", pngData(color.RGBA{B: 255, A: 255})}}
	status, entries, err := f.images(context.Background(), f.kb, f.token, files)
	if err != nil || status != 200 || len(entries) != 2 {
		t.Fatal(status, entries, err)
	}
	for i, item := range entries {
		var state, content, message string
		err = f.h.Pool.QueryRow(context.Background(), `SELECT processing_status::text,content,COALESCE(processing_message,'') FROM knowledge_entry WHERE id=$1::uuid`, item["entry_id"]).Scan(&state, &content, &message)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && (state != "PENDING" || !strings.Contains(content, "图片描述🙂\n\n![](https://local.invalid/")) {
			t.Fatal("description missing", state, content)
		}
		if i == 1 && (state != "FAILED" || content != "" || strings.Contains(message, "private")) {
			t.Fatal("failure unsafe", state, content, message)
		}
	}
	if e, tasks, o := f.counts(t); e != 2 || tasks != 1 || o != 1 {
		t.Fatal("failed images enqueued", e, tasks, o)
	}
	var pending int
	if err = f.h.Pool.QueryRow(context.Background(), `SELECT count(*) FROM storage_cleanup WHERE user_id=$1::uuid`, f.user).Scan(&pending); err != nil || pending != 1 {
		t.Fatal("reservation not consumed/retained", pending, err)
	}
	status, again, err := f.images(context.Background(), f.kb, f.token, files)
	if err != nil || status != 200 || again[0]["entry_id"] != entries[0]["entry_id"] || again[1]["entry_id"] != entries[1]["entry_id"] || calls.Load() != 2 || objects.calls != 2 {
		t.Fatal("dedup reprocessed", again, calls.Load(), objects.calls, err)
	}
	if status, _, err = f.images(context.Background(), f.otherKB, f.token, files); err != nil || status != 404 || objects.calls != 2 {
		t.Fatal("ownership failed", status, err)
	}
}

func TestImageInvalidFormsDoNotCallProvidersIntegration(t *testing.T) {
	f := uploadSetup(t, nil)
	objects := &mockImages{}
	f.h.Images = objects
	f.h.Vision = visionFunc(func(context.Context, string, []byte, string) (string, error) {
		t.Fatal("unexpected vision")
		return "", nil
	})
	valid := uploadedImage{"valid.png", "image/png", pngData(color.Black)}
	for _, files := range [][]uploadedImage{nil, {{"bad.png", "image/png", []byte("fake")}}, {{"bad.jpg", "image/jpeg", valid.data}}, {{"large.png", "image/png", make([]byte, ports.MaxImageBytes+1)}}, {valid, valid, valid, valid, valid, valid, valid, valid, valid, valid, valid}} {
		if status, _, err := f.images(context.Background(), f.kb, f.token, files); err != nil || status != 400 {
			t.Fatal(status, err)
		}
	}
	if objects.calls != 0 {
		t.Fatal("invalid input reached provider")
	}
	if e, tasks, o := f.counts(t); e != 0 || tasks != 0 || o != 0 {
		t.Fatal("invalid input persisted")
	}
}

func TestImageConcurrentDeduplicationIntegration(t *testing.T) {
	f := uploadSetup(t, nil)
	objects := &mockImages{}
	f.h.Images = objects
	f.h.Vision = visionFunc(func(context.Context, string, []byte, string) (string, error) { return "same image", nil })
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, entries, err := f.images(context.Background(), f.kb, f.token, []uploadedImage{{"same.png", "image/png", pngData(color.White)}})
			if err != nil || status != 200 || len(entries) != 1 {
				t.Error(status, err)
				return
			}
			ids <- entries[0]["entry_id"]
		}()
	}
	wg.Wait()
	close(ids)
	var expected string
	for id := range ids {
		if expected == "" {
			expected = id
		}
		if id != expected {
			t.Fatal("duplicate entries")
		}
	}
	if e, tasks, o := f.counts(t); e != 1 || tasks != 1 || o != 1 {
		t.Fatal(e, tasks, o)
	}
	var pending int
	if err := f.h.Pool.QueryRow(context.Background(), `SELECT count(*) FROM storage_cleanup WHERE user_id=$1::uuid`, f.user).Scan(&pending); err != nil || pending != objects.calls-1 {
		t.Fatal("duplicate objects not scheduled", pending, objects.calls, err)
	}
}

func TestImageStorageFailureRollbackAndCancellationIntegration(t *testing.T) {
	for _, scenario := range []string{"storage_failure", "sql_rollback", "cancel", "deleted_base"} {
		t.Run(scenario, func(t *testing.T) {
			f := uploadSetup(t, nil)
			objects := &mockImages{fail: scenario == "storage_failure"}
			f.h.Images = objects
			entered := make(chan struct{})
			f.h.Vision = visionFunc(func(ctx context.Context, id string, data []byte, media string) (string, error) {
				if scenario == "cancel" {
					close(entered)
					<-ctx.Done()
					return "", ctx.Err()
				}
				if scenario == "deleted_base" {
					_, err := f.h.Pool.Exec(ctx, `DELETE FROM knowledge_base WHERE id=$1::uuid`, f.kb)
					if err != nil {
						return "", err
					}
				}
				return "local image", nil
			})
			if scenario == "sql_rollback" {
				constraint := fmt.Sprintf("image_block_%d", time.Now().UnixNano())
				if _, err := f.h.Pool.Exec(context.Background(), `ALTER TABLE index_outbox ADD CONSTRAINT `+constraint+` CHECK(payload->>'kb_id'<>'`+f.kb+`')`); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_, _ = f.h.Pool.Exec(context.Background(), `ALTER TABLE index_outbox DROP CONSTRAINT `+constraint)
				})
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan int, 1)
			go func() {
				status, _, _ := f.images(ctx, f.kb, f.token, []uploadedImage{{"local.png", "image/png", pngData(color.Black)}})
				done <- status
			}()
			if scenario == "cancel" {
				<-entered
				cancel()
			}
			status := <-done
			if scenario == "storage_failure" && status != 200 {
				t.Fatal(status)
			}
			if scenario == "sql_rollback" && status != 500 {
				t.Fatal(status)
			}
			if scenario == "deleted_base" && status != 404 {
				t.Fatal(status)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				var ready int
				if err := f.h.Pool.QueryRow(context.Background(), `SELECT count(*) FROM storage_cleanup WHERE user_id=$1::uuid AND available_at<=now()`, f.user).Scan(&ready); err != nil {
					t.Fatal(err)
				}
				if ready == 1 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("orphan not scheduled")
				}
				time.Sleep(10 * time.Millisecond)
			}
			want := 0
			if scenario == "storage_failure" {
				want = 1
			}
			if e, tasks, o := f.counts(t); e != want || tasks != 0 || o != 0 {
				t.Fatal("failed operation persisted unexpected state", e, tasks, o)
			}
			// Local mock only: process all currently due isolated test jobs.
			repo := &repository.Entries{Pool: f.h.Pool}
			for range 1000 {
				worked, err := repo.CleanupOne(context.Background(), objects)
				if err != nil {
					t.Fatal(err)
				}
				if !worked {
					break
				}
			}
			objects.mu.Lock()
			remaining := len(objects.objects)
			objects.mu.Unlock()
			if remaining != 0 {
				t.Fatal("failed upload object survived cleanup")
			}
		})
	}
}
