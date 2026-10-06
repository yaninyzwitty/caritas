package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/joho/godotenv"
	"github.com/yaninyzwitty/caritas-backend/config"
	loanv1 "github.com/yaninyzwitty/caritas-backend/gen/loan/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

func withAccessToken(ctx context.Context, token string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
}

func main() {
	if err := godotenv.Load(); err != nil {
		slog.Error("failed to load", "error", err)
		os.Exit(1)
	}

	bearerToken := os.Getenv("BEARER_TOKEN")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ctx = withAccessToken(ctx, bearerToken)
	cfg, err := config.Load()
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

	loanCreditClient := loanv1.NewCreditServiceClient(conn)

	creditWithDraw, err := loanCreditClient.RequestCreditWithdrawal(ctx, &loanv1.RequestCreditWithdrawalRequest{
		CreditBalanceId: "f64c396d-9215-4200-a32b-90ed04f5d4b5",
		Amount:          "60",
		Reason:          "overpayment",
		RequestedBy:     "b8feed9c-c0e0-4dfb-a71b-b3ff98669d4e",
	})

	if err != nil {
		slog.Error("failed to request credit withdraw", "error", err)
		os.Exit(1)
	}

	slog.Info("credit withdrawn", "val", creditWithDraw.Success)

}
