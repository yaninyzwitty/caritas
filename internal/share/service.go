package share

// TODO-APPLY THE GOOSE MIGRATIONS

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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

		slog.Info("account, ", "value", account.ID, "member_id", account.MemberID)

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

// ApproveShareAdjustment records the audit approval for an existing adjustment
// transaction (spec I6). The adjustment transaction must already exist with type
// 'adjustment'; this only writes the share_adjustments audit row with the
// approver and audit-report reference. Without it, manual corrections would
// carry no approver/audit trail and could not be distinguished from fraud.
// Single-table write, so no ExecTx (see AGENTS.md: ExecTx is for multi-table
// writes); the at-most-once check guards sequential retries.
func (s *Service) ApproveShareAdjustment(
	ctx context.Context,
	transactionID, approverID pgtype.UUID,
	reason string,
	auditReportID pgtype.UUID,
) (sharesqlc.ShareAdjustment, error) {
	tx, err := s.store.GetTransactionByID(ctx, transactionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sharesqlc.ShareAdjustment{}, ErrTransactionNotFound
		}
		return sharesqlc.ShareAdjustment{}, fmt.Errorf("get transaction: %w", err)
	}
	if tx.Type != sharesqlc.ShareTransactionTypeAdjustment {
		return sharesqlc.ShareAdjustment{}, ErrNotAdjustment
	}
	// Idempotency: an adjustment may be approved at most once. Without this
	// check a retried approval would create duplicate audit rows.
	if _, err := s.store.GetAdjustmentByTransactionID(ctx, transactionID); err == nil {
		return sharesqlc.ShareAdjustment{}, ErrAdjustmentExists
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return sharesqlc.ShareAdjustment{}, fmt.Errorf("check existing adjustment: %w", err)
	}

	adjustment, err := s.store.InsertAdjustment(ctx, sharesqlc.InsertAdjustmentParams{
		ShareTransactionID: transactionID,
		ApproverID:         approverID,
		Reason:             reason,
		AuditReportID:      auditReportID,
	})
	if err != nil {
		return sharesqlc.ShareAdjustment{}, fmt.Errorf("insert adjustment: %w", err)
	}
	return adjustment, nil
}
