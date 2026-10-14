package cache

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

var ErrMiss = errors.New("cache miss")
var ErrUnavailable = errors.New("cache unavailable")

type Store struct{ client *redis.Client }

func New(rawURL string) (*Store, error) {
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, errors.New("invalid REDIS_URL")
	}
	options.DialTimeout = time.Second
	options.ReadTimeout = time.Second
	options.WriteTimeout = time.Second
	options.MaxRetries = -1
	options.ContextTimeoutEnabled = true
	return &Store{client: redis.NewClient(options)}, nil
}
func (s *Store) Close() error { return s.client.Close() }
func (s *Store) Ping(ctx context.Context) error {
	if s.client.Ping(ctx).Err() != nil {
		return ErrUnavailable
	}
	return nil
}
func result(command *redis.StringCmd) ([]byte, error) {
	value, err := command.Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrMiss
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	return value, nil
}
func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	return result(s.client.Get(ctx, key))
}

// Lookup degrades a business-cache outage to a miss. Authentication uses Take instead.
func (s *Store) Lookup(ctx context.Context, key string) ([]byte, bool) {
	value, err := s.Get(ctx, key)
	return value, err == nil
}
func (s *Store) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if ttl < time.Millisecond {
		return errors.New("cache TTL must be at least 1ms")
	}
	if s.client.Set(ctx, key, value, ttl).Err() != nil {
		return ErrUnavailable
	}
	return nil
}

// Take is atomic and fails closed: an outage cannot validate a one-time credential.
func (s *Store) Take(ctx context.Context, key string) ([]byte, error) {
	return result(s.client.GetDel(ctx, key))
}
func (s *Store) Delete(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	if s.client.Del(ctx, keys...).Err() != nil {
		return ErrUnavailable
	}
	return nil
}
func (s *Store) DeletePrefix(ctx context.Context, prefix string) error {
	if prefix == "" {
		return errors.New("empty cache prefix is not allowed")
	}
	pattern := strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`, `]`, `\]`).Replace(prefix) + "*"
	var cursor uint64
	for {
		keys, next, err := s.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return ErrUnavailable
		}
		if err = s.Delete(ctx, keys...); err != nil {
			return err
		}
		cursor = next
		if cursor == 0 {
			return nil
		}
	}
}
