package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/tracelog"
	"github.com/yaninyzwitty/caritas-backend/config"
)

func New(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	dbURL, err := config.GetDatabaseURL()
	if err != nil {
		return nil, fmt.Errorf("failed to get database URL: %w", err)
	}

	poolConfig, err := buildPoolConfig(dbURL, cfg.Database)
	if err != nil {
		return nil, fmt.Errorf("failed to build pool config: %w", err)
	}

	pool, err := newPool(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create pool: %w", err)
	}

	if err := validate(ctx, pool); err != nil {
		return nil, fmt.Errorf("failed to validate pool: %w", err)
	}

	return pool, nil
}

func buildPoolConfig(dbURL string, dbCfg config.DatabaseConfig) (*pgxpool.Config, error) {
	poolConfig, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse database URL: %w", err)
	}

	applyPoolSettings(poolConfig, dbCfg)

	return poolConfig, nil
}

func applyPoolSettings(poolConfig *pgxpool.Config, dbCfg config.DatabaseConfig) {
	poolConfig.MaxConns = int32(dbCfg.MaxOpenConns)
	poolConfig.MinConns = int32(dbCfg.MaxIdleConns)
	poolConfig.MaxConnLifetime = dbCfg.ConnMaxLifetime
	poolConfig.MaxConnIdleTime = dbCfg.ConnMaxIdleTime

	poolConfig.ConnConfig.Tracer = &tracelog.TraceLog{
		Logger:   nil,
		LogLevel: tracelog.LogLevelError,
	}
}

func newPool(ctx context.Context, poolConfig *pgxpool.Config) (*pgxpool.Pool, error) {
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}
	return pool, nil
}

func validate(ctx context.Context, pool *pgxpool.Pool) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("failed to ping database: %w", err)
	}
	return nil
}
