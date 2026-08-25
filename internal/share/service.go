package share

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	sharesqlc "github.com/yaninyzwitty/caritas-backend/internal/share/repository/sqlc"
)

type Service struct {
	store *Store
}

func NewService(store *Store) *Service {
	return &Service{store: store}
}

// OpenShareAccount opens a member's share account. It refuses if the member
// already has one, because GetAccountByMemberID assumes a single account per
// member; a duplicate would later break every by-member lookup. The check-then-
// insert is not wrapped in ExecTx because the write is single-table.
func (s *Service) OpenShareAccount(ctx context.Context, memberID pgtype.UUID, branchID int64) (sharesqlc.ShareAccount, error) {
	if _, err := s.store.GetAccountByMemberID(ctx, memberID); err == nil {
		return sharesqlc.ShareAccount{}, ErrAccountAlreadyExists
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return sharesqlc.ShareAccount{}, fmt.Errorf("check existing account: %w", err)
	}

	account, err := s.store.CreateShareAccount(ctx, sharesqlc.CreateShareAccountParams{
		MemberID: memberID,
		BranchID: branchID,
	})
	if err != nil {
		return sharesqlc.ShareAccount{}, fmt.Errorf("create share account: %w", err)
	}
	return account, nil
}

// postTransaction is the shared ledger writer for purchases and withdrawals.
// It locks the account row (serialising concurrent ops per spec I1), checks the
// reference_id for an existing transaction (spec I4 idempotency — done before
// the balance read so a retried withdrawal does not falsely hit
// insufficient-funds on the already-updated balance), then reads the latest
// balance, applies the signed amount, and inserts the append-only transaction.
// The DB CHECK (balance_after >= 0) backstops I1; the Go-side check gives a
// clean error for withdrawals instead of a constraint violation. Extracting
// this is necessary because purchase and withdrawal share the entire
// lock-check-read-compute-insert sequence; duplicating it would risk divergence.
func (s *Service) postTransaction(
	ctx context.Context,
	accountID pgtype.UUID,
	txType sharesqlc.ShareTransactionType,
	amount pgtype.Numeric,
	referenceID, originatorID pgtype.UUID,
	reason string,
) (sharesqlc.ShareTransaction, error) {
	var result sharesqlc.ShareTransaction
	err := s.store.ExecTx(ctx, func(q sharesqlc.Querier) error {
		account, err := q.LockAndReadAccount(ctx, accountID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrAccountNotFound
			}
			return fmt.Errorf("lock account: %w", err)
		}
		if account.Status != sharesqlc.ShareAccountStatusActive {
			return ErrAccountNotActive
		}

		// Idempotency (spec I4): a retried reference_id returns the original
		// transaction without re-applying the amount. Without this check a
		// retried withdrawal would re-read the reduced balance and fail.

		result, err = q.GetTransactionByReference(ctx, sharesqlc.GetTransactionByReferenceParams{
			ShareAccountID: accountID,
			ReferenceID:    referenceID,
			Type:           txType,
		})

		switch {
		case err == nil:
			return nil // Idempotent retry.

		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("lookup existing transaction: %w", err)
		}

		// Determine the current balance. A missing transaction history implies a
		// newly created account with a zero balance.
		balanceNanos := new(big.Int)

		latest, err := q.GetLatestBalance(ctx, accountID)
		switch {
		case err == nil:
			balanceNanos = numericToNanos(latest)

		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("read latest balance: %w", err)
		}
		amountNanos := numericToNanos(amount)

		switch txType {
		case sharesqlc.ShareTransactionTypeWithdrawal:
			if balanceNanos.Cmp(amountNanos) < 0 {
				return ErrInsufficientBalance
			}
			balanceNanos.Sub(balanceNanos, amountNanos)

		default:
			balanceNanos.Add(balanceNanos, amountNanos)
		}

		result, err = q.InsertShareTransaction(ctx, sharesqlc.InsertShareTransactionParams{
			ShareAccountID: accountID,
			Type:           txType,
			Amount:         amount,
			BalanceAfter:   pgtype.Numeric{Int: balanceNanos, Exp: -9, Valid: true},
			ReferenceID:    referenceID,
			Reason:         pgtype.Text{String: reason, Valid: reason != ""},
			OriginatorID:   originatorID,
		})

		switch {
		case err == nil:
			return nil

		case errors.Is(err, pgx.ErrNoRows):
			// Lost the race to another transaction with the same reference ID.
			// Return the transaction that won.
			result, err = q.GetTransactionByReference(ctx, sharesqlc.GetTransactionByReferenceParams{
				ShareAccountID: accountID,
				ReferenceID:    referenceID,
				Type:           txType,
			})
			if err != nil {
				return fmt.Errorf("read existing transaction: %w", err)
			}
			return nil

		default:
			return fmt.Errorf("insert transaction: %w", err)
		}
	})
	if err != nil {
		return sharesqlc.ShareTransaction{}, err
	}
	return result, nil
}

// PurchaseShares credits a share account. Without it the PurchaseShares RPC has
// no implementation and the SACCO cannot receive capital inflow.
func (s *Service) PurchaseShares(
	ctx context.Context,
	accountID pgtype.UUID,
	amount pgtype.Numeric,
	referenceID, originatorID pgtype.UUID,
	reason string,
) (sharesqlc.ShareTransaction, error) {
	return s.postTransaction(ctx, accountID, sharesqlc.ShareTransactionTypePurchase, amount, referenceID, originatorID, reason)
}

// WithdrawShares debits a share account, refusing to overdraw. Without it the
// WithdrawShares RPC has no implementation and members cannot exit shares.
func (s *Service) WithdrawShares(
	ctx context.Context,
	accountID pgtype.UUID,
	amount pgtype.Numeric,
	referenceID, originatorID pgtype.UUID,
	reason string,
) (sharesqlc.ShareTransaction, error) {
	return s.postTransaction(ctx, accountID, sharesqlc.ShareTransactionTypeWithdrawal, amount, referenceID, originatorID, reason)
}

// CreateAdjustment records a pending manual correction request without changing
// the ledger. Without this separation, an unapproved adjustment would change a
// member's share balance before the audit approval required by the shares spec.
func (s *Service) CreateAdjustment(
	ctx context.Context,
	accountID pgtype.UUID,
	amount pgtype.Numeric,
	referenceID, originatorID pgtype.UUID,
	reason string,
) (sharesqlc.ShareAdjustment, error) {
	account, err := s.store.GetAccountByID(ctx, accountID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sharesqlc.ShareAdjustment{}, ErrAccountNotFound
		}
		return sharesqlc.ShareAdjustment{}, fmt.Errorf("get account: %w", err)
	}
	if account.Status != sharesqlc.ShareAccountStatusActive {
		return sharesqlc.ShareAdjustment{}, ErrAccountNotActive
	}
	if numericToNanos(amount).Sign() == 0 {
		return sharesqlc.ShareAdjustment{}, ErrInsufficientBalance
	}

	adjustment, err := s.store.InsertAdjustment(ctx, sharesqlc.InsertAdjustmentParams{
		ShareAccountID: accountID,
		Amount:         amount,
		ReferenceID:    referenceID,
		RequestedBy:    originatorID,
		Reason:         reason,
	})
	if err == nil {
		return adjustment, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return sharesqlc.ShareAdjustment{}, fmt.Errorf("insert adjustment: %w", err)
	}
	adjustment, err = s.store.GetAdjustmentByReference(ctx, sharesqlc.GetAdjustmentByReferenceParams{
		ShareAccountID: accountID,
		ReferenceID:    referenceID,
	})
	if err != nil {
		return sharesqlc.ShareAdjustment{}, fmt.Errorf("get adjustment by reference: %w", err)
	}
	return adjustment, nil
}

// ApproveShareAdjustment posts a pending adjustment to the append-only ledger
// and records the approver in one transaction. Without it, approval could commit
// without the balance change, or the balance change could commit without audit.
func (s *Service) ApproveShareAdjustment(
	ctx context.Context,
	adjustmentID, approverID pgtype.UUID,
	reason string,
	auditReportID pgtype.UUID,
) (sharesqlc.ShareAdjustment, error) {
	var result sharesqlc.ShareAdjustment
	err := s.store.ExecTx(ctx, func(q sharesqlc.Querier) error {
		adjustment, err := q.LockAdjustmentByID(ctx, adjustmentID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrAdjustmentNotFound
			}
			return fmt.Errorf("lock adjustment: %w", err)
		}
		if adjustment.Status == sharesqlc.ShareAdjustmentStatusApproved {
			result = adjustment
			return nil
		}
		if adjustment.Status != sharesqlc.ShareAdjustmentStatusPending {
			return ErrAdjustmentNotPending
		}

		account, err := q.LockAndReadAccount(ctx, adjustment.ShareAccountID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrAccountNotFound
			}
			return fmt.Errorf("lock account: %w", err)
		}
		if account.Status != sharesqlc.ShareAccountStatusActive {
			return ErrAccountNotActive
		}

		balanceNanos := new(big.Int)
		latest, err := q.GetLatestBalance(ctx, adjustment.ShareAccountID)
		switch {
		case err == nil:
			balanceNanos = numericToNanos(latest)
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("read latest balance: %w", err)
		}

		newBalance := new(big.Int).Set(balanceNanos)
		newBalance.Add(newBalance, numericToNanos(adjustment.Amount))
		if newBalance.Sign() < 0 {
			return ErrInsufficientBalance
		}
		if reason == "" {
			reason = adjustment.Reason
		}

		tx, err := q.InsertShareTransaction(ctx, sharesqlc.InsertShareTransactionParams{
			ShareAccountID: adjustment.ShareAccountID,
			Type:           sharesqlc.ShareTransactionTypeAdjustment,
			Amount:         adjustment.Amount,
			BalanceAfter:   pgtype.Numeric{Int: newBalance, Exp: -9, Valid: true},
			ReferenceID:    adjustment.ReferenceID,
			Reason:         pgtype.Text{String: reason, Valid: reason != ""},
			OriginatorID:   adjustment.RequestedBy,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			tx, err = q.GetTransactionByReference(ctx, sharesqlc.GetTransactionByReferenceParams{
				ShareAccountID: adjustment.ShareAccountID,
				ReferenceID:    adjustment.ReferenceID,
				Type:           sharesqlc.ShareTransactionTypeAdjustment,
			})
		}
		if err != nil {
			return fmt.Errorf("insert adjustment transaction: %w", err)
		}

		result, err = q.UpdateAdjustmentApproved(ctx, sharesqlc.UpdateAdjustmentApprovedParams{
			ID:                 adjustment.ID,
			ShareTransactionID: tx.ID,
			ApproverID:         approverID,
			Reason:             reason,
			AuditReportID:      auditReportID,
		})
		if err != nil {
			return fmt.Errorf("mark adjustment approved: %w", err)
		}
		return nil
	})
	if err != nil {
		return sharesqlc.ShareAdjustment{}, err
	}
	return result, nil
}

// ReverseShareTransaction posts the exact opposite ledger effect for a committed
// transaction. Without it, corrections would need manual adjustments that could
// use the wrong amount or reverse the same transaction more than once.
func (s *Service) ReverseShareTransaction(
	ctx context.Context,
	transactionID, referenceID, originatorID pgtype.UUID,
	reason string,
) (sharesqlc.ShareTransaction, error) {
	var result sharesqlc.ShareTransaction
	err := s.store.ExecTx(ctx, func(q sharesqlc.Querier) error {
		original, err := q.GetTransactionByID(ctx, transactionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTransactionNotFound
			}
			return fmt.Errorf("get transaction: %w", err)
		}
		if original.Type == sharesqlc.ShareTransactionTypeReversal {
			return ErrCannotReverse
		}
		reversals, err := q.GetReversalTransactions(ctx, original.ID)
		if err != nil {
			return fmt.Errorf("get reversals: %w", err)
		}
		if len(reversals) > 0 {
			result = reversals[0]
			return nil
		}

		account, err := q.LockAndReadAccount(ctx, original.ShareAccountID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrAccountNotFound
			}
			return fmt.Errorf("lock account: %w", err)
		}
		if account.Status != sharesqlc.ShareAccountStatusActive {
			return ErrAccountNotActive
		}

		reversalAmount := numericToNanos(original.Amount)
		if original.Type != sharesqlc.ShareTransactionTypeWithdrawal {
			reversalAmount.Neg(reversalAmount)
		}

		balanceNanos := new(big.Int)
		latest, err := q.GetLatestBalance(ctx, original.ShareAccountID)
		switch {
		case err == nil:
			balanceNanos = numericToNanos(latest)
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("read latest balance: %w", err)
		}

		newBalance := new(big.Int).Set(balanceNanos)
		newBalance.Add(newBalance, reversalAmount)
		if newBalance.Sign() < 0 {
			return ErrInsufficientBalance
		}

		result, err = q.InsertShareTransaction(ctx, sharesqlc.InsertShareTransactionParams{
			ShareAccountID: original.ShareAccountID,
			Type:           sharesqlc.ShareTransactionTypeReversal,
			Amount:         pgtype.Numeric{Int: reversalAmount, Exp: -9, Valid: true},
			BalanceAfter:   pgtype.Numeric{Int: newBalance, Exp: -9, Valid: true},
			ReferenceID:    referenceID,
			ReversalOf:     original.ID,
			Reason:         pgtype.Text{String: reason, Valid: reason != ""},
			OriginatorID:   originatorID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			result, err = q.GetTransactionByReference(ctx, sharesqlc.GetTransactionByReferenceParams{
				ShareAccountID: original.ShareAccountID,
				ReferenceID:    referenceID,
				Type:           sharesqlc.ShareTransactionTypeReversal,
			})
		}
		if err != nil {
			return fmt.Errorf("insert reversal transaction: %w", err)
		}
		return nil
	})
	if err != nil {
		return sharesqlc.ShareTransaction{}, err
	}
	return result, nil
}
