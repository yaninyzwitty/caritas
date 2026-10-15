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

// loanSecurityBalances locks the account before reading balances; otherwise a
// withdrawal or another pledge could spend the shares being offered as security.
func loanSecurityBalances(ctx context.Context, q sharesqlc.Querier, memberID pgtype.UUID) (sharesqlc.ShareAccount, *big.Int, *big.Int, error) {
	account, err := q.LockAccountByMemberID(ctx, memberID)
	if errors.Is(err, pgx.ErrNoRows) {
		return account, nil, nil, ErrAccountNotFound
	}
	if err != nil {
		return account, nil, nil, err
	}
	if account.Status != sharesqlc.ShareAccountStatusActive {
		return account, nil, nil, ErrAccountNotActive
	}
	balance := new(big.Int)
	latest, err := q.GetLatestBalance(ctx, account.ID)
	if err == nil {
		balance = numericToNanos(latest)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return account, nil, nil, err
	}
	pledged, err := q.GetActivePledgedAmount(ctx, account.ID)
	return account, balance, numericToNanos(pledged), err
}

// guaranteeAvailable uses unpledged shares for previews and writes, rounded
// down to ledger precision so the displayed allowance cannot overcommit shares.
func guaranteeAvailable(balance, pledged *big.Int) *big.Int {
	available := new(big.Int).Sub(balance, pledged)
	if available.Sign() < 0 {
		available.SetInt64(0)
	}
	return available.Mul(available.Quo(available, big.NewInt(100_000)), big.NewInt(100_000))
}

// CheckGuarantorSecurity checks proposals without reserving shares. Approval
// repeats it under the same account lock because proposals are not reservations.
func CheckGuarantorSecurity(ctx context.Context, q sharesqlc.Querier, memberID pgtype.UUID, amount pgtype.Numeric) (pgtype.UUID, error) {
	account, balance, pledged, err := loanSecurityBalances(ctx, q, memberID)
	if err != nil {
		return pgtype.UUID{}, err
	}
	available := guaranteeAvailable(balance, pledged)
	if numericToNanos(amount).Sign() <= 0 || numericToNanos(amount).Cmp(available) > 0 {
		return account.ID, fmt.Errorf("%w: this guarantor can offer at most KES %s; shares KES %s, active pledges KES %s. Reduce the guaranteed amount or choose another member",
			ErrGuaranteeCapacity,
			new(big.Rat).SetFrac(available, big.NewInt(1_000_000_000)).FloatString(4),
			new(big.Rat).SetFrac(balance, big.NewInt(1_000_000_000)).FloatString(4),
			new(big.Rat).SetFrac(pledged, big.NewInt(1_000_000_000)).FloatString(4))
	}
	return account.ID, nil
}

// ProposeApplicantSecurity owns applicant pledge creation inside the loan's
// transaction; Loans must not independently write share reservations.
func ProposeApplicantSecurity(ctx context.Context, q sharesqlc.Querier, loanID, memberID pgtype.UUID, principal, amount pgtype.Numeric) error {
	account, balance, pledged, err := loanSecurityBalances(ctx, q, memberID)
	if err != nil {
		return err
	}
	if new(big.Int).Mul(new(big.Int).Set(balance), big.NewInt(3)).Cmp(numericToNanos(principal)) < 0 {
		return fmt.Errorf("%w: principal cannot exceed three times the applicant's current shares", ErrLoanSecurity)
	}
	if new(big.Int).Sub(balance, pledged).Cmp(numericToNanos(amount)) < 0 {
		return fmt.Errorf("%w: applicant pledge exceeds shares available after existing pledges", ErrLoanSecurity)
	}
	if numericToNanos(amount).Sign() == 0 {
		return nil
	}
	_, err = q.CreateSharePledge(ctx, sharesqlc.CreateSharePledgeParams{
		ShareAccountID: account.ID, LoanID: loanID, PledgedAmount: amount, Type: sharesqlc.SharePledgeTypeApplicantSecurity,
	})
	return err
}

// ApproveGuarantorSecurity reserves and activates only validated security;
// keeping both writes here prevents an approved guarantee without its pledge.
func ApproveGuarantorSecurity(ctx context.Context, q sharesqlc.Querier, loanID, memberID, approvedBy pgtype.UUID, amount pgtype.Numeric) error {
	accountID, err := CheckGuarantorSecurity(ctx, q, memberID, amount)
	if err != nil {
		return err
	}
	pledge, err := q.CreateSharePledge(ctx, sharesqlc.CreateSharePledgeParams{
		ShareAccountID: accountID, LoanID: loanID, PledgedAmount: amount, Type: sharesqlc.SharePledgeTypeGuarantorSecurity,
	})
	if err != nil {
		return err
	}
	_, err = q.ActivateSharePledge(ctx, sharesqlc.ActivateSharePledgeParams{ID: pledge.ID, ApprovedBy: approvedBy})
	return err
}

// VerifyApplicantSecurity is shared by approval and disbursement. It checks
// share backing, and activates a pending pledge only at approval.
func VerifyApplicantSecurity(ctx context.Context, q sharesqlc.Querier, loanID, memberID, approvedBy pgtype.UUID, principal pgtype.Numeric) (pgtype.Numeric, error) {
	// A stable account lock order prevents loans sharing guarantors from locking
	// their existing collateral accounts in opposite orders.
	if _, err := q.LockLoanShareAccounts(ctx, sharesqlc.LockLoanShareAccountsParams{LoanID: loanID}); err != nil {
		return pgtype.Numeric{}, err
	}
	_, balance, pledged, err := loanSecurityBalances(ctx, q, memberID)
	if err != nil {
		return pgtype.Numeric{}, err
	}
	if new(big.Int).Mul(new(big.Int).Set(balance), big.NewInt(3)).Cmp(numericToNanos(principal)) < 0 || pledged.Cmp(balance) > 0 {
		return pgtype.Numeric{}, fmt.Errorf("%w: applicant's current shares no longer cover the loan or existing pledges", ErrLoanSecurity)
	}
	pledge, err := q.GetApplicantSharePledge(ctx, loanID)
	if errors.Is(err, pgx.ErrNoRows) {
		return pgtype.Numeric{Int: new(big.Int), Valid: true}, nil
	}
	if err != nil {
		return pgtype.Numeric{}, err
	}
	if pledge.Status == sharesqlc.SharePledgeStatusPending && approvedBy.Valid {
		if new(big.Int).Sub(balance, pledged).Cmp(numericToNanos(pledge.PledgedAmount)) < 0 {
			return pgtype.Numeric{}, fmt.Errorf("%w: applicant pledge exceeds currently available shares", ErrLoanSecurity)
		}
		pledge, err = q.ActivateSharePledge(ctx, sharesqlc.ActivateSharePledgeParams{ID: pledge.ID, ApprovedBy: approvedBy})
		if err != nil {
			return pgtype.Numeric{}, err
		}
	}
	if pledge.Status != sharesqlc.SharePledgeStatusActive {
		return pgtype.Numeric{}, fmt.Errorf("%w: applicant pledge is not active", ErrLoanSecurity)
	}
	return pledge.PledgedAmount, nil
}

// VerifyGuarantorSecurity checks the actual active reservation rather than
// trusting the Loans status, including share backing after balance changes.
func VerifyGuarantorSecurity(ctx context.Context, q sharesqlc.Querier, loanID, memberID pgtype.UUID, amount pgtype.Numeric) error {
	account, balance, pledged, err := loanSecurityBalances(ctx, q, memberID)
	if err != nil {
		return err
	}
	pledge, err := q.GetGuarantorSharePledge(ctx, sharesqlc.GetGuarantorSharePledgeParams{ShareAccountID: account.ID, LoanID: loanID})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (pledge.Status != sharesqlc.SharePledgeStatusActive || numericToNanos(pledge.PledgedAmount).Cmp(numericToNanos(amount)) < 0)) {
		return fmt.Errorf("%w: approved guarantor is missing sufficient active share security", ErrLoanSecurity)
	}
	if err != nil {
		return err
	}
	if pledged.Cmp(balance) > 0 {
		return fmt.Errorf("%w: approved guarantor's active pledges exceed their current shares", ErrGuaranteeCapacity)
	}
	return nil
}

// ReleaseLoanSecurity releases either one guarantor's pledge or all loan
// security. Account locks keep release serialized with pledging and withdrawal.
func ReleaseLoanSecurity(ctx context.Context, q sharesqlc.Querier, loanID, memberID pgtype.UUID) error {
	if _, err := q.LockLoanShareAccounts(ctx, sharesqlc.LockLoanShareAccountsParams{LoanID: loanID, MemberID: memberID}); err != nil {
		return err
	}
	return q.ReleaseLoanSharePledges(ctx, sharesqlc.ReleaseLoanSharePledgesParams{LoanID: loanID, MemberID: memberID})
}
