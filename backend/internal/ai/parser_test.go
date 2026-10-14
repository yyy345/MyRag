package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"ai-library/backend/internal/ports"
)

const testToken = "parse-local-test-" + "abcdefghijklmnopqrstuvwxyz0123456789"
const testRequestID = "11111111-1111-4111-8111-111111111111"

func parseReply(id, name, text string) string {
	body, _ := json.Marshal(map[string]any{"request_id": id, "text": text, "metadata": map[string]any{"filename": name, "characters": utf8.RuneCountInString(text)}})
	return string(body)
}

func TestParseMultipartIdentityAndText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/prefix/internal/v1/parse" || r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Error("wrong internal endpoint or credentials")
		}
		if err := r.ParseMultipartForm(1024); err != nil {
			t.Error(err)
			return
		}
		defer r.MultipartForm.RemoveAll()
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			return
		}
		defer file.Close()
		body, _ := io.ReadAll(file)
		if string(body) != "local-bytes" || header.Filename != "中文🙂.txt" || r.FormValue("request_id") != testRequestID {
			t.Error("multipart bytes or identity changed")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, parseReply(testRequestID, header.Filename, "# 中文🙂\n\n正文"))
	}))
	defer server.Close()
	parser, err := NewParser(server.URL+"/prefix/", testToken, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	text, err := parser.Parse(context.Background(), testRequestID, "中文🙂.txt", strings.NewReader("local-bytes"))
	if err != nil || text != "# 中文🙂\n\n正文" {
		t.Fatal("parse response changed", err)
	}
	if strings.Contains(fmt.Sprintf("%+v", parser), testToken) {
		t.Fatal("credentials leaked in formatted parser")
	}
}

func TestParserRejectsBadRepliesAndDoesNotLeakErrors(t *testing.T) {
	valid := parseReply(testRequestID, "test.txt", "local")
	cases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"private_error", 422, `{"detail":"private-document secret-token"}`, ports.ErrParseRejected},
		{"oversize", 413, "private", ports.ErrParsedTextTooLarge},
		{"service_auth", 401, "private", ports.ErrParserUnavailable},
		{"service_failure", 500, "private", ports.ErrParserUnavailable},
		{"wrong_id", 200, parseReply("wrong", "test.txt", "local"), ports.ErrParserUnavailable},
		{"wrong_name", 200, parseReply(testRequestID, "wrong.txt", "local"), ports.ErrParserUnavailable},
		{"wrong_count", 200, strings.Replace(valid, `"characters":5`, `"characters":4`, 1), ports.ErrParserUnavailable},
		{"unknown", 200, strings.Replace(valid, `"request_id":`, `"extra":1,"request_id":`, 1), ports.ErrParserUnavailable},
		{"duplicate", 200, strings.Replace(valid, `"text":"local"`, `"text":"local","text":"local"`, 1), ports.ErrParserUnavailable},
		{"null", 200, strings.Replace(valid, `"text":"local"`, `"text":null`, 1), ports.ErrParserUnavailable},
		{"missing", 200, `{"request_id":"` + testRequestID + `","text":"local"}`, ports.ErrParserUnavailable},
		{"trailing", 200, valid + ` {}`, ports.ErrParserUnavailable},
		{"nul", 200, parseReply(testRequestID, "test.txt", "x\x00y"), ports.ErrParserUnavailable},
		{"blank", 200, parseReply(testRequestID, "test.txt", " \n\t"), ports.ErrParserUnavailable},
		{"invalid_utf8", 200, valid + string([]byte{255}), ports.ErrParserUnavailable},
		{"text_limit", 200, parseReply(testRequestID, "test.txt", strings.Repeat("x", ports.MaxParsedCharacters+1)), ports.ErrParsedTextTooLarge},
		{"body_limit", 200, strings.Repeat("x", maxParseResponseBytes+1), ports.ErrParsedTextTooLarge},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(item.status)
				io.WriteString(w, item.body)
			}))
			defer server.Close()
			parser, _ := NewParser(server.URL, testToken, time.Second)
			text, err := parser.Parse(context.Background(), testRequestID, "test.txt", strings.NewReader("local"))
			if text != "" || !errors.Is(err, item.want) {
				t.Fatal("unsafe or incorrect reply accepted", err)
			}
			if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), testToken) {
				t.Fatal("upstream details leaked")
			}
		})
	}
}

func TestParserRedirectDoesNotForwardCredentials(t *testing.T) {
	var calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	parser, _ := NewParser(source.URL, testToken, time.Second)
	_, err := parser.Parse(context.Background(), testRequestID, "test.txt", strings.NewReader("local"))
	if !errors.Is(err, ports.ErrParserUnavailable) || calls.Load() != 0 {
		t.Fatal("redirect followed")
	}
}

func TestParserTimeoutAndCancellationReachUpstream(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			started, stopped := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				close(started)
				<-r.Context().Done()
				close(stopped)
			}))
			defer server.Close()
			parser, _ := NewParser(server.URL, testToken, 100*time.Millisecond)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelled {
				go func() { <-started; cancel() }()
			}
			_, err := parser.Parse(ctx, testRequestID, "test.txt", strings.NewReader("local"))
			if cancelled && !errors.Is(err, context.Canceled) || !cancelled && !errors.Is(err, ports.ErrParserTimeout) {
				t.Fatal("wrong cancellation classification", err)
			}
			select {
			case <-stopped:
			case <-time.After(2 * time.Second):
				t.Fatal("upstream request not cancelled")
			}
		})
	}
}

func TestParserConfiguration(t *testing.T) {
	for _, item := range []struct {
		url, token string
		timeout    time.Duration
	}{
		{"file:///private", testToken, time.Second}, {"https://user:password@example.test", testToken, time.Second}, {"http://example.test?token=secret", testToken, time.Second},
		{"http://example.test", "short", time.Second}, {"http://example.test", testToken + "\n", time.Second}, {"http://example.test", testToken, 0}, {"http://example.test?", testToken, time.Second},
	} {
		if _, err := NewParser(item.url, item.token, item.timeout); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
}
