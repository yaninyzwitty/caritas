package contribution

import (
	"context"
	"encoding/base64"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	contributionv1 "github.com/yaninyzwitty/caritas-backend/gen/contribution/v1"
	"github.com/yaninyzwitty/caritas-backend/internal/auth"
	contributionsqlc "github.com/yaninyzwitty/caritas-backend/internal/contribution/repository/sqlc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// CreateContributionCharge exposes audited obligations to authenticated staff.
// Without it, charge targets could only be created by directly editing the DB.
func (h *Handlers) CreateContributionCharge(ctx context.Context, req *contributionv1.CreateContributionChargeRequest) (*contributionv1.CreateContributionChargeResponse, error) {
	actor, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "authenticated staff is required")
	}
	memberID, err := stringToUUID(req.GetMemberId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid member_id")
	}
	money := req.GetAmount()
	if money == nil || money.GetCurrencyCode() != "KES" || money.GetUnits() < 0 || money.GetNanos() < 0 || money.GetNanos() >= 1_000_000_000 {
		return nil, status.Error(codes.InvalidArgument, "amount must be positive KES money")
	}
	charge, err := h.service.CreateCharge(ctx, contributionsqlc.CreateContributionChargeParams{
		IdempotencyKey: req.GetIdempotencyKey(),
		MemberID:       memberID,
		BranchID:       actor.BranchID,
		Category:       req.GetCategory(),
		Amount:         moneyToNumeric(money),
		Reason:         req.GetReason(),
		CreatedBy:      actor.ID,
	})
	if err != nil {
		return nil, mapContributionError(err)
	}
	paid, err := h.service.store.SumContributionChargePayments(ctx, contributionsqlc.SumContributionChargePaymentsParams{ChargeID: charge.ID})
	if err != nil {
		return nil, mapContributionError(err)
	}
	return &contributionv1.CreateContributionChargeResponse{Charge: chargeToProto(charge, paid)}, nil
}

// ListContributionCharges exposes paid and outstanding amounts with a stable
// cursor. Without it, clients cannot select a charge to pay or discover arrears.
func (h *Handlers) ListContributionCharges(ctx context.Context, req *contributionv1.ListContributionChargesRequest) (*contributionv1.ListContributionChargesResponse, error) {
	actor, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "authenticated staff is required")
	}
	memberID, err := stringToUUID(req.GetMemberId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid member_id")
	}
	limit := req.GetPageSize()
	if limit <= 0 {
		limit = 50
	} else if limit > 100 {
		limit = 100
	}
	var cursorTime pgtype.Timestamptz
	var cursorID pgtype.UUID
	if token := req.GetPageToken(); token != "" {
		raw, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid page_token")
		}
		timestamp, id, ok := strings.Cut(string(raw), "|")
		parsed, err := time.Parse(time.RFC3339Nano, timestamp)
		if !ok || err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid page_token")
		}
		cursorID, err = stringToUUID(id)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid page_token")
		}
		cursorTime = pgtype.Timestamptz{Time: parsed, Valid: true}
	}
	rows, err := h.service.store.ListContributionCharges(ctx, contributionsqlc.ListContributionChargesParams{
		MemberID:        memberID,
		BranchID:        actor.BranchID,
		CursorCreatedAt: cursorTime,
		CursorID:        cursorID, FetchLimit: limit + 1,
	})
	if err != nil {
		return nil, mapContributionError(err)
	}
	response := &contributionv1.ListContributionChargesResponse{Charges: make([]*contributionv1.ContributionCharge, 0, len(rows))}
	if len(rows) > int(limit) {
		last := rows[limit-1]
		response.NextPageToken = base64.RawURLEncoding.EncodeToString([]byte(last.CreatedAt.Time.Format(time.RFC3339Nano) + "|" + last.ID.String()))
		rows = rows[:limit]
	}
	for _, row := range rows {
		response.Charges = append(response.Charges, chargeToProto(contributionsqlc.ContributionCharge{
			ID:        row.ID,
			MemberID:  row.MemberID,
			Category:  row.Category,
			Amount:    row.Amount,
			Reason:    row.Reason,
			CreatedBy: row.CreatedBy,
			CreatedAt: row.CreatedAt,
		}, row.Paid))
	}
	return response, nil
}

// chargeToProto keeps creation retries and balance listings consistent. Without
// this shared mapping, one endpoint could return the assessment as money paid.
func chargeToProto(row contributionsqlc.ContributionCharge, paid pgtype.Numeric) *contributionv1.ContributionCharge {
	outstanding := numericToScale(row.Amount, -4)
	outstanding.Sub(outstanding, numericToScale(paid, -4))
	return &contributionv1.ContributionCharge{
		Id:          row.ID.String(),
		MemberId:    row.MemberID.String(),
		Category:    row.Category,
		Amount:      numericToMoney(row.Amount),
		Paid:        numericToMoney(paid),
		Outstanding: numericToMoney(pgtype.Numeric{Int: outstanding, Exp: -4, Valid: true}),
		Reason:      row.Reason,
		CreatedBy:   row.CreatedBy.String(),
		CreatedAt:   timestamppb.New(row.CreatedAt.Time),
	}
}
