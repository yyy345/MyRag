package repository

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil || dsn == "" {
		return nil, errors.New("invalid DATABASE_URL")
	}
	config.ConnConfig.ConnectTimeout = 5 * time.Second
	config.MaxConns = 10
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, errors.New("database pool initialization failed")
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, errors.New("database connection failed")
	}
	return pool, nil
}

// CheckSchema is read-only; API startup must never apply migrations implicitly.
func CheckSchema(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return errors.New("cannot inspect database schema")
	}
	defer tx.Rollback(context.Background())
	return validateRuntimeSchema(ctx, tx)
}
