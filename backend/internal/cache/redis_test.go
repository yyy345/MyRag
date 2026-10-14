package cache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRedisIntegration(t *testing.T) {
	if os.Getenv("RUN_REDIS_TESTS") != "1" {
		t.Skip("set RUN_REDIS_TESTS=1 for isolated Redis tests")
	}
	ctx := context.Background()
	store, err := New("redis://127.0.0.1:56380/0")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("go-cache-test:%d:", time.Now().UnixNano())
	defer store.DeletePrefix(ctx, prefix)
	if err = store.Set(ctx, prefix+"ttl", []byte(`{"fixture":true}`), 80*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if value, ok := store.Lookup(ctx, prefix+"ttl"); !ok || string(value) != `{"fixture":true}` {
		t.Fatal("cache read differs")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err = store.Get(ctx, prefix+"ttl")
		if errors.Is(err, ErrMiss) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("TTL did not expire")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err = store.Set(ctx, prefix+"once", []byte("credential"), time.Second); err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := store.Take(ctx, prefix+"once")
			if err == nil && string(value) == "credential" {
				successes.Add(1)
			} else if !errors.Is(err, ErrMiss) {
				t.Errorf("unexpected take error: %v", err)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("one-time key consumed %d times", successes.Load())
	}
	for i := range 150 {
		if err = store.Set(ctx, fmt.Sprintf("%sbatch:%d", prefix, i), []byte("x"), time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if err = store.Set(ctx, prefix+"keep", []byte("kept"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err = store.DeletePrefix(ctx, prefix+"batch:"); err != nil {
		t.Fatal(err)
	}
	for i := range 150 {
		if _, ok := store.Lookup(ctx, fmt.Sprintf("%sbatch:%d", prefix, i)); ok {
			t.Fatal("prefix invalidation left a key")
		}
	}
	if _, ok := store.Lookup(ctx, prefix+"keep"); !ok {
		t.Fatal("unrelated key deleted")
	}
	for _, suffix := range []string{"literal*key:1", "literal-other-key:1"} {
		if err = store.Set(ctx, prefix+suffix, []byte("x"), time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if err = store.DeletePrefix(ctx, prefix+"literal*key:"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Lookup(ctx, prefix+"literal-other-key:1"); !ok {
		t.Fatal("prefix glob escaped its scope")
	}
	if err = store.DeletePrefix(ctx, ""); err == nil {
		t.Fatal("empty prefix accepted")
	}
	if err = store.Set(ctx, prefix+"bad", nil, 0); err == nil {
		t.Fatal("non-expiring key accepted")
	}
	failed, err := New("redis://127.0.0.1:56380/0")
	if err != nil {
		t.Fatal(err)
	}
	failed.Close()
	if _, ok := failed.Lookup(ctx, prefix+"keep"); ok {
		t.Fatal("unavailable cache returned a hit")
	}
	if _, err = failed.Take(ctx, prefix+"keep"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("authentication cache failed open")
	}
}
