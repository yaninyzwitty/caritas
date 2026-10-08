package contribution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	contributionsqlc "github.com/yaninyzwitty/caritas-backend/internal/contribution/repository/sqlc"
	"github.com/yaninyzwitty/caritas-backend/internal/loan"
)

type testSTK struct {
	checkout string
	calls    int
}

func (s *testSTK) InitiateSTK(context.Context, DarajaSTKInitiationRequest) (string, error) {
	s.calls++
	return s.checkout, nil
}

func TestCashAndSTKMonthlyCharges(t *testing.T) {
	ctx := t.Context()
	pool := contributionTestDB(t)
	store := NewStore(pool)
	service := NewService(store, nil, loan.NewService(loan.NewStore(pool), nil))
	member := testUUID("00000000-0000-0000-0000-000000000001")
	loanID := testUUID("00000000-0000-0000-0000-000000000002")
	cashier := testUUID("00000000-0000-0000-0000-000000000003")
	for _, stmt := range []string{
		`INSERT INTO members (id, branch_id, member_number, national_id, status, created_at) VALUES ('00000000-0000-0000-0000-000000000001',1,1,'fees-test','active','2020-01-01')`,
		`INSERT INTO staff_users (id, branch_id, email, name, role) VALUES ('00000000-0000-0000-0000-000000000003',1,'cashier@example.test','Test Cashier','cashier')`,
		`INSERT INTO loans (id, member_id, branch_id, principal, interest_rate, repayment_period_months, status) VALUES ('00000000-0000-0000-0000-000000000002','00000000-0000-0000-0000-000000000001',1,100000,0.01,20,'active')`,
		`INSERT INTO repayment_schedules (loan_id, installment_no, due_date, amount_due, status) VALUES ('00000000-0000-0000-0000-000000000002',1,CURRENT_DATE,100000,'upcoming')`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	session, err := service.OpenCashierSession(ctx, 1, cashier)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	period := pgtype.Date{Time: time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0), Valid: true}
	allocations := []AllocationInput{{Type: contributionsqlc.ContributionAllocationTypeLoanPrincipal, TargetID: loanID, Amount: mustNumeric(t, "6000")}}
	cash := contributionsqlc.InsertContributionReceiptParams{
		IdempotencyKey: text("cash-first"), CashierSessionID: session.ID, MemberID: member,
		BranchID: 1, ContributionPeriod: period, ReceivedAmount: mustNumeric(t, "6060"), ReceivedBy: cashier,
	}
	receipt, err := service.CreateCashReceipt(ctx, cash, allocations)
	if err != nil || receipt.Receipt.Status != contributionsqlc.ContributionReceiptStatusCompleted {
		t.Fatalf("cash: %+v %v", receipt, err)
	}
	retry, err := service.CreateCashReceipt(ctx, cash, allocations)
	if err != nil || retry.Receipt.ID != receipt.Receipt.ID {
		t.Fatalf("cash retry: %+v %v", retry, err)
	}
	provider := &testSTK{checkout: "checkout-1"}
	request := InitiateDarajaSTKPaymentParams{IdempotencyKey: "stk-same-month", PhoneNumber: "254712345678", MemberID: member,
		BranchID: 1, ContributionPeriod: period, Amount: mustNumeric(t, "6000"), Allocations: allocations, RequestedBy: cashier}
	if _, err := service.InitiateDarajaSTKPayment(ctx, request, provider); err != nil {
		t.Fatal(err)
	}
	payment := DarajaSTKPayment{CheckoutRequestID: provider.checkout, MpesaReceipt: "MPESA-1", Amount: request.Amount}
	stk, err := service.ProcessDarajaSTKPayment(ctx, payment)
	if err != nil || stk.Receipt.Status != contributionsqlc.ContributionReceiptStatusCompleted {
		t.Fatalf("stk: %+v %v", stk, err)
	}
	if len(stk.Allocations) != 1 {
		t.Fatalf("fees charged twice: %+v", stk.Allocations)
	}
	if _, err := service.ProcessDarajaSTKPayment(ctx, payment); err != nil {
		t.Fatal(err)
	}
	if _, err := service.InitiateDarajaSTKPayment(ctx, request, provider); err != nil || provider.calls != 1 {
		t.Fatalf("stk retry: calls=%d error=%v", provider.calls, err)
	}

	cash.IdempotencyKey = text("cash-next-month")
	cash.ContributionPeriod.Time = period.Time.AddDate(0, 1, 0)
	next, err := service.CreateCashReceipt(ctx, cash, allocations)
	if err != nil || next.Receipt.Status != contributionsqlc.ContributionReceiptStatusCompleted {
		t.Fatalf("next month: %+v %v", next, err)
	}
	var breakdown []byte
	for _, a := range next.Allocations {
		if a.Type == contributionsqlc.ContributionAllocationTypeLoanPrincipal {
			if err := pool.QueryRow(ctx, `SELECT allocation_breakdown FROM loan_transactions WHERE payment_gateway_transaction_id=$1`, a.ID.String()).Scan(&breakdown); err != nil {
				t.Fatal(err)
			}
		}
	}
	var parts map[string]string
	if err := json.Unmarshal(breakdown, &parts); err != nil {
		t.Fatal(err)
	}
	// Two payments last month reduced 100000 by 5000 + 6000 = 11000.
	if parts["interest"] != "890" || parts["principal"] != "5110" {
		t.Fatalf("breakdown=%s", breakdown)
	}
	var principal, feeTotal string
	if err := pool.QueryRow(ctx, `SELECT principal::text FROM loans WHERE id=$1`, loanID).Scan(&principal); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT SUM(amount)::text FROM contribution_allocations WHERE type IN ('com','lgom') AND status='completed'`).Scan(&feeTotal); err != nil {
		t.Fatal(err)
	}
	if principal != "100000.0000" || feeTotal != "120.0000" {
		t.Fatalf("principal=%s fees=%s", principal, feeTotal)
	}
}

func TestConcurrentReceiptsDoNotCollectMonthlyFeesTwice(t *testing.T) {
	pool := contributionTestDB(t)
	ctx := t.Context()
	service := NewService(NewStore(pool), nil, nil)
	member := testUUID("00000000-0000-0000-0000-000000000001")
	if _, err := pool.Exec(ctx, `INSERT INTO members (id,branch_id,member_number,national_id,status,created_at) VALUES ($1,1,1,'concurrent','active','2020-01-01')`, member); err != nil {
		t.Fatal(err)
	}
	var receipts []CreatedReceipt
	for i := 0; i < 2; i++ {
		params := validReceiptParams(t, "60")
		params.MemberID = member
		params.ContributionPeriod = pgtype.Date{Time: time.Now().UTC(), Valid: true}
		params.ExternalTransactionID = text(fmt.Sprintf("concurrent-%d", i))
		receipt, err := service.CreateReceipt(ctx, params, nil)
		if err != nil {
			t.Fatal(err)
		}
		receipts = append(receipts, receipt)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, receipt := range receipts {
		wg.Go(func() { _, err := service.ProcessReceipt(ctx, receipt.Receipt.ID, pgtype.UUID{}); results <- err })
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrMembershipFeeMismatch) {
			t.Fatal(err)
		}
	}
	var total string
	if err := pool.QueryRow(ctx, `SELECT SUM(amount)::text FROM contribution_allocations WHERE status='completed'`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if successes != 1 || total != "60.0000" {
		t.Fatalf("successes=%d total=%s", successes, total)
	}
}

func contributionTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Skipf("Docker unavailable: %v", r)
		}
	}()
	ctx := t.Context()
	container, err := postgres.Run(ctx, "postgres:17-alpine", postgres.WithDatabase("caritas_test"), postgres.WithUsername("caritas"), postgres.WithPassword("caritas"), postgres.BasicWaitStrategies())
	if err != nil {
		t.Skipf("Docker unavailable: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(container) })
	uri, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS pgcrypto; CREATE TABLE "user" (id TEXT PRIMARY KEY,email TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		up := strings.Split(string(body), "-- +goose Down")[0]
		if _, err := pool.Exec(ctx, up); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
	return pool
}
