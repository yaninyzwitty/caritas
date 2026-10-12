package contribution

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	contributionv1 "github.com/yaninyzwitty/caritas-backend/gen/contribution/v1"
	"github.com/yaninyzwitty/caritas-backend/internal/auth"
	contributionsqlc "github.com/yaninyzwitty/caritas-backend/internal/contribution/repository/sqlc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (h *Handlers) GetCashContext(ctx context.Context, _ *contributionv1.GetCashContextRequest) (*contributionv1.GetCashContextResponse, error) {
	actor, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "authenticated staff is required")
	}
	record, approve := auth.CashPermissions(actor.Role)
	return &contributionv1.GetCashContextResponse{StaffId: actor.ID.String(), BranchId: actor.BranchID, CanRecord: record, CanApprove: approve}, nil
}

func (h *Handlers) GetContributionQuote(ctx context.Context, req *contributionv1.GetContributionQuoteRequest) (*contributionv1.GetContributionQuoteResponse, error) {
	actor, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "authenticated staff is required")
	}
	id, err := stringToUUID(req.GetMemberId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid member_id")
	}
	period, err := time.Parse("2006-01-02", req.GetContributionPeriod())
	now := time.Now().In(time.FixedZone("EAT", 3*60*60))
	if err != nil || period.Day() != 1 || period.After(time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)) {
		return nil, status.Error(codes.InvalidArgument, "choose a valid current or past contribution month")
	}
	branch, err := h.service.store.GetContributionMemberBranch(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && branch != actor.BranchID) {
		return nil, status.Error(codes.NotFound, "member not found in this branch")
	}
	if err != nil {
		return nil, mapContributionError(err)
	}
	rows, err := h.service.store.QuoteMonthlyFees(ctx, contributionsqlc.QuoteMonthlyFeesParams{MemberID: id, Period: pgtype.Date{Time: period, Valid: true}})
	if err != nil {
		return nil, mapContributionError(err)
	}
	response := &contributionv1.GetContributionQuoteResponse{Fees: make([]*contributionv1.ContributionAllocationInput, 0, len(rows))}
	for _, row := range rows {
		if positive(row.Outstanding) {
			response.Fees = append(response.Fees, &contributionv1.ContributionAllocationInput{Type: allocationTypeToProto(row.Type), Amount: numericToMoney(row.Outstanding)})
		}
	}
	return response, nil
}

func (h *Handlers) GetCashReceipt(ctx context.Context, req *contributionv1.GetCashReceiptRequest) (*contributionv1.GetCashReceiptResponse, error) {
	actor, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "authenticated staff is required")
	}
	var row contributionsqlc.ContributionReceipt
	var err error
	if req.GetReceiptId() != "" && req.GetIdempotencyKey() == "" {
		id, parseErr := stringToUUID(req.GetReceiptId())
		if parseErr != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid receipt_id")
		}
		row, err = h.service.store.GetContributionReceiptByID(ctx, id)
	} else if req.GetIdempotencyKey() != "" && req.GetReceiptId() == "" {
		row, err = h.service.store.GetContributionReceiptByIdempotencyKey(ctx, text(req.GetIdempotencyKey()))
	} else {
		return nil, status.Error(codes.InvalidArgument, "supply a receipt ID or an idempotency key")
	}
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (row.BranchID != actor.BranchID || row.SourceChannel != contributionsqlc.ContributionSourceChannelCash)) {
		return nil, status.Error(codes.NotFound, "cash receipt not found")
	}
	if err != nil {
		return nil, mapContributionError(err)
	}
	allocations, err := h.service.store.ListContributionAllocationsByReceipt(ctx, row.ID)
	if err != nil {
		return nil, mapContributionError(err)
	}
	return &contributionv1.GetCashReceiptResponse{Receipt: cashReceiptToProto(row, allocations)}, nil
}

// cashReceiptToProto is shared by posting and recovery so printing never depends on a browser draft.
// Removing it would allow the two paths to disagree about authoritative receipt facts.
func cashReceiptToProto(row contributionsqlc.ContributionReceipt, allocations []contributionsqlc.ContributionAllocation) *contributionv1.CashContributionReceipt {
	receipt := &contributionv1.CashContributionReceipt{
		Id: row.ID.String(), InternalReceiptReference: row.InternalReceiptReference.String,
		SessionId: row.CashierSessionID.String(), Status: string(row.Status), Amount: numericToMoney(row.ReceivedAmount),
		MemberId: row.MemberID.String(), ContributionPeriod: row.ContributionPeriod.Time.Format("2006-01-02"),
		Allocations: make([]*contributionv1.CashReceiptAllocation, 0, len(allocations)),
	}
	if row.ReceivedAt.Valid {
		receipt.ReceivedAt = timestamppb.New(row.ReceivedAt.Time)
	}
	for _, row := range allocations {
		allocation := &contributionv1.CashReceiptAllocation{Type: allocationTypeToProto(row.Type), Amount: numericToMoney(row.Amount), Status: string(row.Status)}
		if row.TargetID.Valid {
			allocation.TargetId = row.TargetID.String()
		}
		if row.AuthoritativeReferenceID.Valid {
			allocation.AuthoritativeReferenceId = row.AuthoritativeReferenceID.String()
		}
		receipt.Allocations = append(receipt.Allocations, allocation)
	}
	return receipt
}

// allocationTypeToProto maps stored types for both quotes and receipts; removing it loses typed API labels.
func allocationTypeToProto(value contributionsqlc.ContributionAllocationType) contributionv1.ContributionAllocationType {
	return contributionv1.ContributionAllocationType(contributionv1.ContributionAllocationType_value["CONTRIBUTION_ALLOCATION_TYPE_"+strings.ToUpper(string(value))])
}

// cashCursor keeps both custody lists on the existing (created_at, id) pagination convention.
// Without it, malformed cursors or different page limits could produce inconsistent list navigation.
func cashCursor(size int32, token string) (int32, pgtype.Timestamptz, pgtype.UUID, error) {
	if size <= 0 {
		size = 50
	}
	if size > 100 {
		size = 100
	}
	var timestamp pgtype.Timestamptz
	var id pgtype.UUID
	if token != "" {
		raw, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil {
			return 0, timestamp, id, status.Error(codes.InvalidArgument, "invalid page_token")
		}
		date, value, ok := strings.Cut(string(raw), "|")
		parsed, err := time.Parse(time.RFC3339Nano, date)
		if !ok || err != nil {
			return 0, timestamp, id, status.Error(codes.InvalidArgument, "invalid page_token")
		}
		id, err = stringToUUID(value)
		if err != nil {
			return 0, timestamp, id, status.Error(codes.InvalidArgument, "invalid page_token")
		}
		timestamp = pgtype.Timestamptz{Time: parsed, Valid: true}
	}
	return size, timestamp, id, nil
}

func (h *Handlers) ListCashierSessions(ctx context.Context, req *contributionv1.ListCashierSessionsRequest) (*contributionv1.ListCashierSessionsResponse, error) {
	actor, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "authenticated staff is required")
	}
	size, timestamp, id, err := cashCursor(req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, err
	}
	rows, err := h.service.store.ListCashierSessions(ctx, contributionsqlc.ListCashierSessionsParams{BranchID: actor.BranchID, CursorCreatedAt: timestamp, CursorID: id, FetchLimit: size + 1})
	if err != nil {
		return nil, mapContributionError(err)
	}
	response := &contributionv1.ListCashierSessionsResponse{Sessions: make([]*contributionv1.CashierSession, 0, len(rows))}
	if len(rows) > int(size) {
		last := rows[size-1]
		response.NextPageToken = base64.RawURLEncoding.EncodeToString([]byte(last.CreatedAt.Time.Format(time.RFC3339Nano) + "|" + last.ID.String()))
		rows = rows[:size]
	}
	staffIDs := make([]pgtype.UUID, 0, len(rows)*2)
	for _, row := range rows {
		staffIDs = append(staffIDs, row.CashierID, row.HandedOverTo)
	}
	names, err := h.cashStaffNames(ctx, actor.BranchID, staffIDs)
	if err != nil {
		return nil, mapContributionError(err)
	}
	for _, row := range rows {
		if row.Status == contributionsqlc.CashierSessionStatusOpen {
			row.ExpectedAmount, err = h.service.store.SumCashReceiptsBySession(ctx, row.ID)
			if err != nil {
				return nil, mapContributionError(err)
			}
		}
		session, err := cashierSessionToProto(row)
		if err != nil {
			return nil, err
		}
		session.CashierName = names[row.CashierID]
		session.HandedOverToName = names[row.HandedOverTo]
		response.Sessions = append(response.Sessions, session)
	}
	return response, nil
}

func (h *Handlers) ListCashDeposits(ctx context.Context, req *contributionv1.ListCashDepositsRequest) (*contributionv1.ListCashDepositsResponse, error) {
	actor, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "authenticated staff is required")
	}
	size, timestamp, id, err := cashCursor(req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, err
	}
	rows, err := h.service.store.ListCashDeposits(ctx, contributionsqlc.ListCashDepositsParams{BranchID: actor.BranchID, CursorCreatedAt: timestamp, CursorID: id, FetchLimit: size + 1})
	if err != nil {
		return nil, mapContributionError(err)
	}
	response := &contributionv1.ListCashDepositsResponse{Deposits: make([]*contributionv1.CashDeposit, 0, len(rows))}
	if len(rows) > int(size) {
		last := rows[size-1]
		response.NextPageToken = base64.RawURLEncoding.EncodeToString([]byte(last.CreatedAt.Time.Format(time.RFC3339Nano) + "|" + last.ID.String()))
		rows = rows[:size]
	}
	staffIDs := make([]pgtype.UUID, 0, len(rows)*2)
	for _, row := range rows {
		staffIDs = append(staffIDs, row.RecordedBy, row.VerifiedBy)
	}
	names, err := h.cashStaffNames(ctx, actor.BranchID, staffIDs)
	if err != nil {
		return nil, mapContributionError(err)
	}
	for _, row := range rows {
		deposit, err := cashDepositToProto(row)
		if err != nil {
			return nil, err
		}
		deposit.RecordedByName = names[row.RecordedBy]
		deposit.VerifiedByName = names[row.VerifiedBy]
		response.Deposits = append(response.Deposits, deposit)
	}
	return response, nil
}

func (h *Handlers) GetCashDeposit(ctx context.Context, req *contributionv1.GetCashDepositRequest) (*contributionv1.GetCashDepositResponse, error) {
	actor, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "authenticated staff is required")
	}
	id, err := stringToUUID(req.GetDepositId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid deposit_id")
	}
	row, err := h.service.store.GetCashDepositByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && row.BranchID != actor.BranchID) {
		return nil, status.Error(codes.NotFound, "cash deposit not found")
	}
	if err != nil {
		return nil, mapContributionError(err)
	}
	deposit, err := cashDepositToProto(row)
	if err != nil {
		return nil, err
	}
	names, err := h.cashStaffNames(ctx, actor.BranchID, []pgtype.UUID{row.RecordedBy, row.VerifiedBy})
	if err != nil {
		return nil, mapContributionError(err)
	}
	deposit.RecordedByName = names[row.RecordedBy]
	deposit.VerifiedByName = names[row.VerifiedBy]
	sessions, err := h.service.store.ListCashDepositSessions(ctx, id)
	if err != nil {
		return nil, mapContributionError(err)
	}
	for _, session := range sessions {
		deposit.SessionIds = append(deposit.SessionIds, session.String())
	}
	return &contributionv1.GetCashDepositResponse{Deposit: deposit}, nil
}

// cashStaffNames resolves custody actors in one branch-scoped query per read.
// Without it, custody screens expose UUIDs or need a separate lookup for every row.
func (h *Handlers) cashStaffNames(ctx context.Context, branchID int64, ids []pgtype.UUID) (map[pgtype.UUID]string, error) {
	rows, err := h.service.store.GetCashStaffNames(ctx, contributionsqlc.GetCashStaffNamesParams{BranchID: branchID, StaffIds: ids})
	if err != nil {
		return nil, err
	}
	names := make(map[pgtype.UUID]string, len(rows))
	for _, row := range rows {
		names[row.ID] = row.Name
	}
	return names, nil
}
