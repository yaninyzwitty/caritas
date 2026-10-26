package contribution

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	contributionsqlc "github.com/yaninyzwitty/caritas-backend/internal/contribution/repository/sqlc"
	"github.com/yaninyzwitty/caritas-backend/internal/loan"
	"github.com/yaninyzwitty/caritas-backend/internal/member"
	"github.com/yaninyzwitty/caritas-backend/internal/share"
)

// TestReceiptPostingIntegration proves the receipt never needs a second connection,
// and that a later rejected allocation cannot commit an earlier ledger posting.
func TestReceiptPostingIntegration(t *testing.T) {
	database := contributionTestDB(t)
	config := database.Config()
	config.MaxConns, config.MinConns = 1, 0
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `
		INSERT INTO members (id,branch_id,member_number,national_id,status,created_at)
		VALUES ('00000000-0000-0000-0000-000000000001',1,1,'posting-test','active','2020-01-01');
		INSERT INTO staff_users (id,branch_id,email,name,role)
		VALUES ('00000000-0000-0000-0000-000000000003',1,'posting@example.test','Cashier','cashier');
		INSERT INTO share_accounts (id,member_id,branch_id,status,opened_at)
		VALUES ('00000000-0000-0000-0000-000000000004','00000000-0000-0000-0000-000000000001',1,'active',NOW());
		INSERT INTO loans (id,member_id,branch_id,principal,interest_rate,repayment_period_months,status)
		VALUES ('00000000-0000-0000-0000-000000000002','00000000-0000-0000-0000-000000000001',1,100000,0.01,20,'active');
		INSERT INTO repayment_schedules (loan_id,installment_no,due_date,amount_due,status)
		VALUES ('00000000-0000-0000-0000-000000000002',1,CURRENT_DATE,5000,'upcoming');
	`); err != nil {
		t.Fatal(err)
	}
	memberID := testUUID("00000000-0000-0000-0000-000000000001")
	cashier := testUUID("00000000-0000-0000-0000-000000000003")
	account := testUUID("00000000-0000-0000-0000-000000000004")
	loanID := testUUID("00000000-0000-0000-0000-000000000002")
	service := NewService(NewStore(pool), share.NewService(share.NewStore(pool)), loan.NewService(loan.NewStore(pool), nil))
	session, err := service.OpenCashierSession(ctx, 1, cashier)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	params := contributionsqlc.InsertContributionReceiptParams{
		IdempotencyKey: text("one-connection"), CashierSessionID: session.ID, MemberID: memberID, BranchID: 1,
		ContributionPeriod: pgtype.Date{Time: time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC), Valid: true},
		ReceivedAmount:     mustNumeric(t, "7060"), ReceivedBy: cashier,
		ReceivedAt: pgtype.Timestamptz{Time: now, Valid: true},
	}
	allocations := []AllocationInput{
		{Type: contributionsqlc.ContributionAllocationTypeSharePurchase, TargetID: account, Amount: mustNumeric(t, "1000")},
		{Type: contributionsqlc.ContributionAllocationTypeLoanPrincipal, TargetID: loanID, Amount: mustNumeric(t, "6000")},
	}
	// Reject an underpayment before accepting cash or sending an STK prompt.
	underpaid := append([]AllocationInput(nil), allocations...)
	underpaid[1].Amount = mustNumeric(t, "4999")
	underpaidCash := params
	underpaidCash.IdempotencyKey, underpaidCash.ReceivedAmount = text("underpaid-cash"), mustNumeric(t, "6059")
	if _, err := service.CreateCashReceipt(ctx, underpaidCash, underpaid); !errors.Is(err, ErrInvalidAllocation) {
		t.Fatalf("cash installment minimum: %v", err)
	}
	provider := &testSTK{checkout: "underpaid-checkout"}
	if _, err := service.InitiateDarajaSTKPayment(ctx, InitiateDarajaSTKPaymentParams{
		IdempotencyKey: "underpaid-stk", PhoneNumber: "254712345678", MemberID: memberID,
		BranchID: 1, ContributionPeriod: params.ContributionPeriod, Amount: underpaidCash.ReceivedAmount,
		Allocations: underpaid, RequestedBy: cashier,
	}, provider); !errors.Is(err, ErrInvalidAllocation) || provider.calls != 0 {
		t.Fatalf("STK installment minimum: calls=%d error=%v", provider.calls, err)
	}
	// KES 6,000 covers the KES 5,000 installment and KES 1,000 interest.
	receipt, err := service.CreateCashReceipt(ctx, params, allocations)
	if err != nil || receipt.Receipt.Status != contributionsqlc.ContributionReceiptStatusCompleted {
		t.Fatalf("single-connection payment: status=%s error=%v", receipt.Receipt.Status, err)
	}
	retry, err := service.CreateCashReceipt(ctx, params, allocations)
	if err != nil || retry.Receipt.ID != receipt.Receipt.ID {
		t.Fatalf("retry: %+v %v", retry, err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO members (id,branch_id,member_number,national_id,status)
		VALUES ('00000000-0000-0000-0000-000000000005',1,2,'other-member','active');
		INSERT INTO share_accounts (id,member_id,branch_id,status,opened_at)
		VALUES ('00000000-0000-0000-0000-000000000006','00000000-0000-0000-0000-000000000005',1,'active',NOW());
	`); err != nil {
		t.Fatal(err)
	}
	wrongTarget := append([]AllocationInput(nil), allocations...)
	wrongTarget[0].TargetID = testUUID("00000000-0000-0000-0000-000000000006")
	wrongPayment := params
	wrongPayment.IdempotencyKey, wrongPayment.ReceivedAmount = text("wrong-member-target"), mustNumeric(t, "7000")
	if _, err := service.CreateCashReceipt(ctx, wrongPayment, wrongTarget); !errors.Is(err, share.ErrAccountNotFound) {
		t.Fatalf("another member's share target: %v", err)
	}

	// Persist accepted money, then freeze its share target before processing.
	params.SourceChannel = contributionsqlc.ContributionSourceChannelCash
	params.IdempotencyKey, params.InternalReceiptReference = text("rollback"), text("CASH-ROLLBACK")
	params.ReceivedAmount = mustNumeric(t, "7000")
	pending, err := service.CreateReceipt(ctx, params, allocations)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE share_accounts SET status='frozen' WHERE id=$1`, account); err != nil {
		t.Fatal(err)
	}
	failed, err := service.ProcessReceipt(ctx, pending.Receipt.ID, cashier)
	if !errors.Is(err, share.ErrAccountNotActive) || failed.Receipt.Status != contributionsqlc.ContributionReceiptStatusManualReview {
		t.Fatalf("rejected allocation: status=%s error=%v", failed.Receipt.Status, err)
	}
	for _, allocation := range failed.Allocations {
		if allocation.Status != contributionsqlc.ContributionAllocationStatusPending {
			t.Fatalf("rolled back allocation: %+v", allocation)
		}
	}
	var repayments, purchases int
	if err := pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM loan_transactions WHERE type='repayment'), (SELECT COUNT(*) FROM share_transactions WHERE type='purchase')`).Scan(&repayments, &purchases); err != nil {
		t.Fatal(err)
	}
	if repayments != 1 || purchases != 1 {
		t.Fatalf("postings after rejection: loans=%d shares=%d", repayments, purchases)
	}

	// A frozen target is rejected before new cash acceptance; committed retries still work.
	params.IdempotencyKey = text("frozen-new-payment")
	if _, err := service.CreateCashReceipt(ctx, params, allocations); !errors.Is(err, share.ErrAccountNotActive) {
		t.Fatalf("frozen target: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE members SET status='suspended' WHERE id=$1`, memberID); err != nil {
		t.Fatal(err)
	}
	params.IdempotencyKey = text("suspended-new-payment")
	if _, err := service.CreateCashReceipt(ctx, params, allocations); !errors.Is(err, member.ErrMemberNotActive) {
		t.Fatalf("suspended member: %v", err)
	}
	var receipts int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM contribution_receipts`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if receipts != 2 {
		t.Fatalf("rejected submissions persisted receipts: count=%d", receipts)
	}
	params.IdempotencyKey, params.ReceivedAmount = text("one-connection"), mustNumeric(t, "7060")
	retry, err = service.CreateCashReceipt(ctx, params, allocations)
	if err != nil || retry.Receipt.ID != receipt.Receipt.ID {
		t.Fatalf("committed retry after suspension: %+v %v", retry, err)
	}

	// An interrupted processing transaction leaves durable work for recovery.
	if _, err := pool.Exec(ctx, `UPDATE share_accounts SET status='active' WHERE id=$1`, account); err != nil {
		t.Fatal(err)
	}
	params.IdempotencyKey, params.InternalReceiptReference = text("recover"), text("CASH-RECOVER")
	params.ReceivedAmount = mustNumeric(t, "7000")
	pending, err = service.CreateReceipt(ctx, params, allocations)
	if err != nil {
		t.Fatal(err)
	}
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := service.ProcessReceipt(canceledCtx, pending.Receipt.ID, cashier); err == nil {
		t.Fatal("canceled processing succeeded")
	}
	if _, err := pool.Exec(ctx, `UPDATE contribution_receipts SET updated_at=NOW()-INTERVAL '1 minute' WHERE id=$1`, pending.Receipt.ID); err != nil {
		t.Fatal(err)
	}
	recoveryCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); service.RecoverPendingReceipts(recoveryCtx) }()
	defer func() { stop(); <-done }()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("pending receipt was not recovered")
		case <-ticker.C:
			row, err := service.GetReceipt(ctx, pending.Receipt.ID)
			if err != nil {
				t.Fatal(err)
			}
			if row.Status == contributionsqlc.ContributionReceiptStatusCompleted {
				return
			}
		}
	}
}
