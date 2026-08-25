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
	"github.com/yaninyzwitty/caritas-backend/internal/loan"
	"github.com/yaninyzwitty/caritas-backend/internal/share"
)

type Service struct {
	store        *Store
	shareService *share.Service
	loanService  *loan.Service
}

func NewService(store *Store, shareService *share.Service, loanService *loan.Service) *Service {
	return &Service{
		store:        store,
		shareService: shareService,
		loanService:  loanService,
	}
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
		created, err := s.createReceipt(ctx, q, params, allocations)
		result = created
		return err
	})
	if err != nil {
		return CreatedReceipt{}, err
	}
	return result, nil
}

// createReceipt is the transaction-scoped body shared by manual receipt entry
// and Daraja callback processing. Removing it would duplicate the idempotent
// receipt/allocation insert logic and risk one path diverging from the other.
func (s *Service) createReceipt(
	ctx context.Context,
	q contributionsqlc.Querier,
	params contributionsqlc.InsertContributionReceiptParams,
	allocations []AllocationInput,
) (CreatedReceipt, error) {
	receipt, err := q.InsertContributionReceipt(ctx, params)
	var result CreatedReceipt
	switch {
	case err == nil:
		result.Receipt = receipt
	case errors.Is(err, pgx.ErrNoRows):
		receipt, err = existingReceipt(ctx, q, params)
		if err != nil {
			return CreatedReceipt{}, err
		}
		result.Receipt = receipt
		rows, err := q.ListContributionAllocationsByReceipt(ctx, receipt.ID)
		if err != nil {
			return CreatedReceipt{}, fmt.Errorf("list existing allocations: %w", err)
		}

		if err := verifyAllocationsMatch(allocations, rows); err != nil {
			return CreatedReceipt{}, fmt.Errorf(
				"verify existing receipt allocations: %w",
				err,
			)
		}

		return CreatedReceipt{
			Receipt:     receipt,
			Allocations: rows,
		}, nil

	default:
		return CreatedReceipt{}, fmt.Errorf("insert receipt: %w", err)
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
			return CreatedReceipt{}, fmt.Errorf(
				"%w: allocation %s already exists for newly created receipt %s",
				ErrInconsistentReceipt,
				allocationKey(
					allocation.Type,
					allocation.TargetID,
				),
				receipt.ID,
			)
		default:
			return CreatedReceipt{}, fmt.Errorf("insert allocation: %w", err)
		}
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

// ProcessReceipt applies a persisted receipt to each owning ledger exactly once.
// Without this gate, webhooks and manual entries could mark receipts completed
// before Shares or Loans has accepted the money, which the final spec forbids.
func (s *Service) ProcessReceipt(ctx context.Context, receiptID, processedBy pgtype.UUID) (CreatedReceipt, error) {
	var result CreatedReceipt
	var processErr error

	err := s.store.ExecTx(ctx, func(q contributionsqlc.Querier) error {
		receipt, err := q.LockContributionReceiptByID(ctx, receiptID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				processErr = ErrReceiptNotFound
				return nil
			}
			return fmt.Errorf("lock receipt: %w", err)
		}
		result.Receipt = receipt

		allocations, err := q.LockContributionAllocationsByReceipt(ctx, receipt.ID)
		if err != nil {
			return fmt.Errorf("lock allocations: %w", err)
		}
		result.Allocations = allocations

		switch receipt.Status {
		case contributionsqlc.ContributionReceiptStatusCompleted:
			return nil
		case contributionsqlc.ContributionReceiptStatusPending:
			receipt, err = q.UpdateContributionReceiptStatus(ctx, contributionsqlc.UpdateContributionReceiptStatusParams{
				ID:            receipt.ID,
				Status:        contributionsqlc.ContributionReceiptStatusProcessing,
				FailureReason: pgtype.Text{},
			})
			if err != nil {
				return fmt.Errorf("mark receipt processing: %w", err)
			}
			result.Receipt = receipt
		case contributionsqlc.ContributionReceiptStatusProcessing:
		default:
			processErr = ErrReceiptNotProcessable
			return nil
		}

		if len(allocations) == 0 {
			processErr = ErrAllocationRequired
			return failReceipt(ctx, q, receipt.ID, processErr, &result)
		}

		result.Allocations = result.Allocations[:0]
		for _, allocation := range allocations {
			switch allocation.Status {
			case contributionsqlc.ContributionAllocationStatusCompleted:
				result.Allocations = append(result.Allocations, allocation)
				continue
			case contributionsqlc.ContributionAllocationStatusPending:
			default:
				processErr = ErrReceiptNotProcessable
				return failReceipt(ctx, q, receipt.ID, processErr, &result)
			}

			referenceID, externalReference, err := s.processAllocation(ctx, receipt, allocation, processedBy)
			if err != nil {
				processErr = fmt.Errorf("%w: %s", err, allocation.Type)
				if _, updateErr := q.UpdateContributionAllocationFailed(ctx, contributionsqlc.UpdateContributionAllocationFailedParams{
					ID:            allocation.ID,
					FailureReason: text(processErr.Error()),
				}); updateErr != nil {
					return fmt.Errorf("mark allocation failed: %w", updateErr)
				}
				return failReceipt(ctx, q, receipt.ID, processErr, &result)
			}

			allocation, err = q.UpdateContributionAllocationCompleted(ctx, contributionsqlc.UpdateContributionAllocationCompletedParams{
				ID:                       allocation.ID,
				AuthoritativeReferenceID: referenceID,
				ExternalReference:        externalReference,
			})
			if err != nil {
				return fmt.Errorf("mark allocation completed: %w", err)
			}
			result.Allocations = append(result.Allocations, allocation)
		}

		receipt, err = q.UpdateContributionReceiptStatus(ctx, contributionsqlc.UpdateContributionReceiptStatusParams{
			ID:            receipt.ID,
			Status:        contributionsqlc.ContributionReceiptStatusCompleted,
			FailureReason: pgtype.Text{},
		})
		if err != nil {
			return fmt.Errorf("mark receipt completed: %w", err)
		}
		result.Receipt = receipt
		return nil
	})
	if err != nil {
		return CreatedReceipt{}, err
	}
	if processErr != nil {
		return result, processErr
	}
	return result, nil
}

// processAllocation keeps the cross-domain dispatch in one switch so adding a
// new allocation type cannot accidentally bypass the owning service rule.
// Removing it would push this switch into ProcessReceipt and make the receipt
// state transitions harder to audit.
func (s *Service) processAllocation(
	ctx context.Context,
	receipt contributionsqlc.ContributionReceipt,
	allocation contributionsqlc.ContributionAllocation,
	processedBy pgtype.UUID,
) (pgtype.UUID, pgtype.Text, error) {
	externalReference := receiptExternalReference(receipt)
	switch allocation.Type {
	case contributionsqlc.ContributionAllocationTypeCom,
		contributionsqlc.ContributionAllocationTypeLgom,
		contributionsqlc.ContributionAllocationTypeOtherCharge:
		return allocation.ID, externalReference, nil
	case contributionsqlc.ContributionAllocationTypeSharePurchase:
		if s.shareService == nil {
			return pgtype.UUID{}, pgtype.Text{}, ErrOwningServiceMissing
		}
		tx, err := s.shareService.PurchaseShares(
			ctx,
			allocation.TargetID,
			allocation.Amount,
			allocation.ID,
			processedBy,
			"contribution receipt "+receipt.ID.String(),
		)
		if err != nil {
			return pgtype.UUID{}, pgtype.Text{}, err
		}
		return tx.ID, externalReference, nil
	case contributionsqlc.ContributionAllocationTypeLoanPrincipal:
		if s.loanService == nil {
			return pgtype.UUID{}, pgtype.Text{}, ErrOwningServiceMissing
		}
		tx, err := s.loanService.RecordRepayment(ctx, allocation.TargetID, allocation.Amount, allocation.ID.String(), processedBy)
		if err != nil {
			return pgtype.UUID{}, pgtype.Text{}, err
		}
		return tx.ID, externalReference, nil
	default:
		return pgtype.UUID{}, pgtype.Text{}, ErrAllocationNotSupported
	}
}

// failReceipt commits a terminal failed status before ProcessReceipt returns
// the domain error. Returning that error directly from inside ExecTx would roll
// back the failure marker and leave reconciliation blind to the partial run.
func failReceipt(
	ctx context.Context,
	q contributionsqlc.Querier,
	receiptID pgtype.UUID,
	cause error,
	result *CreatedReceipt,
) error {
	receipt, err := q.UpdateContributionReceiptStatus(ctx, contributionsqlc.UpdateContributionReceiptStatusParams{
		ID:            receiptID,
		Status:        contributionsqlc.ContributionReceiptStatusFailed,
		FailureReason: text(cause.Error()),
	})
	if err != nil {
		return fmt.Errorf("mark receipt failed: %w", err)
	}
	result.Receipt = receipt
	return nil
}

// receiptExternalReference preserves the payment gateway reference on every
// completed allocation. Removing it would force reconciliation to recover the
// external ID by joining back to the receipt for each allocation row.
func receiptExternalReference(receipt contributionsqlc.ContributionReceipt) pgtype.Text {
	if receipt.ExternalTransactionID.Valid {
		return receipt.ExternalTransactionID
	}
	return receipt.CheckoutRequestID
}

// TODO-verify working of this code
func verifyAllocationsMatch(
	expected []AllocationInput,
	actual []contributionsqlc.ContributionAllocation,
) error {
	if len(expected) != len(actual) {
		return fmt.Errorf(
			"%w: expected %d allocations, found %d",
			ErrInconsistentReceipt,
			len(expected),
			len(actual),
		)
	}

	actualAmounts := make(map[string]*big.Int, len(actual))

	for _, allocation := range actual {
		key := allocationKey(
			allocation.Type,
			allocation.TargetID,
		)

		if _, exists := actualAmounts[key]; exists {
			return fmt.Errorf(
				"%w: duplicate stored allocation %q",
				ErrInconsistentReceipt,
				key,
			)
		}

		actualAmounts[key] = numericToScale(
			allocation.Amount,
			-4,
		)
	}

	seenExpected := make(map[string]struct{}, len(expected))

	for _, allocation := range expected {
		key := allocationKey(
			allocation.Type,
			allocation.TargetID,
		)

		if _, exists := seenExpected[key]; exists {
			return fmt.Errorf(
				"%w: duplicate expected allocation %q",
				ErrInconsistentReceipt,
				key,
			)
		}

		seenExpected[key] = struct{}{}

		actualAmount, exists := actualAmounts[key]
		if !exists {
			return fmt.Errorf(
				"%w: expected allocation %q is missing",
				ErrInconsistentReceipt,
				key,
			)
		}

		expectedAmount := numericToScale(
			allocation.Amount,
			-4,
		)

		if expectedAmount.Cmp(actualAmount) != 0 {
			return fmt.Errorf(
				"%w: amount mismatch for allocation %q",
				ErrInconsistentReceipt,
				key,
			)
		}
	}

	return nil
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
	switch params.SourceChannel {
	case contributionsqlc.ContributionSourceChannelDarajaStk,
		contributionsqlc.ContributionSourceChannelCash,
		contributionsqlc.ContributionSourceChannelManual:
	default:
		return ErrInvalidPayment
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
func allocationKey(
	allocationType contributionsqlc.ContributionAllocationType,
	targetID pgtype.UUID,
) string {
	key := string(allocationType)

	if targetID.Valid {
		return key + ":" + targetID.String()
	}

	return key + ":<none>"
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

// text is the smallest local adapter from Go strings to sqlc's pgtype.Text.
// Removing it would repeat the Valid flag construction at each status update.
func text(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}
