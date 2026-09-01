package contribution

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	contributionsqlc "github.com/yaninyzwitty/caritas-backend/internal/contribution/repository/sqlc"
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
	params.SourceChannel = contributionsqlc.ContributionSourceChannelCash
	params.IdempotencyKey = text(strings.TrimSpace(params.IdempotencyKey.String))
	params.InternalReceiptReference = text("CASH-" + uuid.NewString())
	params.ReceivedAt = pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}

	plan, err := buildAllocationPlan(allocations)
	if err != nil {
		return CreatedReceipt{}, err
	}
	params.AllocationPlan = plan
	if err := validateReceipt(params, allocations); err != nil {
		return CreatedReceipt{}, err
	}

	var result CreatedReceipt
	err = s.store.ExecTx(ctx, func(q contributionsqlc.Querier) error {
		existing, err := q.GetContributionReceiptByIdempotencyKey(ctx, params.IdempotencyKey)
		if err == nil {
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

		session, err := q.LockCashierSession(ctx, params.CashierSessionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCashierSessionNotFound
		}
		if err != nil {
			return fmt.Errorf("lock cashier session: %w", err)
		}
		if session.Status != contributionsqlc.CashierSessionStatusOpen || session.CashierID != params.ReceivedBy || session.BranchID != params.BranchID {
			return ErrCashierSessionState
		}
		result, err = s.createReceipt(ctx, q, params, allocations)
		return err

	})
	if err != nil {
		return CreatedReceipt{}, err
	}

	slog.Info("receiptID", "val", result.Receipt.ID, "receivedBy", params.ReceivedBy)

	processed, processErr := s.ProcessReceipt(ctx, result.Receipt.ID, params.ReceivedBy)
	if processErr != nil && processed.Receipt.ID.Valid {
		return processed, nil
	}
	return processed, processErr
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
