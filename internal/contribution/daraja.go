package contribution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
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

type InitiateDarajaSTKPaymentParams struct {
	IdempotencyKey     string
	PhoneNumber        string
	MemberID           pgtype.UUID
	BranchID           int64
	ContributionPeriod pgtype.Date
	Amount             pgtype.Numeric
	Allocations        []AllocationInput
	RequestedBy        pgtype.UUID
}

// InitiateDarajaSTKPayment stores the immutable request before asking M-Pesa
// for an STK prompt. Without this ordering, a retry after a client timeout could
// create a second prompt with no durable idempotency record to stop it.
func (s *Service) InitiateDarajaSTKPayment(ctx context.Context, params InitiateDarajaSTKPaymentParams, initiator DarajaSTKInitiator) (contributionsqlc.ContributionPaymentRequest, error) {
	if initiator == nil {
		return contributionsqlc.ContributionPaymentRequest{}, ErrDarajaClientMissing
	}
	params.IdempotencyKey = strings.TrimSpace(params.IdempotencyKey)
	params.PhoneNumber = strings.TrimSpace(params.PhoneNumber)

	if params.IdempotencyKey == "" || params.PhoneNumber == "" || !params.MemberID.Valid || params.BranchID == 0 || !params.ContributionPeriod.Valid {
		return contributionsqlc.ContributionPaymentRequest{}, ErrInvalidPayment
	}

	if err := validatePaymentRequestAmount(params.Amount, params.Allocations); err != nil {
		return contributionsqlc.ContributionPaymentRequest{}, err
	}
	plan, err := buildAllocationPlan(params.Allocations)
	if err != nil {
		return contributionsqlc.ContributionPaymentRequest{}, err
	}

	request, created, err := s.createContributionPaymentRequest(ctx, params, plan)
	if err != nil || !created {
		return request, err
	}

	amount, err := darajaWholeAmount(params.Amount)
	if err != nil {
		_, _ = s.store.UpdateContributionPaymentRequestFailed(ctx, contributionsqlc.UpdateContributionPaymentRequestFailedParams{
			ID:            request.ID,
			FailureReason: text(err.Error()),
		})
		return contributionsqlc.ContributionPaymentRequest{}, err
	}
	checkoutID, err := initiator.InitiateSTK(ctx, DarajaSTKInitiationRequest{
		PhoneNumber: params.PhoneNumber,
		Amount:      amount,
	})
	if err != nil {
		_, _ = s.store.UpdateContributionPaymentRequestFailed(ctx, contributionsqlc.UpdateContributionPaymentRequestFailedParams{
			ID:            request.ID,
			FailureReason: text(err.Error()),
		})
		return contributionsqlc.ContributionPaymentRequest{}, err
	}
	return s.store.UpdateContributionPaymentRequestCheckoutID(ctx, contributionsqlc.UpdateContributionPaymentRequestCheckoutIDParams{
		ID:                request.ID,
		CheckoutRequestID: pgtype.Text{String: checkoutID, Valid: true},
	})
}

// createContributionPaymentRequest gives STK initiation one sqlc-only
// idempotent insert path. Without it, InitiateDarajaSTKPayment would duplicate
// the pgx.ErrNoRows conflict handling needed to return an existing request.
func (s *Service) createContributionPaymentRequest(ctx context.Context, params InitiateDarajaSTKPaymentParams, plan []byte) (contributionsqlc.ContributionPaymentRequest, bool, error) {
	request, err := s.store.InsertContributionPaymentRequest(ctx, contributionsqlc.InsertContributionPaymentRequestParams{
		IdempotencyKey:     params.IdempotencyKey,
		CheckoutRequestID:  pgtype.Text{},
		MemberID:           params.MemberID,
		BranchID:           params.BranchID,
		ContributionPeriod: params.ContributionPeriod,
		ExpectedAmount:     params.Amount,
		AllocationPlan:     plan,
		RequestedBy:        params.RequestedBy,
	})
	if err == nil {
		return request, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return contributionsqlc.ContributionPaymentRequest{}, false, fmt.Errorf("insert payment request: %w", err)
	}
	request, err = s.store.GetContributionPaymentRequestByIdempotencyKey(ctx, params.IdempotencyKey)
	if err != nil {
		return contributionsqlc.ContributionPaymentRequest{}, false, fmt.Errorf("get payment request by idempotency key: %w", err)
	}
	if !request.CheckoutRequestID.Valid || strings.TrimSpace(request.CheckoutRequestID.String) == "" {
		return request, false, ErrPaymentRequestInProgress
	}
	return request, false, nil
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
		request, err := q.LockContributionPaymentRequestByCheckoutID(ctx, pgtype.Text{String: checkoutID, Valid: true})
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

		for _, allocation := range allocations {
			slog.Info("allocation params", "Value", allocation.Amount, "allocationType", allocation.Type, "targetID", allocation.TargetID)
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
	return s.ProcessReceipt(ctx, receiptID, result.Receipt.ReceivedBy)
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
		request, err := q.LockContributionPaymentRequestByCheckoutID(ctx, pgtype.Text{String: checkoutID, Valid: true})
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

// buildAllocationPlan serializes the exact allocation plan persisted on the
// pending payment request. Without it, the callback would have to reconstruct
// allocations after money arrives, which can post funds using changed inputs.
func buildAllocationPlan(allocations []AllocationInput) ([]byte, error) {
	plan := allocationPlan{Items: make([]allocationPlanItem, 0, len(allocations))}
	for _, allocation := range allocations {
		amount := numericToScale(allocation.Amount, -4)
		targetID := ""
		if allocation.TargetID.Valid {
			targetID = allocation.TargetID.String()
		}
		plan.Items = append(plan.Items, allocationPlanItem{
			Type:     allocation.Type,
			TargetID: targetID,
			Amount:   new(big.Rat).SetFrac(amount, big.NewInt(10000)).FloatString(4),
		})
	}
	return json.Marshal(plan)
}

// validatePaymentRequestAmount reuses receipt validation before money is
// requested. Without this preflight check, the service could prompt a member
// for an amount that the later callback is guaranteed to reject.
func validatePaymentRequestAmount(amount pgtype.Numeric, allocations []AllocationInput) error {
	return validateReceipt(contributionsqlc.InsertContributionReceiptParams{
		SourceChannel:     contributionsqlc.ContributionSourceChannelDarajaStk,
		CheckoutRequestID: pgtype.Text{String: "pending", Valid: true},
		ReceivedAmount:    amount,
		AllocationPlan:    []byte(`{"items":[]}`),
	}, allocations)
}

// darajaWholeAmount converts NUMERIC money into the whole-shilling amount STK
// accepts. Without this guard, fractional cents could be silently truncated in
// the external payment prompt.
func darajaWholeAmount(amount pgtype.Numeric) (int64, error) {
	value := numericToScale(amount, 0)
	if new(big.Int).Mul(value, big.NewInt(10000)).Cmp(numericToScale(amount, -4)) != 0 {
		return 0, ErrInvalidReceiptAmount
	}
	if !value.IsInt64() || value.Sign() <= 0 {
		return 0, ErrInvalidReceiptAmount
	}
	return value.Int64(), nil
}
