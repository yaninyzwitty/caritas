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
)

func TestApplyForLoanRequiresActiveApplicant(t *testing.T) {
	ctx := context.Background()
	service, pool := applyLoanService(t, ctx)
	applicantID := createLoanMember(t, ctx, pool, 101, "pending")
	guarantorID := createLoanMember(t, ctx, pool, 102, "active")

	_, err := service.ApplyForLoan(context.Background(), applyParams(t, applicantID), []ProposedGuarantor{
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

	_, err := service.ApplyForLoan(context.Background(), applyParams(t, applicantID), []ProposedGuarantor{
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

	loan, err := service.ApplyForLoan(ctx, applyParams(t, applicantID), []ProposedGuarantor{
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

	_, err := service.ApplyForLoan(ctx, applyParams(t, applicantID), []ProposedGuarantor{
		{GuarantorID: guarantorID, GuaranteedAmount: mustNumeric(t, "499")},
	})
	if !errors.Is(err, ErrInsufficientGuarantee) {
		t.Fatalf("expected ErrInsufficientGuarantee, got %v", err)
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

// createLoanMember inserts only the member row needed by RequireActiveMember;
// profile data is irrelevant to loan eligibility in these tests.
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
