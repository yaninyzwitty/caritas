package contribution

import (
	"errors"
	"testing"

	contributionsqlc "github.com/yaninyzwitty/caritas-backend/internal/contribution/repository/sqlc"
)

func TestCalculatedAllocations(t *testing.T) {
	fees := []AllocationInput{
		{Type: contributionsqlc.ContributionAllocationTypeCom, Amount: mustNumeric(t, "30")},
		{Type: contributionsqlc.ContributionAllocationTypeLgom, Amount: mustNumeric(t, "30")},
	}
	loan := testUUID("00000000-0000-0000-0000-000000000101")
	input := []AllocationInput{
		{Type: contributionsqlc.ContributionAllocationTypeLoanPrincipal, TargetID: loan, Amount: mustNumeric(t, "5000")},
		{Type: contributionsqlc.ContributionAllocationTypeLoanInterest, TargetID: loan, Amount: mustNumeric(t, "1000")},
	}
	got, err := calculatedAllocations(input, fees)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Type != contributionsqlc.ContributionAllocationTypeLoanPrincipal ||
		numericToScale(got[0].Amount, -4).Cmp(numericToScale(mustNumeric(t, "6000"), -4)) != 0 {
		t.Fatalf("allocations = %+v", got)
	}
	if err := validateReceipt(validReceiptParams(t, "6060"), got); err != nil {
		t.Fatal(err)
	}
	if err := validateReceipt(validReceiptParams(t, "6000"), got); !errors.Is(err, ErrAllocationTotalMismatch) {
		t.Fatalf("fees must not silently reduce loan payment: %v", err)
	}
	got, err = calculatedAllocations(input, nil)
	if err != nil || len(got) != 1 {
		t.Fatalf("already-paid fees: %+v, %v", got, err)
	}
	for _, amount := range []string{"1", "29", "31", "60"} {
		_, err := calculatedAllocations([]AllocationInput{{Type: contributionsqlc.ContributionAllocationTypeCom, Amount: mustNumeric(t, amount)}}, fees)
		if !errors.Is(err, ErrMembershipFeeMismatch) {
			t.Fatalf("fee %s: %v", amount, err)
		}
	}
	if _, err := calculatedAllocations(fees, nil); !errors.Is(err, ErrMembershipFeeMismatch) {
		t.Fatalf("duplicate monthly collection: %v", err)
	}
}
