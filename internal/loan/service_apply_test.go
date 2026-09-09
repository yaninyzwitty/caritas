package loan

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	loansqlc "github.com/yaninyzwitty/caritas-backend/internal/loan/repository/sqlc"
	"github.com/yaninyzwitty/caritas-backend/internal/member"
	"github.com/yaninyzwitty/caritas-backend/internal/share"
	sharesqlc "github.com/yaninyzwitty/caritas-backend/internal/share/repository/sqlc"
)

func TestApplyForLoanRequiresActiveApplicant(t *testing.T) {
	ctx := context.Background()
	service, pool := applyLoanService(t, ctx)
	applicantID := createLoanMember(t, ctx, pool, 101, "pending")
	guarantorID := createLoanMember(t, ctx, pool, 102, "active")

	_, err := service.ApplyForLoan(context.Background(), applyParams(t, applicantID), mustNumeric(t, "0"), []ProposedGuarantor{
		{GuarantorID: guarantorID, GuaranteedAmount: mustNumeric(t, "500")},
	})
	if !errors.Is(err, ErrMemberNotActive) {
		t.Fatalf("expected ErrMemberNotActive, got %v", err)
	}
}

func TestApplyForLoanRequiresActiveGuarantors(t *testing.T) {
	ctx := context.Background()
	service, pool := applyLoanService(t, ctx)
	applicantID := createLoanMember(t, ctx, pool, 103, "active")
	guarantorID := createLoanMember(t, ctx, pool, 104, "pending")

	_, err := service.ApplyForLoan(context.Background(), applyParams(t, applicantID), mustNumeric(t, "0"), []ProposedGuarantor{
		{GuarantorID: guarantorID, GuaranteedAmount: mustNumeric(t, "500")},
	})
	if !errors.Is(err, ErrGuarantorNotActive) {
		t.Fatalf("expected ErrGuarantorNotActive, got %v", err)
	}
}

func TestApplyForLoanStoresProposedGuarantorAmounts(t *testing.T) {
	ctx := context.Background()
	service, pool := applyLoanService(t, ctx)
	applicantID := createLoanMember(t, ctx, pool, 105, "active")
	firstGuarantorID := createLoanMember(t, ctx, pool, 106, "active")
	secondGuarantorID := createLoanMember(t, ctx, pool, 107, "active")

	loan, err := service.ApplyForLoan(ctx, applyParams(t, applicantID), mustNumeric(t, "0"), []ProposedGuarantor{
		{GuarantorID: firstGuarantorID, GuaranteedAmount: mustNumeric(t, "300")},
		{GuarantorID: secondGuarantorID, GuaranteedAmount: mustNumeric(t, "200")},
	})
	if err != nil {
		t.Fatalf("apply for loan: %v", err)
	}

	guarantors, err := loansqlc.New(pool).ListLoanGuarantors(ctx, loansqlc.ListLoanGuarantorsParams{
		LoanID: loan.ID,
		Limit:  20,
	})
	if err != nil {
		t.Fatalf("list guarantors: %v", err)
	}

	amounts := map[pgtype.UUID]string{}
	for _, guarantor := range guarantors {
		amounts[guarantor.GuarantorID] = numericToString(guarantor.GuaranteedAmount)
	}
	if amounts[firstGuarantorID] != "300" || amounts[secondGuarantorID] != "200" {
		t.Fatalf("guarantor amounts = %v", amounts)
	}
}

func TestApplyForLoanRequiresGuaranteesToCoverPrincipal(t *testing.T) {
	ctx := context.Background()
	service, pool := applyLoanService(t, ctx)
	applicantID := createLoanMember(t, ctx, pool, 108, "active")
	guarantorID := createLoanMember(t, ctx, pool, 109, "active")

	_, err := service.ApplyForLoan(ctx, applyParams(t, applicantID), mustNumeric(t, "0"), []ProposedGuarantor{
		{GuarantorID: guarantorID, GuaranteedAmount: mustNumeric(t, "499")},
	})
	if !errors.Is(err, ErrInsufficientCollateral) {
		t.Fatalf("expected ErrInsufficientCollateral, got %v", err)
	}
}

func TestApplyForLoanAcceptsApplicantShareCollateralWithoutGuarantors(t *testing.T) {
	ctx := context.Background()
	service, pool := applyLoanService(t, ctx)
	applicantID := createLoanMember(t, ctx, pool, 111, "active")

	loan, err := service.ApplyForLoan(ctx, applyParams(t, applicantID), mustNumeric(t, "500"), nil)
	if err != nil {
		t.Fatalf("apply for loan: %v", err)
	}

	pledge, err := sharesqlc.New(pool).GetApplicantSharePledge(ctx, loan.ID)
	if err != nil {
		t.Fatalf("get applicant share pledge: %v", err)
	}
	if numericToString(pledge.PledgedAmount) != "500" || pledge.Status != sharesqlc.SharePledgeStatusPending {
		t.Fatalf("pledge = %+v", pledge)
	}

	approverID := testUUID("00000000-0000-0000-0000-000000000112")
	if _, err := pool.Exec(ctx, `
		INSERT INTO staff_users (id, branch_id, email, password_hash, role, name)
		VALUES ($1, 1, 'approver@example.com', 'test', 'loan_officer', 'Approver')
	`, approverID); err != nil {
		t.Fatalf("insert approver: %v", err)
	}
	if _, err := service.ApproveLoan(ctx, loan.ID, approverID, "shares cover principal"); err != nil {
		t.Fatalf("approve loan: %v", err)
	}
	pledge, err = sharesqlc.New(pool).GetApplicantSharePledge(ctx, loan.ID)
	if err != nil || pledge.Status != sharesqlc.SharePledgeStatusActive {
		t.Fatalf("active pledge = %+v, %v", pledge, err)
	}
	shareService := share.NewService(share.NewStore(pool))
	_, err = shareService.WithdrawShares(ctx, pledge.ShareAccountID, mustNumeric(t, "1"), approverID, approverID, "test withdrawal")
	if !errors.Is(err, share.ErrInsufficientBalance) {
		t.Fatalf("expected pledged shares to block withdrawal, got %v", err)
	}

	if _, err := service.RejectLoan(ctx, loan.ID, approverID, "application withdrawn"); err != nil {
		t.Fatalf("reject loan: %v", err)
	}
	if _, err := shareService.WithdrawShares(ctx, pledge.ShareAccountID, mustNumeric(t, "1"), applicantID, applicantID, "released collateral"); err != nil {
		t.Fatalf("withdraw released shares: %v", err)
	}
}

func TestApplyForLoanRejectsSharesAlreadyPledgedToAnActiveLoan(t *testing.T) {
	ctx := context.Background()
	service, pool := applyLoanService(t, ctx)
	applicantID := createLoanMember(t, ctx, pool, 113, "active")
	approverID := testUUID("00000000-0000-0000-0000-000000000114")
	if _, err := pool.Exec(ctx, `
		INSERT INTO staff_users (id, branch_id, email, password_hash, role, name)
		VALUES ($1, 1, 'overpledge-approver@example.com', 'test', 'loan_officer', 'Approver')
	`, approverID); err != nil {
		t.Fatalf("insert approver: %v", err)
	}

	first := applyParams(t, applicantID)
	first.Principal = mustNumeric(t, "400")
	loan, err := service.ApplyForLoan(ctx, first, mustNumeric(t, "400"), nil)
	if err != nil {
		t.Fatalf("apply for first loan: %v", err)
	}
	if _, err := service.ApproveLoan(ctx, loan.ID, approverID, "shares cover principal"); err != nil {
		t.Fatalf("approve first loan: %v", err)
	}
	if _, _, err := service.DisburseLoan(ctx, loan.ID, approverID, "disburse first loan"); err != nil {
		t.Fatalf("disburse first loan: %v", err)
	}

	second := applyParams(t, applicantID)
	second.Principal = mustNumeric(t, "101")
	_, err = service.ApplyForLoan(ctx, second, mustNumeric(t, "101"), nil)
	if !errors.Is(err, ErrInsufficientCollateral) {
		t.Fatalf("expected ErrInsufficientCollateral, got %v", err)
	}
}

func TestAddGuarantorRequiresActiveGuarantor(t *testing.T) {
	ctx := context.Background()
	service, pool := applyLoanService(t, ctx)
	guarantorID := createLoanMember(t, ctx, pool, 110, "pending")

	_, err := service.AddGuarantor(
		ctx,
		testUUID("00000000-0000-0000-0000-000000000105"),
		guarantorID,
		mustNumeric(t, "500"),
	)
	if !errors.Is(err, ErrGuarantorNotActive) {
		t.Fatalf("expected ErrGuarantorNotActive, got %v", err)
	}
}

// applyLoanService uses the real member service so eligibility tests do not
// rely on a fake interface that production code does not need.
func applyLoanService(t *testing.T, ctx context.Context) (*Service, *pgxpool.Pool) {
	t.Helper()
	store, pool := repaymentStore(t, ctx)
	return NewService(store, member.NewService(member.NewStore(pool))), pool
}

// createLoanMember also gives the member the share balance required by every
// loan's 3x eligibility ceiling; without it these tests would fail on collateral
// before reaching the membership or guarantor rule they exercise.
func createLoanMember(t *testing.T, ctx context.Context, pool *pgxpool.Pool, number int64, status string) pgtype.UUID {
	t.Helper()
	id := testUUID(fmt.Sprintf("00000000-0000-0000-0000-%012d", number))
	_, err := pool.Exec(ctx, `
		INSERT INTO members (id, branch_id, member_number, national_id, status)
		VALUES ($1, 1, $2, $3, $4)
	`, id, number, id.String(), status)
	if err != nil {
		t.Fatalf("insert member: %v", err)
	}
	var accountID pgtype.UUID
	err = pool.QueryRow(ctx, `
		INSERT INTO share_accounts (member_id, branch_id, opened_at)
		VALUES ($1, 1, NOW())
		RETURNING id
	`, id).Scan(&accountID)
	if err != nil {
		t.Fatalf("insert share account: %v", err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO share_transactions (share_account_id, type, amount, balance_after, reference_id, originator_id)
		VALUES ($1, 'purchase', 500, 500, $2, $2)
	`, accountID, id)
	if err != nil {
		t.Fatalf("insert share balance: %v", err)
	}
	return id
}

// applyParams keeps each test focused on guarantor rules instead of repeating
// loan fields that are valid but unrelated to the assertion.
func applyParams(t *testing.T, memberID pgtype.UUID) loansqlc.CreateLoanParams {
	t.Helper()
	return loansqlc.CreateLoanParams{
		MemberID:              memberID,
		BranchID:              1,
		Principal:             mustNumeric(t, "500"),
		InterestRate:          mustNumeric(t, "0.01"),
		RepaymentPeriodMonths: 12,
		UpdatedBy:             testUUID("00000000-0000-0000-0000-000000000107"),
	}
}
