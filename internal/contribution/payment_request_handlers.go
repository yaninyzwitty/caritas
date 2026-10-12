package contribution

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
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
