package contribution

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	contributionsqlc "github.com/yaninyzwitty/caritas-backend/internal/contribution/repository/sqlc"
)

// RecoverPendingReceipts retries persisted work after timeouts or server restarts.
// The receipt lock and ledger references make concurrent RPC/worker retries safe.
func (s *Service) RecoverPendingReceipts(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		params := contributionsqlc.ListPendingContributionReceiptsParams{
			Cutoff: pgtype.Timestamptz{Time: time.Now().Add(-10 * time.Second), Valid: true},
		}
		for ctx.Err() == nil {
			readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			receipts, err := s.store.ListPendingContributionReceipts(readCtx, params)
			cancel()
			if err != nil {
				slog.Error("list pending contribution receipts", "error", err)
				break
			}
			for _, receipt := range receipts {
				if ctx.Err() != nil {
					return
				}
				if _, err := s.ProcessReceipt(ctx, receipt.ID, receipt.ReceivedBy); err != nil {
					slog.Warn("recover contribution receipt", "receipt_id", receipt.ID.String(), "error", err)
				}
			}
			if len(receipts) < 50 {
				break
			}
			last := receipts[len(receipts)-1]
			params.CursorCreatedAt, params.CursorID = last.CreatedAt, last.ID
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
