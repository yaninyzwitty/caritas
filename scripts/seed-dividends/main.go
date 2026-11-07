package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yaninyzwitty/caritas-backend/config"
	"github.com/yaninyzwitty/caritas-backend/internal/ceepdb"
	"github.com/yaninyzwitty/caritas-backend/internal/contribution"
	"github.com/yaninyzwitty/caritas-backend/internal/member"
	"github.com/yaninyzwitty/caritas-backend/internal/repository/sqlc"
	"github.com/yaninyzwitty/caritas-backend/internal/share"
	sharesqlc "github.com/yaninyzwitty/caritas-backend/internal/share/repository/sqlc"
)

func main() {
	nairobi := time.FixedZone("Africa/Nairobi", 3*60*60)
	now := time.Now().In(nairobi)
	year := flag.Int("year", now.Year()-1, "Completed financial year to seed")
	branch := flag.Int64("branch", member.DefaultBranchID, "Development branch to seed")
	flag.Parse()
	if *year < 2000 || *year >= now.Year() || *branch <= 0 {
		log.Fatal("choose an ended financial year from 2000 onward and a positive branch")
	}
	ctx := context.Background()
	databaseURL, err := config.GetDatabaseURL()
	if err != nil {
		log.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	resolution := fmt.Sprintf("DEMO-DIVIDENDS-%d-%d", *branch, *year)
	lookup := ceepdb.GetDividendRegisterParams{BranchID: *branch, FinancialYear: int32(*year)}
	saved, err := ceepdb.New(pool).GetDividendRegister(ctx, lookup)
	if err == nil {
		var existing struct {
			ResolutionReference string `json:"resolutionReference"`
		}
		if err := json.Unmarshal(saved, &existing); err != nil {
			log.Fatal(err)
		}
		if existing.ResolutionReference != resolution {
			log.Fatal("this branch and year already have an approved declaration; choose an unused -year or development -branch")
		}
		log.Printf("dividend demo already exists for branch %d, year %d; existing choices and settlements preserved", *branch, *year)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		log.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO member_branch_counters (branch_id,next_member_number) VALUES ($1,1) ON CONFLICT DO NOTHING`, *branch); err != nil {
		log.Fatal(err)
	}
	managerEmail := fmt.Sprintf("dividend-demo-manager-%d@example.test", *branch)
	if _, err := pool.Exec(ctx, `INSERT INTO staff_users (branch_id,email,name,role)
		VALUES ($1,$2,'Dividend Demo Manager','manager') ON CONFLICT DO NOTHING`, *branch, managerEmail); err != nil {
		log.Fatal(err)
	}
	var officer pgtype.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM staff_users WHERE email=$1 AND branch_id=$2 AND role='manager' AND is_active`, managerEmail, *branch).Scan(&officer); err != nil {
		log.Fatalf("find demo manager: %v", err)
	}

	// Separate, labelled identities keep the historical import away from existing
	// members' ledgers. All identity and lifecycle writes still use member_service.
	scenarios := []struct {
		name, openingBalance string
		memberID, accountID  pgtype.UUID
	}{
		{name: "Peter Mwangi Kamau", openingBalance: "128450"},
		{name: "Grace Wanjiku Njoroge", openingBalance: "76420"},
		{name: "Joseph Otieno Ochieng", openingBalance: "93180"},
		{name: "Mary Wambui Muthoni", openingBalance: "154600"},
		{name: "Abdi Hassan Yusuf", openingBalance: "43870"},
		{name: "Faith Chebet Kiptoo", openingBalance: "68500"},
		{name: "Samuel Mutua Musyoka", openingBalance: "112300"},
		{name: "Mercy Akinyi Odhiambo", openingBalance: "0"},
		{name: "Daniel Karanja Waweru", openingBalance: "51750"},
	}
	members := member.NewService(member.NewStore(pool))
	shareStore := share.NewStore(pool)
	shares := share.NewService(shareStore)
	demoMembers := make(map[string]int, len(scenarios))
	for i := range scenarios {
		scenario := &scenarios[i]
		nationalID := fmt.Sprintf("DIV-DEMO-%d-%d-%02d", *branch, *year, i+1)
		seeded, err := members.GetMemberByNationalID(ctx, *branch, nationalID)
		if errors.Is(err, pgx.ErrNoRows) {
			seeded, err = members.RegisterMember(ctx, *branch, nationalID, sqlc.CreateMemberProfileParams{
				FullName: scenario.name, Email: fmt.Sprintf("dividend-demo-%d-%d-%d@example.test", *branch, *year, i+1),
			})
		}
		if err != nil {
			log.Fatal(err)
		}
		if seeded.FullName.String != scenario.name {
			log.Fatalf("demo identity %s is already used by another profile", nationalID)
		}
		if seeded.Status == "pending" {
			seeded, err = members.UpdateMemberStatus(ctx, seeded.ID, "active", "Dividend development fixture")
			if err != nil {
				log.Fatal(err)
			}
		}
		if seeded.Status != "active" && (i != 8 || seeded.Status != "suspended") {
			log.Fatalf("demo member %s has changed status; refusing to overwrite it", nationalID)
		}
		account, err := shareStore.GetAccountByMemberID(ctx, seeded.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			account, err = shares.OpenShareAccount(ctx, seeded.ID, *branch)
		}
		if err != nil {
			log.Fatal(err)
		}
		if i == 8 && seeded.Status == "active" {
			if _, err := members.UpdateMemberStatus(ctx, seeded.ID, "suspended", "Inactive member dividend development fixture"); err != nil {
				log.Fatal(err)
			}
		}
		scenario.memberID, scenario.accountID = seeded.ID, account.ID
		demoMembers[seeded.ID.String()] = i
	}

	declared := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, nairobi)
	err = contribution.NewStore(pool).ExecReceiptTx(ctx, func(tx pgx.Tx) error {
		q := ceepdb.New(tx)
		shareQueries := sharesqlc.New(tx)
		for i, scenario := range scenarios {
			if _, err := shareQueries.LockAndReadAccount(ctx, scenario.accountID); err != nil {
				return err
			}
			if _, err := shareQueries.GetLatestBalance(ctx, scenario.accountID); err == nil {
				return fmt.Errorf("demo account for %s already has history without this seed's declaration; refusing to overwrite it", scenario.name)
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			key := fmt.Sprintf("dividend-demo:%d:%d:%d", *branch, *year, i+1)
			// Historical imports require explicit posting dates. Normal purchase APIs
			// use today's date and cannot supply a reproducible year-end balance.
			if scenario.openingBalance != "0" {
				if _, err := tx.Exec(ctx, `INSERT INTO share_transactions
					(id,share_account_id,type,amount,balance_after,reference_id,reason,originator_id,created_at)
					VALUES (md5($1)::uuid,$2,'purchase',$3::numeric,$3::numeric,md5($1||':reference')::uuid,
					'Dividend demo imported opening shares',$4,$5) ON CONFLICT DO NOTHING`,
					key+":opening", scenario.accountID, scenario.openingBalance, officer, time.Date(*year, 1, 1, 9, 0, 0, 0, nairobi)); err != nil {
					return err
				}
			}
			for month := 0; month < 6; month++ {
				if (i == 5 && month == 2) || (i == 7 && month < 4) {
					continue
				}
				period := monthStart.AddDate(0, month-6, 0)
				amount := ([][]string{
					{"850", "1200", "950", "1100", "1250", "1000"},
					{"600", "750", "800", "700", "850", "900"},
					{"1000", "1150", "900", "1250", "1050", "1200"},
					{"1400", "1550", "1600", "1450", "1700", "1500"},
					{"500", "650", "550", "700", "600", "750"},
					{"400", "450", "500", "550", "600", "650"},
					{"700", "750", "800", "850", "900", "950"},
					{"300", "350", "400", "450", "500", "550"},
					{"600", "650", "700", "750", "800", "850"},
				})[i][month]
				if i == 6 && month == 2 {
					amount = "50"
				}
				paymentKey := key + ":" + period.Format("2006-01")
				if _, err := tx.Exec(ctx, `WITH posted AS (
					INSERT INTO share_transactions (id,share_account_id,type,amount,balance_after,reference_id,reason,originator_id,created_at)
					VALUES (md5($1||':transaction')::uuid,$2,'purchase',$3::numeric,
					COALESCE((SELECT balance_after FROM share_transactions WHERE share_account_id=$2 ORDER BY created_at DESC,id DESC LIMIT 1),0)+$3::numeric,
					md5($1||':allocation')::uuid,'Dividend demo monthly share contribution',$4,$5)
					ON CONFLICT DO NOTHING RETURNING id
				), receipt AS (
					INSERT INTO contribution_receipts (id,source_channel,external_transaction_id,member_id,branch_id,
					contribution_period,received_amount,allocation_plan,status,received_by,received_at,idempotency_key,created_at,updated_at)
					SELECT md5($1||':receipt')::uuid,'manual',$1,$6,$7,$8,$3::numeric,
					jsonb_build_object('items',jsonb_build_array(jsonb_build_object('type','share_purchase','target_id',$2::text,'amount',$3::numeric::text))),
					'completed',$4,$5,$1,$5,$5 FROM posted
					ON CONFLICT DO NOTHING RETURNING id
				)
				INSERT INTO contribution_allocations (id,receipt_id,type,target_id,amount,status,authoritative_reference_id,created_at,updated_at)
				SELECT md5($1||':allocation')::uuid,r.id,'share_purchase',$2,$3::numeric,'completed',p.id,$5,$5
				FROM receipt r CROSS JOIN posted p ON CONFLICT DO NOTHING`,
					paymentKey, scenario.accountID, amount, officer, period.AddDate(0, 0, 14).Add(10*time.Hour),
					scenario.memberID, *branch, pgtype.Date{Time: period, Valid: true}); err != nil {
					return err
				}
			}
		}

		var rate pgtype.Numeric
		if err := rate.Scan("5"); err != nil {
			return err
		}
		raw, err := q.BuildDividendPreview(ctx, ceepdb.BuildDividendPreviewParams{
			BranchID: *branch, FinancialYear: int32(*year), DeclarationDate: pgtype.Date{Time: declared, Valid: true}, Rate: rate,
		})
		if err != nil {
			return err
		}
		var document map[string]json.RawMessage
		if err := json.Unmarshal(raw, &document); err != nil {
			return err
		}
		if string(document["hasErrors"]) == "true" {
			return fmt.Errorf("resolve the branch's historical share balance errors before seeding dividends")
		}
		var entries []struct {
			MemberID string `json:"memberId"`
			Eligible bool   `json:"eligible"`
			Amount   string `json:"amount"`
		}
		if err := json.Unmarshal(document["allocations"], &entries); err != nil {
			return err
		}
		for _, entry := range entries {
			index, demo := demoMembers[entry.MemberID]
			if !demo && entry.Amount != "0" && entry.Amount != "0.00" {
				return fmt.Errorf("branch has payable non-demo dividends; use an unused development branch")
			}
			if demo && entry.Eligible != (index < 5) {
				return fmt.Errorf("unexpected eligibility for %s", scenarios[index].name)
			}
		}
		canonical, err := json.Marshal(document)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(canonical)
		var totalText string
		if err := json.Unmarshal(document["approvedTotal"], &totalText); err != nil {
			return err
		}
		var total pgtype.Numeric
		if err := total.Scan(totalText); err != nil {
			return err
		}
		declaration, err := q.SaveDividendDeclaration(ctx, ceepdb.SaveDividendDeclarationParams{
			BranchID: *branch, FinancialYear: int32(*year), DeclarationDate: pgtype.Date{Time: declared, Valid: true},
			Rate: rate, ApprovedTotal: total, ResolutionReference: resolution, PreviewHash: hex.EncodeToString(digest[:]), ApprovedBy: officer,
		})
		if err != nil {
			return err
		}
		if err := q.SaveDividendAllocations(ctx, ceepdb.SaveDividendAllocationsParams{DeclarationID: declaration, Allocations: document["allocations"]}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("dividend demo seeded for branch %d, year %d at 5%%; open /dashboard/dividends and load that year", *branch, *year)
}
