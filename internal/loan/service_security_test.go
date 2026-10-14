package loan

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	loansqlc "github.com/yaninyzwitty/caritas-backend/internal/loan/repository/sqlc"
	"github.com/yaninyzwitty/caritas-backend/internal/share"
	sharesqlc "github.com/yaninyzwitty/caritas-backend/internal/share/repository/sqlc"
)

// TestGuarantorSecurityLifecycle exercises reservations across decisions; merely
// checking status would miss double pledging, rollback and stranded collateral.
func TestGuarantorSecurityLifecycle(t *testing.T) {
	ctx := context.Background()
	service, pool := applyLoanService(t, ctx)
	applicantID := createLoanMember(t, ctx, pool, 301, "active")
	guarantorID := createLoanMember(t, ctx, pool, 302, "active")
	extraID := createLoanMember(t, ctx, pool, 303, "active")
	actorID := testUUID("00000000-0000-0000-0000-000000000304")
	if _, err := pool.Exec(ctx, `INSERT INTO staff_users (id, branch_id, email, password_hash, role, name)
		VALUES ($1, 1, 'guarantee-test@example.com', 'test', 'loan_officer', 'Approver')`, actorID); err != nil {
		t.Fatal(err)
	}
	params := applyParams(t, applicantID)
	params.Principal = mustNumeric(t, "200")
	params.InterestRate = mustNumeric(t, "0")
	if _, err := service.ApplyForLoan(ctx, params, mustNumeric(t, "100"), []ProposedGuarantor{{GuarantorID: guarantorID, GuaranteedAmount: mustNumeric(t, "151")}}); !errors.Is(err, share.ErrGuaranteeCapacity) {
		t.Fatalf("application above 30%%: %v", err)
	}
	loan, err := service.ApplyForLoan(ctx, params, mustNumeric(t, "100"), []ProposedGuarantor{{GuarantorID: guarantorID, GuaranteedAmount: mustNumeric(t, "100")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddGuarantor(ctx, loan.ID, extraID, mustNumeric(t, "151")); !errors.Is(err, share.ErrGuaranteeCapacity) {
		t.Fatalf("addition above 30%%: %v", err)
	}
	if _, err := service.AddGuarantor(ctx, loan.ID, extraID, mustNumeric(t, "50")); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApproveLoan(ctx, loan.ID, actorID, "reviewed"); !errors.Is(err, ErrInsufficientCollateral) {
		t.Fatalf("pending guarantees counted: %v", err)
	}
	shares := sharesqlc.New(pool)
	applicantPledge, err := shares.GetApplicantSharePledge(ctx, loan.ID)
	if err != nil || applicantPledge.Status != sharesqlc.SharePledgeStatusPending {
		t.Fatalf("failed approval did not roll back applicant pledge: %+v, %v", applicantPledge, err)
	}
	for range 2 {
		approved, err := service.ApproveGuarantor(ctx, loan.ID, guarantorID, actorID)
		if err != nil || approved.Status != loansqlc.GuarantorStatusApproved {
			t.Fatalf("approval or retry: %+v, %v", approved, err)
		}
	}
	account, err := shares.GetAccountByMemberID(ctx, guarantorID)
	if err != nil {
		t.Fatal(err)
	}
	pledged, err := shares.GetActivePledgedAmount(ctx, account.ID)
	if err != nil || numericToString(pledged) != "100" {
		t.Fatalf("retry reserved more than once: %s, %v", numericToString(pledged), err)
	}
	if _, err := service.ApproveLoan(ctx, loan.ID, actorID, "security sufficient"); err != nil {
		t.Fatalf("extra pending guarantee blocked approval: %v", err)
	}
	if _, err := service.ApproveGuarantor(ctx, loan.ID, extraID, actorID); !errors.Is(err, ErrInvalidStatusTransition) {
		t.Fatalf("guarantee approved after loan approval: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE members SET status = 'suspended' WHERE id = $1`, guarantorID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.DisburseLoan(ctx, loan.ID, actorID, "verify eligibility"); !errors.Is(err, ErrGuarantorNotActive) {
		t.Fatalf("disbursement ignored suspended guarantor: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE members SET status = 'active' WHERE id = $1`, guarantorID); err != nil {
		t.Fatal(err)
	}
	shareService := share.NewService(share.NewStore(pool))
	if _, err := shareService.WithdrawShares(ctx, account.ID, mustNumeric(t, "200"), testUUID("00000000-0000-0000-0000-000000000305"), actorID, "changed capacity"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.DisburseLoan(ctx, loan.ID, actorID, "verify security"); !errors.Is(err, share.ErrGuaranteeCapacity) {
		t.Fatalf("disbursement ignored changed share capacity: %v", err)
	}
	if _, err := service.RejectLoan(ctx, loan.ID, actorID, "application withdrawn"); err != nil {
		t.Fatal(err)
	}
	pledged, err = shares.GetActivePledgedAmount(ctx, account.ID)
	if err != nil || numericToString(pledged) != "0" {
		t.Fatalf("rejection stranded guarantor security: %s, %v", numericToString(pledged), err)
	}

	// A second application can use the released shares. Removal must release
	// its reservation too, and full repayment must release every active pledge.
	params.Principal = mustNumeric(t, "150")
	loan, err = service.ApplyForLoan(ctx, params, mustNumeric(t, "100"), []ProposedGuarantor{{GuarantorID: guarantorID, GuaranteedAmount: mustNumeric(t, "50")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApproveGuarantor(ctx, loan.ID, guarantorID, actorID); err != nil {
		t.Fatal(err)
	}
	if err := service.RemoveGuarantor(ctx, loan.ID, guarantorID, actorID); err != nil {
		t.Fatal(err)
	}
	pledged, err = shares.GetActivePledgedAmount(ctx, account.ID)
	if err != nil || numericToString(pledged) != "0" {
		t.Fatalf("removal stranded security: %s, %v", numericToString(pledged), err)
	}
	if _, err := service.AddGuarantor(ctx, loan.ID, extraID, mustNumeric(t, "50")); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApproveGuarantor(ctx, loan.ID, extraID, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApproveLoan(ctx, loan.ID, actorID, "reviewed"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.DisburseLoan(ctx, loan.ID, actorID, "release funds"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordRepayment(ctx, loan.ID, mustNumeric(t, "150"), "security-payoff", actorID); err != nil {
		t.Fatal(err)
	}
	var activeCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM share_pledges WHERE loan_id = $1 AND status = 'active'`, loan.ID).Scan(&activeCount); err != nil || activeCount != 0 {
		t.Fatalf("payoff stranded active security: %d, %v", activeCount, err)
	}
}

// TestLoanApprovalRechecksContributionHistoryAndMembership verifies the gates
// introduced after application; a successful application must not bypass either.
func TestLoanApprovalRechecksContributionHistoryAndMembership(t *testing.T) {
	ctx := context.Background()
	service, pool := applyLoanService(t, ctx)
	applicantID := createLoanMember(t, ctx, pool, 311, "active")
	loan, err := service.ApplyForLoan(ctx, applyParams(t, applicantID), mustNumeric(t, "500"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE members SET status = 'suspended' WHERE id = $1`, applicantID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApproveLoan(ctx, loan.ID, pgtype.UUID{}, "reviewed"); !errors.Is(err, ErrMemberNotActive) {
		t.Fatalf("approval ignored inactive applicant: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE members SET status = 'active' WHERE id = $1`, applicantID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE share_transactions SET created_at = NOW()
		WHERE share_account_id = (SELECT id FROM share_accounts WHERE member_id = $1)`, applicantID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApproveLoan(ctx, loan.ID, pgtype.UUID{}, "reviewed"); !errors.Is(err, share.ErrContributionHistory) {
		t.Fatalf("multiple purchases in one month passed six-month history: %v", err)
	}
}
