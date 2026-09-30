package main

import (
	"context"
	"errors"
	"log"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yaninyzwitty/caritas-backend/config"
	"github.com/yaninyzwitty/caritas-backend/internal/member"
	"github.com/yaninyzwitty/caritas-backend/internal/repository/sqlc"
)

func main() {
	ctx := context.Background()
	databaseURL, err := config.GetDatabaseURL()
	if err != nil {
		log.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("ping database: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO member_branch_counters (branch_id, next_member_number) VALUES (1, 1) ON CONFLICT DO NOTHING`); err != nil {
		log.Fatalf("seed default branch: %v", err)
	}

	members := member.NewService(member.NewStore(pool))
	// Fictional profiles; contact details are illustrative, not verified recipients.
	profiles := []struct {
		name, nationalID, phone, email, address, born, occupation, employer string
		income                                                           int64
		kin, kinPhone, relationship                                       string
	}{
		{"Peter Mwangi Kamau", "24871635", "+254722684193", "petermkamau82@gmail.com", "Githurai 45, Ruiru, Kiambu", "1982-04-17", "Hardware shop owner", "Kamau Hardware Supplies", 85000, "Lucy Wanjiku Kamau", "+254720936482", "spouse"},
		{"Grace Wanjiku Njoroge", "31749268", "+254710593827", "grace.wnjoroge@gmail.com", "Kahawa West, Nairobi", "1991-08-23", "Primary school teacher", "Tumaini Academy", 58000, "Daniel Maina Njoroge", "+254724861539", "spouse"},
		{"Joseph Otieno Ochieng", "27683519", "+254733418652", "joseph.o.ochieng@yahoo.com", "Umoja II, Nairobi", "1986-11-09", "Electrician", "Self-employed", 72000, "Agnes Achieng Ochieng", "+254735297164", "spouse"},
		{"Mary Wambui Muthoni", "35419682", "+254795263814", "mary.w.muthoni@gmail.com", "Ruaka, Kiambu", "1995-02-14", "Registered nurse", "Neema Community Clinic", 68000, "Esther Muthoni Wairimu", "+254712649385", "mother"},
		{"Abdi Hassan Yusuf", "29368417", "+254748192635", "abdi.hyusuf@gmail.com", "South C, Nairobi", "1988-06-28", "Wholesale trader", "Hassan Household Supplies", 125000, "Amina Hassan Yusuf", "+254721583946", "sister"},
		{"Faith Chebet Kiptoo", "32857146", "+254701846293", "faith.chebet.k@gmail.com", "Ruiru, Kiambu", "1992-09-05", "Accountant", "Baraka Logistics", 96000, "David Kiptoo Cheruiyot", "+254728394561", "spouse"},
		{"Samuel Mutua Musyoka", "26194378", "+254725937184", "samuel.musyoka84@yahoo.com", "Mlolongo, Machakos", "1984-12-19", "Transport operator", "Mutua Transport Services", 110000, "Ruth Mwende Mutua", "+254734618295", "spouse"},
		{"Mercy Akinyi Odhiambo", "34276851", "+254768425913", "mercy.akinyi.o@gmail.com", "Donholm, Nairobi", "1994-03-11", "Pharmacist", "Uzima Pharmacy", 82000, "Rose Atieno Odhiambo", "+254719682347", "mother"},
		{"Daniel Karanja Waweru", "23581764", "+254727154869", "daniel.k.waweru@gmail.com", "Kikuyu, Kiambu", "1979-07-30", "Dairy farmer", "Self-employed", 78000, "Jane Nyambura Waweru", "+254711398652", "spouse"},
		{"Caroline Muthoni Gitau", "30946285", "+254790638251", "caroline.mgitau@gmail.com", "Kasarani, Nairobi", "1990-10-22", "Salon owner", "Muthoni Beauty Studio", 64000, "Stephen Gitau Njuguna", "+254723875416", "brother"},
	}
	nationalIDs := make([]string, 0, len(profiles))
	for _, p := range profiles {
		nationalIDs = append(nationalIDs, p.nationalID)
		born, err := time.Parse("2006-01-02", p.born)
		if err != nil {
			log.Fatal(err)
		}
		seeded, err := members.GetMemberByNationalID(ctx, member.DefaultBranchID, p.nationalID)
		if errors.Is(err, pgx.ErrNoRows) {
			seeded, err = members.RegisterMember(ctx, member.DefaultBranchID, p.nationalID, sqlc.CreateMemberProfileParams{
				FullName: p.name, Phone: p.phone, Email: p.email,
				Address:               pgtype.Text{String: p.address, Valid: true},
				DateOfBirth:            pgtype.Date{Time: born, Valid: true},
				Occupation:            pgtype.Text{String: p.occupation, Valid: true},
				Employer:              pgtype.Text{String: p.employer, Valid: true},
				MonthlyIncome:         pgtype.Numeric{Int: big.NewInt(p.income), Valid: true},
				IDDocumentType:        pgtype.Text{String: "national_id", Valid: true},
				IDDocumentNumber:      pgtype.Text{String: p.nationalID, Valid: true},
				NextOfKinName:         pgtype.Text{String: p.kin, Valid: true},
				NextOfKinPhone:        pgtype.Text{String: p.kinPhone, Valid: true},
				NextOfKinRelationship: pgtype.Text{String: p.relationship, Valid: true},
			})
		}
		if err != nil {
			log.Fatalf("seed member %q: %v", p.nationalID, err)
		}
		if seeded.Status == "pending" {
			if _, err := members.UpdateMemberStatus(ctx, seeded.ID, "active", "Membership application approved after identity verification"); err != nil {
				log.Fatalf("activate member %q: %v", p.nationalID, err)
			}
		}
	}
	if _, err := pool.Exec(ctx, seedData, nationalIDs); err != nil {
		log.Fatalf("seed dependent tables: %v", err)
	}
	log.Print("seed complete")
}

// seedData is one PostgreSQL statement, so every dependent-table write is atomic.
// Fixed IDs and conflict handling make reruns safe without deleting existing data.
// Financial history is a September 2026 snapshot imported for these new profiles.
const seedData = `
WITH
member_seed AS (
 SELECT n, national_id FROM unnest($1::text[]) WITH ORDINALITY AS s(national_id,n)
),
seed_members AS (
 SELECT s.n, m.id, (ARRAY[5000,7500,6000,4000,10000,8000,12000,9000,5500,11000])[s.n] monthly_shares
 FROM member_seed s JOIN members m
 ON m.branch_id=1 AND m.national_id=s.national_id AND NOT m.is_deleted
),
staff_inserted AS (
 INSERT INTO staff_users (id,branch_id,email,role,name) VALUES
 ('8d5d0b02-c7dc-4cab-b884-6969966ca8d7',1,'james.kariuki@caritas.co.ke','manager','James Kariuki Mbugua'),
 ('e398f627-decc-4db7-a681-0e4a1cedd6ef',1,'anne.njeri@caritas.co.ke','cashier','Anne Njeri Mwangi'),
 ('76c21d22-5f45-4efa-b750-45920f6f20a3',1,'beatrice.auma@caritas.co.ke','auditor','Beatrice Auma Oloo')
 ON CONFLICT DO NOTHING RETURNING id,email
),
staff AS (
 SELECT id,email FROM staff_users WHERE email IN (
  'james.kariuki@caritas.co.ke','anne.njeri@caritas.co.ke','beatrice.auma@caritas.co.ke'
 )
 UNION ALL SELECT id,email FROM staff_inserted
),
account_seed AS (
 SELECT n,('8b9cd0ce-5f85-4834-8045-'||right(md5(n::text),12))::uuid id FROM generate_series(1,10) n
),
accounts_inserted AS (
 INSERT INTO share_accounts (id,member_id,branch_id,opened_at,created_at)
 SELECT s.id,m.id,1,'2026-02-02 09:00+03','2026-02-02 09:00+03' FROM account_seed s JOIN seed_members m USING(n)
 WHERE NOT EXISTS (SELECT 1 FROM share_accounts a WHERE a.member_id=m.id AND NOT a.is_deleted)
 ON CONFLICT DO NOTHING RETURNING id,member_id
),
accounts AS (
 SELECT m.n,m.monthly_shares,COALESCE(a.id,i.id) id FROM seed_members m JOIN account_seed s USING(n)
 LEFT JOIN LATERAL (SELECT id FROM share_accounts WHERE member_id=m.id AND NOT is_deleted ORDER BY created_at,id LIMIT 1) a ON TRUE
 LEFT JOIN accounts_inserted i ON i.member_id=m.id
 WHERE COALESCE(a.id,i.id) IS NOT NULL
),
share_tx AS (
 INSERT INTO share_transactions (id,share_account_id,type,amount,balance_after,reference_id,reason,originator_id,created_at)
 SELECT ('60418300-d869-49a7-a2af-'||right(md5((a.n||':'||i)::text),12))::uuid,a.id,'purchase',a.monthly_shares,a.monthly_shares*i,
 ('77e0f098-e3cf-46f3-8798-'||right(md5((a.n||':'||i)::text),12))::uuid,'Monthly member share contribution',
 (SELECT id FROM staff WHERE email='anne.njeri@caritas.co.ke'),
 timestamptz '2026-02-15 10:30+03'+(i-1)*interval '1 month'
 FROM accounts a CROSS JOIN generate_series(1,8) i
 ON CONFLICT DO NOTHING RETURNING id
),
loan_seed AS (
 SELECT n,('6c789999-fd91-4abf-a238-'||right(md5(n::text),12))::uuid id,
 (ARRAY[60000,120000,90000,48000,120000])[n]::numeric principal FROM generate_series(1,5) n
),
seed_loans AS (
 INSERT INTO loans (id,member_id,branch_id,principal,interest_rate,repayment_period_months,status,disbursed_at,created_at,updated_by)
 SELECT l.id,m.id,1,l.principal,1,12,'active','2026-08-20 11:00+03','2026-08-18 09:00+03',
 (SELECT id FROM staff WHERE email='james.kariuki@caritas.co.ke') FROM loan_seed l JOIN seed_members m USING(n)
 ON CONFLICT DO NOTHING RETURNING id
),
loans_ready AS (
 SELECT s.n,s.principal,COALESCE(l.id,i.id) id FROM loan_seed s
 LEFT JOIN loans l ON l.id=s.id
 LEFT JOIN seed_loans i ON i.id=s.id
 WHERE COALESCE(l.id,i.id) IS NOT NULL
),
schedules AS (
 INSERT INTO repayment_schedules (id,loan_id,installment_no,due_date,amount_due,status)
 SELECT ('855cb0c6-b578-4201-b781-'||right(md5((l.n||':'||i)::text),12))::uuid,l.id,i,
 (date '2026-09-01'+i*interval '1 month'-interval '1 day')::date,l.principal/12,
 CASE i WHEN 1 THEN 'paid'::repayment_schedule_status ELSE 'upcoming'::repayment_schedule_status END
 FROM loans_ready l CROSS JOIN generate_series(1,12) i ON CONFLICT DO NOTHING RETURNING id
),
loan_tx AS (
 INSERT INTO loan_transactions (id,loan_id,type,amount,reference_id,allocation_breakdown,created_by,created_at)
 SELECT ('00646b2c-3b9d-4fa2-909f-'||right(md5(((l.n-1)*2+k)::text),12))::uuid,l.id,
 CASE k WHEN 1 THEN 'disbursement'::loan_transaction_type ELSE 'repayment'::loan_transaction_type END,
 CASE k WHEN 1 THEN l.principal ELSE l.principal/12+l.principal*0.01 END,
 ('2ad1a132-9efa-4227-904f-'||right(md5(((l.n-1)*2+k)::text),12))::uuid,
 CASE k WHEN 1 THEN '{}'::jsonb ELSE jsonb_build_object('principal',l.principal/12,'interest',l.principal*0.01) END,
 (SELECT id FROM staff WHERE email='james.kariuki@caritas.co.ke'),
 CASE k WHEN 1 THEN timestamptz '2026-08-20 11:00+03' ELSE timestamptz '2026-09-15 10:30+03' END
 FROM loans_ready l CROSS JOIN generate_series(1,2) k ON CONFLICT DO NOTHING RETURNING id
),
loan_audits AS (
 INSERT INTO loan_audit_trails (id,loan_id,field_changed,previous_value,new_value,changed_by,change_reason,created_at)
 SELECT ('c4761bb7-5a68-43dd-9f7b-'||right(md5((l.n||':'||k)::text),12))::uuid,l.id,'status',
 CASE k WHEN 1 THEN 'approved' ELSE 'disbursed' END,CASE k WHEN 1 THEN 'disbursed' ELSE 'active' END,
 (SELECT id FROM staff WHERE email='james.kariuki@caritas.co.ke'),
 CASE k WHEN 1 THEN 'Approved loan disbursed after collateral verification' ELSE 'First monthly repayment received' END,
 CASE k WHEN 1 THEN timestamptz '2026-08-20 11:00+03' ELSE timestamptz '2026-09-15 10:30+03' END
 FROM loans_ready l CROSS JOIN generate_series(1,2) k ON CONFLICT DO NOTHING RETURNING id
),
guarantors AS (
 INSERT INTO loan_guarantors (loan_id,guarantor_id,guaranteed_amount,status,approved_at,approved_by,created_at)
 SELECT l.id,m.id,l.principal-a.monthly_shares*6,'approved','2026-08-19 14:00+03',
 (SELECT id FROM staff WHERE email='james.kariuki@caritas.co.ke'),'2026-08-18 10:00+03'
 FROM loans_ready l JOIN accounts a USING(n) JOIN seed_members m ON m.n=l.n+5 ON CONFLICT DO NOTHING RETURNING loan_id
),
pledges AS (
 INSERT INTO share_pledges (id,share_account_id,loan_id,pledged_amount,type,status,approved_by,approved_at,created_at)
 SELECT ('3d05d317-6249-4def-b8c4-'||right(md5(a.n::text),12))::uuid,a.id,l.id,
 CASE WHEN a.n=l.n THEN borrower.monthly_shares*6 ELSE l.principal-borrower.monthly_shares*6 END,
 CASE WHEN a.n=l.n THEN 'applicant_security'::share_pledge_type ELSE 'guarantor_security'::share_pledge_type END,
 'active',(SELECT id FROM staff WHERE email='james.kariuki@caritas.co.ke'),'2026-08-19 14:00+03','2026-08-18 10:00+03'
 FROM loans_ready l JOIN accounts borrower USING(n) JOIN accounts a ON a.n IN (l.n,l.n+5)
 ON CONFLICT DO NOTHING RETURNING id
),
adjustments AS (
 INSERT INTO share_adjustments (id,share_account_id,amount,reference_id,requested_by,reason)
 SELECT ('88128ccb-d373-45e6-997e-'||right(md5(a.n::text),12))::uuid,a.id,500,
 ('48a06af9-059e-467d-95bc-'||right(md5(a.n::text),12))::uuid,
 (SELECT id FROM staff WHERE email='beatrice.auma@caritas.co.ke'),'Correction of underposted share contribution; reconciliation report REC-2026-0918'
 FROM accounts a WHERE a.n=3 ON CONFLICT DO NOTHING RETURNING id
),
sessions AS (
 INSERT INTO cashier_sessions (id,branch_id,cashier_id,status,expected_amount,counted_amount,variance,opened_at,closed_at,
 closed_by,handed_over_at,handed_over_to,deposited_at)
 SELECT 'b3b9a1cc-eadd-45c7-833b-adc5527c769c',1,c.id,'deposited',29300,29300,0,'2026-09-15 08:00+03',
 '2026-09-15 16:30+03',c.id,'2026-09-15 17:00+03',m.id,'2026-09-16 09:30+03'
 FROM staff c CROSS JOIN staff m WHERE c.email='anne.njeri@caritas.co.ke' AND m.email='james.kariuki@caritas.co.ke'
 ON CONFLICT DO NOTHING RETURNING id
),
receipt_seed AS (
 SELECT l.n,('a52c0268-9d9b-4c6f-8127-'||right(md5(l.n::text),12))::uuid id,
 a.monthly_shares+l.principal/12+l.principal*0.01 total,
 jsonb_build_object('share_purchase',a.monthly_shares,'loan_principal',l.principal/12,'loan_interest',l.principal*0.01) plan,
 (ARRAY[NULL,NULL,'UIF7K9R2WX','UIF3M8N6QP','UIF9H4T7CV'])[l.n] external_id,
 CASE WHEN l.n>2 THEN 'ws_CO_150920261030'||(238417+l.n) END checkout
 FROM loans_ready l JOIN accounts a USING(n)
),
receipts AS (
 INSERT INTO contribution_receipts (id,source_channel,external_transaction_id,checkout_request_id,member_id,branch_id,contribution_period,
 received_amount,allocation_plan,status,received_by,received_at,idempotency_key,internal_receipt_reference,cashier_session_id)
 SELECT r.id,CASE WHEN r.n<=2 THEN 'cash'::contribution_source_channel ELSE 'daraja_stk'::contribution_source_channel END,
 r.external_id,r.checkout,m.id,1,'2026-09-01',r.total,r.plan,'completed',
 (SELECT id FROM staff WHERE email='anne.njeri@caritas.co.ke'),'2026-09-15 10:30+03','contribution-20260915-'||(1846+r.n),
 CASE WHEN r.n<=2 THEN 'CR/2026/09/'||(1846+r.n) END,
 CASE WHEN r.n<=2 THEN COALESCE((SELECT id FROM sessions),'b3b9a1cc-eadd-45c7-833b-adc5527c769c'::uuid) END
 FROM receipt_seed r JOIN seed_members m USING(n) ON CONFLICT DO NOTHING RETURNING id
),
receipts_ready AS (
 SELECT s.n,s.total,s.plan,s.external_id,s.checkout,COALESCE(r.id,i.id) id FROM receipt_seed s
 LEFT JOIN contribution_receipts r ON r.id=s.id
 LEFT JOIN receipts i ON i.id=s.id
 WHERE COALESCE(r.id,i.id) IS NOT NULL
),
allocations AS (
 INSERT INTO contribution_allocations (id,receipt_id,type,target_id,amount,status,authoritative_reference_id,external_reference)
 SELECT ('77e0f098-e3cf-46f3-8798-'||right(md5((r.n||':'||(k+7))::text),12))::uuid,r.id,
 (ARRAY['share_purchase','loan_principal','loan_interest']::contribution_allocation_type[])[k],
 CASE k WHEN 1 THEN a.id ELSE l.id END,
 CASE k WHEN 1 THEN a.monthly_shares WHEN 2 THEN l.principal/12 ELSE l.principal*0.01 END,'completed',
 CASE k WHEN 1 THEN ('60418300-d869-49a7-a2af-'||right(md5((r.n||':8')::text),12))::uuid
 ELSE ('00646b2c-3b9d-4fa2-909f-'||right(md5((r.n*2)::text),12))::uuid END,
 COALESCE(r.external_id,'CR/2026/09/'||(1846+r.n))
 FROM receipts_ready r JOIN accounts a USING(n) JOIN loans_ready l USING(n) CROSS JOIN generate_series(1,3) k
 ON CONFLICT DO NOTHING RETURNING id
),
payment_requests AS (
 INSERT INTO contribution_payment_requests (id,checkout_request_id,idempotency_key,member_id,branch_id,contribution_period,
 expected_amount,allocation_plan,status,receipt_id,requested_by)
 SELECT ('bf019d81-8fbe-47f9-a7ed-'||right(md5(r.n::text),12))::uuid,r.checkout,'contribution-20260915-'||(1846+r.n),
 m.id,1,'2026-09-01',r.total,r.plan,
 'completed',r.id,(SELECT id FROM staff WHERE email='james.kariuki@caritas.co.ke')
 FROM receipts_ready r JOIN seed_members m USING(n) WHERE r.n>2 ON CONFLICT DO NOTHING RETURNING id
),
deposits AS (
 INSERT INTO cash_deposits (id,branch_id,amount,bank_reference,status,recorded_by,verified_by,verified_at)
 SELECT 'c935406e-cf34-44c9-9063-beb207476118',1,29300,'COOP/CBD/20260916/738291','verified',m.id,a.id,'2026-09-16 14:00+03'
 FROM staff m CROSS JOIN staff a WHERE m.email='james.kariuki@caritas.co.ke' AND a.email='beatrice.auma@caritas.co.ke'
 ON CONFLICT DO NOTHING RETURNING id
),
deposit_sessions AS (
 INSERT INTO cash_deposit_sessions (deposit_id,session_id)
 SELECT d.id,s.id FROM deposits d CROSS JOIN sessions s ON CONFLICT DO NOTHING RETURNING deposit_id
)
SELECT count(*) FROM seed_members;
`
