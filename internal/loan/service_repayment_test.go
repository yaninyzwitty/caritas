package loan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	loansqlc "github.com/yaninyzwitty/caritas-backend/internal/loan/repository/sqlc"
)

var repaymentMemberNumber int64

func TestRecordRepaymentExactPaymentClosesLoanWithoutCredit(t *testing.T) {
	ctx := context.Background()
	store, pool := repaymentStore(t, ctx)
	service := NewService(store, nil)

	loanID := createRepaymentLoan(t, ctx, pool, loansqlc.LoanStatusActive)
	createSchedule(t, ctx, pool, loanID, 1, "50000")
	createSchedule(t, ctx, pool, loanID, 2, "50000")
	createdBy := testUUID("00000000-0000-0000-0000-000000000010")

	tx, err := service.RecordRepayment(ctx, loanID, mustNumeric(t, "100000"), "exact-1", createdBy)
	if err != nil {
		t.Fatalf("record repayment: %v", err)
	}

	if tx.ID == (pgtype.UUID{}) {
		t.Fatal("expected transaction id")
	}
	assertLoanStatus(t, ctx, pool, loanID, "closed")
	assertScheduleStatuses(t, ctx, pool, loanID, []string{"paid", "paid"})
	assertCreditCount(t, ctx, pool, loanID, 0)
}

func TestRecordRepaymentOverpaymentClosesLoanAndCreatesCredit(t *testing.T) {
	ctx := context.Background()
	store, pool := repaymentStore(t, ctx)
	service := NewService(store, nil)

	loanID := createRepaymentLoan(t, ctx, pool, loansqlc.LoanStatusActive)
	createSchedule(t, ctx, pool, loanID, 1, "150000")
	createdBy := testUUID("00000000-0000-0000-0000-000000000011")

	if _, err := service.RecordRepayment(ctx, loanID, mustNumeric(t, "160000"), "overpay-1", createdBy); err != nil {
		t.Fatalf("record repayment: %v", err)
	}

	assertLoanStatus(t, ctx, pool, loanID, "closed")
	assertScheduleStatuses(t, ctx, pool, loanID, []string{"paid"})
	assertCreditAmount(t, ctx, pool, loanID, "10000.0000")
}

func TestRecordRepaymentPartialPaymentMarksFirstUnpaidSchedulePartial(t *testing.T) {
	ctx := context.Background()
	store, pool := repaymentStore(t, ctx)
	service := NewService(store, nil)

	loanID := createRepaymentLoan(t, ctx, pool, loansqlc.LoanStatusActive)
	createSchedule(t, ctx, pool, loanID, 1, "50000")
	createSchedule(t, ctx, pool, loanID, 2, "50000")
	createdBy := testUUID("00000000-0000-0000-0000-000000000012")

	if _, err := service.RecordRepayment(ctx, loanID, mustNumeric(t, "70000"), "partial-1", createdBy); err != nil {
		t.Fatalf("record repayment: %v", err)
	}

	assertLoanStatus(t, ctx, pool, loanID, "active")
	assertScheduleStatuses(t, ctx, pool, loanID, []string{"paid", "partial"})
	assertCreditCount(t, ctx, pool, loanID, 0)
}

func TestRecordRepaymentDuplicateGatewayIDReturnsOriginalTransaction(t *testing.T) {
	ctx := context.Background()
	store, pool := repaymentStore(t, ctx)
	service := NewService(store, nil)

	loanID := createRepaymentLoan(t, ctx, pool, loansqlc.LoanStatusActive)
	createSchedule(t, ctx, pool, loanID, 1, "100000")
	createdBy := testUUID("00000000-0000-0000-0000-000000000013")

	first, err := service.RecordRepayment(ctx, loanID, mustNumeric(t, "100000"), "duplicate-1", createdBy)
	if err != nil {
		t.Fatalf("record first repayment: %v", err)
	}
	second, err := service.RecordRepayment(ctx, loanID, mustNumeric(t, "100000"), "duplicate-1", createdBy)
	if err != nil {
		t.Fatalf("record duplicate repayment: %v", err)
	}

	if first.ID != second.ID {
		t.Fatalf("expected original transaction id, got %v then %v", first.ID, second.ID)
	}
	assertTransactionCount(t, ctx, pool, loanID, 1)
}

func TestRecordRepaymentRejectsBlockedStatuses(t *testing.T) {
	ctx := context.Background()
	store, pool := repaymentStore(t, ctx)
	service := NewService(store, nil)
	createdBy := testUUID("00000000-0000-0000-0000-000000000014")

	for _, status := range []loansqlc.LoanStatus{
		loansqlc.LoanStatusPending,
		loansqlc.LoanStatusApproved,
		loansqlc.LoanStatusRejected,
		loansqlc.LoanStatusClosed,
		loansqlc.LoanStatusWrittenOff,
		loansqlc.LoanStatusRestructuring,
		loansqlc.LoanStatusManualReview,
	} {
		t.Run(string(status), func(t *testing.T) {
			loanID := createRepaymentLoan(t, ctx, pool, status)
			createSchedule(t, ctx, pool, loanID, 1, "100000")

			_, err := service.RecordRepayment(ctx, loanID, mustNumeric(t, "100000"), "blocked-"+string(status), createdBy)
			if !errors.Is(err, ErrPaymentNotAllowed) {
				t.Fatalf("expected ErrPaymentNotAllowed, got %v", err)
			}
			assertTransactionCount(t, ctx, pool, loanID, 0)
		})
	}
}

func TestAllocateRepaymentExactPayment(t *testing.T) {
	schedules := []loansqlc.RepaymentSchedule{
		{AmountDue: mustNumeric(t, "50000")},
		{AmountDue: mustNumeric(t, "50000")},
	}

	allocation := allocateRepayment(mustNumeric(t, "100000"), mustNumeric(t, "0"), schedules)

	if allocation.Principal != "100000" || allocation.Credit != "0" || !allocation.loanClosed {
		t.Fatalf("allocation = %+v", allocation)
	}
}

func TestAllocateRepaymentOverpayment(t *testing.T) {
	schedules := []loansqlc.RepaymentSchedule{
		{AmountDue: mustNumeric(t, "150000")},
	}

	allocation := allocateRepayment(mustNumeric(t, "160000"), mustNumeric(t, "0"), schedules)

	if allocation.Principal != "150000" || allocation.Credit != "10000" || !allocation.loanClosed {
		t.Fatalf("allocation = %+v", allocation)
	}
}

func TestAllocateRepaymentPartialPayment(t *testing.T) {
	schedules := []loansqlc.RepaymentSchedule{
		{AmountDue: mustNumeric(t, "50000")},
		{AmountDue: mustNumeric(t, "50000")},
	}

	allocation := allocateRepayment(mustNumeric(t, "70000"), mustNumeric(t, "0"), schedules)

	if allocation.Principal != "70000" || allocation.Credit != "0" || allocation.loanClosed {
		t.Fatalf("allocation = %+v", allocation)
	}
}

func repaymentStore(t *testing.T, ctx context.Context) (*Store, *pgxpool.Pool) {
	t.Helper()

	container, err := startPostgresContainer(t, ctx)
	if err != nil {
		t.Skipf("skipping Testcontainers-backed repayment test: Docker is unavailable: %v", err)
	}
	t.Cleanup(func() {
		_ = testcontainers.TerminateContainer(container)
	})

	connString, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := applyMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return NewStore(pool), pool
}

func startPostgresContainer(t *testing.T, ctx context.Context) (container *postgres.PostgresContainer, err error) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Skipf("skipping Testcontainers-backed repayment test: Docker is unavailable: %v", recovered)
		}
	}()
	return postgres.Run(ctx,
		"postgres:17-alpine",
		postgres.WithDatabase("caritas_test"),
		postgres.WithUsername("caritas"),
		postgres.WithPassword("caritas"),
		postgres.BasicWaitStrategies(),
	)
}

func applyMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS pgcrypto"); err != nil {
		return err
	}

	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.sql"))
	if err != nil {
		return err
	}
	sort.Strings(files)
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		up := strings.Split(string(body), "-- +goose Down")[0]
		up = strings.Replace(up, "-- +goose Up", "", 1)
		if strings.TrimSpace(up) == "" {
			continue
		}
		if _, err := pool.Exec(ctx, up); err != nil {
			return err
		}
	}
	return nil
}

func createRepaymentLoan(t *testing.T, ctx context.Context, pool *pgxpool.Pool, status loansqlc.LoanStatus) pgtype.UUID {
	t.Helper()
	q := loansqlc.New(pool)

	memberID, err := newUUID()
	if err != nil {
		t.Fatalf("generate member id: %v", err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO members (id, branch_id, member_number, national_id, status)
		VALUES ($1, 1, $2, $3, 'active')
	`, memberID, atomic.AddInt64(&repaymentMemberNumber, 1), memberID.String())
	if err != nil {
		t.Fatalf("insert member: %v", err)
	}

	loan, err := q.CreateLoan(ctx, loansqlc.CreateLoanParams{
		MemberID:              memberID,
		BranchID:              1,
		Principal:             mustNumeric(t, "100000"),
		InterestRate:          mustNumeric(t, "0"),
		RepaymentPeriodMonths: 2,
		UpdatedBy:             testUUID("00000000-0000-0000-0000-000000000001"),
	})
	if err != nil {
		t.Fatalf("create loan: %v", err)
	}
	if status != loan.Status {
		if _, err := q.UpdateLoanStatus(ctx, loansqlc.UpdateLoanStatusParams{
			ID:        loan.ID,
			Status:    status,
			UpdatedBy: testUUID("00000000-0000-0000-0000-000000000001"),
		}); err != nil {
			t.Fatalf("update loan status: %v", err)
		}
	}
	return loan.ID
}

func createSchedule(t *testing.T, ctx context.Context, pool *pgxpool.Pool, loanID pgtype.UUID, installmentNo int, amount string) {
	t.Helper()
	_, err := loansqlc.New(pool).CreateRepaymentSchedule(ctx, loansqlc.CreateRepaymentScheduleParams{
		LoanID:        loanID,
		InstallmentNo: int32(installmentNo),
		DueDate:       pgtype.Date{Time: time.Now().AddDate(0, installmentNo, 0), Valid: true},
		AmountDue:     mustNumeric(t, amount),
		Status:        loansqlc.RepaymentScheduleStatusUpcoming,
	})
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
}

func assertLoanStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, loanID pgtype.UUID, want string) {
	t.Helper()
	loan, err := loansqlc.New(pool).GetLoanByID(ctx, loanID)
	if err != nil {
		t.Fatalf("get loan: %v", err)
	}
	if string(loan.Status) != want {
		t.Fatalf("loan status = %q, want %q", loan.Status, want)
	}
}

func assertScheduleStatuses(t *testing.T, ctx context.Context, pool *pgxpool.Pool, loanID pgtype.UUID, want []string) {
	t.Helper()
	schedules, err := loansqlc.New(pool).ListRepaymentSchedulesByLoan(ctx, loanID)
	if err != nil {
		t.Fatalf("list repayment schedules: %v", err)
	}
	got := make([]string, 0, len(schedules))
	for _, schedule := range schedules {
		got = append(got, string(schedule.Status))
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("schedule statuses = %v, want %v", got, want)
	}
}

func assertCreditCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, loanID pgtype.UUID, want int) {
	t.Helper()
	got := len(listCreditsByLoan(t, ctx, pool, loanID))
	if got != want {
		t.Fatalf("credit count = %d, want %d", got, want)
	}
}

func assertCreditAmount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, loanID pgtype.UUID, want string) {
	t.Helper()
	credits := listCreditsByLoan(t, ctx, pool, loanID)
	if len(credits) != 1 {
		t.Fatalf("credit count = %d, want 1", len(credits))
	}

	got := decimalStringFromScale(numericToScale(credits[0].Amount, -4), -4)
	if got != want {
		t.Fatalf("credit amount = %q, want %q", got, want)
	}
}

func assertTransactionCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, loanID pgtype.UUID, want int) {
	t.Helper()
	got := len(listAllLoanTransactions(t, ctx, pool, loanID))
	if got != want {
		t.Fatalf("transaction count = %d, want %d", got, want)
	}
}

func listCreditsByLoan(t *testing.T, ctx context.Context, pool *pgxpool.Pool, loanID pgtype.UUID) []loansqlc.CreditBalance {
	t.Helper()
	credits, err := loansqlc.New(pool).ListCreditBalancesByLoan(ctx, loanID)
	if err != nil {
		t.Fatalf("list credit balances by loan: %v", err)
	}
	return credits
}

func listAllLoanTransactions(t *testing.T, ctx context.Context, pool *pgxpool.Pool, loanID pgtype.UUID) []loansqlc.LoanTransaction {
	t.Helper()
	q := loansqlc.New(pool)
	var all []loansqlc.LoanTransaction
	var cursor LoanCursor

	for {
		transactions, err := q.ListLoanTransactions(ctx, loansqlc.ListLoanTransactionsParams{
			LoanID:  loanID,
			Column2: cursor.CreatedAt,
			ID:      cursor.ID,
			Limit:   100,
		})
		if err != nil {
			t.Fatalf("list loan transactions: %v", err)
		}

		all = append(all, transactions...)
		if len(transactions) < 100 {
			return all
		}

		last := transactions[len(transactions)-1]
		cursor = LoanCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
}

func mustNumeric(t *testing.T, value string) pgtype.Numeric {
	t.Helper()
	numeric, err := parseNumeric(value)
	if err != nil {
		t.Fatalf("parse numeric: %v", err)
	}
	return numeric
}

func testUUID(value string) pgtype.UUID {
	var id pgtype.UUID
	if err := id.Scan(value); err != nil {
		panic(err)
	}
	return id
}
