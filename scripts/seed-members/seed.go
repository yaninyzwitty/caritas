package main

import (
	"context"
	"flag"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yaninyzwitty/caritas-backend/config"
	"github.com/yaninyzwitty/caritas-backend/internal/member"
	"github.com/yaninyzwitty/caritas-backend/internal/repository/sqlc"
)

func main() {
	ctx := context.Background()
	configPath := flag.String("config", "config.yaml", "the path to your config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	dbURL, err := config.GetDatabaseURL()
	if err != nil {
		log.Fatalf("Failed to get database URL: %v", err)
	}

	poolConfig, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		log.Fatalf("Failed to parse database URL: %v", err)
	}

	if cfg.Database.MaxOpenConns > 2147483647 {
		log.Fatalf("Database.MaxOpenConns exceeds int32 max: %d", cfg.Database.MaxOpenConns)
	}
	if cfg.Database.MaxIdleConns > 2147483647 {
		log.Fatalf("Database.MaxIdleConns exceeds int32 max: %d", cfg.Database.MaxIdleConns)
	}

	poolConfig.MaxConns = int32(cfg.Database.MaxOpenConns)
	poolConfig.MinConns = int32(cfg.Database.MaxIdleConns)
	poolConfig.MaxConnLifetime = cfg.Database.ConnMaxLifetime
	poolConfig.MaxConnIdleTime = cfg.Database.ConnMaxIdleTime

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		log.Fatalf("Failed to create database pool: %v", err)
	}
	defer pool.Close()

	pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
	defer pingCancel()
	if err := pool.Ping(pingCtx); err != nil {
		log.Fatalf("Failed to ping database: %v", err)
	}

	store := member.NewStore(pool)
	memberService := member.NewService(store)

	members := []struct {
		nationalID string
		profile    sqlc.CreateMemberProfileParams
	}{
		{
			nationalID: "12345678",
			profile: sqlc.CreateMemberProfileParams{
				FullName:   "John Doe",
				Phone:      "+254712345678",
				Email:      "john.doe@example.com",
				Address:    pgtype.Text{String: "123 Nairobi Street", Valid: true},
				Occupation: pgtype.Text{String: "Engineer", Valid: true},
				Employer:   pgtype.Text{String: "Tech Corp", Valid: true},
			},
		},
		{
			nationalID: "23456789",
			profile: sqlc.CreateMemberProfileParams{
				FullName:   "Jane Smith",
				Phone:      "+254723456789",
				Email:      "jane.smith@example.com",
				Address:    pgtype.Text{String: "456 Mombasa Road", Valid: true},
				Occupation: pgtype.Text{String: "Teacher", Valid: true},
				Employer:   pgtype.Text{String: "City School", Valid: true},
			},
		},
		{
			nationalID: "34567890",
			profile: sqlc.CreateMemberProfileParams{
				FullName:   "Michael Johnson",
				Phone:      "+254734567890",
				Email:      "michael.j@example.com",
				Address:    pgtype.Text{String: "789 Kisumu Avenue", Valid: true},
				Occupation: pgtype.Text{String: "Doctor", Valid: true},
				Employer:   pgtype.Text{String: "General Hospital", Valid: true},
			},
		},
		{
			nationalID: "45678901",
			profile: sqlc.CreateMemberProfileParams{
				FullName:   "Sarah Williams",
				Phone:      "+254745678901",
				Email:      "sarah.w@example.com",
				Address:    pgtype.Text{String: "321 Nakuru Highway", Valid: true},
				Occupation: pgtype.Text{String: "Accountant", Valid: true},
				Employer:   pgtype.Text{String: "Finance Ltd", Valid: true},
			},
		},
		{
			nationalID: "56789012",
			profile: sqlc.CreateMemberProfileParams{
				FullName:   "David Brown",
				Phone:      "+254756789012",
				Email:      "david.brown@example.com",
				Address:    pgtype.Text{String: "654 Eldoret Lane", Valid: true},
				Occupation: pgtype.Text{String: "Farmer", Valid: true},
				Employer:   pgtype.Text{String: "Agri Co", Valid: true},
			},
		},
		{
			nationalID: "67890123",
			profile: sqlc.CreateMemberProfileParams{
				FullName:   "Emily Davis",
				Phone:      "+254767890123",
				Email:      "emily.davis@example.com",
				Address:    pgtype.Text{String: "987 Thika Road", Valid: true},
				Occupation: pgtype.Text{String: "Nurse", Valid: true},
				Employer:   pgtype.Text{String: "Medical Center", Valid: true},
			},
		},
		{
			nationalID: "78901234",
			profile: sqlc.CreateMemberProfileParams{
				FullName:   "James Wilson",
				Phone:      "+254778901234",
				Email:      "james.wilson@example.com",
				Address:    pgtype.Text{String: "147 Nyeri Street", Valid: true},
				Occupation: pgtype.Text{String: "Lawyer", Valid: true},
				Employer:   pgtype.Text{String: "Law Firm", Valid: true},
			},
		},
		{
			nationalID: "89012345",
			profile: sqlc.CreateMemberProfileParams{
				FullName:   "Linda Martinez",
				Phone:      "+254789012345",
				Email:      "linda.martinez@example.com",
				Address:    pgtype.Text{String: "258 Malindi Road", Valid: true},
				Occupation: pgtype.Text{String: "Business Owner", Valid: true},
				Employer:   pgtype.Text{String: "Self Employed", Valid: true},
			},
		},
		{
			nationalID: "90123456",
			profile: sqlc.CreateMemberProfileParams{
				FullName:   "Robert Taylor",
				Phone:      "+254790123456",
				Email:      "robert.taylor@example.com",
				Address:    pgtype.Text{String: "369 Garissa Avenue", Valid: true},
				Occupation: pgtype.Text{String: "Driver", Valid: true},
				Employer:   pgtype.Text{String: "Transport Co", Valid: true},
			},
		},
		{
			nationalID: "01234567",
			profile: sqlc.CreateMemberProfileParams{
				FullName:   "Patricia Anderson",
				Phone:      "+254701234567",
				Email:      "patricia.anderson@example.com",
				Address:    pgtype.Text{String: "741 Kakamega Street", Valid: true},
				Occupation: pgtype.Text{String: "Chef", Valid: true},
				Employer:   pgtype.Text{String: "Restaurant", Valid: true},
			},
		},
	}

	successCount := 0
	for i, m := range members {
		log.Printf("Registering member %d: %s (National ID: %s)", i+1, m.profile.FullName, m.nationalID)

		member, err := memberService.RegisterMember(ctx, 1, m.nationalID, m.profile)
		if err != nil {
			log.Printf("Failed to register member %d: %v", i+1, err)
			continue
		}

		log.Printf("Successfully registered member %d: ID=%s, MemberNumber=%d, Status=%s",
			i+1, member.ID, member.MemberNumber, member.Status)
		successCount++
	}

	log.Printf("Registration complete. Successfully registered %d out of 10 members.", successCount)
}
