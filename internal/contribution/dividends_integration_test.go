package contribution

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/yaninyzwitty/caritas-backend/internal/ceepdb"
	"github.com/yaninyzwitty/caritas-backend/internal/share"
	sharesqlc "github.com/yaninyzwitty/caritas-backend/internal/share/repository/sqlc"
)

// TestDividendRegisterIntegration checks real SQL aggregation, decimal rounding,
// frozen eligibility and atomic posting; mocks cannot verify these database rules.
// CI only: do not run this PostgreSQL/Docker test locally.
func TestDividendRegisterIntegration(t *testing.T) {
	pool := contributionTestDB(t)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `
        INSERT INTO members (branch_id,member_number,national_id,status,created_at)
        SELECT 1,n,'dividend-' || n,CASE WHEN n = 3 THEN 'suspended' ELSE 'active' END,'2020-01-01'::timestamptz
        FROM generate_series(1,3) n;
        INSERT INTO staff_users (id,branch_id,email,name,role)
        VALUES ('10000000-0000-0000-0000-000000000001',1,'dividends@example.test','Manager','manager');
        INSERT INTO share_accounts (member_id,branch_id,status,opened_at)
        SELECT id,1,'active','2020-01-01' FROM members;
        INSERT INTO share_transactions (share_account_id,type,amount,balance_after,reference_id,created_at)
        SELECT id,'purchase',123.10,123.10,gen_random_uuid(),'2025-12-31 20:59:59+00' FROM share_accounts;
        WITH payments AS MATERIALIZED (
            SELECT sa.id AS account_id,sa.member_id,gen_random_uuid() AS transaction_id,gen_random_uuid() AS receipt_id,
                   make_date(2026,mo,1) AS period,mo
            FROM share_accounts sa CROSS JOIN generate_series(1,6) mo
        ), txns AS (
            INSERT INTO share_transactions (id,share_account_id,type,amount,balance_after,reference_id,created_at)
            SELECT transaction_id,account_id,'purchase',100,123.10+mo*100,receipt_id,
                   (period::timestamp + INTERVAL '10 days') AT TIME ZONE 'Africa/Nairobi' FROM payments RETURNING id
        ), receipts AS (
            INSERT INTO contribution_receipts (id,source_channel,member_id,branch_id,contribution_period,received_amount,allocation_plan,status,received_at)
            SELECT receipt_id,'manual',member_id,1,period,100,'{}','completed',
                   (period::timestamp + INTERVAL '10 days') AT TIME ZONE 'Africa/Nairobi' FROM payments RETURNING id
        )
        INSERT INTO contribution_allocations (receipt_id,type,target_id,amount,status,authoritative_reference_id)
        SELECT p.receipt_id,'share_purchase',p.account_id,100,'completed',p.transaction_id
        FROM payments p JOIN txns t ON t.id = p.transaction_id JOIN receipts r ON r.id = p.receipt_id;
    `); err != nil {
		t.Fatal(err)
	}
	q := ceepdb.New(pool)
	params := ceepdb.BuildDividendPreviewParams{BranchID: 1, FinancialYear: 2025,
		DeclarationDate: pgtype.Date{Time: time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC), Valid: true}, Rate: mustNumeric(t, "5")}
	raw, err := q.BuildDividendPreview(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		ApprovedTotal string `json:"approvedTotal"`
		Allocations   []struct {
			MemberID     string `json:"memberId"`
			Eligible     bool   `json:"eligible"`
			ShareBalance string `json:"shareBalance"`
			Amount       string `json:"amount"`
		} `json:"allocations"`
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if report.ApprovedTotal != "12.32" || len(report.Allocations) != 3 || !report.Allocations[0].Eligible || report.Allocations[2].Eligible {
		t.Fatalf("eligibility or rounded total: %s", raw)
	}
	if report.Allocations[0].ShareBalance != "123.1000" || report.Allocations[0].Amount != "6.16" {
		t.Fatalf("year-end cutoff or half-cent rounding: %s", raw)
	}
	officer := testUUID("10000000-0000-0000-0000-000000000001")
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	err = NewStore(pool).ExecReceiptTx(ctx, func(tx pgx.Tx) error {
		queries := ceepdb.New(tx)
		declaration, err := queries.SaveDividendDeclaration(ctx, ceepdb.SaveDividendDeclarationParams{
			BranchID: 1, FinancialYear: 2025, DeclarationDate: params.DeclarationDate, Rate: params.Rate,
			ApprovedTotal: mustNumeric(t, "12.32"), ResolutionReference: "AGM-2026", PreviewHash: "reviewed", ApprovedBy: officer,
		})
		if err != nil {
			return err
		}
		return queries.SaveDividendAllocations(ctx, ceepdb.SaveDividendAllocationsParams{DeclarationID: declaration, Allocations: document["allocations"]})
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE members SET status='suspended' WHERE member_number IN (1,2)`); err != nil {
		t.Fatal(err)
	}
	var allocations []pgtype.UUID
	rows, err := pool.Query(ctx, `SELECT a.id FROM dividend_allocations a JOIN members m ON m.id=a.member_id WHERE a.eligible ORDER BY m.member_number`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id pgtype.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		allocations = append(allocations, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if len(allocations) != 2 {
		t.Fatalf("expected two payable allocations: %v", allocations)
	}
	service := share.NewService(share.NewStore(pool))
	for i, id := range allocations {
		err := NewStore(pool).ExecReceiptTx(ctx, func(tx pgx.Tx) error {
			queries := ceepdb.New(tx)
			allocation, err := queries.LockDividendAllocation(ctx, ceepdb.LockDividendAllocationParams{ID: id, BranchID: 1})
			if err != nil {
				return err
			}
			choice := "shares"
			if i == 1 {
				choice = "cash"
			}
			if err := queries.RecordDividendChoice(ctx, ceepdb.RecordDividendChoiceParams{ID: id, Choice: text(choice), ChoiceBy: officer}); err != nil {
				return err
			}
			settlement := ceepdb.SettleDividendAllocationParams{ID: id, SettledBy: officer}
			if choice == "shares" {
				posted, err := service.PostDividendInTx(ctx, sharesqlc.New(tx), allocation.ShareAccountID, allocation.MemberID, 1, allocation.Amount, id, officer)
				if err != nil {
					return err
				}
				retry, err := service.PostDividendInTx(ctx, sharesqlc.New(tx), allocation.ShareAccountID, allocation.MemberID, 1, allocation.Amount, id, officer)
				if err != nil {
					return err
				}
				if retry.ID != posted.ID {
					t.Fatal("retry created another dividend")
				}
				settlement.PostedTransactionID = posted.ID
			} else {
				settlement.VoucherReference = text("SIGNED-001")
				settlement.PaymentDate = params.DeclarationDate
			}
			return queries.SettleDividendAllocation(ctx, settlement)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	saved, err := q.GetDividendRegister(ctx, ceepdb.GetDividendRegisterParams{BranchID: 1, FinancialYear: 2025})
	if err != nil {
		t.Fatal(err)
	}
	var totals struct {
		CashPaid    string `json:"cashPaid"`
		Reinvested  string `json:"reinvested"`
		Outstanding string `json:"outstanding"`
	}
	if err := json.Unmarshal(saved, &totals); err != nil {
		t.Fatal(err)
	}
	if totals.CashPaid != "6.16" || totals.Reinvested != "6.16" || numericToScale(mustNumeric(t, totals.Outstanding), -2).Sign() != 0 {
		t.Fatalf("settlement totals: %s", saved)
	}
	if _, err := q.GetDividendRegister(ctx, ceepdb.GetDividendRegisterParams{BranchID: 2, FinancialYear: 2025}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("branch isolation: %v", err)
	}
	// Payment recorded against arrears must not qualify, even with enough money received.
	if _, err := pool.Exec(ctx, `UPDATE members SET status='active' WHERE member_number IN (1,2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE contribution_receipts SET contribution_period='2025-12-01' WHERE contribution_period='2026-01-01'`); err != nil {
		t.Fatal(err)
	}
	changed, err := q.BuildDividendPreview(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(changed, &report); err != nil {
		t.Fatal(err)
	}
	if report.Allocations[0].Eligible || report.Allocations[1].Eligible {
		t.Fatalf("late arrears qualified: %s", changed)
	}
	var posted int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM share_transactions WHERE type='dividend'`).Scan(&posted); err != nil {
		t.Fatal(err)
	}
	if posted != 1 {
		t.Fatalf("cash or retry created share credit: %d", posted)
	}

	// A second year's unpaid allocation exercises the guards without modifying
	// the settled register above. Declaration and allocation must commit together.
	if _, err := pool.Exec(ctx, `
		INSERT INTO members (id,branch_id,member_number,national_id,status)
		VALUES ('00000000-0000-0000-0000-000000000004',2,1,'dividend-other-branch','active');
		INSERT INTO dividend_declarations (id,branch_id,financial_year,declaration_date,rate,approved_total,resolution_reference,preview_hash,approved_by)
		VALUES ('20000000-0000-0000-0000-000000000004',1,2024,'2026-07-20',5,6.16,'AGM-2024','reviewed','10000000-0000-0000-0000-000000000001');
		INSERT INTO dividend_allocations (id,declaration_id,member_id,branch_id,share_account_id,evidence,eligible,share_balance,amount,choice,choice_by,choice_at)
		SELECT '30000000-0000-0000-0000-000000000004','20000000-0000-0000-0000-000000000004',m.id,1,sa.id,'{}',TRUE,123.10,6.16,
		       'cash','10000000-0000-0000-0000-000000000001',NOW()
		FROM members m JOIN share_accounts sa ON sa.member_id=m.id WHERE m.branch_id=1 AND m.member_number=1;
	`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		query   string
		code    string
		message string
	}{
		{
			"declaration branch mismatch",
			`INSERT INTO dividend_allocations (declaration_id,member_id,branch_id,evidence,eligible,share_balance,amount)
			 VALUES ('20000000-0000-0000-0000-000000000004','00000000-0000-0000-0000-000000000004',2,'{}',FALSE,0,0)`,
			"23503", "",
		},
		{
			"member branch mismatch",
			`INSERT INTO dividend_allocations (declaration_id,member_id,branch_id,evidence,eligible,share_balance,amount)
			 VALUES ('20000000-0000-0000-0000-000000000004','00000000-0000-0000-0000-000000000004',1,'{}',FALSE,0,0)`,
			"23503", "",
		},
		{
			"share account belongs to another member",
			`INSERT INTO dividend_allocations (declaration_id,member_id,branch_id,share_account_id,evidence,eligible,share_balance,amount)
			 SELECT '20000000-0000-0000-0000-000000000004',m.id,1,sa.id,'{}',FALSE,0,0
			 FROM members m CROSS JOIN share_accounts sa JOIN members owner ON owner.id=sa.member_id
			 WHERE m.branch_id=1 AND m.member_number=2 AND owner.branch_id=1 AND owner.member_number=1`,
			"23503", "",
		},
		{
			"declaration immutable",
			`UPDATE dividend_declarations SET resolution_reference='changed' WHERE financial_year=2024 AND branch_id=1`,
			"P0001", "approved dividend declaration is immutable",
		},
		{
			"allocation ID immutable",
			`UPDATE dividend_allocations SET id=gen_random_uuid() WHERE id='30000000-0000-0000-0000-000000000004'`,
			"P0001", "approved dividend allocation is immutable",
		},
		{
			"allocation amount immutable",
			`UPDATE dividend_allocations SET amount=7 WHERE id='30000000-0000-0000-0000-000000000004'`,
			"P0001", "approved dividend allocation is immutable",
		},
		{
			"choice immutable",
			`UPDATE dividend_allocations SET choice='shares' WHERE id='30000000-0000-0000-0000-000000000004'`,
			"P0001", "dividend choice is immutable",
		},
		{
			"settlement immutable",
			`UPDATE dividend_allocations SET voucher_reference='changed' WHERE voucher_reference='SIGNED-001'`,
			"P0001", "settled dividend allocation is immutable",
		},
		{
			"duplicate cash voucher",
			`UPDATE dividend_allocations SET voucher_reference='SIGNED-001',payment_date='2026-07-20',
			 settled_by='10000000-0000-0000-0000-000000000001',settled_at=NOW()
			 WHERE id='30000000-0000-0000-0000-000000000004'`,
			"23505", "",
		},
		{
			"declaration without its allocations",
			`INSERT INTO dividend_declarations (branch_id,financial_year,declaration_date,rate,approved_total,resolution_reference,preview_hash,approved_by)
			 VALUES (1,2023,'2026-07-20',5,1,'AGM-2023','reviewed','10000000-0000-0000-0000-000000000001')`,
			"P0001", "dividend allocations do not equal approved total",
		},
		{
			"extra allocation exceeds approved total",
			`INSERT INTO dividend_allocations (declaration_id,member_id,branch_id,share_account_id,evidence,eligible,share_balance,amount)
			 SELECT '20000000-0000-0000-0000-000000000004',m.id,1,sa.id,'{}',TRUE,20,1
			 FROM members m JOIN share_accounts sa ON sa.member_id=m.id WHERE m.branch_id=1 AND m.member_number=2`,
			"P0001", "dividend allocations do not equal approved total",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			_, err = tx.Exec(ctx, tc.query)
			if err == nil {
				err = tx.Commit(ctx) // Reconciliation is intentionally deferred until commit.
			}
			if rollbackErr := tx.Rollback(ctx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
				t.Fatal(rollbackErr)
			}
			var databaseError *pgconn.PgError
			if !errors.As(err, &databaseError) || databaseError.Code != tc.code || !strings.Contains(databaseError.Message, tc.message) {
				t.Fatalf("expected database error %s containing %q, got %v", tc.code, tc.message, err)
			}
		})
	}
}
