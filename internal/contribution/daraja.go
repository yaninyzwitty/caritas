package contribution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	contributionsqlc "github.com/yaninyzwitty/caritas-backend/internal/contribution/repository/sqlc"
)

// DarajaSTKPayment is the normalized successful STK callback payload. Without
// this boundary, HTTP parsing details would leak into receipt creation and make
// retries harder to reason about.
type DarajaSTKPayment struct {
	CheckoutRequestID string
	MpesaReceipt      string
	Amount            pgtype.Numeric
	ReceivedAt        time.Time
}

// ProcessDarajaSTKPayment turns one successful Daraja STK callback into one
// contribution receipt, then processes that receipt. Removing it would push
// idempotency and request matching into the HTTP handler.
func (s *Service) ProcessDarajaSTKPayment(ctx context.Context, payment DarajaSTKPayment) (CreatedReceipt, error) {
	checkoutID := strings.TrimSpace(payment.CheckoutRequestID)
	mpesaReceipt := strings.TrimSpace(payment.MpesaReceipt)
	if checkoutID == "" || mpesaReceipt == "" || !positive(payment.Amount) {
		return CreatedReceipt{}, ErrInvalidPayment
	}

	var receiptID pgtype.UUID
	var result CreatedReceipt
	var processErr error
	err := s.store.ExecTx(ctx, func(q contributionsqlc.Querier) error {
		request, err := q.LockContributionPaymentRequestByCheckoutID(ctx, checkoutID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrPaymentRequestNotFound
			}
			return fmt.Errorf("lock payment request: %w", err)
		}
		if request.Status == contributionsqlc.ContributionPaymentRequestStatusFailed {
			return ErrReceiptNotProcessable
		}

		if request.Status == contributionsqlc.ContributionPaymentRequestStatusCompleted {
			if !request.ReceiptID.Valid {
				return fmt.Errorf("%w: completed payment request %s has no receipt ID", ErrReceiptNotProcessable, request.ID)
			}

			receiptID = request.ReceiptID
			return nil
		}

		// compare amounts from m-pesa receipt and the expected amount in the payment request.
		// The amounts are scaled to 4 decimal places for comparison.
		if numericToScale(request.ExpectedAmount, -4).Cmp(numericToScale(payment.Amount, -4)) != 0 {
			processErr = ErrPaymentAmountMismatch
			if _, err := q.UpdateContributionPaymentRequestFailed(ctx, contributionsqlc.UpdateContributionPaymentRequestFailedParams{
				ID:            request.ID,
				FailureReason: text(processErr.Error()),
			}); err != nil {
				return fmt.Errorf("mark payment request failed: %w", err)
			}
			return nil
		}

		allocations, err := parseAllocationPlan(request.AllocationPlan)
		if err != nil {
			processErr = err
			if _, updateErr := q.UpdateContributionPaymentRequestFailed(ctx, contributionsqlc.UpdateContributionPaymentRequestFailedParams{
				ID:            request.ID,
				FailureReason: text(err.Error()),
			}); updateErr != nil {
				return fmt.Errorf("mark payment request failed: %w", updateErr)
			}
			return nil
		}

		receivedAt := payment.ReceivedAt
		if receivedAt.IsZero() {
			receivedAt = time.Now().UTC()
		}
		params := contributionsqlc.InsertContributionReceiptParams{
			SourceChannel:         contributionsqlc.ContributionSourceChannelDarajaStk,
			ExternalTransactionID: pgtype.Text{String: mpesaReceipt, Valid: true},
			CheckoutRequestID:     pgtype.Text{String: checkoutID, Valid: true},
			MemberID:              request.MemberID,
			BranchID:              request.BranchID,
			ContributionPeriod:    request.ContributionPeriod,
			ReceivedAmount:        payment.Amount,
			AllocationPlan:        request.AllocationPlan,
			ReceivedBy:            request.RequestedBy,
			ReceivedAt:            pgtype.Timestamptz{Time: receivedAt, Valid: true},
		}
		if err := validateReceipt(params, allocations); err != nil {
			processErr = err
			if _, updateErr := q.UpdateContributionPaymentRequestFailed(ctx, contributionsqlc.UpdateContributionPaymentRequestFailedParams{
				ID:            request.ID,
				FailureReason: text(err.Error()),
			}); updateErr != nil {
				return fmt.Errorf("mark payment request failed: %w", updateErr)
			}
			return nil
		}
		result, err = s.createReceipt(ctx, q, params, allocations)
		if err != nil {
			return err
		}
		if result.Receipt.CheckoutRequestID.Valid && result.Receipt.CheckoutRequestID.String != checkoutID {
			return ErrReceiptNotProcessable
		}
		if _, err := q.UpdateContributionPaymentRequestCompleted(ctx, contributionsqlc.UpdateContributionPaymentRequestCompletedParams{
			ID:        request.ID,
			ReceiptID: result.Receipt.ID,
		}); err != nil {
			return fmt.Errorf("mark payment request completed: %w", err)
		}
		receiptID = result.Receipt.ID
		return nil
	})
	if err != nil {
		return CreatedReceipt{}, err
	}
	if processErr != nil {
		return result, processErr
	}
	return s.ProcessReceipt(ctx, receiptID, pgtype.UUID{})
}

// MarkDarajaSTKPaymentFailed records a terminal Daraja failure against the
// stored checkout request. Without it, failed STK callbacks would be
// acknowledged but invisible to office reconciliation.
func (s *Service) MarkDarajaSTKPaymentFailed(ctx context.Context, checkoutID, reason string) error {
	checkoutID = strings.TrimSpace(checkoutID)
	if checkoutID == "" {
		return ErrPaymentRequestNotFound
	}
	return s.store.ExecTx(ctx, func(q contributionsqlc.Querier) error {
		request, err := q.LockContributionPaymentRequestByCheckoutID(ctx, checkoutID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrPaymentRequestNotFound
			}
			return fmt.Errorf("lock payment request: %w", err)
		}
		if request.Status == contributionsqlc.ContributionPaymentRequestStatusCompleted {
			return nil
		}
		_, err = q.UpdateContributionPaymentRequestFailed(ctx, contributionsqlc.UpdateContributionPaymentRequestFailedParams{
			ID:            request.ID,
			FailureReason: text(reason),
		})
		if err != nil {
			return fmt.Errorf("mark payment request failed: %w", err)
		}
		return nil
	})
}

// allocationPlan stores the immutable request plan in the same compact JSON
// shape the receipt keeps for audit. Removing it would force the webhook to
// derive allocations after money arrives, which can change under retries.
type allocationPlan struct {
	Items []allocationPlanItem `json:"items"`
}

// allocationPlanItem is the JSON form of one allocation row. Without this
// explicit shape, bad target IDs or amount strings would reach sqlc as vague
// database errors.
type allocationPlanItem struct {
	Type     contributionsqlc.ContributionAllocationType `json:"type"`
	TargetID string                                      `json:"target_id"`
	Amount   string                                      `json:"amount"`
}

// parseAllocationPlan converts the stored immutable JSON plan into the rows
// CreateReceipt already validates. Removing it would duplicate JSON parsing in
// every callback source.
func parseAllocationPlan(body []byte) ([]AllocationInput, error) {
	var plan allocationPlan
	if err := json.Unmarshal(body, &plan); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidAllocationPlan, err)
	}
	if len(plan.Items) == 0 {
		return nil, ErrAllocationRequired
	}
	allocations := make([]AllocationInput, 0, len(plan.Items))
	for _, item := range plan.Items {
		var amount pgtype.Numeric
		if err := amount.Scan(strings.TrimSpace(item.Amount)); err != nil {
			return nil, ErrInvalidAllocation
		}
		allocation := AllocationInput{Type: item.Type, Amount: amount}
		if strings.TrimSpace(item.TargetID) != "" {
			if err := allocation.TargetID.Scan(strings.TrimSpace(item.TargetID)); err != nil {
				return nil, ErrInvalidAllocation
			}
		}
		allocations = append(allocations, allocation)
	}
	return allocations, nil
}
