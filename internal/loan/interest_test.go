package loan

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	loansqlc "github.com/yaninyzwitty/caritas-backend/internal/loan/repository/sqlc"
)

func TestAllocateRepaymentInterestFirst(t *testing.T) {
	for _, tc := range []struct {
		name, amount, prior, due, principal, interest, credit string
		closed                                                bool
	}{
		{"first month", "6000", "0", "1000", "5000", "1000", "0", false},
		{"second month", "6000", "5000", "950", "5050", "950", "0", false},
		{"third month", "6000", "10050", "899.5", "5100.5", "899.5", "0", false},
		{"partial interest", "600", "0", "1000", "0", "600", "0", false},
		{"remaining interest", "5400", "0", "400", "5000", "400", "0", false},
		{"overpayment", "2000", "99000", "10", "1000", "10", "990", true},
		{"interest prevents closure", "10", "100000", "20", "0", "10", "0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := allocateRepayment(mustNumeric(t, tc.amount), mustNumeric(t, tc.prior), mustNumeric(t, "100000"), mustNumeric(t, tc.due))
			if got.Principal != tc.principal || got.Interest != tc.interest || got.Credit != tc.credit || got.loanClosed != tc.closed {
				t.Fatalf("allocation = %+v", got)
			}
		})
	}
}

func TestMonthlyInterestRepayments(t *testing.T) {
	ctx := t.Context()
	store, pool := repaymentStore(t, ctx)
	service := NewService(store, nil)
	loanID := createRepaymentLoan(t, ctx, pool, loansqlc.LoanStatusActive)
	createSchedule(t, ctx, pool, loanID, 1, "100000")
	if _, err := pool.Exec(ctx, "UPDATE loans SET interest_rate = 0.01 WHERE id = $1", loanID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -3, 0)
	actor := testUUID("00000000-0000-0000-0000-000000000001")
	for _, tc := range []struct {
		ref, amount, interest, principal string
		month                            int
	}{
		{"partial", "600", "600", "0", 0},
		{"remaining", "5400", "400", "5000", 0},
		{"next-month", "6000", "950", "5050", 1},
		{"third-month", "6000", "899.5", "5100.5", 2},
	} {
		period := pgtype.Date{Time: first.AddDate(0, tc.month, 0), Valid: true}
		tx, err := service.RecordRepaymentForPeriod(ctx, loanID, mustNumeric(t, tc.amount), tc.ref, actor, period)
		if err != nil {
			t.Fatal(err)
		}
		var breakdown repaymentAllocation
		if err := json.Unmarshal(tx.AllocationBreakdown, &breakdown); err != nil {
			t.Fatal(err)
		}
		if breakdown.Interest != tc.interest || breakdown.Principal != tc.principal {
			t.Fatalf("%s: %+v", tc.ref, breakdown)
		}
		duplicate, err := service.RecordRepaymentForPeriod(ctx, loanID, mustNumeric(t, tc.amount), tc.ref, actor, period)
		if err != nil || duplicate.ID != tx.ID {
			t.Fatalf("retry: %v, %+v", err, duplicate)
		}
	}
	var principal, balance, assessed string
	err := pool.QueryRow(ctx, `SELECT l.principal::text,
        (l.principal - (SELECT SUM((allocation_breakdown->>'principal')::numeric) FROM loan_transactions WHERE loan_id = l.id))::text,
        (SELECT SUM(amount) FROM loan_monthly_interest WHERE loan_id = l.id)::text
        FROM loans l WHERE id = $1`, loanID).Scan(&principal, &balance, &assessed)
	if err != nil {
		t.Fatal(err)
	}
	if principal != "100000.0000" || balance != "84849.5000" || assessed != "2849.5000" {
		t.Fatalf("principal=%s balance=%s assessed=%s", principal, balance, assessed)
	}
	_, err = service.RecordRepaymentForPeriod(ctx, loanID, mustNumeric(t, "100"), "backdate", actor, pgtype.Date{Time: first, Valid: true})
	if !errors.Is(err, ErrInvalidRepaymentPeriod) {
		t.Fatalf("backdate: %v", err)
	}
}
