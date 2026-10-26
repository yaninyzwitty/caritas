package contribution

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	contributionsqlc "github.com/yaninyzwitty/caritas-backend/internal/contribution/repository/sqlc"
	loansqlc "github.com/yaninyzwitty/caritas-backend/internal/loan/repository/sqlc"
	"github.com/yaninyzwitty/caritas-backend/internal/member"
	sharesqlc "github.com/yaninyzwitty/caritas-backend/internal/share/repository/sqlc"
)

func (s *Service) OpenCashierSession(ctx context.Context, branchID int64, cashierID pgtype.UUID) (contributionsqlc.CashierSession, error) {
	session, err := s.store.InsertCashierSession(ctx, contributionsqlc.InsertCashierSessionParams{
		BranchID:  branchID,
		CashierID: cashierID,
	})
	if err == nil {
		return session, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return contributionsqlc.CashierSession{}, fmt.Errorf("open cashier session: %w", err)
	}
	session, err = s.store.GetOpenCashierSession(ctx, cashierID)
	if err != nil {
		return contributionsqlc.CashierSession{}, fmt.Errorf("get open cashier session: %w", err)
	}
	return session, nil
}

func (s *Service) CreateCashReceipt(ctx context.Context, params contributionsqlc.InsertContributionReceiptParams, allocations []AllocationInput) (CreatedReceipt, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	params.SourceChannel = contributionsqlc.ContributionSourceChannelCash
	params.IdempotencyKey = text(strings.TrimSpace(params.IdempotencyKey.String))
	params.InternalReceiptReference = text("CASH-" + uuid.NewString())
	params.ReceivedAt = pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}

	var result CreatedReceipt
	err := s.store.ExecReceiptTx(ctx, func(tx pgx.Tx) error {
		q := contributionsqlc.New(tx)
		session, err := q.LockCashierSession(ctx, params.CashierSessionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCashierSessionNotFound
		}
		if err != nil {
			return fmt.Errorf("lock cashier session: %w", err)
		}

		existing, err := q.GetContributionReceiptByIdempotencyKey(ctx, params.IdempotencyKey)
		if err == nil {
			allocations, err = allocationsForRetry(allocations, existing.AllocationPlan)
			if err != nil {
				return err
			}
			rows, err := q.ListContributionAllocationsByReceipt(ctx, existing.ID)
			if err != nil {
				return fmt.Errorf("list existing cash allocations: %w", err)
			}
			if existing.SourceChannel != contributionsqlc.ContributionSourceChannelCash ||
				existing.MemberID != params.MemberID || existing.BranchID != params.BranchID ||
				existing.CashierSessionID != params.CashierSessionID ||
				existing.ContributionPeriod != params.ContributionPeriod ||
				numericToScale(existing.ReceivedAmount, -4).Cmp(numericToScale(params.ReceivedAmount, -4)) != 0 {
				return ErrInconsistentReceipt
			}
			if err := verifyAllocationsMatch(allocations, rows); err != nil {
				return err
			}
			result = CreatedReceipt{Receipt: existing, Allocations: rows}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("get cash receipt by idempotency key: %w", err)
		}

		if session.Status != contributionsqlc.CashierSessionStatusOpen || session.CashierID != params.ReceivedBy || session.BranchID != params.BranchID {
			return ErrCashierSessionState
		}
		if err := member.RequireActiveMemberInTx(ctx, tx, params.MemberID, params.BranchID); err != nil {
			return err
		}
		fees, err := monthlyFees(ctx, q, params.MemberID, params.ContributionPeriod, pgtype.UUID{})
		if err != nil {
			return err
		}
		allocations, err = calculatedAllocations(allocations, fees)
		if err != nil {
			return err
		}
		if err := validateChargeAllocations(ctx, q, params.MemberID, params.BranchID, allocations, pgtype.UUID{}); err != nil {
			return err
		}
		// Snapshot only cash receipt wording; it must not affect STK retry comparisons.
		var chargeIDs []pgtype.UUID
		for _, item := range allocations {
			if item.Type == contributionsqlc.ContributionAllocationTypeOtherCharge || item.Type == contributionsqlc.ContributionAllocationTypePenalty {
				chargeIDs = append(chargeIDs, item.TargetID)
			}
		}
		if len(chargeIDs) > 0 {
			charges, err := q.LockContributionCharges(ctx, chargeIDs)
			if err != nil {
				return fmt.Errorf("snapshot cash receipt charges: %w", err)
			}
			for _, charge := range charges {
				label := map[string]string{
					"literature": "Literature", "caritas_registration": "Caritas registration",
					"lsf": "LSF", "laptop": "Laptop", "penalty": "Non-loan penalty", "other": "Other charge",
				}[charge.Category]
				for i := range allocations {
					if allocations[i].TargetID == charge.ID {
						allocations[i].DisplayLabel = label + " · " + charge.Reason
					}
				}
			}
		}
		params.AllocationPlan, err = buildAllocationPlan(allocations)
		if err != nil {
			return err
		}
		if err := validateReceipt(params, allocations); err != nil {
			return err
		}
		// Lock loan targets before shares, matching receipt processing and loan collateral operations.
		if err := validateContributionLoanMinimums(ctx, loansqlc.New(tx), params.MemberID, params.BranchID, params.ContributionPeriod, allocations); err != nil {
			return err
		}
		targets := append([]AllocationInput(nil), allocations...)
		sort.Slice(targets, func(i, j int) bool {
			if targets[i].Type != targets[j].Type {
				return targets[i].Type < targets[j].Type
			}
			return targets[i].TargetID.String() < targets[j].TargetID.String()
		})
		for _, target := range targets {
			switch target.Type {
			case contributionsqlc.ContributionAllocationTypeSharePurchase:
				if s.shareService == nil {
					return ErrOwningServiceMissing
				}
				err = s.shareService.ValidateContributionTarget(ctx, sharesqlc.New(tx), target.TargetID, params.MemberID, params.BranchID)
			case contributionsqlc.ContributionAllocationTypeLoanPrincipal:
				if s.loanService == nil {
					return ErrOwningServiceMissing
				}
				err = s.loanService.ValidateContributionTarget(ctx, loansqlc.New(tx), target.TargetID, params.MemberID, params.BranchID)
			}
			if err != nil {
				return err
			}
		}
		result, err = s.createReceipt(ctx, q, params, allocations)
		return err

	})
	if err != nil {
		return CreatedReceipt{}, err
	}

	processed, processErr := s.ProcessReceipt(ctx, result.Receipt.ID, params.ReceivedBy)
	if processErr != nil && processed.Receipt.ID.Valid {
		return processed, nil
	}
	if processErr != nil {
		// Cash acceptance already committed; recovery will retry its pending allocation plan.
		slog.Warn("cash receipt processing deferred", "receipt_id", result.Receipt.ID.String(), "error", processErr)
		return result, nil
	}
	return processed, nil
}

func (s *Service) CloseCashierSession(ctx context.Context, id, cashierID pgtype.UUID, counted pgtype.Numeric, reason string) (contributionsqlc.CashierSession, error) {
	if !counted.Valid || counted.Int == nil || counted.Int.Sign() < 0 {
		return contributionsqlc.CashierSession{}, ErrCashDepositInvalid
	}
	var result contributionsqlc.CashierSession
	err := s.store.ExecTx(ctx, func(q contributionsqlc.Querier) error {
		session, err := q.LockCashierSession(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCashierSessionNotFound
		}
		if err != nil {
			return fmt.Errorf("lock cashier session: %w", err)
		}
		if session.CashierID != cashierID {
			return ErrCashierSessionState
		}
		if session.Status != contributionsqlc.CashierSessionStatusOpen {
			if session.CountedAmount.Valid && numericToScale(session.CountedAmount, -4).Cmp(numericToScale(counted, -4)) == 0 {
				result = session
				return nil
			}
			return ErrCashierSessionState
		}
		expected, err := q.SumCashReceiptsBySession(ctx, id)
		if err != nil {
			return fmt.Errorf("sum cash receipts: %w", err)
		}

		variance := numericToScale(counted, -4)
		variance.Sub(variance, numericToScale(expected, -4))
		if variance.Sign() != 0 && strings.TrimSpace(reason) == "" {
			return ErrCashVarianceReason
		}

		result, err = q.CloseCashierSession(ctx, contributionsqlc.CloseCashierSessionParams{
			ID:             id,
			ExpectedAmount: expected,
			CountedAmount:  counted,
			VarianceReason: text(strings.TrimSpace(reason)),
			ClosedBy:       cashierID,
		})
		return err
	})
	return result, err
}

func (s *Service) AcceptCashHandover(ctx context.Context, id, acceptedBy pgtype.UUID, branchID int64) (contributionsqlc.CashierSession, error) {
	var result contributionsqlc.CashierSession
	err := s.store.ExecTx(ctx, func(q contributionsqlc.Querier) error {
		session, err := q.LockCashierSession(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCashierSessionNotFound
		}
		if err != nil {
			return fmt.Errorf("lock cashier session: %w", err)
		}
		if session.BranchID != branchID {
			return ErrCashierSessionState
		}
		if session.CashierID == acceptedBy {
			return ErrCashSeparationOfDuties
		}
		if session.Status == contributionsqlc.CashierSessionStatusHandedOver || session.Status == contributionsqlc.CashierSessionStatusDeposited {
			result = session
			return nil
		}
		if session.Status != contributionsqlc.CashierSessionStatusClosed {
			return ErrCashierSessionState
		}
		result, err = q.AcceptCashHandover(ctx, contributionsqlc.AcceptCashHandoverParams{ID: id, HandedOverTo: acceptedBy})
		return err
	})
	return result, err
}

func (s *Service) RecordCashDeposit(ctx context.Context, sessionIDs []pgtype.UUID, amount pgtype.Numeric, bankReference string, branchID int64, recordedBy pgtype.UUID) (contributionsqlc.CashDeposit, error) {
	bankReference = strings.TrimSpace(bankReference)
	if len(sessionIDs) == 0 || !positive(amount) || bankReference == "" {
		return contributionsqlc.CashDeposit{}, ErrCashDepositInvalid
	}
	seen := make(map[pgtype.UUID]struct{}, len(sessionIDs))
	for _, id := range sessionIDs {
		if !id.Valid {
			return contributionsqlc.CashDeposit{}, ErrCashDepositInvalid
		}
		if _, ok := seen[id]; ok {
			return contributionsqlc.CashDeposit{}, ErrCashDepositInvalid
		}
		seen[id] = struct{}{}
	}

	var result contributionsqlc.CashDeposit
	err := s.store.ExecTx(ctx, func(q contributionsqlc.Querier) error {
		existing, err := q.GetCashDepositByBankReference(ctx, bankReference)
		if err == nil {
			linked, err := q.ListCashDepositSessions(ctx, existing.ID)
			if err != nil {
				return fmt.Errorf("list deposited sessions: %w", err)
			}
			if existing.BranchID != branchID || numericToScale(existing.Amount, -4).Cmp(numericToScale(amount, -4)) != 0 || len(linked) != len(seen) {
				return ErrCashDepositInvalid
			}
			for _, id := range linked {
				if _, ok := seen[id]; !ok {
					return ErrCashDepositInvalid
				}
			}
			result = existing
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("get cash deposit: %w", err)
		}

		sessions, err := q.LockCashierSessions(ctx, sessionIDs)
		if err != nil {
			return fmt.Errorf("lock cashier sessions: %w", err)
		}
		if len(sessions) != len(sessionIDs) {
			return ErrCashierSessionNotFound
		}
		total := new(big.Int)
		for _, session := range sessions {
			if session.BranchID != branchID || session.Status != contributionsqlc.CashierSessionStatusHandedOver || !session.CountedAmount.Valid {
				return ErrCashierSessionState
			}
			total.Add(total, numericToScale(session.CountedAmount, -4))
		}
		if total.Cmp(numericToScale(amount, -4)) != 0 {
			return ErrCashDepositInvalid
		}

		result, err = q.InsertCashDeposit(ctx, contributionsqlc.InsertCashDepositParams{
			BranchID:      branchID,
			Amount:        amount,
			BankReference: bankReference,
			RecordedBy:    recordedBy,
		})
		if err != nil {
			return fmt.Errorf("insert cash deposit: %w", err)
		}
		for _, session := range sessions {
			if _, err := q.InsertCashDepositSession(ctx, contributionsqlc.InsertCashDepositSessionParams{DepositID: result.ID, SessionID: session.ID}); err != nil {
				return fmt.Errorf("link cash deposit session: %w", err)
			}
			if _, err := q.MarkCashierSessionDeposited(ctx, session.ID); err != nil {
				return fmt.Errorf("mark cashier session deposited: %w", err)
			}
		}
		return nil
	})
	return result, err
}

func (s *Service) VerifyCashDeposit(ctx context.Context, id, verifiedBy pgtype.UUID, branchID int64) (contributionsqlc.CashDeposit, error) {
	deposit, err := s.store.GetCashDepositByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return contributionsqlc.CashDeposit{}, ErrCashDepositInvalid
	}
	if err != nil {
		return contributionsqlc.CashDeposit{}, fmt.Errorf("get cash deposit: %w", err)
	}
	if deposit.BranchID != branchID {
		return contributionsqlc.CashDeposit{}, ErrCashDepositInvalid
	}
	if deposit.Status == contributionsqlc.CashDepositStatusVerified {
		return deposit, nil
	}
	if deposit.RecordedBy == verifiedBy {
		return contributionsqlc.CashDeposit{}, ErrCashDepositSelfVerify
	}
	deposit, err = s.store.VerifyCashDeposit(ctx, contributionsqlc.VerifyCashDepositParams{ID: id, VerifiedBy: verifiedBy})
	if errors.Is(err, pgx.ErrNoRows) {
		deposit, err = s.store.GetCashDepositByID(ctx, id)
		if err == nil && deposit.Status == contributionsqlc.CashDepositStatusVerified {
			return deposit, nil
		}
	}
	if err != nil {
		return contributionsqlc.CashDeposit{}, fmt.Errorf("verify cash deposit: %w", err)
	}
	return deposit, nil
}
