package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"ai-library/backend/internal/ai"
	"ai-library/backend/internal/auth"
	"ai-library/backend/internal/cache"
	"ai-library/backend/internal/chat"
	"ai-library/backend/internal/knowledge"
	"ai-library/backend/internal/platform"
	"ai-library/backend/internal/queue"
	"ai-library/backend/internal/repository"
	"ai-library/backend/internal/storage"
)

func main() {
	probe := flag.Bool("healthcheck", false, "check local API liveness without starting a service")
	flag.Parse()
	if *probe {
		addr := os.Getenv("GO_HTTP_ADDR")
		if addr == "" {
			addr = "127.0.0.1:8080"
		}
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			os.Exit(1)
		}
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		client := &http.Client{Timeout: 3 * time.Second}
		res, err := client.Get("http://" + net.JoinHostPort(host, port) + "/health/live")
		if err != nil {
			os.Exit(1)
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			os.Exit(1)
		}
		return
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
	if err = repository.CheckSchema(startup, pool); err != nil {
		cancel()
		log.Fatal(err)
	}
	cancel()
	redis, err := cache.New(config.RedisURL)
	if err != nil {
		log.Fatal(err)
	}
	defer redis.Close()
	captcha := auth.Captcha{Store: redis}
	authService := &auth.Service{Pool: pool, Captcha: captcha, Secret: []byte(config.JWTSecret)}
	parser, err := ai.NewParser(config.AIBaseURL, config.AIServiceToken, config.AIParseTimeout)
	if err != nil {
		log.Fatal(err)
	}
	knowledgeService := &knowledge.Handler{Pool: pool, Cache: redis, Auth: authService, Parser: parser}
	vision, err := ai.NewVision(config.AIBaseURL, config.AIServiceToken, time.Minute)
	if err != nil {
		log.Fatal(err)
	}
	knowledgeService.Vision = vision
	ossConfig, enabled, err := storage.LoadConfig(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	if enabled {
		knowledgeService.Images, err = storage.New(ossConfig)
		if err != nil {
			log.Fatal(err)
		}
	}
	chatService := &chat.Handler{Store: &repository.Entries{Pool: pool}, Auth: authService, Cache: redis}
	chatService.Streamer, err = ai.NewStreamer(config.AIBaseURL, config.AIServiceToken, 3*time.Minute)
	if err != nil {
		log.Fatal(err)
	}
	chatService.Extractor, err = ai.NewExtractor(config.AIBaseURL, config.AIServiceToken, 2*time.Minute)
	if err != nil {
		log.Fatal(err)
	}
	listener, err := net.Listen("tcp", config.HTTPAddr)
	if err != nil {
		log.Fatal("Go API could not bind configured address")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	healthClient := &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	readiness := func(ctx context.Context) error {
		if err := pool.Ping(ctx); err != nil {
			return err
		}
		if err := redis.Ping(ctx); err != nil {
			return err
		}
		if err := queue.Ping(ctx, os.Getenv("RABBITMQ_URL")); err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(config.AIBaseURL, "/")+"/health/ready", nil)
		if err != nil {
			return err
		}
		res, err := healthClient.Do(request)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return ai.ErrChatStream
		}
		return nil
	}
	log.Print("starting Go business API")
	if err := platform.Run(ctx, listener, platform.HandlerWithReadiness(readiness, captcha.Register, authService.RegisterRoutes, knowledgeService.Register, chatService.Register), 10*time.Second); err != nil {
		log.Fatal("Go API shutdown failed")
	}
	log.Print("Go API stopped")
}
