package contribution

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	contributionv1 "github.com/yaninyzwitty/caritas-backend/gen/contribution/v1"
	"github.com/yaninyzwitty/caritas-backend/internal/auth"
	contributionsqlc "github.com/yaninyzwitty/caritas-backend/internal/contribution/repository/sqlc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetCashContext supplies the existing workspace with the authenticated branch
// and role capabilities; browser-provided branch IDs cannot replace this context.
func (h *Handlers) GetCashContext(ctx context.Context, _ *contributionv1.GetCashContextRequest) (*contributionv1.GetCashContextResponse, error) {
	actor, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "authenticated staff is required")
	}
	return &contributionv1.GetCashContextResponse{
		StaffId: actor.ID.String(), BranchId: actor.BranchID,
		CanRecord:  actor.Role == "cashier" || actor.Role == "manager" || actor.Role == "system_admin",
		CanApprove: actor.Role == "manager" || actor.Role == "system_admin",
	}, nil
}

// GetContributionQuote reuses monthlyFees inside its required transaction so
// the preview cannot invent a different fee calculation from payment posting.
func (h *Handlers) GetContributionQuote(ctx context.Context, req *contributionv1.GetContributionQuoteRequest) (*contributionv1.GetContributionQuoteResponse, error) {
	actor, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "authenticated staff is required")
	}
	memberID, err := stringToUUID(req.GetMemberId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid member_id")
	}
	period, err := time.Parse("2006-01-02", req.GetContributionPeriod())
	if err != nil || period.Day() != 1 {
		return nil, status.Error(codes.InvalidArgument, "contribution_period must be the first day of the month")
	}
	response := &contributionv1.GetContributionQuoteResponse{}
	err = h.service.store.ExecTx(ctx, func(q contributionsqlc.Querier) error {
		exists, err := q.ContributionMemberExists(ctx, contributionsqlc.ContributionMemberExistsParams{MemberID: memberID, BranchID: actor.BranchID})
		if err != nil {
			return err
		}
		if !exists {
			return ErrPaymentRequestNotFound
		}
		fees, err := monthlyFees(ctx, q, memberID, pgtype.Date{Time: period, Valid: true}, pgtype.UUID{})
		if err != nil {
			return err
		}
		for _, fee := range fees {
			feeType := contributionv1.ContributionAllocationType_CONTRIBUTION_ALLOCATION_TYPE_COM
			if fee.Type == contributionsqlc.ContributionAllocationTypeLgom {
				feeType = contributionv1.ContributionAllocationType_CONTRIBUTION_ALLOCATION_TYPE_LGOM
			}
			response.Fees = append(response.Fees, &contributionv1.ContributionAllocationInput{Type: feeType, Amount: numericToMoney(fee.Amount)})
		}
		return nil
	})
	if err != nil {
		return nil, mapContributionError(err)
	}
	return response, nil
}
