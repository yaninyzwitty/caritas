package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yaninyzwitty/caritas-backend/internal/repository/sqlc"
)

type Transactor interface {
	sqlc.Querier
	ExecTx(ctx context.Context, fn func(q sqlc.Querier) error) error
}

type Store struct {
	sqlc.Querier
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{
		Querier: sqlc.New(pool),
		pool:    pool,
	}
}

func (s *Store) ExecTx(ctx context.Context, fn func(q sqlc.Querier) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin ctx: %w", err)
	}

	defer tx.Rollback(ctx)

	q := sqlc.New(tx)
	if err := fn(q); err != nil {
		return fmt.Errorf("exec tx: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

var _ Transactor = (*Store)(nil)
