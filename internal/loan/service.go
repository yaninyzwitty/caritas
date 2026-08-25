package loan

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	loansqlc "github.com/yaninyzwitty/caritas-backend/internal/loan/repository/sqlc"
	"github.com/yaninyzwitty/caritas-backend/internal/member"
)

const maxGuarantors = 20

type LoanCursor struct {
	CreatedAt pgtype.Timestamptz
	ID        pgtype.UUID
}

type ProposedGuarantor struct {
	GuarantorID      pgtype.UUID
	GuaranteedAmount pgtype.Numeric
}

type Service struct {
	store         *Store
	memberService *member.Service
}

func NewService(store *Store, memberService *member.Service) *Service {
	return &Service{store: store, memberService: memberService}
}

func (s *Service) ApplyForLoan(ctx context.Context, params loansqlc.CreateLoanParams, guarantors []ProposedGuarantor) (loansqlc.CreateLoanRow, error) {

	// increase timeout to prevent defer tx rollback errors, due to context time getting done
	applyLoanCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if !positive(params.Principal) {
		return loansqlc.CreateLoanRow{}, ErrInvalidLoanAmount
	}
	if !nonNegative(params.InterestRate) {
		return loansqlc.CreateLoanRow{}, ErrInvalidInterestRate
	}
	if params.RepaymentPeriodMonths < 1 || params.RepaymentPeriodMonths > 36 {
		return loansqlc.CreateLoanRow{}, ErrInvalidRepaymentPeriod
	}
	if len(guarantors) < 1 {
		return loansqlc.CreateLoanRow{}, ErrInsufficientGuarantors
	}
	if len(guarantors) > maxGuarantors {
		return loansqlc.CreateLoanRow{}, ErrTooManyGuarantors
	}
	if err := s.requireActiveMember(applyLoanCtx, params.MemberID, ErrMemberNotActive); err != nil {
		return loansqlc.CreateLoanRow{}, err
	}

	totalGuarantee := new(big.Int)
	seen := make(map[pgtype.UUID]struct{}, len(guarantors))
	for _, guarantor := range guarantors {
		if !positive(guarantor.GuaranteedAmount) {
			return loansqlc.CreateLoanRow{}, ErrInvalidGuaranteedAmount
		}
		if sameUUID(params.MemberID, guarantor.GuarantorID) {
			return loansqlc.CreateLoanRow{}, ErrSelfGuarantee
		}
		if _, ok := seen[guarantor.GuarantorID]; ok {
			return loansqlc.CreateLoanRow{}, ErrDuplicateGuarantor
		}
		seen[guarantor.GuarantorID] = struct{}{}

		if err := s.requireActiveMember(applyLoanCtx, guarantor.GuarantorID, ErrGuarantorNotActive); err != nil {
			return loansqlc.CreateLoanRow{}, err
		}
		totalGuarantee.Add(totalGuarantee, numericToScale(guarantor.GuaranteedAmount, -4))
	}
	if totalGuarantee.Cmp(numericToScale(params.Principal, -4)) < 0 {
		return loansqlc.CreateLoanRow{}, ErrInsufficientGuarantee
	}

	var loan loansqlc.CreateLoanRow
	err := s.store.ExecTx(applyLoanCtx, func(q loansqlc.Querier) error {
		var err error
		loan, err = q.CreateLoan(applyLoanCtx, params)
		if err != nil {
			return fmt.Errorf("create loan: %w", err)
		}

		for _, guarantor := range guarantors {
			if _, err := q.CreateLoanGuarantor(applyLoanCtx, loansqlc.CreateLoanGuarantorParams{
				LoanID:           loan.ID,
				GuarantorID:      guarantor.GuarantorID,
				GuaranteedAmount: guarantor.GuaranteedAmount,
				Status:           loansqlc.GuarantorStatusPending,
			}); err != nil {
				return fmt.Errorf("create guarantor: %w", err)
			}
		}

		if _, err := q.InsertLoanAuditTrail(applyLoanCtx, loansqlc.InsertLoanAuditTrailParams{
			LoanID:       loan.ID,
			FieldChanged: "loan.application",
			NewValue:     string(loansqlc.LoanStatusPending),
			ChangedBy:    params.UpdatedBy,
			ChangeReason: "loan application submitted",
		}); err != nil {
			return fmt.Errorf("insert audit trail: %w", err)
		}
		return nil
	})
	if err != nil {
		return loansqlc.CreateLoanRow{}, err
	}
	return loan, nil
}

func (s *Service) GetLoan(ctx context.Context, loanID pgtype.UUID) (loansqlc.GetLoanByIDRow, error) {
	loan, err := s.store.GetLoanByID(ctx, loanID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return loansqlc.GetLoanByIDRow{}, ErrLoanNotFound
		}
		return loansqlc.GetLoanByIDRow{}, fmt.Errorf("get loan: %w", err)
	}
	return loan, nil
}

func (s *Service) GetLoanStatus(ctx context.Context, loanID pgtype.UUID) (loansqlc.GetLoanByIDRow, error) {
	return s.GetLoan(ctx, loanID)
}

func (s *Service) ListLoansByBranch(
	ctx context.Context,
	branchID int64,
	cursor *LoanCursor,
	limit int32,
	status *loansqlc.LoanStatus,
) ([]loansqlc.ListLoansByBranchRow, error) {
	params := loansqlc.ListLoansByBranchParams{
		BranchID: branchID,
		Limit:    normalizeLimit(limit),
	}
	if cursor != nil {
		params.Column2 = cursor.CreatedAt
		params.ID = cursor.ID
	}
	if status != nil {
		params.StatusFilter = loansqlc.NullLoanStatus{LoanStatus: *status, Valid: true}
	}

	loans, err := s.store.ListLoansByBranch(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list loans by branch: %w", err)
	}
	return loans, nil
}

func (s *Service) ListLoansByMember(
	ctx context.Context,
	memberID pgtype.UUID,
	cursor *LoanCursor,
	limit int32,
) ([]loansqlc.ListLoansByMemberRow, error) {
	params := loansqlc.ListLoansByMemberParams{
		MemberID: memberID,
		Limit:    normalizeLimit(limit),
	}
	if cursor != nil {
		params.Column2 = cursor.CreatedAt
		params.ID = cursor.ID
	}

	loans, err := s.store.ListLoansByMember(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list loans by member: %w", err)
	}
	return loans, nil
}

func (s *Service) AddGuarantor(
	ctx context.Context,
	loanID, guarantorID pgtype.UUID,
	guaranteedAmount pgtype.Numeric,
) (loansqlc.LoanGuarantor, error) {
	// must be positive amount
	if !positive(guaranteedAmount) {
		return loansqlc.LoanGuarantor{}, ErrInvalidGuaranteedAmount
	}
	if err := s.requireActiveMember(ctx, guarantorID, ErrGuarantorNotActive); err != nil {
		return loansqlc.LoanGuarantor{}, err
	}

	var guarantor loansqlc.LoanGuarantor
	err := s.store.ExecTx(ctx, func(q loansqlc.Querier) error {
		current, err := q.LockLoanByID(ctx, loanID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrLoanNotFound
			}
			return fmt.Errorf("lock loan: %w", err)
		}

		// loan might already be disbursed for example
		if current.Status != loansqlc.LoanStatusPending {
			return fmt.Errorf("%w: cannot add guarantor to %s loan", ErrInvalidStatusTransition, current.Status)
		}

		// a member cannot guarantee themselves a loan
		if sameUUID(current.MemberID, guarantorID) {
			return ErrSelfGuarantee
		}

		existing, err := q.GetLoanGuarantor(ctx, loansqlc.GetLoanGuarantorParams{
			LoanID:      loanID,
			GuarantorID: guarantorID,
		})
		switch {
		case err == nil:
			guarantor = existing
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("check guarantor: %w", err)
		}

		guarantors, err := q.ListLoanGuarantors(ctx, loansqlc.ListLoanGuarantorsParams{
			LoanID: loanID,
			Limit:  maxGuarantors,
		})
		if err != nil {
			return fmt.Errorf("list guarantors: %w", err)
		}

		// less than 20 guarantors
		if len(guarantors) >= maxGuarantors {
			return ErrTooManyGuarantors
		}

		guarantor, err = q.CreateLoanGuarantor(ctx, loansqlc.CreateLoanGuarantorParams{
			LoanID:           loanID,
			GuarantorID:      guarantorID,
			GuaranteedAmount: guaranteedAmount,
			Status:           loansqlc.GuarantorStatusPending,
		})
		if err != nil {
			return fmt.Errorf("create guarantor: %w", err)
		}
		return nil
	})
	if err != nil {
		return loansqlc.LoanGuarantor{}, err
	}
	return guarantor, nil
}

func (s *Service) ApproveGuarantor(
	ctx context.Context,
	loanID, guarantorID, approvedBy pgtype.UUID,
) (loansqlc.LoanGuarantor, error) {
	var guarantor loansqlc.LoanGuarantor
	err := s.store.ExecTx(ctx, func(q loansqlc.Querier) error {
		// TODO-that guarantor must have the right amount of colateral inorder to get approved, and must not reduce their shares by more than 30%
		current, err := q.LockGuarantor(ctx, loansqlc.LockGuarantorParams{
			LoanID:      loanID,
			GuarantorID: guarantorID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrGuarantorNotFound
			}
			return fmt.Errorf("lock guarantor: %w", err)
		}

		if current.Status != loansqlc.GuarantorStatusPending {
			return fmt.Errorf("%w: cannot approve %s guarantor", ErrInvalidGuarantorStatus, current.Status)
		}

		guarantor, err = q.UpdateGuarantorStatus(ctx, loansqlc.UpdateGuarantorStatusParams{
			LoanID:      loanID,
			ApprovedBy:  approvedBy,
			GuarantorID: guarantorID,
			Status:      loansqlc.GuarantorStatusApproved,
		})
		if err != nil {
			slog.Error("approve guarantor", "error", err)
			return fmt.Errorf("approve guarantor: %w", err)
		}

		// very important for an audit trail
		_, err = q.InsertLoanAuditTrail(ctx, loansqlc.InsertLoanAuditTrailParams{
			LoanID:        loanID,
			FieldChanged:  "guarantor.status",
			PreviousValue: text(string(current.Status)),
			NewValue:      string(loansqlc.GuarantorStatusApproved),
			ChangedBy:     approvedBy,
			ChangeReason:  "guarantor approved",
		})
		if err != nil {
			return fmt.Errorf("insert audit trail: %w", err)
		}
		return nil
	})
	if err != nil {
		return loansqlc.LoanGuarantor{}, err
	}
	return guarantor, nil
}

func (s *Service) RemoveGuarantor(ctx context.Context, loanID, guarantorID pgtype.UUID) error {
	return s.store.ExecTx(ctx, func(q loansqlc.Querier) error {
		loan, err := q.LockLoanByID(ctx, loanID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrLoanNotFound
			}
			return fmt.Errorf("lock loan: %w", err)
		}
		if loan.Status != loansqlc.LoanStatusPending {
			return fmt.Errorf("%w: cannot remove guarantor from %s loan", ErrInvalidStatusTransition, loan.Status)
		}

		current, err := q.LockGuarantor(ctx, loansqlc.LockGuarantorParams{
			LoanID:      loanID,
			GuarantorID: guarantorID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrGuarantorNotFound
			}
			return fmt.Errorf("lock guarantor: %w", err)
		}
		if current.Status == loansqlc.GuarantorStatusRejected {
			return nil
		}

		_, err = q.UpdateGuarantorStatus(ctx, loansqlc.UpdateGuarantorStatusParams{
			LoanID:      loanID,
			Status:      loansqlc.GuarantorStatusRejected,
			GuarantorID: guarantorID,
		})
		if err != nil {
			return fmt.Errorf("remove guarantor: %w", err)
		}

		_, err = q.InsertLoanAuditTrail(ctx, loansqlc.InsertLoanAuditTrailParams{
			LoanID:        loanID,
			FieldChanged:  "guarantor.status",
			PreviousValue: text(string(current.Status)),
			NewValue:      string(loansqlc.GuarantorStatusRejected),
			ChangedBy:     guarantorID,
			ChangeReason:  "guarantor removed",
		})
		if err != nil {
			return fmt.Errorf("insert audit trail: %w", err)
		}
		return nil
	})
}

func (s *Service) ApproveLoan(
	ctx context.Context,
	loanID, approvedBy pgtype.UUID,
	reason string,
) (loansqlc.UpdateLoanStatusRow, error) {
	var loan loansqlc.UpdateLoanStatusRow
	err := s.store.ExecTx(ctx, func(q loansqlc.Querier) error {
		current, err := q.LockLoanByID(ctx, loanID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrLoanNotFound
			}
			return fmt.Errorf("lock loan: %w", err)
		}
		if current.Status != loansqlc.LoanStatusPending {
			return fmt.Errorf("%w: cannot approve %s loan", ErrInvalidStatusTransition, current.Status)
		}
		if err := verifyApprovedGuarantees(ctx, q, loanID, current.Principal); err != nil {
			return err
		}

		loan, err = q.UpdateLoanStatus(ctx, loansqlc.UpdateLoanStatusParams{
			ID:        loanID,
			Status:    loansqlc.LoanStatusApproved,
			UpdatedBy: approvedBy,
		})
		if err != nil {
			return fmt.Errorf("approve loan: %w", err)
		}
		if err := insertStatusAudit(ctx, q, loanID, current.Status, loansqlc.LoanStatusApproved, approvedBy, reason); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return loansqlc.UpdateLoanStatusRow{}, err
	}
	return loan, nil
}

func (s *Service) RejectLoan(
	ctx context.Context,
	loanID, rejectedBy pgtype.UUID,
	reason string,
) (loansqlc.UpdateLoanStatusRow, error) {
	var loan loansqlc.UpdateLoanStatusRow
	err := s.store.ExecTx(ctx, func(q loansqlc.Querier) error {
		current, err := q.LockLoanByID(ctx, loanID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrLoanNotFound
			}
			return fmt.Errorf("lock loan: %w", err)
		}
		if current.Status != loansqlc.LoanStatusPending && current.Status != loansqlc.LoanStatusApproved {
			return fmt.Errorf("%w: cannot reject %s loan", ErrInvalidStatusTransition, current.Status)
		}

		loan, err = q.UpdateLoanStatus(ctx, loansqlc.UpdateLoanStatusParams{
			ID:        loanID,
			Status:    loansqlc.LoanStatusRejected,
			UpdatedBy: rejectedBy,
		})
		if err != nil {
			return fmt.Errorf("reject loan: %w", err)
		}
		if err := insertStatusAudit(ctx, q, loanID, current.Status, loansqlc.LoanStatusRejected, rejectedBy, reason); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return loansqlc.UpdateLoanStatusRow{}, err
	}
	return loan, nil
}

func (s *Service) DisburseLoan(
	ctx context.Context,
	loanID, disbursedBy pgtype.UUID,
	reason string,
) (loansqlc.LoanTransaction, loansqlc.MarkLoanDisbursedRow, error) {
	var tx loansqlc.LoanTransaction
	var loan loansqlc.MarkLoanDisbursedRow

	err := s.store.ExecTx(ctx, func(q loansqlc.Querier) error {
		current, err := q.LockLoanByID(ctx, loanID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrLoanNotFound
			}
			return fmt.Errorf("lock loan: %w", err)
		}

		existing, err := q.GetLoanDisbursement(ctx, loanID)
		switch {
		case err == nil:
			tx = existing
			loan = markRowFromLock(current)
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("check existing disbursement: %w", err)
		}

		if current.Status != loansqlc.LoanStatusApproved {
			return fmt.Errorf("%w: cannot disburse %s loan", ErrInvalidStatusTransition, current.Status)
		}
		if err := verifyApprovedGuarantees(ctx, q, loanID, current.Principal); err != nil {
			return err
		}

		referenceID, err := newUUID()
		if err != nil {
			return fmt.Errorf("generate reference id: %w", err)
		}
		tx, err = q.InsertLoanTransaction(ctx, loansqlc.InsertLoanTransactionParams{
			LoanID:              loanID,
			Type:                loansqlc.LoanTransactionTypeDisbursement,
			Amount:              current.Principal,
			ReferenceID:         referenceID,
			AllocationBreakdown: []byte("{}"),
			CreatedBy:           disbursedBy,
		})
		if err != nil {
			return fmt.Errorf("insert disbursement transaction: %w", err)
		}

		loan, err = q.MarkLoanDisbursed(ctx, loansqlc.MarkLoanDisbursedParams{
			ID:        loanID,
			UpdatedBy: disbursedBy,
		})
		if err != nil {
			return fmt.Errorf("mark loan disbursed: %w", err)
		}

		// Create the initial schedule in this transaction so an active loan can
		// never commit without the rows RecordRepayment locks and allocates over.
		if current.RepaymentPeriodMonths <= 0 {
			return ErrInvalidRepaymentPeriod
		}
		total := numericToScale(current.Principal, -4)
		months := big.NewInt(int64(current.RepaymentPeriodMonths))
		installment := new(big.Int).Quo(total, months)
		remainder := new(big.Int).Mod(total, months)
		if installment.Sign() <= 0 {
			return ErrInvalidLoanAmount
		}

		for installmentNo := int32(1); installmentNo <= current.RepaymentPeriodMonths; installmentNo++ {
			amountDue := new(big.Int).Set(installment)
			if installmentNo == current.RepaymentPeriodMonths {
				amountDue.Add(amountDue, remainder)
			}
			dueSeed := loan.DisbursedAt.Time.AddDate(0, int(installmentNo), 0)
			dueDate := time.Date(dueSeed.Year(), dueSeed.Month()+1, 0, 0, 0, 0, 0, dueSeed.Location())
			if _, err := q.CreateRepaymentSchedule(ctx, loansqlc.CreateRepaymentScheduleParams{
				LoanID:        loanID,
				InstallmentNo: installmentNo,
				DueDate:       pgtype.Date{Time: dueDate, Valid: true},
				AmountDue:     numericFromScale(amountDue),
				Status:        loansqlc.RepaymentScheduleStatusUpcoming,
			}); err != nil {
				return fmt.Errorf("create repayment schedule: %w", err)
			}
		}

		if err := insertStatusAudit(ctx, q, loanID, current.Status, loansqlc.LoanStatusActive, disbursedBy, reason); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return loansqlc.LoanTransaction{}, loansqlc.MarkLoanDisbursedRow{}, err
	}
	return tx, loan, nil
}

func (s *Service) RecordRepayment(
	ctx context.Context,
	loanID pgtype.UUID,
	amount pgtype.Numeric,
	gatewayTransactionID string,
	createdBy pgtype.UUID,
) (loansqlc.LoanTransaction, error) {
	if !positive(amount) {
		return loansqlc.LoanTransaction{}, ErrInvalidLoanAmount
	}
	gatewayTransactionID = strings.TrimSpace(gatewayTransactionID)
	if gatewayTransactionID == "" {
		return loansqlc.LoanTransaction{}, ErrGatewayTransactionID
	}

	var tx loansqlc.LoanTransaction
	err := s.store.ExecTx(ctx, func(q loansqlc.Querier) error {
		current, err := q.LockLoanByID(ctx, loanID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrLoanNotFound
			}
			return fmt.Errorf("lock loan: %w", err)
		}
		tx, err = q.GetLoanTransactionByGatewayID(ctx, loansqlc.GetLoanTransactionByGatewayIDParams{
			LoanID:                      loanID,
			PaymentGatewayTransactionID: pgtype.Text{String: gatewayTransactionID, Valid: true},
		})
		switch {
		case err == nil:
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("check existing repayment: %w", err)
		}

		switch current.Status {
		case loansqlc.LoanStatusDisbursed, loansqlc.LoanStatusActive, loansqlc.LoanStatusDelinquent:
		default:
			return ErrPaymentNotAllowed
		}

		schedules, err := q.LockActiveRepaymentSchedulesByLoan(ctx, loanID)
		if err != nil {
			return fmt.Errorf("lock repayment schedules: %w", err)
		}
		if len(schedules) == 0 {
			return ErrRepaymentScheduleMissing
		}

		priorApplied, err := q.SumLoanAppliedRepayments(ctx, loanID)
		if err != nil {
			return fmt.Errorf("sum applied repayments: %w", err)
		}

		allocation := allocateRepayment(amount, priorApplied, schedules)
		allocationJSON, err := json.Marshal(allocation)
		if err != nil {
			return fmt.Errorf("encode allocation: %w", err)
		}

		referenceID, err := newUUID()
		if err != nil {
			return fmt.Errorf("generate reference id: %w", err)
		}
		tx, err = q.InsertLoanTransaction(ctx, loansqlc.InsertLoanTransactionParams{
			LoanID:                      loanID,
			Type:                        loansqlc.LoanTransactionTypeRepayment,
			Amount:                      amount,
			ReferenceID:                 referenceID,
			PaymentGatewayTransactionID: pgtype.Text{String: gatewayTransactionID, Valid: true},
			AllocationBreakdown:         allocationJSON,
			CreatedBy:                   createdBy,
		})
		if err != nil {
			return fmt.Errorf("insert repayment transaction: %w", err)
		}

		if allocation.Credit != "0" {
			if _, err := q.CreateCreditBalance(ctx, loansqlc.CreateCreditBalanceParams{
				MemberID: current.MemberID,
				LoanID:   loanID,
				Amount:   numericFromScale(allocation.creditAmount),
				Source:   loansqlc.CreditBalanceSourceOverpayment,
			}); err != nil {
				return fmt.Errorf("create credit balance: %w", err)
			}
		}

		totalApplied := numericToScale(priorApplied, -4)
		totalApplied.Add(totalApplied, allocation.loanAppliedAmount)
		if err := updateScheduleStatuses(ctx, q, schedules, totalApplied); err != nil {
			return err
		}
		if allocation.loanClosed {
			if _, err := q.UpdateLoanStatus(ctx, loansqlc.UpdateLoanStatusParams{
				ID:        loanID,
				Status:    loansqlc.LoanStatusClosed,
				UpdatedBy: createdBy,
			}); err != nil {
				return fmt.Errorf("close loan: %w", err)
			}
			if err := insertStatusAudit(ctx, q, loanID, current.Status, loansqlc.LoanStatusClosed, createdBy, "loan fully repaid"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return loansqlc.LoanTransaction{}, err
	}
	return tx, nil
}

func (s *Service) ListGuarantors(
	ctx context.Context,
	loanID pgtype.UUID,
	cursor *LoanCursor,
	limit int32,
) ([]loansqlc.LoanGuarantor, error) {
	params := loansqlc.ListLoanGuarantorsParams{
		LoanID: loanID,
		Limit:  normalizeLimit(limit),
	}
	if cursor != nil {
		params.Column2 = cursor.CreatedAt
		params.GuarantorID = cursor.ID
	}

	guarantors, err := s.store.ListLoanGuarantors(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list guarantors: %w", err)
	}
	return guarantors, nil
}

func (s *Service) ListRepaymentSchedules(ctx context.Context, loanID pgtype.UUID) ([]loansqlc.RepaymentSchedule, error) {
	schedules, err := s.store.ListRepaymentSchedulesByLoan(ctx, loanID)
	if err != nil {
		return nil, fmt.Errorf("list repayment schedules: %w", err)
	}
	return schedules, nil
}

func (s *Service) ListLoanTransactions(
	ctx context.Context,
	loanID pgtype.UUID,
	cursor *LoanCursor,
	limit int32,
) ([]loansqlc.LoanTransaction, error) {
	params := loansqlc.ListLoanTransactionsParams{
		LoanID: loanID,
		Limit:  normalizeLimit(limit),
	}
	if cursor != nil {
		params.Column2 = cursor.CreatedAt
		params.ID = cursor.ID
	}

	transactions, err := s.store.ListLoanTransactions(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list loan transactions: %w", err)
	}
	return transactions, nil
}

func (s *Service) ListCreditBalances(ctx context.Context, memberID pgtype.UUID, limit int32) ([]loansqlc.CreditBalance, error) {
	credits, err := s.store.ListCreditBalancesByMember(ctx, loansqlc.ListCreditBalancesByMemberParams{
		MemberID: memberID,
		Limit:    normalizeLimit(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list credit balances: %w", err)
	}
	return credits, nil
}

type repaymentAllocation struct {
	Principal string `json:"principal"`
	Interest  string `json:"interest"`
	Penalty   string `json:"penalty"`
	Credit    string `json:"credit"`

	loanAppliedAmount *big.Int
	creditAmount      *big.Int
	loanClosed        bool
}

func allocateRepayment(amount, priorApplied pgtype.Numeric, schedules []loansqlc.RepaymentSchedule) repaymentAllocation {
	payment := numericToScale(amount, -4)
	prior := numericToScale(priorApplied, -4)
	totalDue := sumScheduleAmountDue(schedules)

	outstanding := new(big.Int).Sub(totalDue, prior)
	if outstanding.Sign() < 0 {
		outstanding.SetInt64(0)
	}

	loanApplied := minBigInt(payment, outstanding)
	credit := new(big.Int).Sub(payment, loanApplied)
	totalApplied := new(big.Int).Add(prior, loanApplied)

	return repaymentAllocation{
		Principal:         decimalStringFromScale(loanApplied, -4),
		Interest:          "0",
		Penalty:           "0",
		Credit:            decimalStringFromScale(credit, -4),
		loanAppliedAmount: loanApplied,
		creditAmount:      credit,
		loanClosed:        totalApplied.Cmp(totalDue) >= 0,
	}
}

func updateScheduleStatuses(ctx context.Context, q loansqlc.Querier, schedules []loansqlc.RepaymentSchedule, applied *big.Int) error {
	remaining := new(big.Int).Set(applied)
	for _, schedule := range schedules {
		due := numericToScale(schedule.AmountDue, -4)
		nextStatus := schedule.Status
		switch {
		case remaining.Cmp(due) >= 0:
			nextStatus = loansqlc.RepaymentScheduleStatusPaid
			remaining.Sub(remaining, due)
		case remaining.Sign() > 0:
			nextStatus = loansqlc.RepaymentScheduleStatusPartial
			remaining.SetInt64(0)
		}

		if nextStatus == schedule.Status {
			continue
		}
		if _, err := q.UpdateRepaymentScheduleStatus(ctx, loansqlc.UpdateRepaymentScheduleStatusParams{
			ID:     schedule.ID,
			Status: nextStatus,
		}); err != nil {
			return fmt.Errorf("update repayment schedule status: %w", err)
		}
	}
	return nil
}

func sumScheduleAmountDue(schedules []loansqlc.RepaymentSchedule) *big.Int {
	total := new(big.Int)
	for _, schedule := range schedules {
		total.Add(total, numericToScale(schedule.AmountDue, -4))
	}
	return total
}

func verifyApprovedGuarantees(ctx context.Context, q loansqlc.Querier, loanID pgtype.UUID, principal pgtype.Numeric) error {
	count, err := q.CountApprovedGuarantors(ctx, loanID)
	if err != nil {
		return fmt.Errorf("count approved guarantors: %w", err)
	}
	if count < 1 {
		return ErrInsufficientGuarantors
	}

	guarantors, err := q.ListLoanGuarantors(ctx, loansqlc.ListLoanGuarantorsParams{
		LoanID: loanID,
		Limit:  maxGuarantors,
	})
	if err != nil {
		return fmt.Errorf("list guarantors: %w", err)
	}

	total := new(big.Int)
	for _, guarantor := range guarantors {
		if guarantor.Status != loansqlc.GuarantorStatusApproved {
			continue
		}
		total.Add(total, numericToScale(guarantor.GuaranteedAmount, -4))
	}
	if total.Cmp(numericToScale(principal, -4)) < 0 {
		return ErrInsufficientGuarantee
	}
	return nil
}

func insertStatusAudit(
	ctx context.Context,
	q loansqlc.Querier,
	loanID pgtype.UUID,
	previous, next loansqlc.LoanStatus,
	changedBy pgtype.UUID,
	reason string,
) error {
	_, err := q.InsertLoanAuditTrail(ctx, loansqlc.InsertLoanAuditTrailParams{
		LoanID:        loanID,
		FieldChanged:  "status",
		PreviousValue: text(string(previous)),
		NewValue:      string(next),
		ChangedBy:     changedBy,
		ChangeReason:  reason,
	})
	if err != nil {
		return fmt.Errorf("insert audit trail: %w", err)
	}
	return nil
}

func markRowFromLock(loan loansqlc.LockLoanByIDRow) loansqlc.MarkLoanDisbursedRow {
	return loansqlc.MarkLoanDisbursedRow(loan)
}

func normalizeLimit(limit int32) int32 {
	if limit <= 0 || limit > 100 {
		return 50
	}
	return limit
}

// requireActiveMember keeps member-service errors behind loan-domain errors so
// callers know whether the applicant or guarantor failed the eligibility gate.
func (s *Service) requireActiveMember(ctx context.Context, memberID pgtype.UUID, inactiveErr error) error {
	if s.memberService == nil {
		return inactiveErr
	}
	if err := s.memberService.RequireActiveMember(ctx, memberID); err != nil {
		return inactiveErr
	}
	return nil
}

func positive(n pgtype.Numeric) bool {
	return n.Valid && n.Int != nil && n.Int.Sign() > 0
}

func nonNegative(n pgtype.Numeric) bool {
	return n.Valid && n.Int != nil && n.Int.Sign() >= 0
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

func numericFromScale(n *big.Int) pgtype.Numeric {
	return pgtype.Numeric{Int: new(big.Int).Set(n), Exp: -4, Valid: true}
}

func minBigInt(a, b *big.Int) *big.Int {
	if a.Cmp(b) < 0 {
		return new(big.Int).Set(a)
	}
	return new(big.Int).Set(b)
}

func decimalStringFromScale(n *big.Int, scale int32) string {
	if n.Sign() == 0 {
		return "0"
	}
	if scale >= 0 {
		out := new(big.Int).Set(n)
		out.Mul(out, pow10(scale))
		return out.String()
	}

	digits := new(big.Int).Abs(n).String()
	places := int(-scale)
	for len(digits) <= places {
		digits = "0" + digits
	}

	whole := digits[:len(digits)-places]
	frac := strings.TrimRight(digits[len(digits)-places:], "0")
	if frac == "" {
		if n.Sign() < 0 {
			return "-" + whole
		}
		return whole
	}
	if n.Sign() < 0 {
		return "-" + whole + "." + frac
	}
	return whole + "." + frac
}

func pow10(exp int32) *big.Int {
	result := big.NewInt(1)
	ten := big.NewInt(10)
	for i := int32(0); i < exp; i++ {
		result.Mul(result, ten)
	}
	return result
}

func text(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

func sameUUID(a, b pgtype.UUID) bool {
	return a.Valid && b.Valid && a.Bytes == b.Bytes
}

func newUUID() (pgtype.UUID, error) {
	var id pgtype.UUID
	_, err := rand.Read(id.Bytes[:])
	if err != nil {
		return pgtype.UUID{}, err
	}

	id.Bytes[6] = (id.Bytes[6] & 0x0f) | 0x40
	id.Bytes[8] = (id.Bytes[8] & 0x3f) | 0x80
	id.Valid = true
	return id, nil
}
