// Standalone dispatcher: does not start AI workers or consume legacy messages.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ai-library/backend/internal/queue"
	"ai-library/backend/internal/repository"
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
	store := &repository.Entries{Pool: pool}
	publisher := queue.NewPublisher(os.Getenv("RABBITMQ_URL"),os.Getenv("INDEX_TASK_QUEUE"))
	for ctx.Err() == nil {
		worked, err := store.DispatchOne(ctx, publisher)
		if err != nil && ctx.Err() == nil {
			log.Print("outbox dispatch failed; unconfirmed records remain pending")
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
