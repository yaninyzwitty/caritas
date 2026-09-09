package loan

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	loansqlc "github.com/yaninyzwitty/caritas-backend/internal/loan/repository/sqlc"
	sharesqlc "github.com/yaninyzwitty/caritas-backend/internal/share/repository/sqlc"
)

type Store struct {
	loansqlc.Querier
	pool *pgxpool.Pool
}

// ExecCollateralTx gives loan and share sqlc queries the same transaction.
// Without it, a loan could be approved while its share pledge fails to activate.
func (s *Store) ExecCollateralTx(ctx context.Context, fn func(*loansqlc.Queries, *sharesqlc.Queries) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			slog.Error("rollback transaction", "error", err)
		}
	}()
	if err := fn(loansqlc.New(tx), sharesqlc.New(tx)); err != nil {
		return fmt.Errorf("exec tx: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
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
		if err := tx.Rollback(ctx); err != nil &&
			!errors.Is(err, pgx.ErrTxClosed) {
			// ignore only pgx.ErrTxClosed errors
			// for noise
			slog.Error("rollback transaction", "error", err)

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
