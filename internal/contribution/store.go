package contribution

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

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
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	defer func() {
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			slog.Error("rollback transaction", "error", err)
		}
	}()

	q := contributionsqlc.New(tx)
	if err := fn(q); err != nil {
		return fmt.Errorf("exec tx: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}
