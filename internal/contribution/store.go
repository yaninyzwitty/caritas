package contribution

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	contributionsqlc "github.com/yaninyzwitty/caritas-backend/internal/contribution/repository/sqlc"
)

type Store struct {
	contributionsqlc.Querier
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{
		Querier: contributionsqlc.New(pool),
		pool:    pool,
	}
}

// ExecTx keeps receipt creation and its allocation rows atomic. The final
// domain spec treats a receipt's allocation plan as one unit, so committing the
// receipt without its rows would leave later webhook processing unable to prove
// what the received money was meant to do.
func (s *Store) ExecTx(ctx context.Context, fn func(q contributionsqlc.Querier) error) error {
	return s.ExecReceiptTx(ctx, func(tx pgx.Tx) error {
		return fn(contributionsqlc.New(tx))
	})
}

// ExecReceiptTx supplies one transaction to all owning ledgers. Without it,
// receipt processing holds a pool connection while waiting for another one.
func (s *Store) ExecReceiptTx(ctx context.Context, fn func(pgx.Tx) error) error {
	started := time.Now()
	tx, err := s.pool.Begin(ctx)
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond || err != nil {
		slog.Warn("acquire receipt transaction", "duration", elapsed, "acquired_connections", s.pool.Stat().AcquiredConns(), "max_connections", s.pool.Stat().MaxConns(), "error", err)
	}
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := tx.Rollback(rollbackCtx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			slog.Error("rollback transaction", "error", err)
		}
	}()

	if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '2s'"); err != nil {
		return fmt.Errorf("set receipt lock timeout: %w", err)
	}
	if err := fn(tx); err != nil {
		return fmt.Errorf("exec tx: %w", err)
	}

	started = time.Now()
	err = tx.Commit(ctx)
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond || err != nil {
		slog.Warn("commit receipt transaction", "duration", elapsed, "error", err)
	}
	if err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}
