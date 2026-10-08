package contribution

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	contributionsqlc "github.com/yaninyzwitty/caritas-backend/internal/contribution/repository/sqlc"
)

// CreateCharge records an obligation separately from cash received. Without the
// obligation, a partial payment cannot leave a verifiable outstanding balance.
func (s *Service) CreateCharge(ctx context.Context, params contributionsqlc.CreateContributionChargeParams) (contributionsqlc.ContributionCharge, error) {
	params.IdempotencyKey = strings.TrimSpace(params.IdempotencyKey)
	params.Reason = strings.TrimSpace(params.Reason)
	if params.IdempotencyKey == "" || params.Reason == "" || !params.MemberID.Valid || !params.CreatedBy.Valid || params.BranchID <= 0 || !positive(params.Amount) {
		return contributionsqlc.ContributionCharge{}, ErrInvalidCharge
	}
	scaled := numericToScale(params.Amount, -4)
	if len(scaled.String()) > 19 || (params.Amount.Exp < -4 && new(big.Int).Rem(params.Amount.Int, pow10(-4-params.Amount.Exp)).Sign() != 0) {
		return contributionsqlc.ContributionCharge{}, ErrInvalidCharge
	}
	switch params.Category {
	case "literature", "caritas_registration", "lsf", "laptop", "penalty", "other":
	default:
		return contributionsqlc.ContributionCharge{}, ErrInvalidCharge
	}
	row, err := s.store.CreateContributionCharge(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		row, err = s.store.GetContributionChargeByKey(ctx, params.IdempotencyKey)
		if errors.Is(err, pgx.ErrNoRows) {
			return contributionsqlc.ContributionCharge{}, ErrInvalidCharge
		}
		if err == nil && (row.MemberID != params.MemberID || row.BranchID != params.BranchID || row.Category != params.Category || row.Reason != params.Reason || numericToScale(row.Amount, -4).Cmp(scaled) != 0) {
			return contributionsqlc.ContributionCharge{}, ErrChargeConflict
		}
	}
	if err != nil {
		return contributionsqlc.ContributionCharge{}, fmt.Errorf("create charge: %w", err)
	}
	return row, nil
}

// validateChargeAllocations locks all charge targets before any ledger posting.
// Without these locks and the balance check, concurrent receipts can overpay a
// charge, or a wrong member's charge can fail after a share/loan posting commits.
func validateChargeAllocations(ctx context.Context, q contributionsqlc.Querier, memberID pgtype.UUID, branchID int64, allocations []AllocationInput, excludeReceipt pgtype.UUID) error {
	payments := make(map[pgtype.UUID]AllocationInput)
	var ids []pgtype.UUID
	for _, item := range allocations {
		if item.Type != contributionsqlc.ContributionAllocationTypeOtherCharge && item.Type != contributionsqlc.ContributionAllocationTypePenalty {
			continue
		}
		if !item.TargetID.Valid || !positive(item.Amount) {
			return ErrInvalidAllocation
		}
		if _, exists := payments[item.TargetID]; exists {
			return ErrDuplicateAllocation
		}
		if item.Amount.Exp < -4 && new(big.Int).Rem(item.Amount.Int, pow10(-4-item.Amount.Exp)).Sign() != 0 {
			return ErrInvalidAllocation
		}
		payments[item.TargetID] = item
		ids = append(ids, item.TargetID)
	}
	if len(ids) == 0 {
		return nil
	}
	charges, err := q.LockContributionCharges(ctx, ids)
	if err != nil {
		return fmt.Errorf("lock charges: %w", err)
	}
	if len(charges) != len(ids) {
		return ErrInvalidCharge
	}
	for _, charge := range charges {
		item := payments[charge.ID]
		if charge.MemberID != memberID || charge.BranchID != branchID ||
			(charge.Category == "penalty") != (item.Type == contributionsqlc.ContributionAllocationTypePenalty) {
			return ErrInvalidCharge
		}
		paid, err := q.SumContributionChargePayments(ctx, contributionsqlc.SumContributionChargePaymentsParams{ChargeID: charge.ID, ExcludeReceipt: excludeReceipt})
		if err != nil {
			return fmt.Errorf("sum charge payments: %w", err)
		}
		outstanding := numericToScale(charge.Amount, -4)
		outstanding.Sub(outstanding, numericToScale(paid, -4))
		if numericToScale(item.Amount, -4).Cmp(outstanding) > 0 {
			return ErrChargeOverpayment
		}
	}
	return nil
}
