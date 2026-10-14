package main

import (
	"ai-library/backend/internal/cache"
	"ai-library/backend/internal/ports"
	"ai-library/backend/internal/queue"
	"ai-library/backend/internal/repository"
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if os.Getenv("RABBITMQ_URL") == "" {
		log.Fatal("RABBITMQ_URL is required")
	}
	pool, err := repository.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	if err = repository.CheckSchema(startup, pool); err != nil {
		cancel()
		log.Fatal(err)
	}
	cancel()
	store, err := cache.New(os.Getenv("REDIS_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	entries := &repository.Entries{Pool: pool}
	consumer := queue.NewResults(os.Getenv("RABBITMQ_URL"),os.Getenv("INDEX_RESULT_QUEUE"))
	for ctx.Err() == nil {
		err = consumer.Run(ctx, func(parent context.Context, result ports.IndexResult) error {
			request, cancel := context.WithTimeout(parent, 10*time.Second)
			defer cancel()
			return entries.ApplyResult(request, result, store)
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Print("result consumer unavailable; reconnecting")
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}
