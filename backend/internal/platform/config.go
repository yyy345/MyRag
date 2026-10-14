package platform

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr, DatabaseURL, JWTSecret, AIBaseURL, AIServiceToken, RedisURL string
	AIParseTimeout                                                        time.Duration
}

// Do not accidentally expose credentials through formatted logs.
func (Config) String() string { return "Config{credentials:redacted}" }

func LoadConfig(getenv func(string) string) (Config, error) {
	c := Config{HTTPAddr: getenv("GO_HTTP_ADDR"), DatabaseURL: getenv("DATABASE_URL"), JWTSecret: getenv("JWT_SECRET"), AIBaseURL: getenv("AI_BASE_URL"), AIServiceToken: getenv("AI_SERVICE_TOKEN")}
	c.RedisURL = getenv("REDIS_URL")
	redis, redisErr := url.Parse(c.RedisURL)
	if redisErr != nil || redis.Hostname() == "" || (redis.Scheme != "redis" && redis.Scheme != "rediss") {
		return Config{}, errors.New("REDIS_URL must identify a Redis server")
	}
	if c.HTTPAddr == "" {
		c.HTTPAddr = "127.0.0.1:8080"
	}
	if c.AIBaseURL == "" {
		c.AIBaseURL = "http://127.0.0.1:8000"
	}
	_, port, err := net.SplitHostPort(c.HTTPAddr)
	n, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || n < 1 || n > 65535 {
		return Config{}, errors.New("GO_HTTP_ADDR must be host:port with port 1..65535")
	}
	if len(strings.TrimSpace(c.JWTSecret)) < 32 {
		return Config{}, errors.New("JWT_SECRET must contain at least 32 characters")
	}
	if len(c.AIServiceToken) < 32 || strings.IndexFunc(c.AIServiceToken, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
		return Config{}, errors.New("AI_SERVICE_TOKEN must contain at least 32 printable non-space ASCII characters")
	}
	c.AIParseTimeout = time.Minute
	if raw := getenv("AI_PARSE_TIMEOUT"); raw != "" {
		c.AIParseTimeout, err = time.ParseDuration(raw)
		if err != nil || c.AIParseTimeout <= 0 || c.AIParseTimeout > 5*time.Minute {
			return Config{}, errors.New("AI_PARSE_TIMEOUT must be a positive duration no greater than 5m")
		}
	}
	db, err := url.Parse(c.DatabaseURL)
	if err != nil || db.Hostname() == "" || (db.Scheme != "postgres" && db.Scheme != "postgresql") || db.Path == "" || db.Path == "/" {
		return Config{}, errors.New("DATABASE_URL must identify a PostgreSQL database")
	}
	ai, err := url.Parse(c.AIBaseURL)
	if err != nil || ai.Hostname() == "" || (ai.Scheme != "http" && ai.Scheme != "https") || ai.User != nil || ai.RawQuery != "" || ai.ForceQuery || ai.Fragment != "" {
		return Config{}, errors.New("AI_BASE_URL must be an HTTP(S) URL without credentials, query or fragment")
	}
	c.AIBaseURL = strings.TrimRight(c.AIBaseURL, "/")
	return c, nil
}
