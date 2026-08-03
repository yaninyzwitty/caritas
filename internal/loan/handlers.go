package loan

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	loanv1 "github.com/yaninyzwitty/caritas-backend/gen/loan/v1"
	loansqlc "github.com/yaninyzwitty/caritas-backend/internal/loan/repository/sqlc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Handlers struct {
	loanv1.UnimplementedLoanServiceServer
	loanv1.UnimplementedRepaymentServiceServer
	loanv1.UnimplementedCreditServiceServer
	store   *Store
	service *Service
}

func NewHandlers(store *Store, service *Service) *Handlers {
	return &Handlers{
		store:   store,
		service: service,
	}
}

const defaultBranchID = 1

// mapServiceError keeps loan RPCs on stable gRPC codes while preserving service
// sentinel errors for Go callers. Without it, expected domain failures are sent
// to clients as Internal/Unknown and cannot be handled safely.
func mapServiceError(err error) error {
	switch {
	case errors.Is(err, ErrLoanNotFound), errors.Is(err, ErrGuarantorNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, pgx.ErrNoRows):
		return status.Error(codes.NotFound, "not found")
	case errors.Is(err, ErrInvalidLoanAmount), errors.Is(err, ErrInvalidInterestRate), errors.Is(err, ErrInvalidRepaymentPeriod),
		errors.Is(err, ErrInvalidGuaranteedAmount), errors.Is(err, ErrGatewayTransactionID), errors.Is(err, ErrDuplicateGuarantor):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, ErrInvalidStatusTransition), errors.Is(err, ErrInvalidGuarantorStatus), errors.Is(err, ErrSelfGuarantee),
		errors.Is(err, ErrTooManyGuarantors), errors.Is(err, ErrInsufficientGuarantors), errors.Is(err, ErrInsufficientGuarantee),
		errors.Is(err, ErrPaymentNotAllowed), errors.Is(err, ErrRepaymentScheduleMissing), errors.Is(err, ErrMemberNotActive),
		errors.Is(err, ErrGuarantorNotActive):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	default:
		return status.Error(codes.Internal, "internal server error")
	}
}

func (h *Handlers) ApplyForLoan(ctx context.Context, req *loanv1.ApplyForLoanRequest) (*loanv1.ApplyForLoanResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request must never be nil")
	}

	if req.GetBranchId() == "" {
		return nil, status.Error(codes.InvalidArgument, "branch id is invalid")

	}

	if req.GetMemberId() == "" {
		return nil, status.Error(codes.InvalidArgument, "member id is required")

	}

	interestRate := req.GetInterestRate()

	if interestRate == "" {
		interestRate = "0.01" // equitable to 0.01 of previous loan balance
	}

	interestAmountInPercentage, err := parseNumeric(interestRate)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "interest rate must be a valid numeric value")
	}

	if len(req.GetGuarantors()) <= 0 || len(req.GetGuarantors()) > 20 {
		return nil, status.Error(codes.InvalidArgument, "guarantors must contain between 1 and 20 guarantors")
	}

	memberID, err := stringToUUID(req.GetMemberId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid member_id")
	}

	branchID := resolveBranchID(0)

	principalAmount, err := parseNumeric(req.GetPrincipal())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "principal must be a valid numeric value")
	}

	loanOfficerID, err := stringToUUID(req.GetOfficerId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid officer_id")
	}

	guarantors := make([]ProposedGuarantor, 0, len(req.GetGuarantors()))
	for _, guarantor := range req.GetGuarantors() {
		id, err := stringToUUID(guarantor.GetGuarantorId())
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid guarantor_id")
		}
		guaranteedAmount, err := parseNumeric(guarantor.GetGuaranteedAmount())
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "guaranteed_amount must be a valid numeric value")
		}
		guarantors = append(guarantors, ProposedGuarantor{
			GuarantorID:      id,
			GuaranteedAmount: guaranteedAmount,
		})
	}

	loan, err := h.service.ApplyForLoan(ctx, loansqlc.CreateLoanParams{
		MemberID:              memberID,
		BranchID:              branchID,
		Principal:             principalAmount,
		InterestRate:          interestAmountInPercentage,
		RepaymentPeriodMonths: req.GetRepaymentPeriodMonths(),
		// updated by should only be handled by the admin
		UpdatedBy: loanOfficerID,
	}, guarantors)

	if err != nil {
		return nil, mapServiceError(err)
	}

	return &loanv1.ApplyForLoanResponse{
		LoanId: loan.ID.String(),
		Status: loanStatusToProto(loan.Status),
	}, nil

}

func (h *Handlers) ApproveLoan(ctx context.Context, req *loanv1.ApproveLoanRequest) (*loanv1.ApproveLoanResponse, error) {
	if req.GetLoanId() == "" {
		return nil, status.Error(codes.InvalidArgument, "loan id is required")
	}

	if req.GetReason() == "" {
		return nil, status.Error(codes.InvalidArgument, "reason for the loan approval is required")
	}
	//

	if req.GetOfficerId() == "" {
		return nil, status.Error(codes.InvalidArgument, "loan officer id is required")
	}

	loanID, err := stringToUUID(req.GetLoanId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "loan id is required")
	}

	loanOfficerID, err := stringToUUID(req.GetOfficerId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid officer_id")
	}

	reason := req.GetReason()

	loanApproval, err := h.service.ApproveLoan(ctx, loanID, loanOfficerID, reason)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &loanv1.ApproveLoanResponse{
		NewStatus:  loanStatusToProto(loanApproval.Status),
		ApprovedAt: timestamppb.New(loanApproval.UpdatedAt.Time),
	}, nil

}

func (h *Handlers) RejectLoan(ctx context.Context, req *loanv1.RejectLoanRequest) (*loanv1.RejectLoanResponse, error) {
	if req.LoanId == "" {
		return nil, status.Error(codes.InvalidArgument, "loan id is required")
	}

	if req.Reason == "" {
		return nil, status.Error(codes.InvalidArgument, "exact reason is required")
	}

	loanOfficerID, err := stringToUUID(req.GetLoanOfficer())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid loan_officer")
	}

	loanID, err := stringToUUID(req.GetLoanId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "loan id is required")
	}

	loanRejection, err := h.service.RejectLoan(ctx, loanID, loanOfficerID, req.Reason)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &loanv1.RejectLoanResponse{
		NewStatus:  loanStatusToProto(loanRejection.Status),
		RejectedAt: timestamppb.New(loanRejection.UpdatedAt.Time),
	}, nil
}

func (h *Handlers) DisburseLoan(ctx context.Context, req *loanv1.DisburseLoanRequest) (*loanv1.DisburseLoanResponse, error) {
	if req.GetLoanId() == "" {
		return nil, status.Error(codes.InvalidArgument, "loanId is required")
	}

	if req.Reason == "" {
		return nil, status.Error(codes.InvalidArgument, "the exact reason is required")
	}

	loanID, err := stringToUUID(req.GetLoanId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid loan_id")
	}

	// TODO--implement real and great authentication and supply automatically
	disbursedByID, err := stringToUUID(req.GetLoanOfficer())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid loan_officer")
	}

	transaction, loanDisbursed, err := h.service.DisburseLoan(ctx, loanID, disbursedByID, req.GetReason())

	// TODO-look to handle errors for different conditions ie based on status, fail preconditon, no rows etc
	if err != nil {
		return nil, mapServiceError(err)

	}

	return &loanv1.DisburseLoanResponse{
		NewStatus:   loanStatusToProto(loanDisbursed.Status),
		DisbursedAt: timestamppb.New(loanDisbursed.UpdatedAt.Time),
		ReferenceId: transaction.ReferenceID.String(),
	}, nil

}
func (h *Handlers) GetLoan(ctx context.Context, req *loanv1.GetLoanRequest) (*loanv1.GetLoanResponse, error) {
	if req.GetLoanId() == "" {
		return nil, status.Error(codes.InvalidArgument, "loan id is required")
	}

	loanID, err := stringToUUID(req.GetLoanId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid loan_id")
	}

	loan, err := h.service.GetLoan(ctx, loanID)
	if err != nil {
		return nil, mapServiceError(err)
	}

	branchID := strconv.FormatInt(loan.BranchID, 10)

	return &loanv1.GetLoanResponse{
		Loan: &loanv1.Loan{
			Id:                    loan.ID.String(), // TODO we can drop the returned loan id
			MemberId:              loan.MemberID.String(),
			BranchId:              branchID,
			Principal:             numericToString(loan.Principal),
			InterestRate:          numericToString(loan.InterestRate),
			RepaymentPeriodMonths: loan.RepaymentPeriodMonths,
			Status:                loanStatusToProto(loan.Status),
			DisbursedAt:           timestamppb.New(loan.DisbursedAt.Time),
			CreatedAt:             timestamppb.New(loan.CreatedAt.Time),
			UpdatedAt:             timestamppb.New(loan.UpdatedAt.Time),
			UpdatedBy:             loan.UpdatedBy.String(),
			PreviousStatus:        string(loan.PreviousStatus.LoanStatus),
		},
	}, nil

}
func (h *Handlers) ListLoans(ctx context.Context, req *loanv1.ListLoansRequest) (*loanv1.ListLoansResponse, error) {
	const (
		defaultPageSize = 50
		maxPageSize     = 1000
	)

	// TODO-check the use of the branch id in this prospection
	if req.BranchId == "" {
		return nil, status.Error(codes.InvalidArgument, "branch id is required")
	}
	if req.MemberId == "" {
		return nil, status.Error(codes.InvalidArgument, "member id is required")
	}

	memberID, err := stringToUUID(req.GetMemberId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid member_id")
	}

	limit := req.GetPageSize()

	switch {
	case limit <= 0:
		limit = defaultPageSize
	case limit > maxPageSize:
		limit = maxPageSize
	}

	cursorTS, cursorID, err := decodeCursor(req.GetPageToken())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid page_token")
	}

	loans, err := h.store.ListLoansByMember(ctx, loansqlc.ListLoansByMemberParams{
		MemberID: memberID,
		Limit:    limit + 1,
		Column2:  cursorTS,
		ID:       cursorID,
	})

	if err != nil {
		return nil, mapServiceError(err)
	}

	resp := loanv1.ListLoansResponse{}

	hasMore := len(loans) > int(limit)

	if hasMore {
		last := loans[limit-1]

		token, err := encodeCursor(last.CreatedAt, last.ID)
		if err != nil {
			return nil, status.Error(codes.Internal, "failed to encode next page token")
		}

		resp.NextPageToken = token

		// drop the look ahead row from response
		loans = loans[:limit]
	}

	resp.Loans = make([]*loanv1.Loan, 0, len(loans))

	for _, loan := range loans {

		branchID := strconv.Itoa(int(loan.BranchID))
		loanPrincipal := numericToString(loan.Principal)
		interestRate := numericToString(loan.InterestRate)

		resp.Loans = append(resp.Loans, &loanv1.Loan{
			MemberId:              loan.MemberID.String(),
			BranchId:              branchID,
			Principal:             loanPrincipal,
			Id:                    loan.ID.String(),
			InterestRate:          interestRate,
			RepaymentPeriodMonths: loan.RepaymentPeriodMonths,
			Status:                loanStatusToProto(loan.Status),
			DisbursedAt:           timestamppb.New(loan.DisbursedAt.Time),
			CreatedAt:             timestamppb.New(loan.CreatedAt.Time),
			UpdatedAt:             timestamppb.New(loan.UpdatedAt.Time),
			UpdatedBy:             loan.UpdatedBy.String(),
			PreviousStatus:        string(loan.PreviousStatus.LoanStatus),
		})
	}

	return &resp, nil

}
func (h *Handlers) GetLoanStatus(ctx context.Context, req *loanv1.GetLoanStatusRequest) (*loanv1.GetLoanStatusResponse, error) {
	if req.GetLoanId() == "" {
		return nil, status.Error(codes.InvalidArgument, "loan id is required")
	}

	loanID, err := stringToUUID(req.GetLoanId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid loan_id")
	}

	loanStatus, err := h.service.GetLoanStatus(ctx, loanID)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &loanv1.GetLoanStatusResponse{
		Status: loanStatusToProto(loanStatus.Status),
	}, nil

}

func (h *Handlers) AddGuarantor(ctx context.Context, req *loanv1.AddGuarantorRequest) (*loanv1.AddGuarantorResponse, error) {
	if req.GetGuaranteedAmount() == "" {
		return nil, status.Error(codes.InvalidArgument, "guaranteed amount is required")
	}

	if req.GetLoanId() == "" {
		return nil, status.Error(codes.InvalidArgument, "loan id is required")
	}

	if req.GetGuarantorId() == "" {
		return nil, status.Error(codes.InvalidArgument, "guarantor id is required")
	}

	loanID, err := stringToUUID(req.GetLoanId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid loan_id")
	}
	guarantorID, err := stringToUUID(req.GetGuarantorId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid guarantor_id")
	}

	guaranteedAmount, err := parseNumeric(req.GuaranteedAmount)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "guaranteed amount must be a valid numeric value")
	}

	guarantor, err := h.service.AddGuarantor(ctx, loanID, guarantorID, guaranteedAmount)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &loanv1.AddGuarantorResponse{
		Status:    guarantorStatusToProto(guarantor.Status),
		CreatedAt: timestamppb.New(guarantor.CreatedAt.Time),
	}, nil

}

func (h *Handlers) RemoveGuarantor(ctx context.Context, req *loanv1.RemoveGuarantorRequest) (*loanv1.RemoveGuarantorResponse, error) {
	if req.GetGuarantorId() == "" {
		return nil, status.Error(codes.InvalidArgument, "guarantor id is required")
	}

	if req.GetLoanId() == "" {
		return nil, status.Error(codes.InvalidArgument, "loan id is required")
	}

	guarantorID, err := stringToUUID(req.GetGuarantorId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid guarantor_id")
	}
	loanID, err := stringToUUID(req.GetLoanId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid loan_id")
	}

	if err := h.service.RemoveGuarantor(ctx, loanID, guarantorID); err != nil {
		return nil, mapServiceError(err)
	}

	return &loanv1.RemoveGuarantorResponse{Success: true}, nil
}
func (h *Handlers) ApproveGuarantor(ctx context.Context, req *loanv1.ApproveGuarantorRequest) (*loanv1.ApproveGuarantorResponse, error) {
	if req.GetApprovedBy() == "" {
		return nil, status.Error(codes.InvalidArgument, "loan officer id who approved the guarantor is required")
	}

	if req.GetGuarantorId() == "" {
		return nil, status.Error(codes.InvalidArgument, "guarantor id is required")
	}

	if req.GetLoanId() == "" {
		return nil, status.Error(codes.InvalidArgument, "loan id is required")
	}

	loanID, err := stringToUUID(req.GetLoanId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid loan_id")
	}
	guarantorID, err := stringToUUID(req.GetGuarantorId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid guarantor_id")
	}
	loanOfficerID, err := stringToUUID(req.GetApprovedBy())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid approved_by")
	}

	guarantorApproval, err := h.service.ApproveGuarantor(ctx, loanID, guarantorID, loanOfficerID)
	if err != nil {
		return nil, mapServiceError(err)
	}
	// TODO-map guarantor approval status well in the client

	return &loanv1.ApproveGuarantorResponse{
		NewStatus:  guarantorStatusToProto(guarantorApproval.Status),
		ApprovedAt: timestamppb.New(guarantorApproval.ApprovedAt.Time),
	}, nil

}
func (h *Handlers) ListGuarantors(ctx context.Context, req *loanv1.ListGuarantorsRequest) (*loanv1.ListGuarantorsResponse, error) {
	if req.GetLoanId() == "" {
		return nil, status.Error(codes.InvalidArgument, "loan id is required")
	}

	const (
		defaultPageSize = 50
		maxPageSize     = 1000
	)

	limit := req.GetPageSize()
	switch {
	case limit <= 0:
		limit = defaultPageSize
	case limit > maxPageSize:
		limit = maxPageSize
	}

	cursorTS, cursorID, err := decodeCursor(req.GetPageToken())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid page_token")
	}
	var cursor *LoanCursor
	if req.GetPageToken() != "" {
		cursor = &LoanCursor{CreatedAt: cursorTS, ID: cursorID}
	}

	loanID, err := stringToUUID(req.GetLoanId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid loan_id")
	}

	guarantors, err := h.service.ListGuarantors(ctx, loanID, cursor, limit+1)
	if err != nil {
		return nil, mapServiceError(err)

	}

	resp := loanv1.ListGuarantorsResponse{}
	hasMore := len(guarantors) > int(limit)

	if hasMore {
		last := guarantors[limit-1]

		token, err := encodeCursor(last.CreatedAt, last.GuarantorID)
		if err != nil {
			return nil, status.Error(codes.Internal, "failed to encode next page token")
		}

		resp.NextPageToken = token

		// drop the look ahead row from the response
		guarantors = guarantors[:limit]
	}

	resp.Guarantors = make([]*loanv1.LoanGuarantor, 0, len(guarantors))

	for _, guarantor := range guarantors {

		resp.Guarantors = append(resp.Guarantors, &loanv1.LoanGuarantor{
			LoanId:           guarantor.LoanID.String(),
			GuarantorId:      guarantor.GuarantorID.String(),
			GuaranteedAmount: numericToString(guarantor.GuaranteedAmount),
			Status:           guarantorStatusToProto(guarantor.Status),
			ApprovedAt:       timestamppb.New(guarantor.ApprovedAt.Time),
			ApprovedBy:       guarantor.ApprovedBy.String(),
			CreatedAt:        timestamppb.New(guarantor.CreatedAt.Time),
		})
	}

	return &resp, nil

}

func (h *Handlers) RecordRepayment(ctx context.Context, req *loanv1.RecordRepaymentRequest) (*loanv1.RecordRepaymentResponse, error) {
	if req.GetLoanId() == "" {
		return nil, status.Error(codes.InvalidArgument, "loan id is required")
	}
	if req.GetAmount() == "" {
		return nil, status.Error(codes.InvalidArgument, "amount is required")
	}
	if req.GetPaymentGatewayTransactionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "payment gateway transaction id is required")
	}
	if req.GetCreatedBy() == "" {
		return nil, status.Error(codes.InvalidArgument, "created by is required")
	}

	loanID, err := stringToUUID(req.GetLoanId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid loan_id")
	}
	amount, err := parseNumeric(req.GetAmount())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid amount")
	}
	createdBy, err := stringToUUID(req.GetCreatedBy())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid created_by")
	}

	tx, err := h.service.RecordRepayment(ctx, loanID, amount, req.GetPaymentGatewayTransactionId(), createdBy)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &loanv1.RecordRepaymentResponse{
		TransactionId: tx.ID.String(),
		RecordedAt:    timestamppb.New(tx.CreatedAt.Time),
		Allocation:    allocationBreakdownToProto(tx.AllocationBreakdown),
	}, nil
}

func (h *Handlers) GetRepaymentSchedule(ctx context.Context, req *loanv1.GetRepaymentScheduleRequest) (*loanv1.GetRepaymentScheduleResponse, error) {
	if req.GetLoanId() == "" {
		return nil, status.Error(codes.InvalidArgument, "loan id is required")
	}

	loanID, err := stringToUUID(req.GetLoanId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid loan_id")
	}

	schedules, err := h.service.ListRepaymentSchedules(ctx, loanID)
	if err != nil {
		return nil, mapServiceError(err)
	}

	resp := loanv1.GetRepaymentScheduleResponse{
		Schedule: make([]*loanv1.RepaymentSchedule, 0, len(schedules)),
	}
	for _, schedule := range schedules {
		resp.Schedule = append(resp.Schedule, repaymentScheduleToProto(schedule))
	}
	return &resp, nil
}

func (h *Handlers) GetInstallmentDetails(ctx context.Context, req *loanv1.GetInstallmentDetailsRequest) (*loanv1.GetInstallmentDetailsResponse, error) {
	if req.GetLoanId() == "" {
		return nil, status.Error(codes.InvalidArgument, "loan id is required")
	}
	if req.GetInstallmentNo() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "installment no is required")
	}

	loanID, err := stringToUUID(req.GetLoanId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid loan_id")
	}

	schedules, err := h.service.ListRepaymentSchedules(ctx, loanID)
	if err != nil {
		return nil, mapServiceError(err)
	}
	for _, schedule := range schedules {
		if schedule.InstallmentNo == req.GetInstallmentNo() {
			return &loanv1.GetInstallmentDetailsResponse{Installment: repaymentScheduleToProto(schedule)}, nil
		}
	}
	return nil, status.Error(codes.NotFound, "installment not found")
}

func (h *Handlers) GetPaymentHistory(ctx context.Context, req *loanv1.GetPaymentHistoryRequest) (*loanv1.GetPaymentHistoryResponse, error) {
	if req.GetLoanId() == "" {
		return nil, status.Error(codes.InvalidArgument, "loan id is required")
	}
	if req.GetTypeFilter() != loanv1.TransactionType_TRANSACTION_TYPE_UNSPECIFIED {
		return nil, status.Error(codes.InvalidArgument, "transaction type filter is not supported")
	}

	const (
		defaultPageSize = 50
		maxPageSize     = 1000
	)

	limit := req.GetPageSize()
	switch {
	case limit <= 0:
		limit = defaultPageSize
	case limit > maxPageSize:
		limit = maxPageSize
	}

	cursorTS, cursorID, err := decodeCursor(req.GetPageToken())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid page_token")
	}
	var cursor *LoanCursor
	if req.GetPageToken() != "" {
		cursor = &LoanCursor{CreatedAt: cursorTS, ID: cursorID}
	}

	loanID, err := stringToUUID(req.GetLoanId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid loan_id")
	}

	transactions, err := h.service.ListLoanTransactions(ctx, loanID, cursor, limit+1)
	if err != nil {
		return nil, mapServiceError(err)
	}

	resp := loanv1.GetPaymentHistoryResponse{}
	hasMore := len(transactions) > int(limit)
	if hasMore {
		last := transactions[limit-1]
		token, err := encodeCursor(last.CreatedAt, last.ID)
		if err != nil {
			return nil, status.Error(codes.Internal, "failed to encode next page token")
		}
		resp.NextPageToken = token
		transactions = transactions[:limit]
	}

	resp.Transactions = make([]*loanv1.LoanTransaction, 0, len(transactions))
	for _, transaction := range transactions {
		resp.Transactions = append(resp.Transactions, loanTransactionToProto(transaction))
	}
	return &resp, nil
}

func (h *Handlers) GetCreditBalance(ctx context.Context, req *loanv1.GetCreditBalanceRequest) (*loanv1.GetCreditBalanceResponse, error) {
	if req.GetMemberId() == "" {
		return nil, status.Error(codes.InvalidArgument, "member id is required")
	}

	memberID, err := stringToUUID(req.GetMemberId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid member_id")
	}

	var loanID pgtype.UUID
	if req.GetLoanId() != "" {
		loanID, err = stringToUUID(req.GetLoanId())
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid loan_id")
		}
	}

	credits, err := h.service.ListCreditBalances(ctx, memberID, 100)
	if err != nil {
		return nil, mapServiceError(err)
	}

	resp := loanv1.GetCreditBalanceResponse{
		Credits: make([]*loanv1.CreditBalance, 0, len(credits)),
	}
	for _, credit := range credits {
		if loanID.Valid && !sameUUID(credit.LoanID, loanID) {
			continue
		}
		resp.Credits = append(resp.Credits, creditBalanceToProto(credit))
	}
	return &resp, nil
}

func (h *Handlers) RequestCreditWithdrawal(context.Context, *loanv1.RequestCreditWithdrawalRequest) (*loanv1.RequestCreditWithdrawalResponse, error) {
	return nil, status.Error(codes.Unimplemented, ErrUnsupportedLoanOperation.Error())
}

func stringToUUID(id string) (pgtype.UUID, error) {
	if id == "" {
		return pgtype.UUID{}, fmt.Errorf("empty uuid")
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid uuid: %w", err)
	}
	return pgtype.UUID{Bytes: [16]byte(parsed), Valid: true}, nil
}

func resolveBranchID(branchID int64) int64 {
	if branchID == 0 {
		return defaultBranchID
	}
	return branchID
}

func parseNumeric(val string) (pgtype.Numeric, error) {
	var numeric pgtype.Numeric

	if err := numeric.Scan(val); err != nil {
		return pgtype.Numeric{}, fmt.Errorf("parse numeric %q: %w", val, err)
	}
	return numeric, nil
}

func numericToString(n pgtype.Numeric) string {
	if !n.Valid || n.Int == nil {
		return ""
	}

	return decimalStringFromScale(n.Int, n.Exp)
}

func decodeCursor(token string) (pgtype.Timestamptz, pgtype.UUID, error) {
	var ts pgtype.Timestamptz
	var id pgtype.UUID
	if token == "" {
		return ts, id, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return ts, id, fmt.Errorf("invalid page token: %w", err)
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return ts, id, fmt.Errorf("invalid page token")
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return ts, id, fmt.Errorf("invalid page token: %w", err)
	}
	id, err = stringToUUID(parts[1])
	if err != nil {
		return ts, id, err
	}
	return pgtype.Timestamptz{Time: t, Valid: true}, id, nil
}

func encodeCursor(t pgtype.Timestamptz, id pgtype.UUID) (string, error) {
	if !t.Valid || !id.Valid {
		return "", nil
	}
	idStr, err := uuidToString(id)
	if err != nil {
		return "", err
	}
	raw := strings.Join([]string{
		t.Time.UTC().Format(time.RFC3339Nano),
		idStr,
	}, "|")
	return base64.RawURLEncoding.EncodeToString([]byte(raw)), nil
}

func uuidToString(id pgtype.UUID) (string, error) {
	if !id.Valid {
		return "", fmt.Errorf("invalid uuid")
	}
	return uuid.UUID(id.Bytes).String(), nil
}

func repaymentScheduleToProto(schedule loansqlc.RepaymentSchedule) *loanv1.RepaymentSchedule {
	return &loanv1.RepaymentSchedule{
		Id:            schedule.ID.String(),
		LoanId:        schedule.LoanID.String(),
		InstallmentNo: schedule.InstallmentNo,
		DueDate:       timestamppb.New(schedule.DueDate.Time),
		AmountDue:     numericToString(schedule.AmountDue),
		Status:        repaymentStatusToProto(schedule.Status),
	}
}

func loanStatusToProto(status loansqlc.LoanStatus) loanv1.LoanStatus {
	switch status {
	case loansqlc.LoanStatusPending:
		return loanv1.LoanStatus_LOAN_STATUS_PENDING
	case loansqlc.LoanStatusApproved:
		return loanv1.LoanStatus_LOAN_STATUS_APPROVED
	case loansqlc.LoanStatusRejected:
		return loanv1.LoanStatus_LOAN_STATUS_REJECTED
	case loansqlc.LoanStatusDisbursed:
		return loanv1.LoanStatus_LOAN_STATUS_DISBURSED
	case loansqlc.LoanStatusRestructuring:
		return loanv1.LoanStatus_LOAN_STATUS_RESTRUCTURING
	case loansqlc.LoanStatusActive:
		return loanv1.LoanStatus_LOAN_STATUS_ACTIVE
	case loansqlc.LoanStatusDelinquent:
		return loanv1.LoanStatus_LOAN_STATUS_DELINQUENT
	case loansqlc.LoanStatusClosed:
		return loanv1.LoanStatus_LOAN_STATUS_CLOSED
	case loansqlc.LoanStatusWrittenOff:
		return loanv1.LoanStatus_LOAN_STATUS_WRITTEN_OFF
	case loansqlc.LoanStatusManualReview:
		return loanv1.LoanStatus_LOAN_STATUS_MANUAL_REVIEW
	default:
		return loanv1.LoanStatus_LOAN_STATUS_UNSPECIFIED
	}
}

func guarantorStatusToProto(status loansqlc.GuarantorStatus) loanv1.GuarantorStatus {
	switch status {
	case loansqlc.GuarantorStatusPending:
		return loanv1.GuarantorStatus_GUARANTOR_STATUS_PENDING
	case loansqlc.GuarantorStatusApproved:
		return loanv1.GuarantorStatus_GUARANTOR_STATUS_APPROVED
	case loansqlc.GuarantorStatusRejected:
		return loanv1.GuarantorStatus_GUARANTOR_STATUS_REJECTED
	default:
		return loanv1.GuarantorStatus_GUARANTOR_STATUS_UNSPECIFIED
	}
}

func allocationBreakdownToProto(raw []byte) *loanv1.AllocationBreakdown {
	var allocation struct {
		Principal string `json:"principal"`
		Interest  string `json:"interest"`
		Penalty   string `json:"penalty"`
		Credit    string `json:"credit"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &allocation); err != nil {
			return &loanv1.AllocationBreakdown{}
		}
	}
	return &loanv1.AllocationBreakdown{
		Principal: allocation.Principal,
		Interest:  allocation.Interest,
		Penalty:   allocation.Penalty,
		Credit:    allocation.Credit,
	}
}

func repaymentStatusToProto(status loansqlc.RepaymentScheduleStatus) loanv1.RepaymentStatus {
	switch status {
	case loansqlc.RepaymentScheduleStatusUpcoming:
		return loanv1.RepaymentStatus_REPAYMENT_STATUS_UPCOMING
	case loansqlc.RepaymentScheduleStatusDue:
		return loanv1.RepaymentStatus_REPAYMENT_STATUS_DUE
	case loansqlc.RepaymentScheduleStatusPaid:
		return loanv1.RepaymentStatus_REPAYMENT_STATUS_PAID
	case loansqlc.RepaymentScheduleStatusMissed:
		return loanv1.RepaymentStatus_REPAYMENT_STATUS_MISSED
	case loansqlc.RepaymentScheduleStatusPartial:
		return loanv1.RepaymentStatus_REPAYMENT_STATUS_PARTIAL
	default:
		return loanv1.RepaymentStatus_REPAYMENT_STATUS_UNSPECIFIED
	}
}

func loanTransactionToProto(tx loansqlc.LoanTransaction) *loanv1.LoanTransaction {
	var gatewayID string
	if tx.PaymentGatewayTransactionID.Valid {
		gatewayID = tx.PaymentGatewayTransactionID.String
	}
	return &loanv1.LoanTransaction{
		Type:                        string(tx.Type),
		Amount:                      numericToString(tx.Amount),
		ReferenceId:                 tx.ReferenceID.String(),
		PaymentGatewayTransactionId: gatewayID,
		CreatedAt:                   timestamppb.New(tx.CreatedAt.Time),
		CreatedBy:                   tx.CreatedBy.String(),
		LoanId:                      tx.LoanID.String(),
		TransactionId:               tx.ID.String(),
	}
}

func creditBalanceToProto(credit loansqlc.CreditBalance) *loanv1.CreditBalance {
	return &loanv1.CreditBalance{
		Id:             credit.ID.String(),
		MemberId:       credit.MemberID.String(),
		LoanId:         credit.LoanID.String(),
		Amount:         numericToString(credit.Amount),
		Source:         string(credit.Source),
		Status:         creditStatusToProto(credit.Status),
		CreatedAt:      timestamppb.New(credit.CreatedAt.Time),
		LastActivityAt: timestamppb.New(credit.LastActivityAt.Time),
	}
}

func creditStatusToProto(status loansqlc.CreditBalanceStatus) loanv1.CreditStatus {
	switch status {
	case loansqlc.CreditBalanceStatusAvailable:
		return loanv1.CreditStatus_CREDIT_STATUS_AVAILABLE
	case loansqlc.CreditBalanceStatusFrozen:
		return loanv1.CreditStatus_CREDIT_STATUS_FROZEN
	case loansqlc.CreditBalanceStatusWithdrawn:
		return loanv1.CreditStatus_CREDIT_STATUS_WITHDRAWN
	default:
		return loanv1.CreditStatus_CREDIT_STATUS_UNSPECIFIED
	}
}
