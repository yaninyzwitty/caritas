package contribution

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	contributionsqlc "github.com/yaninyzwitty/caritas-backend/internal/contribution/repository/sqlc"
)

func TestValidateReceiptAcceptsBalancedPlan(t *testing.T) {
	params := validReceiptParams(t, "1000")

	err := validateReceipt(params, []AllocationInput{
		{Type: contributionsqlc.ContributionAllocationTypeCom, Amount: mustNumeric(t, "30")},
		{Type: contributionsqlc.ContributionAllocationTypeLgom, Amount: mustNumeric(t, "30")},
		{
			Type:     contributionsqlc.ContributionAllocationTypeSharePurchase,
			TargetID: testUUID("00000000-0000-0000-0000-000000000101"),
			Amount:   mustNumeric(t, "940"),
		},
	})
	if err != nil {
		t.Fatalf("validate receipt: %v", err)
	}
}

func TestValidateReceiptRequiresReference(t *testing.T) {
	params := validReceiptParams(t, "1000")
	params.ExternalTransactionID = pgtype.Text{}

	err := validateReceipt(params, []AllocationInput{
		{Type: contributionsqlc.ContributionAllocationTypeCom, Amount: mustNumeric(t, "1000")},
	})
	if !errors.Is(err, ErrReceiptReferenceRequired) {
		t.Fatalf("error = %v, want %v", err, ErrReceiptReferenceRequired)
	}
}

func TestValidateReceiptRejectsUnsupportedSourceChannel(t *testing.T) {
	params := validReceiptParams(t, "1000")
	params.SourceChannel = contributionsqlc.ContributionSourceChannel("daraja_paybill")

	err := validateReceipt(params, []AllocationInput{
		{Type: contributionsqlc.ContributionAllocationTypeCom, Amount: mustNumeric(t, "1000")},
	})
	if !errors.Is(err, ErrInvalidPayment) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidPayment)
	}
}

func TestValidateReceiptRequiresAllocationTotalToMatchAmount(t *testing.T) {
	params := validReceiptParams(t, "1000")

	err := validateReceipt(params, []AllocationInput{
		{Type: contributionsqlc.ContributionAllocationTypeCom, Amount: mustNumeric(t, "30")},
	})
	if !errors.Is(err, ErrAllocationTotalMismatch) {
		t.Fatalf("error = %v, want %v", err, ErrAllocationTotalMismatch)
	}
}

func TestValidateReceiptRejectsDuplicateAllocation(t *testing.T) {
	params := validReceiptParams(t, "60")

	err := validateReceipt(params, []AllocationInput{
		{Type: contributionsqlc.ContributionAllocationTypeCom, Amount: mustNumeric(t, "30")},
		{Type: contributionsqlc.ContributionAllocationTypeCom, Amount: mustNumeric(t, "30")},
	})
	if !errors.Is(err, ErrDuplicateAllocation) {
		t.Fatalf("error = %v, want %v", err, ErrDuplicateAllocation)
	}
}

func TestValidateReceiptRequiresTargetForLedgerAllocation(t *testing.T) {
	params := validReceiptParams(t, "1000")

	err := validateReceipt(params, []AllocationInput{
		{Type: contributionsqlc.ContributionAllocationTypeSharePurchase, Amount: mustNumeric(t, "1000")},
	})
	if !errors.Is(err, ErrInvalidAllocation) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidAllocation)
	}
}

func TestProcessAllocationCompletesContributionOwnedFeeLocally(t *testing.T) {
	receipt := contributionsqlc.ContributionReceipt{
		ID:                    testUUID("00000000-0000-0000-0000-000000000201"),
		ExternalTransactionID: pgtype.Text{String: "mpesa-receipt-2", Valid: true},
	}
	allocation := contributionsqlc.ContributionAllocation{
		ID:   testUUID("00000000-0000-0000-0000-000000000202"),
		Type: contributionsqlc.ContributionAllocationTypeCom,
	}
	service := NewService(nil, nil, nil)

	referenceID, externalReference, err := service.processAllocation(
		t.Context(),
		receipt,
		allocation,
		testUUID("00000000-0000-0000-0000-000000000203"),
	)
	if err != nil {
		t.Fatalf("process allocation: %v", err)
	}
	if referenceID != allocation.ID {
		t.Fatalf("reference id = %v, want allocation id %v", referenceID, allocation.ID)
	}
	if externalReference.String != "mpesa-receipt-2" || !externalReference.Valid {
		t.Fatalf("external reference = %+v", externalReference)
	}
}

func TestProcessAllocationRejectsUnsupportedLoanBreakdown(t *testing.T) {
	receipt := contributionsqlc.ContributionReceipt{
		ID:                    testUUID("00000000-0000-0000-0000-000000000301"),
		ExternalTransactionID: pgtype.Text{String: "mpesa-receipt-3", Valid: true},
	}
	allocation := contributionsqlc.ContributionAllocation{
		ID:   testUUID("00000000-0000-0000-0000-000000000302"),
		Type: contributionsqlc.ContributionAllocationTypeLoanInterest,
	}
	service := NewService(nil, nil, nil)

	_, _, err := service.processAllocation(
		t.Context(),
		receipt,
		allocation,
		testUUID("00000000-0000-0000-0000-000000000303"),
	)
	if !errors.Is(err, ErrAllocationNotSupported) {
		t.Fatalf("error = %v, want %v", err, ErrAllocationNotSupported)
	}
}

func validReceiptParams(t *testing.T, amount string) contributionsqlc.InsertContributionReceiptParams {
	t.Helper()
	return contributionsqlc.InsertContributionReceiptParams{
		SourceChannel:         contributionsqlc.ContributionSourceChannelDarajaStk,
		ExternalTransactionID: pgtype.Text{String: "mpesa-receipt-1", Valid: true},
		MemberID:              testUUID("00000000-0000-0000-0000-000000000001"),
		BranchID:              1,
		ContributionPeriod:    pgtype.Date{Time: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), Valid: true},
		ReceivedAmount:        mustNumeric(t, amount),
		AllocationPlan:        []byte(`{"items":[]}`),
		ReceivedAt:            pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}
}

func mustNumeric(t *testing.T, value string) pgtype.Numeric {
	t.Helper()
	var n pgtype.Numeric
	if err := n.Scan(value); err != nil {
		t.Fatalf("parse numeric: %v", err)
	}
	return n
}

func testUUID(value string) pgtype.UUID {
	var id pgtype.UUID
	if err := id.Scan(value); err != nil {
		panic(err)
	}
	return id
}
