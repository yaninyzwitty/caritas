package loan

import (
	"context"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	loanv1 "github.com/yaninyzwitty/caritas-backend/gen/loan/v1"
	"github.com/yaninyzwitty/caritas-backend/internal/auth"
	loansqlc "github.com/yaninyzwitty/caritas-backend/internal/loan/repository/sqlc"
	"github.com/yaninyzwitty/caritas-backend/internal/share"
	sharesqlc "github.com/yaninyzwitty/caritas-backend/internal/share/repository/sqlc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ManageCredit spends or reserves a partial credit under its row lock.
// Without this boundary, a refund and share transfer can spend the same money.
func (h *Handlers) ManageCredit(ctx context.Context, req *loanv1.ManageCreditRequest) (*loanv1.ManageCreditResponse, error) {
	actor, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "authenticated staff required")
	}
	creditID, err := stringToUUID(req.GetCreditBalanceId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid credit balance")
	}
	operationID, err := stringToUUID(req.GetOperationId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "a stable operation UUID is required")
	}
	action := req.GetAction()
	reviewing := action == "approve_refund" || action == "reject_refund" || action == "record_refund"
	if reviewing && !auth.CreditApprovalAllowed(actor.Role) {
		return nil, status.Error(codes.PermissionDenied, "credit approval required")
	}
	record, _ := auth.CashPermissions(actor.Role)
	if !reviewing && !record {
		return nil, status.Error(codes.PermissionDenied, "credit recording permission required")
	}
	var result loansqlc.CreditOperation
	err = h.store.ExecCollateralTx(ctx, func(q *loansqlc.Queries, shares *sharesqlc.Queries, tx pgx.Tx) error {
		credit, err := q.LockCreditBalanceByID(ctx, creditID)
		if err != nil {
			return err
		}
		branch, err := q.GetCreditMemberBranch(ctx, credit.MemberID)
		if err != nil {
			return err
		}
		if branch != actor.BranchID {
			return status.Error(codes.NotFound, "credit not found")
		}
		operation, err := q.GetCreditOperation(ctx, operationID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if operation.ID.Valid && operation.CreditBalanceID != creditID {
			return status.Error(codes.AlreadyExists, "operation belongs to another credit")
		}
		if reviewing {
			if action != "reject_refund" && credit.Status != loansqlc.CreditBalanceStatusAvailable {
				return status.Error(codes.FailedPrecondition, "credit is restricted")
			}
			if !operation.ID.Valid || operation.Action != "request_refund" {
				return status.Error(codes.NotFound, "refund request not found")
			}
			if operation.RequestedBy == actor.ID {
				return status.Error(codes.PermissionDenied, "another officer must review and settle this refund")
			}
			if strings.TrimSpace(req.GetReason()) == "" {
				return status.Error(codes.InvalidArgument, "review reason required")
			}
			switch action {
			case "approve_refund", "reject_refund":
				next := "approved"
				if action == "reject_refund" {
					next = "rejected"
				}
				if operation.Status == next {
					result = operation
					return nil
				}
				if operation.Status != "pending" {
					return status.Error(codes.FailedPrecondition, "only pending refunds can be reviewed")
				}
				result, err = q.ReviewCreditOperation(ctx, loansqlc.ReviewCreditOperationParams{ID: operationID, Status: next, ReviewedBy: actor.ID, ReviewReason: pgtype.Text{String: req.GetReason(), Valid: true}})
				return err
			case "record_refund":
				reference := strings.ToUpper(strings.TrimSpace(req.GetPayoutReference()))
				evidence := strings.TrimSpace(req.GetPayoutEvidence())
				if reference == "" || evidence == "" {
					return status.Error(codes.InvalidArgument, "verified payout reference and evidence required")
				}
				if operation.Status == "completed" {
					if operation.PayoutReference.String != reference {
						return status.Error(codes.AlreadyExists, "refund already settled with a different reference")
					}
					result = operation
					return nil
				}
				if operation.Status != "approved" {
					return status.Error(codes.FailedPrecondition, "refund must be approved before recording payment")
				}
				result, err = q.CompleteCreditOperation(ctx, loansqlc.CompleteCreditOperationParams{ID: operationID, CompletedBy: actor.ID, PayoutReference: pgtype.Text{String: reference, Valid: true}, PayoutEvidence: pgtype.Text{String: evidence, Valid: true}})
				return err
			}
		}
		if action != "transfer_shares" && action != "apply_loan" && action != "request_refund" {
			return status.Error(codes.InvalidArgument, "invalid credit action")
		}
		var amount pgtype.Numeric
		validAmount, _ := regexp.MatchString(`^(0|[1-9]\d{0,14})(\.\d{1,2})?$`, req.GetAmount())
		if err := amount.Scan(req.GetAmount()); !validAmount || err != nil || !amount.Valid || amount.NaN || amount.InfinityModifier != pgtype.Finite || numericToScale(amount, -4).Sign() <= 0 {
			return status.Error(codes.InvalidArgument, "positive decimal amount required")
		}
		var target pgtype.UUID
		var period pgtype.Date
		if action == "transfer_shares" && req.GetTargetId() == "" {
			account, lookupErr := shares.GetAccountByMemberID(ctx, credit.MemberID)
			if lookupErr != nil {
				return lookupErr
			}
			target = account.ID
		} else if action != "request_refund" {
			target, err = stringToUUID(req.GetTargetId())
			if err != nil {
				return status.Error(codes.InvalidArgument, "invalid destination")
			}
		}
		if action == "apply_loan" {
			parsed, parseErr := time.Parse("2006-01-02", req.GetContributionPeriod())
			now := time.Now().UTC().Add(3 * time.Hour)
			if parseErr != nil || parsed.Day() != 1 || parsed.After(time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)) {
				return status.Error(codes.InvalidArgument, "valid contribution month required")
			}
			period = pgtype.Date{Time: parsed, Valid: true}
		}
		if strings.TrimSpace(req.GetConsentReference()) == "" || strings.TrimSpace(req.GetReason()) == "" {
			return status.Error(codes.InvalidArgument, "member instruction reference and reason required")
		}
		if operation.ID.Valid {
			if operation.Action != action || numericToScale(operation.Amount, -4).Cmp(numericToScale(amount, -4)) != 0 || operation.TargetID != target || operation.ContributionPeriod != period || operation.ConsentReference != req.GetConsentReference() {
				return status.Error(codes.AlreadyExists, "operation details differ from the saved request")
			}
			result = operation
			return nil
		}
		if credit.Status != loansqlc.CreditBalanceStatusAvailable {
			return status.Error(codes.FailedPrecondition, "credit is restricted")
		}
		funds, err := q.GetCreditFunds(ctx, creditID)
		if err != nil {
			return err
		}
		available := numericToScale(funds.Amount, -4)
		available.Sub(available, numericToScale(funds.Reserved, -4))
		available.Sub(available, numericToScale(funds.Used, -4))
		if available.Cmp(numericToScale(amount, -4)) < 0 {
			return status.Error(codes.FailedPrecondition, "amount exceeds available credit")
		}
		result, err = q.InsertCreditOperation(ctx, loansqlc.InsertCreditOperationParams{ID: operationID, CreditBalanceID: creditID, Amount: amount, Action: action, TargetID: target, ContributionPeriod: period, Status: "pending", Reason: req.GetReason(), ConsentReference: req.GetConsentReference(), RequestedBy: actor.ID})
		if err != nil {
			return err
		}
		switch action {
		case "transfer_shares":
			owner := share.NewService(share.NewStore(h.store.pool))
			if _, err = owner.PurchaseSharesInTx(ctx, shares, target, amount, operationID, actor.ID, "member credit transfer: "+req.GetReason(), credit.MemberID, actor.BranchID); err != nil {
				return err
			}
		case "apply_loan":
			if _, err = h.service.RecordRepaymentForPeriodInTx(ctx, q, shares, target, amount, "credit:"+operationID.String(), actor.ID, period, credit.MemberID, actor.BranchID); err != nil {
				return err
			}
		case "request_refund":
			return nil
		}
		result, err = q.CompleteCreditOperation(ctx, loansqlc.CompleteCreditOperationParams{ID: operationID, CompletedBy: actor.ID})
		return err
	})
	if err != nil {
		var databaseError *pgconn.PgError
		if errors.As(err, &databaseError) && databaseError.Code == "23505" {
			return nil, status.Error(codes.AlreadyExists, "payout reference already belongs to a recorded refund")
		}
		if status.Code(err) != codes.Unknown {
			return nil, err
		}
		return nil, mapServiceError(err)
	}
	return &loanv1.ManageCreditResponse{Operation: creditOperationToProto(result)}, nil
}

// ListCreditOperations exposes reservations and settlement evidence next to the credit.
// Without it, a member's reduced available amount has no inspectable explanation.
func (h *Handlers) ListCreditOperations(ctx context.Context, req *loanv1.ListCreditOperationsRequest) (*loanv1.ListCreditOperationsResponse, error) {
	actor, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "authenticated staff required")
	}
	id, err := stringToUUID(req.GetCreditBalanceId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid credit")
	}
	credit, err := h.store.GetCreditBalanceByID(ctx, id)
	if err != nil {
		return nil, mapServiceError(err)
	}
	branch, err := h.store.GetCreditMemberBranch(ctx, credit.MemberID)
	if err != nil || branch != actor.BranchID {
		return nil, status.Error(codes.NotFound, "credit not found")
	}
	params := loansqlc.ListCreditOperationsParams{CreditBalanceID: id}
	if req.GetPageToken() != "" {
		raw, err := base64.RawURLEncoding.DecodeString(req.GetPageToken())
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid cursor")
		}
		stamp, id, found := strings.Cut(string(raw), "|")
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
	rows, err := h.store.ListCreditOperations(ctx, params)
	if err != nil {
		return nil, mapServiceError(err)
	}
	response := &loanv1.ListCreditOperationsResponse{Operations: make([]*loanv1.CreditOperation, 0, len(rows))}
	if len(rows) > 50 {
		rows = rows[:50]
		last := rows[49]
		response.NextPageToken = base64.RawURLEncoding.EncodeToString([]byte(last.CreatedAt.Time.Format(time.RFC3339Nano) + "|" + last.ID.String()))
	}
	for _, row := range rows {
		response.Operations = append(response.Operations, creditOperationToProto(row))
	}
	return response, nil
}

// creditOperationToProto shares the audit projection between mutations and history.
// Without it the two endpoints can disagree about reservation or settlement status.
func creditOperationToProto(row loansqlc.CreditOperation) *loanv1.CreditOperation {
	return &loanv1.CreditOperation{Id: row.ID.String(), CreditBalanceId: row.CreditBalanceID.String(), Amount: numericToString(row.Amount), Action: row.Action, TargetId: row.TargetID.String(), Status: row.Status, Reason: row.Reason, ConsentReference: row.ConsentReference, RequestedBy: row.RequestedBy.String(), PayoutReference: row.PayoutReference.String, CreatedAt: timestamppb.New(row.CreatedAt.Time)}
}
