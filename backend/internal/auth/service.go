package auth

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

type User struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Email     string `json:"email"`
	CreatedAt string `json:"created_at"`
}
type credentials struct {
	Username    string `json:"username"`
	Email       string `json:"email"`
	Password    string `json:"password"`
	CaptchaID   string `json:"captchaId"`
	CaptchaCode string `json:"captchaCode"`
}
type Claims struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	jwt.RegisteredClaims
}
type Service struct {
	Pool    *pgxpool.Pool
	Captcha Captcha
	Secret  []byte
}

const maxUsernameRunes = 200
const maxEmailBytes = 320

// Login accepts either a registered username or an email address. Unicode
// usernames must use the same character limit as registration.
func validLoginIdentity(identity string) bool {
	return identity != "" && (utf8.RuneCountInString(identity) <= maxUsernameRunes || len(identity) <= maxEmailBytes)
}

func (s *Service) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/register", s.Register)
	mux.HandleFunc("POST /api/auth/login", s.Login)
	mux.HandleFunc("GET /api/auth/me", s.Me)
}
func decode(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(target); err != nil {
		return &Error{400, 5000, "请求参数格式不正确"}
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return &Error{400, 5000, "请求参数格式不正确"}
	}
	return nil
}
func (s *Service) token(user User) (string, error) {
	now := time.Now().UTC()
	claims := Claims{ID: user.ID, Username: user.Username, RegisteredClaims: jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(7 * 24 * time.Hour))}}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.Secret)
}
func (s *Service) Register(w http.ResponseWriter, r *http.Request) {
	var input credentials
	if err := decode(w, r, &input); err != nil {
		fail(w, err)
		return
	}
	if err := s.Captcha.Verify(r.Context(), input.CaptchaID, input.CaptchaCode); err != nil {
		fail(w, err)
		return
	}
	input.Username = strings.TrimSpace(input.Username)
	input.Email = strings.TrimSpace(input.Email)
	address, err := mail.ParseAddress(input.Email)
	if input.Username == "" || utf8.RuneCountInString(input.Username) > maxUsernameRunes || err != nil || address.Address != input.Email || len(input.Email) > maxEmailBytes || utf8.RuneCountInString(input.Password) < 6 || len(input.Password) > 72 {
		fail(w, &Error{400, 1002, "请填写有效的用户名、邮箱和密码（至少6个字符，最多72字节）"})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), 10)
	if err != nil {
		fail(w, err)
		return
	}
	id, err := newID()
	if err != nil {
		fail(w, err)
		return
	}
	var created time.Time
	err = s.Pool.QueryRow(r.Context(), `INSERT INTO "user" (id,username,email,password_hash,updated_at) VALUES ($1::uuid,$2,$3,$4,now()) RETURNING created_at`, id, input.Username, input.Email, string(hash)).Scan(&created)
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" {
			fail(w, &Error{409, 1003, "用户名或邮箱已存在"})
		} else {
			fail(w, err)
		}
		return
	}
	user := User{ID: id, Username: input.Username, Email: input.Email, CreatedAt: created.UTC().Format("2006-01-02T15:04:05.000Z")}
	token, err := s.token(user)
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, 200, 0, map[string]any{"token": token, "user": user}, "ok")
}

func (s *Service) Authenticate(r *http.Request) (*Claims, error) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return nil, &Error{401, 1001, "未登录，请先登录"}
	}
	invalid := &Error{401, 1001, "登录已过期，请重新登录"}
	if len(header) > 16384 {
		return nil, invalid
	}
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(strings.TrimPrefix(header, "Bearer "), claims, func(*jwt.Token) (any, error) { return s.Secret, nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())
	if err != nil || !token.Valid || !uuidPattern.MatchString(claims.ID) || claims.Username == "" {
		return nil, invalid
	}
	return claims, nil
}
func (s *Service) Login(w http.ResponseWriter, r *http.Request) {
	var input credentials
	if err := decode(w, r, &input); err != nil {
		fail(w, err)
		return
	}
	if err := s.Captcha.Verify(r.Context(), input.CaptchaID, input.CaptchaCode); err != nil {
		fail(w, err)
		return
	}
	invalid := &Error{401, 1002, "用户名或密码错误"}
	if !validLoginIdentity(input.Username) || input.Password == "" {
		fail(w, invalid)
		return
	}
	var user User
	var hash string
	var created time.Time
	err := s.Pool.QueryRow(r.Context(), `SELECT id::text,username,email,password_hash,created_at FROM "user" WHERE username=$1 OR email=$1 LIMIT 1`, input.Username).Scan(&user.ID, &user.Username, &user.Email, &hash, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, invalid)
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	// bcryptjs historically truncates at 72 UTF-8 bytes, including long legacy passwords.
	password := []byte(input.Password)
	if len(password) > 72 {
		password = password[:72]
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), password) != nil {
		fail(w, invalid)
		return
	}
	user.CreatedAt = created.UTC().Format("2006-01-02T15:04:05.000Z")
	token, err := s.token(user)
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, 200, 0, map[string]any{"token": token, "user": user}, "ok")
}
func (s *Service) Me(w http.ResponseWriter, r *http.Request) {
	claims, err := s.Authenticate(r)
	if err != nil {
		fail(w, err)
		return
	}
	var user User
	var created time.Time
	err = s.Pool.QueryRow(r.Context(), `SELECT id::text,username,email,created_at FROM "user" WHERE id=$1::uuid`, claims.ID).Scan(&user.ID, &user.Username, &user.Email, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, &Error{404, 1001, "用户不存在"})
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	user.CreatedAt = created.UTC().Format("2006-01-02T15:04:05.000Z")
	reply(w, 200, 0, user, "ok")
}
