// Package ai contains internal HTTP adapters, never model-provider credentials.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"ai-library/backend/internal/ports"
)

const maxParseResponseBytes = 24*1024*1024 + 65536

type Parser struct {
	endpoint, token string
	client          *http.Client
}

var _ ports.DocumentParser = (*Parser)(nil)

func (*Parser) String() string { return "Parser{credentials:redacted}" }

func NewParser(baseURL, token string, timeout time.Duration) (*Parser, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || timeout <= 0 {
		return nil, errors.New("invalid internal parser configuration")
	}
	if len(token) < 32 || strings.IndexFunc(token, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
		return nil, errors.New("invalid internal parser credentials")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // Internal traffic must not send documents to an environment proxy.
	client := &http.Client{Timeout: timeout, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &Parser{endpoint: strings.TrimRight(baseURL, "/") + "/internal/v1/parse", token: token, client: client}, nil
}

func (p *Parser) Parse(ctx context.Context, requestID, filename string, source io.Reader) (string, error) {
	reader, writer := io.Pipe()
	defer reader.Close()
	multipartWriter := multipart.NewWriter(writer)
	go func() {
		err := multipartWriter.WriteField("request_id", requestID)
		if err == nil {
			var part io.Writer
			part, err = multipartWriter.CreateFormFile("file", filename)
			if err == nil {
				var count int64
				count, err = io.Copy(part, io.LimitReader(source, ports.MaxDocumentBytes+1))
				if count > ports.MaxDocumentBytes {
					err = ports.ErrDocumentTooLarge
				}
			}
		}
		if err == nil {
			err = multipartWriter.Close()
		}
		_ = writer.CloseWithError(err)
	}()
	request, err := http.NewRequestWithContext(ctx, "POST", p.endpoint, reader)
	if err != nil {
		return "", ports.ErrParserUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+p.token)
	request.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	response, err := p.client.Do(request)
	if err != nil {
		if errors.Is(err, ports.ErrDocumentTooLarge) {
			return "", ports.ErrDocumentTooLarge
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		var timed interface{ Timeout() bool }
		if errors.As(err, &timed) && timed.Timeout() {
			return "", ports.ErrParserTimeout
		}
		return "", ports.ErrParserUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		// Never forward Python error bodies: they may contain provider messages,
		// document text, stack traces or internal configuration.
		switch response.StatusCode {
		case 400, 415, 422:
			return "", ports.ErrParseRejected
		case 413:
			return "", ports.ErrParsedTextTooLarge
		default:
			return "", ports.ErrParserUnavailable
		}
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return "", ports.ErrParserUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxParseResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		var timed interface{ Timeout() bool }
		if errors.As(err, &timed) && timed.Timeout() {
			return "", ports.ErrParserTimeout
		}
		return "", ports.ErrParserUnavailable
	}
	if len(body) > maxParseResponseBytes {
		return "", ports.ErrParsedTextTooLarge
	}
	fields, err := parseObject(body, "request_id", "text", "metadata")
	if err != nil {
		return "", ports.ErrParserUnavailable
	}
	var actualID, text, actualName string
	if json.Unmarshal(fields["request_id"], &actualID) != nil || actualID != requestID || json.Unmarshal(fields["text"], &text) != nil {
		return "", ports.ErrParserUnavailable
	}
	metadata, err := parseObject(fields["metadata"], "filename", "characters")
	var characters int
	if err != nil || json.Unmarshal(metadata["filename"], &actualName) != nil || actualName != filename || json.Unmarshal(metadata["characters"], &characters) != nil || characters != utf8.RuneCountInString(text) || strings.ContainsRune(text, 0) || strings.TrimSpace(strings.Trim(text, "\ufeff")) == "" {
		return "", ports.ErrParserUnavailable
	}
	if characters > ports.MaxParsedCharacters {
		return "", ports.ErrParsedTextTooLarge
	}
	return text, nil
}

func parseObject(body []byte, required ...string) (map[string]json.RawMessage, error) {
	if !utf8.Valid(body) {
		return nil, ports.ErrParserUnavailable
	}
	d := json.NewDecoder(bytes.NewReader(body))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ports.ErrParserUnavailable
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return nil, ports.ErrParserUnavailable
		}
		name, ok := key.(string)
		if !ok || fields[name] != nil {
			return nil, ports.ErrParserUnavailable
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, ports.ErrParserUnavailable
		}
		fields[name] = value
	}
	if _, err := d.Token(); err != nil || d.Decode(new(any)) != io.EOF || len(fields) != len(required) {
		return nil, ports.ErrParserUnavailable
	}
	for _, name := range required {
		if len(fields[name]) == 0 || bytes.Equal(bytes.TrimSpace(fields[name]), []byte("null")) {
			return nil, ports.ErrParserUnavailable
		}
	}
	return fields, nil
}
