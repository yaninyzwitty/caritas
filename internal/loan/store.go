package loan

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	loansqlc "github.com/yaninyzwitty/caritas-backend/internal/loan/repository/sqlc"
)

type Store struct {
	loansqlc.Querier
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{
		Querier: loansqlc.New(pool),
		pool:    pool,
	}
}

func (s *Store) ExecTx(ctx context.Context, fn func(q loansqlc.Querier) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	defer func() {
		if err := tx.Rollback(ctx); err != nil {
			slog.Error("rollback error", "error", err)
		}

	}()

	q := loansqlc.New(tx)
	if err := fn(q); err != nil {
		return fmt.Errorf("exec tx: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}
