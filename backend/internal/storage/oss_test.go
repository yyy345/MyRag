package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-library/backend/internal/ports"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/credentials"
)

const userID = "11111111-1111-4111-8111-111111111111"
const entryID = "22222222-2222-4222-8222-222222222222"

func fixture(media string) []byte {
	var b bytes.Buffer
	im := image.NewRGBA(image.Rect(0, 0, 2, 2))
	if media == "image/png" {
		_ = png.Encode(&b, im)
	} else {
		_ = jpeg.Encode(&b, im, nil)
	}
	return b.Bytes()
}

func mockStore(t *testing.T, handler http.HandlerFunc) *Store {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	cfg := oss.NewConfig().WithRegion("cn-hangzhou").WithEndpoint(server.URL).WithUsePathStyle(true).WithHttpClient(client).WithRetryMaxAttempts(1).WithLogLevel(oss.LogOff).WithCredentialsProvider(credentials.NewStaticCredentialsProvider("mock-id", "mock-private-secret", "mock-session"))
	return &Store{client: oss.NewClient(cfg), bucket: "test-bucket", publicBase: server.URL + "/test-bucket"}
}

func TestStoragePutAccessAndDeleteLocalHTTP(t *testing.T) {
	objects := map[string][]byte{}
	s := mockStore(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && (!strings.HasPrefix(r.Header.Get("Authorization"), "OSS4-HMAC-SHA256 ") || r.Header.Get("x-oss-security-token") != "mock-session") {
			t.Error("missing signed credentials")
		}
		switch r.Method {
		case "PUT":
			if r.Header.Get("x-oss-object-acl") != "public-read" {
				t.Error("wrong ACL")
			}
			data, _ := io.ReadAll(r.Body)
			if err := ValidateImage(data, r.Header.Get("Content-Type")); err != nil {
				t.Error(err)
			}
			objects[r.URL.Path] = data
			w.Header().Set("ETag", `"mock-etag"`)
		case "GET":
			data, ok := objects[r.URL.Path]
			if !ok {
				w.WriteHeader(404)
				return
			}
			w.Write(data)
		case "DELETE":
			delete(objects, r.URL.Path)
			w.WriteHeader(204)
		}
	})
	for _, media := range []string{"image/png", "image/jpeg"} {
		data := fixture(media)
		link, err := s.PutImage(context.Background(), userID, entryID, data, media)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.Get(link)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !bytes.Equal(data, got) {
			t.Fatal("public URL content mismatch")
		}
	}
	if err := s.DeleteImage(context.Background(), userID, entryID); err != nil {
		t.Fatal(err)
	}
	if len(objects) != 0 {
		t.Fatal("objects survived")
	}
	if err := s.DeleteImage(context.Background(), userID, entryID); err != nil {
		t.Fatal("idempotent delete", err)
	}
}

func TestStorageInvalidInputDoesNotSend(t *testing.T) {
	s := mockStore(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid input reached OSS") })
	for _, input := range []struct {
		data        []byte
		media, user string
	}{
		{nil, "image/png", userID}, {[]byte("not an image"), "image/png", userID},
		{fixture("image/png"), "image/jpeg", userID}, {fixture("image/png"), "image/gif", userID},
		{fixture("image/png"), "image/png", "../outside"},
		{fixture("image/png")[:20], "image/png", userID},
		{make([]byte, ports.MaxImageBytes+1), "image/png", userID},
	} {
		if _, err := s.PutImage(context.Background(), input.user, entryID, input.data, input.media); !errors.Is(err, ports.ErrInvalidImage) {
			t.Fatal(err)
		}
	}
}

func TestStorageFailureRedactionAndDeleteBoth(t *testing.T) {
	var calls int
	s := mockStore(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(403)
		fmt.Fprint(w, "<Error><Code>AccessDenied</Code><Message>mock-private-secret private document</Message></Error>")
	})
	if _, err := s.PutImage(context.Background(), userID, entryID, fixture("image/png"), "image/png"); !errors.Is(err, ports.ErrStorageUnavailable) || strings.Contains(err.Error(), "private") {
		t.Fatal(err)
	}
	if err := s.DeleteImage(context.Background(), userID, entryID); !errors.Is(err, ports.ErrStorageUnavailable) {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatal("both extensions not attempted", calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.PutImage(ctx, userID, entryID, fixture("image/png"), "image/png"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestStorageConfigAndRedaction(t *testing.T) {
	if _, enabled, err := LoadConfig(func(string) string { return "" }); err != nil || enabled {
		t.Fatal("unset should disable")
	}
	values := map[string]string{"OSS_REGION": "oss-cn-hangzhou", "OSS_BUCKET": "test-bucket", "OSS_ACCESS_KEY_ID": "mock-id", "OSS_ACCESS_KEY_SECRET": "mock-private-secret"}
	cfg, enabled, err := LoadConfig(func(k string) string { return values[k] })
	if err != nil || !enabled || cfg.Region != "cn-hangzhou" {
		t.Fatal(cfg, err)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%+v %+v", cfg, s), "mock-private-secret") {
		t.Fatal("credential leak")
	}
	for _, key := range []string{"OSS_REGION", "OSS_BUCKET", "OSS_ACCESS_KEY_ID", "OSS_ACCESS_KEY_SECRET"} {
		old := values[key]
		values[key] = "bad private\n"
		if _, _, err = LoadConfig(func(k string) string { return values[k] }); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("unsafe config", key, err)
		}
		values[key] = old
	}
}
