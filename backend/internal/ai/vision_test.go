package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestVisionHTTPContractAndErrors(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	const token = "local-vision-token-abcdefghijklmnopqrstuvwxyz"
	for _, tc := range []struct {
		name, body string
		status     int
		want       error
	}{
		{"success", `{"request_id":"` + id + `","description":"中文🙂"}`, 200, nil},
		{"mismatch", `{"request_id":"bad","description":"text"}`, 200, ErrVisionUnavailable},
		{"empty", `{"request_id":"` + id + `","description":" "}`, 200, ErrVisionUnavailable},
		{"extra", `{"request_id":"` + id + `","description":"text","extra":1}`, 200, ErrVisionUnavailable},
		{"timeout", "private provider response", 504, ErrVisionTimeout},
		{"rate_limit", "private provider response", 429, ErrVisionUnavailable},
		{"malformed", "private provider response", 200, ErrVisionUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/internal/v1/describe-image" || r.Header.Get("Authorization") != "Bearer "+token {
					t.Error("wrong internal request")
				}
				var body map[string]string
				if json.NewDecoder(r.Body).Decode(&body) != nil || body["request_id"] != id || body["image_base64"] != "eA==" || body["media_type"] != "image/png" {
					t.Error("wrong payload", body)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			vision, err := NewVision(server.URL, token, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			text, err := vision.DescribeImage(context.Background(), id, []byte("x"), "image/png")
			if !errors.Is(err, tc.want) || (tc.want == nil && text != "中文🙂") {
				t.Fatal(text, err)
			}
			if err != nil && strings.Contains(err.Error(), "private") {
				t.Fatal("error leak")
			}
		})
	}
}
