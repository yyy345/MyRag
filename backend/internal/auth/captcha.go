package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ai-library/backend/internal/cache"
)

type Error struct {
	Status, Code int
	Message      string
}

func (e *Error) Error() string { return e.Message }
func reply(w http.ResponseWriter, status, code int, data any, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "data": data, "message": message})
}
func fail(w http.ResponseWriter, err error) {
	var app *Error
	if errors.As(err, &app) {
		reply(w, app.Status, app.Code, nil, app.Message)
	} else {
		reply(w, 500, 5000, nil, "服务器内部错误")
	}
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func newID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	id[6] = (id[6] & 15) | 64
	id[8] = (id[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:]), nil
}

type CaptchaStore interface {
	Set(context.Context, string, []byte, time.Duration) error
	Take(context.Context, string) ([]byte, error)
}
type Captcha struct{ Store CaptchaStore }

func (c Captcha) Register(mux *http.ServeMux) { mux.HandleFunc("GET /api/auth/captcha", c.Issue) }
func (c Captcha) Issue(w http.ResponseWriter, r *http.Request) {
	a, err := rand.Int(rand.Reader, big.NewInt(15))
	if err != nil {
		fail(w, err)
		return
	}
	b, err := rand.Int(rand.Reader, big.NewInt(15))
	if err != nil {
		fail(w, err)
		return
	}
	id, err := newID()
	if err != nil {
		fail(w, err)
		return
	}
	answer, _ := json.Marshal(strconv.FormatInt(a.Int64()+b.Int64()+2, 10))
	if err = c.Store.Set(r.Context(), "captcha:"+id, answer, 5*time.Minute); err != nil {
		fail(w, &Error{503, 5000, "验证码服务暂不可用，请稍后重试"})
		return
	}
	svg := mathSVG(fmt.Sprintf("%d+%d=?", a.Int64()+1, b.Int64()+1))
	// Keep the legacy JSON body even though its advertised MIME type is SVG.
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{"id": id, "svg": svg}, "message": "ok"})
}
func (c Captcha) Verify(ctx context.Context, id, code string) error {
	if id == "" || code == "" {
		return &Error{400, 1002, "请输入验证码"}
	}
	invalid := &Error{400, 1002, "验证码错误或已过期"}
	if !uuidPattern.MatchString(id) || len(code) > 64 {
		return invalid
	}
	value, err := c.Store.Take(ctx, "captcha:"+id)
	if errors.Is(err, cache.ErrMiss) {
		return invalid
	}
	if err != nil {
		return &Error{503, 5000, "验证码服务暂不可用，请稍后重试"}
	}
	var answer string
	if json.Unmarshal(value, &answer) != nil || subtle.ConstantTimeCompare([]byte(strings.ToLower(code)), []byte(answer)) != 1 {
		return invalid
	}
	return nil
}

// Draw glyph paths instead of embedding the equation or answer as SVG text.
func mathSVG(equation string) string {
	segments := [10]string{"abcdef", "bc", "abdeg", "abcdg", "bcfg", "acdfg", "acdefg", "abc", "abcdefg", "abcdfg"}
	paths := map[rune]string{'a': "M2 0H10", 'b': "M12 2V10", 'c': "M12 14V22", 'd': "M2 24H10", 'e': "M0 14V22", 'f': "M0 2V10", 'g': "M2 12H10"}
	var svg strings.Builder
	svg.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" width="160" height="50" viewBox="0 0 160 50"><rect width="160" height="50" fill="#fefdf9"/><g fill="none" stroke="#946c09" stroke-width="2.5" stroke-linecap="round">`)
	for i, ch := range equation {
		fmt.Fprintf(&svg, `<g transform="translate(%d 12)">`, 10+i*20)
		if ch >= '0' && ch <= '9' {
			for _, segment := range segments[ch-'0'] {
				fmt.Fprintf(&svg, `<path d="%s"/>`, paths[segment])
			}
		} else {
			p := map[rune]string{'+': "M0 12H12M6 6V18", '=': "M0 8H12M0 16H12", '?': "M0 4Q6 -3 12 4Q14 10 6 13V17M6 23V24"}[ch]
			fmt.Fprintf(&svg, `<path d="%s"/>`, p)
		}
		svg.WriteString(`</g>`)
	}
	svg.WriteString(`</g><path d="M0 35Q65 15 160 32M0 16Q95 45 160 12" fill="none" stroke="#9bb5b1" stroke-width="1"/></svg>`)
	return svg.String()
}
