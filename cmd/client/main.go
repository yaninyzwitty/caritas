package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/yaninyzwitty/caritas-backend/config"
	loanv1 "github.com/yaninyzwitty/caritas-backend/gen/loan/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	configPath := flag.String("config", "config.yaml", "the path to your config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	address := fmt.Sprintf(":%d", cfg.GRPC.Port)
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		slog.Error("grpc newClient", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			slog.Error("failed to close connection", "error", err)
		}
	}()

	loanServiceClient := loanv1.NewLoanServiceClient(conn)

	loan, err := loanServiceClient.GetLoan(ctx, &loanv1.GetLoanRequest{
		LoanId: "c5398380-a8c7-4545-9bf0-2c2d1dfd0c6b",
	})
	if err != nil {
		slog.Error("failed to get loan", "error", err)
		os.Exit(1)
	}

	slog.Error("get loan", "id", loan.Loan.Id, "loan.amount", loan.Loan.Principal, "loan.status", loan.Loan.Status)

}
