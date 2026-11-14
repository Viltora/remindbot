// Package storage owns the PostgreSQL connection pool and schema migrations.
package storage

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"remindbot/internal/config"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// How long to wait for Postgres on startup, e.g. when the bot and the database
// start together after a server reboot.
const connectTimeout = 30 * time.Second

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, cfg config.DB, log *slog.Logger) (*Store, error) {
	pcfg, err := pgxpool.ParseConfig(cfg.ConnString())
	if err != nil {
		return nil, fmt.Errorf("parse db config: %w", err)
	}
	pcfg.ConnConfig.Password = cfg.Password

	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("create db pool: %w", err)
	}

	deadline := time.Now().Add(connectTimeout)
	for {
		err := pool.Ping(ctx)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			pool.Close()
			return nil, fmt.Errorf("postgres is unavailable (%s): %w", cfg.ConnString(), err)
		}
		log.Warn("waiting for postgres", "err", err)
		select {
		case <-ctx.Done():
			pool.Close()
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}

	return &Store{pool: pool}, nil
}

func (s *Store) Close() {
	s.pool.Close()
}

func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// Pool is shared with the River job queue, which keeps its jobs in the same database.
func (s *Store) Pool() *pgxpool.Pool {
	return s.pool
}

// Migrate applies pending migrations: ours (goose) and the River job queue's.
func (s *Store) Migrate(ctx context.Context, log *slog.Logger) error {
	if err := s.migrateSchema(ctx, log); err != nil {
		return err
	}

	migrator, err := rivermigrate.New(riverpgxv5.New(s.pool), nil)
	if err != nil {
		return fmt.Errorf("init river migrations: %w", err)
	}
	res, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	if err != nil {
		return fmt.Errorf("apply river migrations: %w", err)
	}
	for _, v := range res.Versions {
		log.Info("river migration applied", "version", v.Version)
	}
	return nil
}

// migrateSchema applies goose migrations. A Postgres advisory lock guarantees
// that only one process migrates at a time.
func (s *Store) migrateSchema(ctx context.Context, log *slog.Logger) error {
	db := stdlib.OpenDBFromPool(s.pool) // closing db does not close the pool
	defer db.Close()

	fsys, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, fsys, goose.WithSessionLocker(locker))
	if err != nil {
		return fmt.Errorf("init migrations: %w", err)
	}

	results, err := provider.Up(ctx)
	for _, r := range results {
		log.Info("migration applied", "file", r.Source.Path, "duration", r.Duration)
	}
	if err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	current, err := provider.GetDBVersion(ctx)
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	log.Info("database schema is up to date", "version", current)
	return nil
}
