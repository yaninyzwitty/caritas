package contribution

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	contributionv1 "github.com/yaninyzwitty/caritas-backend/gen/contribution/v1"
	"github.com/yaninyzwitty/caritas-backend/internal/auth"
	contributionsqlc "github.com/yaninyzwitty/caritas-backend/internal/contribution/repository/sqlc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// GetContributionPayment reads the request and receipt together so clients
// can recover an uncertain initiation without sending another M-Pesa prompt.
func (h *Handlers) GetContributionPayment(ctx context.Context, req *contributionv1.GetContributionPaymentRequest) (*contributionv1.GetContributionPaymentResponse, error) {
	actor, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "authenticated staff is required")
	}
	params := contributionsqlc.LookupContributionPaymentRequestParams{BranchID: actor.BranchID}
	switch lookup := req.GetLookup().(type) {
	case *contributionv1.GetContributionPaymentRequest_PaymentRequestId:
		id, err := stringToUUID(lookup.PaymentRequestId)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid payment_request_id")
		}
		params.PaymentRequestID = id
	case *contributionv1.GetContributionPaymentRequest_IdempotencyKey:
		key := strings.TrimSpace(lookup.IdempotencyKey)
		if key == "" {
			return nil, status.Error(codes.InvalidArgument, "idempotency_key is required")
		}
		params.IdempotencyKey = pgtype.Text{String: key, Valid: true}
	default:
		return nil, status.Error(codes.InvalidArgument, "payment_request_id or idempotency_key is required")
	}
	row, err := h.service.store.LookupContributionPaymentRequest(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "payment request not found")
	}
	if err != nil {
		return nil, mapContributionError(err)
	}
	response := &contributionv1.GetContributionPaymentResponse{
		PaymentRequestId:   row.ID.String(),
		Status:             paymentRequestStatusToProto(row.Status),
		Amount:             numericToMoney(row.ExpectedAmount),
		ContributionPeriod: row.ContributionPeriod.Time.Format("2006-01-02"),
		CreatedAt:          timestamppb.New(row.CreatedAt.Time),
		CheckoutRequestId:  row.CheckoutRequestID.String,
		RecoveryStatus:     row.RecoveryStatus,
	}
	if row.Status == contributionsqlc.ContributionPaymentRequestStatusFailed {
		// Stored provider/internal errors are not safe user-facing messages.
		response.FailureReason = "The payment request could not be completed. Contact staff before starting another payment if money was deducted."
	}
	if row.ReceiptID.Valid {
		response.Receipt = &contributionv1.ContributionPaymentReceipt{
			Id:             row.ReceiptID.String(),
			Status:         row.ReceiptStatus,
			Amount:         numericToMoney(row.ReceivedAmount),
			MpesaReference: row.ExternalTransactionID.String,
			ReceivedAt:     timestamppb.New(row.ReceivedAt.Time),
		}
	}
	return response, nil
}

// ListDarajaPayments makes unresolved collections recoverable across browser sessions.
// Without it, staff can only find requests retained in one browser's storage.
func (h *Handlers) ListDarajaPayments(ctx context.Context, req *contributionv1.ListDarajaPaymentsRequest) (*contributionv1.ListDarajaPaymentsResponse, error) {
	actor, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "authenticated staff required")
	}
	params := contributionsqlc.ListUnresolvedDarajaPaymentsParams{BranchID: actor.BranchID}
	if req.GetPageToken() != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(req.GetPageToken())
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid cursor")
		}
		stamp, id, found := strings.Cut(string(decoded), "|")
		parsed, err := time.Parse(time.RFC3339Nano, stamp)
		if !found || err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid cursor")
		}
		params.CursorID, err = stringToUUID(id)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid cursor")
		}
		params.CursorCreatedAt = pgtype.Timestamptz{Time: parsed, Valid: true}
	}
	rows, err := h.service.store.ListUnresolvedDarajaPayments(ctx, params)
	if err != nil {
		return nil, mapContributionError(err)
	}
	response := &contributionv1.ListDarajaPaymentsResponse{Payments: make([]*contributionv1.DarajaPayment, 0, len(rows))}
	if len(rows) > 50 {
		rows = rows[:50]
		last := rows[49]
		response.NextPageToken = base64.RawURLEncoding.EncodeToString([]byte(last.CreatedAt.Time.Format(time.RFC3339Nano) + "|" + last.ID.String()))
	}
	for _, row := range rows {
		recovery := row.RecoveryStatus
		if row.Status == contributionsqlc.ContributionPaymentRequestStatusFailed && !strings.HasPrefix(recovery, "received") {
			recovery = "provider_failed"
		}
		if !row.CheckoutRequestID.Valid || row.QueryAttempts >= 8 {
			recovery = "needs_review"
		}
		response.Payments = append(response.Payments, &contributionv1.DarajaPayment{
			PaymentRequestId: row.ID.String(), MemberId: row.MemberID.String(), CheckoutRequestId: row.CheckoutRequestID.String,
			Amount: numericToMoney(row.ExpectedAmount), Status: string(row.Status), RecoveryStatus: recovery,
			ReceiptStatus: row.ReceiptStatus, MpesaReference: row.MpesaReference, LastError: row.PostingError,
			CreatedAt: timestamppb.New(row.CreatedAt.Time), ProviderResult: row.ProviderResult.String,
		})
	}
	return response, nil
}

// RecoverDarajaPayment keeps verification, audit and retries on the original request.
// Removing it forces staff to create unrelated receipts that can double-post money.
func (h *Handlers) RecoverDarajaPayment(ctx context.Context, req *contributionv1.RecoverDarajaPaymentRequest) (*contributionv1.RecoverDarajaPaymentResponse, error) {
	actor, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "authenticated staff required")
	}
	id, err := stringToUUID(req.GetPaymentRequestId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid payment request")
	}
	_, approve := auth.CashPermissions(actor.Role)
	if req.GetAction() != "query" && !approve {
		return nil, status.Error(codes.PermissionDenied, "reconciliation approval required")
	}
	err = h.service.store.ExecTx(ctx, func(q contributionsqlc.Querier) error {
		request, err := q.LockDarajaRequest(ctx, contributionsqlc.LockDarajaRequestParams{ID: id, BranchID: actor.BranchID})
		if err != nil {
			return err
		}
		if req.GetAction() != "query" {
			reason := strings.TrimSpace(req.GetReason())
			if reason == "" {
				return ErrInvalidPayment
			}
			if req.GetAction() == "restore_checkout" {
				reason += " (checkout: " + strings.TrimSpace(req.GetCheckoutRequestId()) + ")"
			}
			if err := q.InsertPaymentRecoveryAction(ctx, contributionsqlc.InsertPaymentRecoveryActionParams{PaymentRequestID: id, Action: req.GetAction(), Reason: reason, PerformedBy: actor.ID}); err != nil {
				return err
			}
		}
		switch req.GetAction() {
		case "restore_checkout":
			checkout := strings.TrimSpace(req.GetCheckoutRequestId())
			if checkout == "" {
				return ErrInvalidPayment
			}
			if request.CheckoutRequestID.Valid && request.CheckoutRequestID.String != checkout {
				return ErrInconsistentReceipt
			}
			if request.Status != contributionsqlc.ContributionPaymentRequestStatusPending || request.ReceiptID.Valid {
				return status.Error(codes.FailedPrecondition, "only pending requests without a receipt can have their checkout restored")
			}
			// Repeating a successful restoration must not reset the provider retry budget.
			if request.CheckoutRequestID.Valid {
				return nil
			}
			if err := q.RestoreDarajaCheckout(ctx, contributionsqlc.RestoreDarajaCheckoutParams{ID: id, CheckoutRequestID: text(checkout)}); err != nil {
				return err
			}
			return q.RequeueDarajaCallbacks(ctx, checkout)
		case "query":
			if err := q.RequeueDarajaCallbacks(ctx, request.CheckoutRequestID.String); err != nil {
				return err
			}
			return q.ScheduleDarajaQuery(ctx, contributionsqlc.ScheduleDarajaQueryParams{ID: id, BranchID: actor.BranchID})
		case "retry_posting":
			if !request.ReceiptID.Valid {
				return ErrReceiptNotFound
			}
			if strings.TrimSpace(req.GetReason()) == "" {
				return ErrInvalidPayment
			}
			return q.RetryDarajaReceipt(ctx, contributionsqlc.RetryDarajaReceiptParams{ID: request.ReceiptID, BranchID: actor.BranchID})
		case "reconcile":
			amount := moneyToNumeric(req.GetAmount())
			reference := strings.ToUpper(strings.TrimSpace(req.GetMpesaReference()))
			if reference == "" || strings.TrimSpace(req.GetEvidence()) == "" || strings.TrimSpace(req.GetReason()) == "" ||
				req.GetReceivedAt() == nil || req.GetReceivedAt().CheckValid() != nil || req.GetReceivedAt().AsTime().After(time.Now()) ||
				numericToScale(request.ExpectedAmount, -4).Cmp(numericToScale(amount, -4)) != 0 {
				return ErrInvalidPayment
			}
			checkout := request.CheckoutRequestID.String
			if checkout == "" {
				checkout = strings.TrimSpace(req.GetCheckoutRequestId())
				if checkout == "" {
					return ErrPaymentRequestNotFound
				}
				if _, err = q.UpdateContributionPaymentRequestCheckoutID(ctx, contributionsqlc.UpdateContributionPaymentRequestCheckoutIDParams{ID: id, CheckoutRequestID: text(checkout)}); err != nil {
					return err
				}
			}
			if request.ReceiptID.Valid {
				receipt, err := q.GetContributionReceiptByID(ctx, request.ReceiptID)
				if err != nil {
					return err
				}
				if receipt.ExternalTransactionID.String != reference {
					return ErrInconsistentReceipt
				}
			}
			_, err = q.InsertPaymentReconciliation(ctx, contributionsqlc.InsertPaymentReconciliationParams{
				PaymentRequestID: id, ExternalTransactionID: reference, Amount: amount,
				ReceivedAt: pgtype.Timestamptz{Time: req.GetReceivedAt().AsTime(), Valid: true},
				Evidence:   strings.TrimSpace(req.GetEvidence()), Reason: strings.TrimSpace(req.GetReason()), ReconciledBy: actor.ID,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				existing, lookupErr := q.GetPaymentReconciliation(ctx, reference)
				if errors.Is(lookupErr, pgx.ErrNoRows) {
					return ErrInconsistentReceipt
				}
				if lookupErr != nil {
					return lookupErr
				}
				if existing.PaymentRequestID != id || numericToScale(existing.Amount, -4).Cmp(numericToScale(amount, -4)) != 0 {
					return ErrInconsistentReceipt
				}
			} else if err != nil {
				return err
			}
			// Verified records enter the same durable callback path, preserving receipt idempotency.
			metadata, _ := json.Marshal(map[string]any{"Body": map[string]any{"stkCallback": map[string]any{
				"CheckoutRequestID": checkout, "ResultCode": 0, "ResultDesc": "Staff verified against business records",
				"CallbackMetadata": map[string]any{"Item": []map[string]any{
					{"Name": "Amount", "Value": numericToScale(amount, 0).String()},
					{"Name": "MpesaReceiptNumber", "Value": reference},
					{"Name": "TransactionDate", "Value": req.GetReceivedAt().AsTime().In(time.FixedZone("Africa/Nairobi", 10800)).Format("20060102150405")},
				}},
			}}})
			if err := q.InsertDarajaCallback(ctx, contributionsqlc.InsertDarajaCallbackParams{CheckoutRequestID: checkout, Payload: metadata, PayloadHash: fmt.Sprintf("%x", sha256.Sum256(metadata))}); err != nil {
				return err
			}
			return q.RequeueDarajaCallbacks(ctx, checkout)
		default:
			return ErrInvalidPayment
		}
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "payment request not found")
	}
	if err != nil {
		if _, ok := status.FromError(err); ok {
			return nil, err
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "contribution_payment_requests_checkout_request_id_key" {
			return nil, status.Error(codes.AlreadyExists, "checkout ID belongs to another payment request")
		}
		return nil, mapContributionError(err)
	}
	payment, err := h.GetContributionPayment(ctx, &contributionv1.GetContributionPaymentRequest{Lookup: &contributionv1.GetContributionPaymentRequest_PaymentRequestId{PaymentRequestId: id.String()}})
	return &contributionv1.RecoverDarajaPaymentResponse{Payment: payment}, err
}
