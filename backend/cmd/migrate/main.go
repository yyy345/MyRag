package main

import (
	"ai-library/backend/internal/repository"
	"context"
	"flag"
	"log"
	"os"
	"time"
)

func main() {
	directory := flag.String("migrations", "../server/prisma/migrations", "historical SQL directory")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	migrations, err := repository.ReadMigrations(*directory)
	if err != nil {
		log.Fatal(err)
	}
	pool, err := repository.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	if err = repository.Apply(ctx, pool, migrations); err != nil {
		log.Fatal(err)
	}
	log.Print("database migrations and schema checks complete")
}
