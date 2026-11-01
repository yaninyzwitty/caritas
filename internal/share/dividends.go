package share

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	sharesqlc "github.com/yaninyzwitty/caritas-backend/internal/share/repository/sqlc"
)

// PostDividendInTx keeps an approved dividend and its allocation settlement in
// one transaction. Ordinary purchases cannot replace it: approved dividends
// remain payable after a member becomes inactive, without enabling other writes.
func (s *Service) PostDividendInTx(ctx context.Context, q *sharesqlc.Queries, accountID, memberID pgtype.UUID, branchID int64,
	amount pgtype.Numeric, allocationID, officerID pgtype.UUID) (sharesqlc.ShareTransaction, error) {
	return s.postTransactionInTx(ctx, q, accountID, sharesqlc.ShareTransactionTypeDividend, amount,
		allocationID, officerID, "Approved annual dividend reinvestment", memberID, branchID)
}
