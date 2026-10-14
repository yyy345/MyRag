// Explicit standalone process. The API never launches storage cleanup implicitly.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ai-library/backend/internal/repository"
	"ai-library/backend/internal/storage"
)

func main() {
	cfg, enabled, err := storage.LoadConfig(os.Getenv)
	if err != nil || !enabled {
		log.Fatal("complete OSS configuration is required for storage cleanup")
	}
	objects, err := storage.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	pool, err := repository.Open(startup, os.Getenv("DATABASE_URL"))
	if err != nil {
		cancel()
		log.Fatal(err)
	}
	defer pool.Close()
	if err = repository.CheckSchema(startup, pool); err != nil {
		cancel()
		log.Fatal(err)
	}
	cancel()
	store := &repository.Entries{Pool: pool}
	for ctx.Err() == nil {
		worked, err := store.CleanupOne(ctx, objects)
		if err != nil && ctx.Err() == nil {
			log.Print("storage cleanup failed; durable job remains pending")
		}
		if worked && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}
