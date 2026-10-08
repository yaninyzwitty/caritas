package contribution

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	contributionsqlc "github.com/yaninyzwitty/caritas-backend/internal/contribution/repository/sqlc"
)

// TestContributionChargePaymentsIntegration verifies real SQL balances, cash
// idempotency and monthly fees. Mocks cannot prove these postings are atomic.
func TestContributionChargePaymentsIntegration(t *testing.T) {
	pool := contributionTestDB(t)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `
        INSERT INTO members (id,branch_id,member_number,national_id,status,created_at)
        VALUES ('00000000-0000-0000-0000-000000000001',1,1,'charge-member','active','2020-01-01');
        INSERT INTO staff_users (id,branch_id,email,name,role)
        VALUES ('00000000-0000-0000-0000-000000000003',1,'charges@example.test','Cashier','cashier');
    `); err != nil {
		t.Fatal(err)
	}
	member := testUUID("00000000-0000-0000-0000-000000000001")
	cashier := testUUID("00000000-0000-0000-0000-000000000003")
	service := NewService(NewStore(pool), nil, nil)
	params := contributionsqlc.CreateContributionChargeParams{
		IdempotencyKey: "literature-1", MemberID: member, BranchID: 1, Category: "literature",
		Amount: mustNumeric(t, "500"), Reason: "Literature supplied", CreatedBy: cashier,
	}
	charge, err := service.CreateCharge(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := service.CreateCharge(ctx, params)
	if err != nil || retry.ID != charge.ID {
		t.Fatalf("charge retry: %+v %v", retry, err)
	}
	changed := params
	changed.Amount = mustNumeric(t, "501")
	if _, err := service.CreateCharge(ctx, changed); !errors.Is(err, ErrChargeConflict) {
		t.Fatalf("changed charge retry: %v", err)
	}
	params.IdempotencyKey, params.Category, params.Reason = "penalty-1", "penalty", "Non-loan penalty"
	params.Amount = mustNumeric(t, "100")
	penalty, err := service.CreateCharge(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.OpenCashierSession(ctx, 1, cashier)
	if err != nil {
		t.Fatal(err)
	}
	cash := contributionsqlc.InsertContributionReceiptParams{
		IdempotencyKey: text("charges-cash-1"), CashierSessionID: session.ID, MemberID: member,
		BranchID: 1, ContributionPeriod: pgtype.Date{Time: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), Valid: true},
		ReceivedAmount: mustNumeric(t, "360"), ReceivedBy: cashier,
	}
	items := []AllocationInput{
		{Type: contributionsqlc.ContributionAllocationTypeOtherCharge, TargetID: charge.ID, Amount: mustNumeric(t, "200")},
		{Type: contributionsqlc.ContributionAllocationTypePenalty, TargetID: penalty.ID, Amount: mustNumeric(t, "100")},
	}
	receipt, err := service.CreateCashReceipt(ctx, cash, items)
	if err != nil || receipt.Receipt.Status != contributionsqlc.ContributionReceiptStatusCompleted {
		t.Fatalf("cash payment: %+v %v", receipt, err)
	}
	repeat, err := service.CreateCashReceipt(ctx, cash, items)
	if err != nil || repeat.Receipt.ID != receipt.Receipt.ID {
		t.Fatalf("cash retry: %+v %v", repeat, err)
	}
	paid, err := service.store.SumContributionChargePayments(ctx, contributionsqlc.SumContributionChargePaymentsParams{ChargeID: charge.ID})
	if err != nil || numericToScale(paid, -4).Cmp(numericToScale(mustNumeric(t, "200"), -4)) != 0 {
		t.Fatalf("partial paid: %v %v", paid, err)
	}
	stk, err := service.InitiateDarajaSTKPayment(ctx, InitiateDarajaSTKPaymentParams{
		IdempotencyKey: "charges-stk", MemberID: member, BranchID: 1, PhoneNumber: "254712345678",
		ContributionPeriod: cash.ContributionPeriod, Amount: mustNumeric(t, "300"), RequestedBy: cashier,
		Allocations: []AllocationInput{{Type: contributionsqlc.ContributionAllocationTypeOtherCharge, TargetID: charge.ID, Amount: mustNumeric(t, "300")}},
	}, &testSTK{checkout: "charges-checkout"})
	if err != nil {
		t.Fatal(err)
	}
	settled, err := service.ProcessDarajaSTKPayment(ctx, DarajaSTKPayment{
		CheckoutRequestID: stk.CheckoutRequestID.String, MpesaReceipt: "charges-mpesa", Amount: mustNumeric(t, "300"), ReceivedAt: time.Now(),
	})
	if err != nil || settled.Receipt.Status != contributionsqlc.ContributionReceiptStatusCompleted {
		t.Fatalf("settlement: %+v %v", settled, err)
	}
	cash.IdempotencyKey, cash.ReceivedAmount = text("charges-overpayment"), mustNumeric(t, "1")
	if _, err := service.CreateCashReceipt(ctx, cash, []AllocationInput{{Type: contributionsqlc.ContributionAllocationTypeOtherCharge, TargetID: charge.ID, Amount: mustNumeric(t, "1")}}); !errors.Is(err, ErrChargeOverpayment) {
		t.Fatalf("overpayment: %v", err)
	}
	rows, err := service.store.ListContributionCharges(ctx, contributionsqlc.ListContributionChargesParams{MemberID: member, BranchID: 1, FetchLimit: 1})
	if err != nil || len(rows) != 1 {
		t.Fatalf("first page: %v %v", rows, err)
	}
	last := rows[0]
	next, err := service.store.ListContributionCharges(ctx, contributionsqlc.ListContributionChargesParams{MemberID: member, BranchID: 1, FetchLimit: 1, CursorCreatedAt: last.CreatedAt, CursorID: last.ID})
	if err != nil || len(next) != 1 || next[0].ID == last.ID {
		t.Fatalf("next page: %v %v", next, err)
	}
	for _, row := range append(rows, next...) {
		if numericToScale(row.Amount, -4).Cmp(numericToScale(row.Paid, -4)) != 0 {
			t.Fatalf("charge not settled: %+v", row)
		}
	}
	wrongBranch, err := service.store.ListContributionCharges(ctx, contributionsqlc.ListContributionChargesParams{MemberID: member, BranchID: 2, FetchLimit: 10})
	if err != nil || len(wrongBranch) != 0 {
		t.Fatalf("cross-branch list: %v %v", wrongBranch, err)
	}
	var total string
	if err := pool.QueryRow(ctx, `SELECT SUM(received_amount)::text FROM contribution_receipts`).Scan(&total); err != nil || total != "660.0000" {
		t.Fatalf("received total (including fees once): %s %v", total, err)
	}
	// An STK prompt is not a balance reservation: cash can settle the obligation
	// before its callback. The collected STK money must still have a receipt.
	params.IdempotencyKey, params.Category, params.Reason = "stale-charge", "lsf", "LSF obligation"
	stale, err := service.CreateCharge(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	staleItems := []AllocationInput{{Type: contributionsqlc.ContributionAllocationTypeOtherCharge, TargetID: stale.ID, Amount: mustNumeric(t, "100")}}
	prompt, err := service.InitiateDarajaSTKPayment(ctx, InitiateDarajaSTKPaymentParams{
		IdempotencyKey: "stale-stk", MemberID: member, BranchID: 1, PhoneNumber: "254712345678",
		ContributionPeriod: cash.ContributionPeriod, Amount: mustNumeric(t, "100"), RequestedBy: cashier, Allocations: staleItems,
	}, &testSTK{checkout: "stale-checkout"})
	if err != nil {
		t.Fatal(err)
	}
	cash.IdempotencyKey, cash.ReceivedAmount = text("stale-cash"), mustNumeric(t, "100")
	if _, err := service.CreateCashReceipt(ctx, cash, staleItems); err != nil {
		t.Fatal(err)
	}
	failed, err := service.ProcessDarajaSTKPayment(ctx, DarajaSTKPayment{
		CheckoutRequestID: prompt.CheckoutRequestID.String, MpesaReceipt: "stale-mpesa", Amount: mustNumeric(t, "100"), ReceivedAt: time.Now(),
	})
	if !errors.Is(err, ErrChargeOverpayment) || !failed.Receipt.ID.Valid || failed.Receipt.Status != contributionsqlc.ContributionReceiptStatusFailed {
		t.Fatalf("collected stale STK payment must remain visible: %+v %v", failed, err)
	}
}

// TestConcurrentContributionChargePaymentsIntegration proves the row lock
// prevents two receipts settling more than the obligation under concurrency.
func TestConcurrentContributionChargePaymentsIntegration(t *testing.T) {
	pool := contributionTestDB(t)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `
        INSERT INTO members (id,branch_id,member_number,national_id,status,created_at)
        VALUES ('00000000-0000-0000-0000-000000000001',1,1,'concurrent-charge','pending','2020-01-01');
        INSERT INTO staff_users (id,branch_id,email,name,role)
        VALUES ('00000000-0000-0000-0000-000000000003',1,'concurrent-charge@example.test','Cashier','cashier');
    `); err != nil {
		t.Fatal(err)
	}
	member := testUUID("00000000-0000-0000-0000-000000000001")
	cashier := testUUID("00000000-0000-0000-0000-000000000003")
	service := NewService(NewStore(pool), nil, nil)
	charge, err := service.CreateCharge(ctx, contributionsqlc.CreateContributionChargeParams{
		IdempotencyKey: "concurrent-charge", MemberID: member, BranchID: 1, Category: "laptop",
		Amount: mustNumeric(t, "500"), Reason: "Laptop", CreatedBy: cashier,
	})
	if err != nil {
		t.Fatal(err)
	}
	var receipts []pgtype.UUID
	for _, reference := range []string{"charge-payment-1", "charge-payment-2"} {
		params := validReceiptParams(t, "300")
		params.SourceChannel = contributionsqlc.ContributionSourceChannelManual
		params.ExternalTransactionID, params.ReceivedBy = text(reference), cashier
		receipt, err := service.CreateReceipt(ctx, params, []AllocationInput{{Type: contributionsqlc.ContributionAllocationTypeOtherCharge, TargetID: charge.ID, Amount: mustNumeric(t, "300")}})
		if err != nil {
			t.Fatal(err)
		}
		receipts = append(receipts, receipt.Receipt.ID)
	}
	var wg sync.WaitGroup
	results := make(chan error, len(receipts))
	start := make(chan struct{})
	for _, id := range receipts {
		wg.Go(func() {
			<-start
			_, err := service.ProcessReceipt(ctx, id, cashier)
			results <- err
		})
	}
	close(start)
	wg.Wait()
	close(results)
	succeeded, rejected := 0, 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrChargeOverpayment) {
			rejected++
		} else {
			t.Fatalf("concurrent posting: %v", err)
		}
	}
	paid, err := service.store.SumContributionChargePayments(ctx, contributionsqlc.SumContributionChargePaymentsParams{ChargeID: charge.ID})
	if err != nil || succeeded != 1 || rejected != 1 || numericToScale(paid, -4).Cmp(numericToScale(mustNumeric(t, "300"), -4)) != 0 {
		t.Fatalf("success=%d rejected=%d paid=%v error=%v", succeeded, rejected, paid, err)
	}
}
