package contribution

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/yaninyzwitty/caritas-backend/internal/ceepdb"
)

// TestCEEPReportIntegration exercises real historical SQL and snapshot constraints;
// mocks cannot detect aggregation fan-out, wrong cutoffs or partial snapshot saves.
// This PostgreSQL/Docker test is for CI only, not local execution.
func TestCEEPReportIntegration(t *testing.T) {
	pool := contributionTestDB(t)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `
        INSERT INTO members (id,branch_id,member_number,national_id,status,created_at) VALUES
          ('00000000-0000-0000-0000-000000000001',1,173,'ceep-paid','active','2020-01-01'),
          ('00000000-0000-0000-0000-000000000002',1,174,'ceep-unpaid','active','2020-01-01'),
          ('00000000-0000-0000-0000-000000000003',2,173,'ceep-other-branch','active','2020-01-01'),
          ('00000000-0000-0000-0000-000000000004',1,175,'ceep-closed-later','closed','2020-01-01'),
          ('00000000-0000-0000-0000-000000000005',1,176,'ceep-activated-later','active','2020-01-01');
        INSERT INTO member_status_history (member_id,from_status,to_status,created_at) VALUES
          ('00000000-0000-0000-0000-000000000004','active','closed','2026-05-01'),
          ('00000000-0000-0000-0000-000000000005','pending','active','2026-05-01');
        INSERT INTO member_profiles (member_id,full_name,phone,email) VALUES
          ('00000000-0000-0000-0000-000000000001','CEEP Member','0700000000','ceep@example.test');
        INSERT INTO staff_users (id,branch_id,email,name,role) VALUES
          ('10000000-0000-0000-0000-000000000001',1,'ceep-manager@example.test','Manager','manager');
        INSERT INTO cashier_sessions (id,branch_id,cashier_id) VALUES
          ('90000000-0000-0000-0000-000000000001',1,'10000000-0000-0000-0000-000000000001');
        INSERT INTO share_accounts (id,member_id,branch_id,opened_at) VALUES
          ('20000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000001',1,'2020-01-01');
        INSERT INTO loans (id,member_id,branch_id,principal,interest_rate,repayment_period_months,status,disbursed_at,created_at) VALUES
          ('30000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000001',1,110000,0.01,36,'active','2026-03-01','2026-03-01'),
          ('30000000-0000-0000-0000-000000000002','00000000-0000-0000-0000-000000000001',1,10000,0.01,36,'active','2026-04-01','2026-04-01');
        INSERT INTO loan_transactions (id,loan_id,type,amount,reference_id,allocation_breakdown,created_at) VALUES
          ('40000000-0000-0000-0000-000000000001','30000000-0000-0000-0000-000000000001','disbursement',110000,gen_random_uuid(),'{}','2026-03-01'),
          ('40000000-0000-0000-0000-000000000002','30000000-0000-0000-0000-000000000001','repayment',10000,gen_random_uuid(),'{"period":"2026-03-01","principal":"10000","interest":"0","penalty":"0","credit":"0"}','2026-03-20'),
          ('40000000-0000-0000-0000-000000000003','30000000-0000-0000-0000-000000000002','disbursement',10000,gen_random_uuid(),'{}','2026-04-01'),
          ('40000000-0000-0000-0000-000000000004','30000000-0000-0000-0000-000000000001','repayment',6000,gen_random_uuid(),'{"period":"2026-04-01","principal":"5000","interest":"1000","penalty":"0","credit":"0"}','2026-04-10'),
          ('40000000-0000-0000-0000-000000000005','30000000-0000-0000-0000-000000000002','repayment',50,gen_random_uuid(),'{"period":"2026-04-01","principal":"0","interest":"50","penalty":"0","credit":"0"}','2026-04-20'),
          ('40000000-0000-0000-0000-000000000006','30000000-0000-0000-0000-000000000001','repayment',500,gen_random_uuid(),'{"period":"2026-04-01","principal":"500","interest":"0","penalty":"0","credit":"0"}','2026-04-25');
        INSERT INTO loan_monthly_interest (loan_id,period,previous_balance,rate,amount,created_at) VALUES
          ('30000000-0000-0000-0000-000000000001','2026-04-01',100000,0.01,1000,'2026-04-01'),
          ('30000000-0000-0000-0000-000000000002','2026-04-01',10000,0.01,100,'2026-04-01');
        INSERT INTO share_transactions (id,share_account_id,type,amount,balance_after,reference_id,created_at) VALUES
          ('50000000-0000-0000-0000-000000000001','20000000-0000-0000-0000-000000000001','purchase',333100,333100,gen_random_uuid(),'2026-03-01'),
          ('50000000-0000-0000-0000-000000000002','20000000-0000-0000-0000-000000000001','purchase',1000,334100,gen_random_uuid(),'2026-04-10'),
          ('50000000-0000-0000-0000-000000000003','20000000-0000-0000-0000-000000000001','purchase',250,334350,gen_random_uuid(),'2026-04-20'),
          ('50000000-0000-0000-0000-000000000004','20000000-0000-0000-0000-000000000001','dividend',100,334450,gen_random_uuid(),'2026-04-29');
        INSERT INTO contribution_monthly_fees (member_id,period,type,created_at) VALUES
          ('00000000-0000-0000-0000-000000000001','2026-04-01','com','2026-04-01'),
          ('00000000-0000-0000-0000-000000000001','2026-04-01','lgom','2026-04-01');
        INSERT INTO contribution_charges (id,idempotency_key,member_id,branch_id,category,amount,reason,created_by,created_at) VALUES
          ('80000000-0000-0000-0000-000000000001','ceep-literature','00000000-0000-0000-0000-000000000001',1,'literature',500,'Books','10000000-0000-0000-0000-000000000001','2026-04-01');
        INSERT INTO contribution_receipts (id,source_channel,member_id,branch_id,contribution_period,received_amount,allocation_plan,status,received_at,created_at,updated_at,internal_receipt_reference,idempotency_key,cashier_session_id,received_by) VALUES
          ('60000000-0000-0000-0000-000000000001','cash','00000000-0000-0000-0000-000000000001',1,'2026-04-01',7060,'{}','completed','2026-04-10','2026-04-10','2026-04-10','CEEP-1','ceep-1','90000000-0000-0000-0000-000000000001','10000000-0000-0000-0000-000000000001'),
          ('60000000-0000-0000-0000-000000000002','cash','00000000-0000-0000-0000-000000000001',1,'2026-04-01',300,'{}','completed','2026-04-20','2026-04-20','2026-04-20','CEEP-2','ceep-2','90000000-0000-0000-0000-000000000001','10000000-0000-0000-0000-000000000001'),
          ('60000000-0000-0000-0000-000000000003','cash','00000000-0000-0000-0000-000000000001',1,'2026-04-01',500,'{}','completed','2026-04-25','2026-04-25','2026-04-25','CEEP-3','ceep-3','90000000-0000-0000-0000-000000000001','10000000-0000-0000-0000-000000000001'),
          ('60000000-0000-0000-0000-000000000004','cash','00000000-0000-0000-0000-000000000001',1,'2026-04-01',100,'{}','failed','2026-04-26','2026-04-26','2026-04-26','CEEP-FAILED','ceep-failed','90000000-0000-0000-0000-000000000001','10000000-0000-0000-0000-000000000001'),
          ('60000000-0000-0000-0000-000000000005','cash','00000000-0000-0000-0000-000000000001',1,'2026-04-01',200,'{}','pending','2026-04-27','2026-04-27','2026-04-27','CEEP-PENDING','ceep-pending','90000000-0000-0000-0000-000000000001','10000000-0000-0000-0000-000000000001'),
          ('60000000-0000-0000-0000-000000000006','cash','00000000-0000-0000-0000-000000000001',1,'2026-04-01',50,'{}','completed','2026-04-28','2026-04-28','2026-06-01','CEEP-LATE','ceep-late','90000000-0000-0000-0000-000000000001','10000000-0000-0000-0000-000000000001');
        INSERT INTO contribution_allocations (receipt_id,type,target_id,amount,status,authoritative_reference_id,created_at,updated_at) VALUES
          ('60000000-0000-0000-0000-000000000001','com',NULL,30,'completed',NULL,'2026-04-10','2026-04-10'),
          ('60000000-0000-0000-0000-000000000001','lgom',NULL,30,'completed',NULL,'2026-04-10','2026-04-10'),
          ('60000000-0000-0000-0000-000000000001','share_purchase','20000000-0000-0000-0000-000000000001',1000,'completed','50000000-0000-0000-0000-000000000002','2026-04-10','2026-04-10'),
          ('60000000-0000-0000-0000-000000000001','loan_principal','30000000-0000-0000-0000-000000000001',6000,'completed','40000000-0000-0000-0000-000000000004','2026-04-10','2026-04-10'),
          ('60000000-0000-0000-0000-000000000002','share_purchase','20000000-0000-0000-0000-000000000001',250,'completed','50000000-0000-0000-0000-000000000003','2026-04-20','2026-04-20'),
          ('60000000-0000-0000-0000-000000000002','loan_principal','30000000-0000-0000-0000-000000000002',50,'completed','40000000-0000-0000-0000-000000000005','2026-04-20','2026-04-20'),
          ('60000000-0000-0000-0000-000000000003','loan_principal','30000000-0000-0000-0000-000000000001',500,'completed','40000000-0000-0000-0000-000000000006','2026-04-25','2026-04-25'),
          ('60000000-0000-0000-0000-000000000004','other_charge','80000000-0000-0000-0000-000000000001',100,'completed','80000000-0000-0000-0000-000000000001','2026-04-26','2026-04-26'),
          ('60000000-0000-0000-0000-000000000005','share_purchase','20000000-0000-0000-0000-000000000001',200,'pending',NULL,'2026-04-27','2026-04-27'),
          ('60000000-0000-0000-0000-000000000006','com',NULL,50,'completed',NULL,'2026-04-28','2026-06-01');
    `); err != nil {
		t.Fatal(err)
	}
	q := ceepdb.New(pool)
	params := ceepdb.BuildCEEPReportParams{BranchID: 1,
		Period: pgtype.Date{Time: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), Valid: true},
		Cutoff: pgtype.Timestamptz{Time: time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC), Valid: true}}
	raw, err := q.BuildCEEPReport(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	rows := report["rows"].([]any)
	if len(rows) != 3 || report["hasErrors"] != false {
		t.Fatalf("population/reconciliation: %s", raw)
	}
	paid := rows[0].(map[string]any)
	for key, expected := range map[string]string{
		"totalPaid": "7860", "principalPaid": "5500", "interestPaid": "1050", "sharesPaid": "1250",
		"previousShares": "333100", "totalShares": "334450", "otherShareMovements": "100",
		"openingPrincipal": "100000", "closingPrincipal": "104500", "principalDisbursed": "120000",
		"interestAssessed": "1100",
		"loanBalance": "110000",
	} {
		var actual pgtype.Numeric
		if err := actual.Scan(paid[key].(string)); err != nil {
			t.Fatal(err)
		}
		if numericToScale(actual, -4).Cmp(numericToScale(mustNumeric(t, expected), -4)) != 0 {
			t.Fatalf("%s = %v, expected %s", key, paid[key], expected)
		}
	}
	if len(paid["loans"].([]any)) != 2 || paid["recNo"] != "BO8001" || paid["memberNumber"] != "173" {
		t.Fatalf("loan/receipt detail: %v", paid)
	}
	for _, row := range rows[1:] {
		if row.(map[string]any)["totalPaid"] != "0" {
			t.Fatalf("unpaid member: %v", row)
		}
	}
	charge := paid["charges"].([]any)[0].(map[string]any)
	if charge["paid"] != "100.0000" || charge["outstanding"] != "400.0000" || paid["literature"] != "0" {
		t.Fatalf("partial receipt settlement must be separate from CEEP cash: %v", paid)
	}
	// Two officers retrying the same month must create one report and one row
	// per member. Each sqlc save is a single atomic statement.
	report["status"] = "finalized"
	raw, err = json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	save := ceepdb.SaveCEEPReportParams{Report: raw, BranchID: 1, Period: params.Period, OfficerID: testUUID("10000000-0000-0000-0000-000000000001")}
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() {
			if err := q.SaveCEEPReport(ctx, save); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	lookup := ceepdb.GetCEEPReportParams{BranchID: 1, Period: params.Period}
	saved, err := q.GetCEEPReport(ctx, lookup)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM ceep_snapshots`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("duplicate snapshots: %d %v", count, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ceep_snapshots SET data = '{}'`); err == nil {
		t.Fatal("snapshot update allowed")
	}
	if _, err := pool.Exec(ctx, `UPDATE ceep_reports SET metadata = '{}'`); err == nil {
		t.Fatal("report update allowed")
	}
	if _, err := pool.Exec(ctx, `UPDATE member_profiles SET full_name = 'Changed later'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO share_transactions (share_account_id,type,amount,balance_after,reference_id,created_at)
        VALUES ('20000000-0000-0000-0000-000000000001','adjustment',50,334500,gen_random_uuid(),'2026-04-30')`); err != nil {
		t.Fatal(err)
	}
	again, err := q.GetCEEPReport(ctx, lookup)
	if err != nil || string(again) != string(saved) {
		t.Fatalf("historical report changed: %v", err)
	}
	lookup.BranchID = 2
	save.BranchID = 2
	if err := q.SaveCEEPReport(ctx, save); err == nil {
		t.Fatal("same member/month saved in a second report")
	}
	if _, err := q.GetCEEPReport(ctx, lookup); err == nil {
		t.Fatal("cross-branch report visible or conflicting save committed partially")
	}
}
