package contribution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	contributionsqlc "github.com/yaninyzwitty/caritas-backend/internal/contribution/repository/sqlc"
)

type Service struct {
	store *Store
}

func NewService(store *Store) *Service {
	return &Service{store: store}
}

type AllocationInput struct {
	Type     contributionsqlc.ContributionAllocationType
	TargetID pgtype.UUID
	Amount   pgtype.Numeric
}

type CreatedReceipt struct {
	Receipt     contributionsqlc.ContributionReceipt
	Allocations []contributionsqlc.ContributionAllocation
}

// CreateReceipt persists the received money and its allocation rows in one
// transaction. The final spec says a contribution receipt is complete only
// after every authoritative allocation succeeds, so this first step must never
// leave a receipt without the rows that future processing will retry or audit.
func (s *Service) CreateReceipt(
	ctx context.Context,
	params contributionsqlc.InsertContributionReceiptParams,
	allocations []AllocationInput,
) (CreatedReceipt, error) {
	if err := validateReceipt(params, allocations); err != nil {
		return CreatedReceipt{}, err
	}

	var result CreatedReceipt
	err := s.store.ExecTx(ctx, func(q contributionsqlc.Querier) error {
		receipt, err := q.InsertContributionReceipt(ctx, params)
		switch {
		case err == nil:
			result.Receipt = receipt
		case errors.Is(err, pgx.ErrNoRows):
			receipt, err = existingReceipt(ctx, q, params)
			if err != nil {
				return err
			}
			result.Receipt = receipt
			rows, err := q.ListContributionAllocationsByReceipt(ctx, receipt.ID)
			if err != nil {
				return fmt.Errorf("list existing allocations: %w", err)
			}
			result.Allocations = rows
			return nil
		default:
			return fmt.Errorf("insert receipt: %w", err)
		}

		result.Allocations = make([]contributionsqlc.ContributionAllocation, 0, len(allocations))
		for _, allocation := range allocations {
			row, err := q.InsertContributionAllocation(ctx, contributionsqlc.InsertContributionAllocationParams{
				ReceiptID: result.Receipt.ID,
				Type:      allocation.Type,
				TargetID:  allocation.TargetID,
				Amount:    allocation.Amount,
			})
			switch {
			case err == nil:
				result.Allocations = append(result.Allocations, row)
			case errors.Is(err, pgx.ErrNoRows):
				rows, err := q.ListContributionAllocationsByReceipt(ctx, result.Receipt.ID)
				if err != nil {
					return fmt.Errorf("list existing allocations: %w", err)
				}
				result.Allocations = rows
				return nil
			default:
				return fmt.Errorf("insert allocation: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return CreatedReceipt{}, err
	}
	return result, nil
}

func (s *Service) GetReceipt(ctx context.Context, id pgtype.UUID) (contributionsqlc.ContributionReceipt, error) {
	receipt, err := s.store.GetContributionReceiptByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return contributionsqlc.ContributionReceipt{}, ErrReceiptNotFound
		}
		return contributionsqlc.ContributionReceipt{}, fmt.Errorf("get receipt: %w", err)
	}
	return receipt, nil
}

func existingReceipt(
	ctx context.Context,
	q contributionsqlc.Querier,
	params contributionsqlc.InsertContributionReceiptParams,
) (contributionsqlc.ContributionReceipt, error) {
	if params.ExternalTransactionID.Valid {
		receipt, err := q.GetContributionReceiptByExternalTransactionID(ctx, params.ExternalTransactionID)
		if err == nil {
			return receipt, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return contributionsqlc.ContributionReceipt{}, fmt.Errorf("get receipt by external transaction id: %w", err)
		}
	}
	if params.CheckoutRequestID.Valid {
		receipt, err := q.GetContributionReceiptByCheckoutRequestID(ctx, params.CheckoutRequestID)
		if err == nil {
			return receipt, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return contributionsqlc.ContributionReceipt{}, fmt.Errorf("get receipt by checkout request id: %w", err)
		}
	}
	return contributionsqlc.ContributionReceipt{}, ErrReceiptNotFound
}

func validateReceipt(params contributionsqlc.InsertContributionReceiptParams, allocations []AllocationInput) error {
	if !positive(params.ReceivedAmount) {
		return ErrInvalidReceiptAmount
	}
	if !json.Valid(params.AllocationPlan) {
		return ErrInvalidAllocationPlan
	}
	if len(allocations) == 0 {
		return ErrAllocationRequired
	}
	hasExternalID := params.ExternalTransactionID.Valid && strings.TrimSpace(params.ExternalTransactionID.String) != ""
	hasCheckoutID := params.CheckoutRequestID.Valid && strings.TrimSpace(params.CheckoutRequestID.String) != ""
	if !hasExternalID && !hasCheckoutID {
		return ErrReceiptReferenceRequired
	}

	total := new(big.Int)
	seen := make(map[string]struct{}, len(allocations))
	for _, allocation := range allocations {
		if !positive(allocation.Amount) {
			return ErrInvalidAllocation
		}
		key := string(allocation.Type)
		if allocation.TargetID.Valid {
			key += ":" + allocation.TargetID.String()
		}
		if _, ok := seen[key]; ok {
			return ErrDuplicateAllocation
		}
		seen[key] = struct{}{}

		switch allocation.Type {
		case contributionsqlc.ContributionAllocationTypeSharePurchase,
			contributionsqlc.ContributionAllocationTypeLoanPrincipal,
			contributionsqlc.ContributionAllocationTypeLoanInterest,
			contributionsqlc.ContributionAllocationTypePenalty,
			contributionsqlc.ContributionAllocationTypeOverpaymentCredit:
			// Owning-domain allocations must name the exact ledger target up
			// front. Inferring it later from amount, member or phone number is
			// how shares get posted as loan repayments or to the wrong member.
			if !allocation.TargetID.Valid {
				return ErrInvalidAllocation
			}
		}
		total.Add(total, numericToScale(allocation.Amount, -4))
	}
	if total.Cmp(numericToScale(params.ReceivedAmount, -4)) != 0 {
		return ErrAllocationTotalMismatch
	}
	return nil
}

func positive(n pgtype.Numeric) bool {
	return n.Valid && n.Int != nil && n.Int.Sign() > 0
}

func numericToScale(n pgtype.Numeric, scale int32) *big.Int {
	if !n.Valid || n.Int == nil {
		return new(big.Int)
	}

	out := new(big.Int).Set(n.Int)
	diff := n.Exp - scale
	switch {
	case diff > 0:
		out.Mul(out, pow10(diff))
	case diff < 0:
		out.Quo(out, pow10(-diff))
	}
	return out
}

func pow10(exp int32) *big.Int {
	result := big.NewInt(1)
	ten := big.NewInt(10)
	for i := int32(0); i < exp; i++ {
		result.Mul(result, ten)
	}
	return result
}
