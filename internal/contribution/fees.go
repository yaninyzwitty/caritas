package contribution

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	contributionsqlc "github.com/yaninyzwitty/caritas-backend/internal/contribution/repository/sqlc"
)

// monthlyFees locks the obligations until receipt posting commits. Summing
// completed allocations also respects payments recorded before this migration.
func monthlyFees(ctx context.Context, q contributionsqlc.Querier, memberID pgtype.UUID, period pgtype.Date, excludeReceipt pgtype.UUID) ([]AllocationInput, error) {
	if !period.Valid || period.InfinityModifier != pgtype.Finite {
		return nil, ErrInvalidPayment
	}
	// 1st day of the month
	// from midnight East African Time
	period.Time = time.Date(period.Time.Year(), period.Time.Month(), 1, 0, 0, 0, 0, time.UTC)
	now := time.Now().In(time.FixedZone("EAT", 3*60*60))

	if period.Time.After(time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)) {
		return nil, ErrInvalidPayment
	}
	if err := q.AssessMonthlyFees(ctx, contributionsqlc.AssessMonthlyFeesParams{MemberID: memberID, Period: period}); err != nil {
		return nil, fmt.Errorf("assess membership fees: %w", err)
	}
	fees, err := q.LockMonthlyFees(ctx, contributionsqlc.LockMonthlyFeesParams{MemberID: memberID, Period: period})
	if err != nil {
		return nil, fmt.Errorf("lock membership fees: %w", err)
	}
	var result []AllocationInput
	for _, fee := range fees {
		paid, err := q.SumMonthlyFeePayments(ctx, contributionsqlc.SumMonthlyFeePaymentsParams{
			MemberID: memberID, Period: period, Type: fee.Type, ExcludeReceipt: excludeReceipt,
		})
		if err != nil {
			return nil, fmt.Errorf("sum membership fee payments: %w", err)
		}
		due := numericToScale(fee.Amount, -4)
		due.Sub(due, numericToScale(paid, -4))
		if due.Sign() > 0 {
			result = append(result, AllocationInput{Type: fee.Type, Amount: pgtype.Numeric{Int: due, Exp: -4, Valid: true}})
		}
	}
	return result, nil
}

// calculatedAllocations keeps caller-selected share/loan totals, supplies the
// mandatory fees, and combines old client principal/interest splits into one
// loan payment. Only Loans decides how much of that payment settles interest.
func calculatedAllocations(input, fees []AllocationInput) ([]AllocationInput, error) {
	result := make([]AllocationInput, 0, len(input)+2)
	seen := make(map[string]bool)
	loans := make(map[pgtype.UUID]int)
	for _, item := range input {
		if !positive(item.Amount) {
			return nil, ErrInvalidAllocation
		}
		key := allocationKey(item.Type, item.TargetID)
		if seen[key] {
			return nil, ErrDuplicateAllocation
		}
		seen[key] = true
		switch item.Type {
		case contributionsqlc.ContributionAllocationTypeCom, contributionsqlc.ContributionAllocationTypeLgom:
			if item.TargetID.Valid {
				return nil, ErrInvalidAllocation
			}
			matches := false
			for _, fee := range fees {
				if fee.Type == item.Type && numericToScale(fee.Amount, -4).Cmp(numericToScale(item.Amount, -4)) == 0 {
					matches = true
				}
			}
			if !matches {
				return nil, ErrMembershipFeeMismatch
			}
			continue
		case contributionsqlc.ContributionAllocationTypeLoanPrincipal, contributionsqlc.ContributionAllocationTypeLoanInterest:
			if !item.TargetID.Valid {
				return nil, ErrInvalidAllocation
			}
			if index, ok := loans[item.TargetID]; ok {
				amount := numericToScale(result[index].Amount, -4)
				amount.Add(amount, numericToScale(item.Amount, -4))
				result[index].Amount = pgtype.Numeric{Int: amount, Exp: -4, Valid: true}
				continue
			}
			loans[item.TargetID] = len(result)
			item.Type = contributionsqlc.ContributionAllocationTypeLoanPrincipal
		}
		result = append(result, item)
	}
	return append(result, fees...), nil
}

// Retry against the original server-calculated fees, even after they are paid.
func allocationsForRetry(input []AllocationInput, plan []byte) ([]AllocationInput, error) {
	stored, err := parseAllocationPlan(plan)
	if err != nil {
		return nil, err
	}
	var fees []AllocationInput
	for _, item := range stored {
		if item.Type == contributionsqlc.ContributionAllocationTypeCom || item.Type == contributionsqlc.ContributionAllocationTypeLgom {
			fees = append(fees, item)
		}
	}
	return calculatedAllocations(input, fees)
}
