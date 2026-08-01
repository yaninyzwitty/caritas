package loan

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	loansqlc "github.com/yaninyzwitty/caritas-backend/internal/loan/repository/sqlc"
)

type memberEligibilityFunc func(context.Context, pgtype.UUID) error

func (f memberEligibilityFunc) RequireActiveMember(ctx context.Context, memberID pgtype.UUID) error {
	return f(ctx, memberID)
}

func TestApplyForLoanRequiresActiveApplicant(t *testing.T) {
	applicantID := testUUID("00000000-0000-0000-0000-000000000101")
	guarantorID := testUUID("00000000-0000-0000-0000-000000000102")
	service := NewService(nil, memberEligibilityFunc(func(_ context.Context, memberID pgtype.UUID) error {
		if sameUUID(memberID, applicantID) {
			return errors.New("inactive")
		}
		return nil
	}))

	_, err := service.ApplyForLoan(context.Background(), applyParams(t, applicantID), []pgtype.UUID{guarantorID})
	if !errors.Is(err, ErrMemberNotActive) {
		t.Fatalf("expected ErrMemberNotActive, got %v", err)
	}
}

func TestApplyForLoanRequiresActiveGuarantors(t *testing.T) {
	applicantID := testUUID("00000000-0000-0000-0000-000000000103")
	guarantorID := testUUID("00000000-0000-0000-0000-000000000104")
	service := NewService(nil, memberEligibilityFunc(func(_ context.Context, memberID pgtype.UUID) error {
		if sameUUID(memberID, guarantorID) {
			return errors.New("inactive")
		}
		return nil
	}))

	_, err := service.ApplyForLoan(context.Background(), applyParams(t, applicantID), []pgtype.UUID{guarantorID})
	if !errors.Is(err, ErrGuarantorNotActive) {
		t.Fatalf("expected ErrGuarantorNotActive, got %v", err)
	}
}

func TestAddGuarantorRequiresActiveGuarantor(t *testing.T) {
	service := NewService(nil, memberEligibilityFunc(func(context.Context, pgtype.UUID) error {
		return errors.New("inactive")
	}))

	_, err := service.AddGuarantor(
		context.Background(),
		testUUID("00000000-0000-0000-0000-000000000105"),
		testUUID("00000000-0000-0000-0000-000000000106"),
		mustNumeric(t, "500"),
	)
	if !errors.Is(err, ErrGuarantorNotActive) {
		t.Fatalf("expected ErrGuarantorNotActive, got %v", err)
	}
}

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
