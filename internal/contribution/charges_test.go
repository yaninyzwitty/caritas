package contribution

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	contributionsqlc "github.com/yaninyzwitty/caritas-backend/internal/contribution/repository/sqlc"
)

// chargeQueries isolates charge validation from PostgreSQL. Without this fake,
// checking wrong-member payments and overpayments would require Docker.
type chargeQueries struct {
	contributionsqlc.Querier
	charges        []contributionsqlc.ContributionCharge
	paid           pgtype.Numeric
	excludeReceipt pgtype.UUID
	createErr      error
}

// LockContributionCharges supplies locked records to the validation unit tests;
// removing it would invoke the unused embedded database interface.
func (q *chargeQueries) LockContributionCharges(context.Context, []pgtype.UUID) ([]contributionsqlc.ContributionCharge, error) {
	return q.charges, nil
}

// SumContributionChargePayments captures retry exclusion as well as paid money;
// without it these tests cannot detect double counting the current receipt.
func (q *chargeQueries) SumContributionChargePayments(_ context.Context, params contributionsqlc.SumContributionChargePaymentsParams) (pgtype.Numeric, error) {
	q.excludeReceipt = params.ExcludeReceipt
	return q.paid, nil
}

// CreateContributionCharge simulates the idempotent insert's conflict result;
// without it retry behavior would need an integration test to exercise.
func (q *chargeQueries) CreateContributionCharge(context.Context, contributionsqlc.CreateContributionChargeParams) (contributionsqlc.ContributionCharge, error) {
	return q.charges[0], q.createErr
}

// GetContributionChargeByKey returns the existing obligation after a conflict;
// without it changed retry details cannot be checked without a database.
func (q *chargeQueries) GetContributionChargeByKey(context.Context, string) (contributionsqlc.ContributionCharge, error) {
	return q.charges[0], nil
}

// TestValidateChargeAllocations checks liability and identity boundaries without
// PostgreSQL; removing it loses coverage of payments to the wrong obligation.
func TestValidateChargeAllocations(t *testing.T) {
	member := testUUID("00000000-0000-0000-0000-000000000001")
	target := testUUID("00000000-0000-0000-0000-000000000002")
	other := testUUID("00000000-0000-0000-0000-000000000003")
	for _, tc := range []struct {
		name, amount, category string
		member                 pgtype.UUID
		branch                 int64
		kind                   contributionsqlc.ContributionAllocationType
		missing, duplicate     bool
		want                   error
	}{
		{name: "partial payment", amount: "100", category: "literature", member: member, branch: 1, kind: contributionsqlc.ContributionAllocationTypeOtherCharge},
		{name: "settles remaining balance", amount: "300", category: "laptop", member: member, branch: 1, kind: contributionsqlc.ContributionAllocationTypeOtherCharge},
		{name: "overpayment", amount: "300.0001", category: "laptop", member: member, branch: 1, kind: contributionsqlc.ContributionAllocationTypeOtherCharge, want: ErrChargeOverpayment},
		{name: "wrong member", amount: "100", category: "literature", member: other, branch: 1, kind: contributionsqlc.ContributionAllocationTypeOtherCharge, want: ErrInvalidCharge},
		{name: "wrong branch", amount: "100", category: "literature", member: member, branch: 2, kind: contributionsqlc.ContributionAllocationTypeOtherCharge, want: ErrInvalidCharge},
		{name: "missing charge", amount: "100", member: member, branch: 1, kind: contributionsqlc.ContributionAllocationTypeOtherCharge, missing: true, want: ErrInvalidCharge},
		{name: "non-loan penalty", amount: "100", category: "penalty", member: member, branch: 1, kind: contributionsqlc.ContributionAllocationTypePenalty},
		{name: "penalty disguised as charge", amount: "100", category: "penalty", member: member, branch: 1, kind: contributionsqlc.ContributionAllocationTypeOtherCharge, want: ErrInvalidCharge},
		{name: "charge disguised as penalty", amount: "100", category: "literature", member: member, branch: 1, kind: contributionsqlc.ContributionAllocationTypePenalty, want: ErrInvalidCharge},
		{name: "same target twice", amount: "100", category: "penalty", member: member, branch: 1, kind: contributionsqlc.ContributionAllocationTypePenalty, duplicate: true, want: ErrDuplicateAllocation},
		{name: "fraction beyond ledger precision", amount: "100.0000000001", category: "literature", member: member, branch: 1, kind: contributionsqlc.ContributionAllocationTypeOtherCharge, want: ErrInvalidAllocation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := &chargeQueries{paid: mustNumeric(t, "200")}
			if !tc.missing {
				q.charges = []contributionsqlc.ContributionCharge{{ID: target, MemberID: tc.member, BranchID: tc.branch, Category: tc.category, Amount: mustNumeric(t, "500")}}
			}
			items := []AllocationInput{{Type: tc.kind, TargetID: target, Amount: mustNumeric(t, tc.amount)}}
			if tc.duplicate {
				items = append(items, AllocationInput{Type: contributionsqlc.ContributionAllocationTypeOtherCharge, TargetID: target, Amount: mustNumeric(t, "100")})
			}
			err := validateChargeAllocations(t.Context(), q, member, 1, items, other)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if err == nil && q.excludeReceipt != other {
				t.Fatal("current receipt was not excluded from paid amounts")
			}
		})
	}
	if err := validateChargeAllocations(t.Context(), nil, member, 1, nil, pgtype.UUID{}); err != nil {
		t.Fatal(err)
	}
}

// TestCreateChargeValidationAndRetry prevents invalid obligations and changed
// idempotency retries; without it duplicate assessments can regress unnoticed.
func TestCreateChargeValidationAndRetry(t *testing.T) {
	member := testUUID("00000000-0000-0000-0000-000000000001")
	params := contributionsqlc.CreateContributionChargeParams{
		IdempotencyKey: "charge-1", MemberID: member, BranchID: 1, CreatedBy: member,
		Category: "laptop", Amount: mustNumeric(t, "500"), Reason: "Laptop payment obligation",
	}
	q := &chargeQueries{createErr: pgx.ErrNoRows, charges: []contributionsqlc.ContributionCharge{{
		MemberID: member, BranchID: 1, Category: params.Category, Amount: params.Amount, Reason: params.Reason,
	}}}
	service := NewService(&Store{Querier: q}, nil, nil)
	if _, err := service.CreateCharge(t.Context(), params); err != nil {
		t.Fatal(err)
	}
	changed := params
	changed.Amount = mustNumeric(t, "501")
	if _, err := service.CreateCharge(t.Context(), changed); !errors.Is(err, ErrChargeConflict) {
		t.Fatalf("changed retry error = %v", err)
	}
	for _, category := range []string{"literature", "caritas_registration", "lsf", "laptop", "penalty", "other"} {
		q.createErr = nil
		valid := params
		valid.Category = category
		if _, err := service.CreateCharge(t.Context(), valid); err != nil {
			t.Fatalf("category %s: %v", category, err)
		}
	}
	for _, tc := range []struct{ category, amount, reason string }{
		{"unrecognized", "500", "reason"}, {"laptop", "0", "reason"},
		{"laptop", "-1", "reason"}, {"laptop", "500.00001", "reason"}, {"laptop", "500.0000000001", "reason"},
		{"laptop", "1000000000000000", "reason"}, {"laptop", "500", " "},
	} {
		invalid := params
		invalid.Category, invalid.Amount, invalid.Reason = tc.category, mustNumeric(t, tc.amount), tc.reason
		if _, err := service.CreateCharge(t.Context(), invalid); !errors.Is(err, ErrInvalidCharge) {
			t.Fatalf("invalid input %+v: %v", tc, err)
		}
	}
}

// TestChargeBalanceAndTarget checks the API's remaining balance and mandatory
// charge reference; without it a payment could lose its obligation identity.
func TestChargeBalanceAndTarget(t *testing.T) {
	response := chargeToProto(contributionsqlc.ContributionCharge{Amount: mustNumeric(t, "500")}, mustNumeric(t, "200"))
	if response.GetPaid().GetUnits() != 200 || response.GetOutstanding().GetUnits() != 300 {
		t.Fatalf("incorrect balance: %v", response)
	}
	for _, kind := range []contributionsqlc.ContributionAllocationType{contributionsqlc.ContributionAllocationTypeOtherCharge, contributionsqlc.ContributionAllocationTypePenalty} {
		if err := validateReceipt(validReceiptParams(t, "100"), []AllocationInput{{Type: kind, Amount: mustNumeric(t, "100")}}); !errors.Is(err, ErrInvalidAllocation) {
			t.Fatalf("missing target for %s: %v", kind, err)
		}
	}
}
