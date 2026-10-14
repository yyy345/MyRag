package main

import (
	"ai-library/backend/internal/ai"
	"ai-library/backend/internal/cache"
	"ai-library/backend/internal/platform"
	"ai-library/backend/internal/repository"
	"ai-library/backend/internal/scheduler"
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if os.Getenv("AUTO_EXTRACT_ENABLED") != "true" {
		log.Fatal("AUTO_EXTRACT_ENABLED=true is required to start scheduled model requests")
	}
	config, err := platform.LoadConfig(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	pool, err := repository.Open(startup, config.DatabaseURL)
	if err != nil {
		cancel()
		log.Fatal(err)
	}
	defer pool.Close()
	err = repository.CheckSchema(startup, pool)
	cancel()
	if err != nil {
		log.Fatal(err)
	}
	redis, err := cache.New(config.RedisURL)
	if err != nil {
		log.Fatal(err)
	}
	defer redis.Close()
	extractor, err := ai.NewExtractor(config.AIBaseURL, config.AIServiceToken, 2*time.Minute)
	if err != nil {
		log.Fatal(err)
	}
	runner := &scheduler.Runner{Store: &repository.Entries{Pool: pool}, Extractor: extractor, Cache: redis}
	for ctx.Err() == nil {
		if err = runner.RunDue(ctx, time.Now()); err != nil && ctx.Err() == nil {
			log.Print("scheduled extraction incomplete; pending work will retry")
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
	}
}
