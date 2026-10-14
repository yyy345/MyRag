package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"ai-library/backend/internal/ports"
)

var ErrVisionUnavailable = errors.New("图片描述暂不可用")
var ErrVisionTimeout = errors.New("图片描述超时")

type Vision struct {
	endpoint, token string
	client          *http.Client
}

func (*Vision) String() string { return "Vision{credentials:redacted}" }
func NewVision(baseURL, token string, timeout time.Duration) (*Vision, error) {
	p, err := NewParser(baseURL, token, timeout)
	if err != nil {
		return nil, err
	}
	return &Vision{endpoint: strings.TrimSuffix(p.endpoint, "/parse") + "/describe-image", token: p.token, client: p.client}, nil
}
func (v *Vision) DescribeImage(ctx context.Context, id string, data []byte, mediaType string) (string, error) {
	if len(data) == 0 || len(data) > ports.MaxImageBytes {
		return "", ports.ErrInvalidImage
	}
	body, _ := json.Marshal(map[string]string{"request_id": id, "image_base64": base64.StdEncoding.EncodeToString(data), "media_type": mediaType})
	r, err := http.NewRequestWithContext(ctx, "POST", v.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", ErrVisionUnavailable
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+v.token)
	response, err := v.client.Do(r)
	if err != nil {
		return "", visionError(ctx, err)
	}
	defer response.Body.Close()
	if response.StatusCode == 504 {
		return "", ErrVisionTimeout
	}
	if response.StatusCode != 200 {
		return "", ErrVisionUnavailable
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return "", ErrVisionUnavailable
	}
	body, err = io.ReadAll(io.LimitReader(response.Body, 256*1024+1))
	if err != nil {
		return "", visionError(ctx, err)
	}
	if len(body) > 256*1024 {
		return "", ErrVisionUnavailable
	}
	fields, err := parseObject(body, "request_id", "description")
	var actual, text string
	if err != nil || json.Unmarshal(fields["request_id"], &actual) != nil || actual != id || json.Unmarshal(fields["description"], &text) != nil || strings.TrimSpace(text) == "" || strings.ContainsRune(text, 0) || utf8.RuneCountInString(text) > 20_000 {
		return "", ErrVisionUnavailable
	}
	return text, nil
}
func visionError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var timed interface{ Timeout() bool }
	if errors.As(err, &timed) && timed.Timeout() {
		return ErrVisionTimeout
	}
	return ErrVisionUnavailable
}
