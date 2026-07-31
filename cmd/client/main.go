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

	repaymentServiceClient := loanv1.NewRepaymentServiceClient(conn)

	recordPayment, err := repaymentServiceClient.RecordRepayment(ctx, &loanv1.RecordRepaymentRequest{
		LoanId:                      "c5398380-a8c7-4545-9bf0-2c2d1dfd0c6b",
		Amount:                      "60000",
		PaymentGatewayTransactionId: "71393be9-2298-4bb5-87ab-7527fea24194", // enforced by payment provider
		CreatedBy:                   "c9b9f2e3-e782-4df3-958b-26cb50e2e5c4",
	})

	if err != nil {
		slog.Error("failed to record payment", "error", err)
		os.Exit(1)
	}

	slog.Info("record payment", "transaction_id", recordPayment.TransactionId, "credit", recordPayment.Allocation.Credit)

}
