package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yaninyzwitty/caritas-backend/config"
	authsqlc "github.com/yaninyzwitty/caritas-backend/internal/auth/repository/sqlc"
)

func main() {
	email := flag.String("email", "", "system admin email")
	name := flag.String("name", "", "system admin name")
	authUserID := flag.String("auth-user-id", "", "Better Auth user id")
	branchID := flag.Int64("branch-id", 0, "system admin branch id")
	flag.Parse()

	if *branchID <= 0 || strings.TrimSpace(*email) == "" || strings.TrimSpace(*name) == "" || strings.TrimSpace(*authUserID) == "" {
		log.Fatal("auth-user-id, branch-id, email, and name are required")
	}

	dbURL, err := config.GetDatabaseURL()
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	q := authsqlc.New(pool)
	count, err := q.CountStaffUsers(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if count != 0 {
		log.Fatal("staff users already exist")
	}

	staff, err := q.CreateStaffUser(ctx, authsqlc.CreateStaffUserParams{
		AuthUserID: pgtype.Text{String: strings.TrimSpace(*authUserID), Valid: true},
		Name:       strings.TrimSpace(*name),
		BranchID:   *branchID,
		Email:      strings.ToLower(strings.TrimSpace(*email)),
		Role:       "system_admin",
	})
	if errors.Is(err, pgx.ErrNoRows) {
		log.Fatal("staff email or auth user already exists")
	}
	if err != nil {
		log.Fatal(err)
	}

	fmt.Fprintf(os.Stdout, "created system admin %s\n", staff.Email)
}
