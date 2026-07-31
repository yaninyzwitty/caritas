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
	service := NewService(store)

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
	service := NewService(store)

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
	service := NewService(store)

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
	service := NewService(store)

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
	service := NewService(store)
	createdBy := testUUID("00000000-0000-0000-0000-000000000014")

	for _, status := range []loansqlc.LoanStatus{
		loansqlc.LoanStatusClosed,
		loansqlc.LoanStatusWrittenOff,
		loansqlc.LoanStatusRestructuring,
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

	loanID, err := newUUID()
	if err != nil {
		t.Fatalf("generate loan id: %v", err)
	}
	disbursedAt := pgtype.Timestamptz{}
	if status == loansqlc.LoanStatusDisbursed {
		disbursedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO loans (id, member_id, branch_id, principal, interest_rate, repayment_period_months, status, disbursed_at, updated_by)
		VALUES ($1, $2, 1, 100000, 0, 2, $3, $4, $5)
	`, loanID, memberID, status, disbursedAt, testUUID("00000000-0000-0000-0000-000000000001"))
	if err != nil {
		t.Fatalf("insert loan: %v", err)
	}
	return loanID
}

func createSchedule(t *testing.T, ctx context.Context, pool *pgxpool.Pool, loanID pgtype.UUID, installmentNo int, amount string) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		INSERT INTO repayment_schedules (loan_id, installment_no, due_date, amount_due, status)
		VALUES ($1, $2, CURRENT_DATE + ($2 || ' months')::interval, $3, 'upcoming')
	`, loanID, installmentNo, amount)
	if err != nil {
		t.Fatalf("insert schedule: %v", err)
	}
}

func assertLoanStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, loanID pgtype.UUID, want string) {
	t.Helper()
	var got string
	if err := pool.QueryRow(ctx, "SELECT status::text FROM loans WHERE id = $1", loanID).Scan(&got); err != nil {
		t.Fatalf("query loan status: %v", err)
	}
	if got != want {
		t.Fatalf("loan status = %q, want %q", got, want)
	}
}

func assertScheduleStatuses(t *testing.T, ctx context.Context, pool *pgxpool.Pool, loanID pgtype.UUID, want []string) {
	t.Helper()
	rows, err := pool.Query(ctx, "SELECT status::text FROM repayment_schedules WHERE loan_id = $1 ORDER BY installment_no", loanID)
	if err != nil {
		t.Fatalf("query schedule statuses: %v", err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			t.Fatalf("scan schedule status: %v", err)
		}
		got = append(got, status)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("schedule rows: %v", err)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("schedule statuses = %v, want %v", got, want)
	}
}

func assertCreditCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, loanID pgtype.UUID, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM credit_balances WHERE loan_id = $1", loanID).Scan(&got); err != nil {
		t.Fatalf("query credit count: %v", err)
	}
	if got != want {
		t.Fatalf("credit count = %d, want %d", got, want)
	}
}

func assertCreditAmount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, loanID pgtype.UUID, want string) {
	t.Helper()
	var got string
	if err := pool.QueryRow(ctx, "SELECT amount::text FROM credit_balances WHERE loan_id = $1", loanID).Scan(&got); err != nil {
		t.Fatalf("query credit amount: %v", err)
	}
	if got != want {
		t.Fatalf("credit amount = %q, want %q", got, want)
	}
}

func assertTransactionCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, loanID pgtype.UUID, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM loan_transactions WHERE loan_id = $1", loanID).Scan(&got); err != nil {
		t.Fatalf("query transaction count: %v", err)
	}
	if got != want {
		t.Fatalf("transaction count = %d, want %d", got, want)
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
