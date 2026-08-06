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
	"google.golang.org/grpc/metadata"
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

	loanClient := loanv1.NewLoanServiceClient(conn)

	accessToken := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJiZGFjYzFjOS1jYTRhLTQxZTEtYjE5OS0zZjE2NzEwZTdiYzkiLCJyb2xlIjoic3lzdGVtX2FkbWluIiwiYnJhbmNoX2lkIjoxLCJpc3MiOiJjYXJpdGFzLWJhY2tlbmQiLCJhdWQiOiJjYXJpdGFzLWFkbWluIiwiaWF0IjoxNzg1OTg5NDU2LCJleHAiOjE3ODU5OTAzNTZ9.932D8_ZlAlY6ZkrSdqfRlNL_gtWDoe400ljadLp44BU"

	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+accessToken)

	loan, err := loanClient.RejectLoan(ctx, &loanv1.RejectLoanRequest{
		LoanId: "71393be9-2298-4bb5-87ab-7527fea24194",
		Reason: "Member doesn't get paid enough to cover for the loan",
	})
	if err != nil {
		slog.Error("RejectLoan failed", "error", err)
		os.Exit(1)
	}
	slog.Info("loan rejected", "status", loan.NewStatus.String())

}
