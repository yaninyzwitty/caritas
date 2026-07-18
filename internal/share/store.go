package share

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	sharesqlc "github.com/yaninyzwitty/caritas-backend/internal/share/repository/sqlc"
)

type Store struct {
	sharesqlc.Querier
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{
		Querier: sharesqlc.New(pool),
		pool:    pool,
	}
}

// ExecTx runs fn against a single pgx transaction so multi-table writes (the
// ledger post: lock account, read balance, insert transaction) commit atomically.
// Without it a crash mid-post could leave the balance check and the transaction
// insert on different sides of a commit, corrupting the ledger.
func (s *Store) ExecTx(ctx context.Context, fn func(q sharesqlc.Querier) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	defer func() {
		if err := tx.Rollback(ctx); err != nil {
			slog.Error("rollback error", "error", err)
		}

	}()

	q := sharesqlc.New(tx)
	if err := fn(q); err != nil {
		return fmt.Errorf("exec tx: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}
