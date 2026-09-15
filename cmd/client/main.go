package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"github.com/yaninyzwitty/caritas-backend/config"
	memberv1 "github.com/yaninyzwitty/caritas-backend/gen/member/v1"
	sharev1 "github.com/yaninyzwitty/caritas-backend/gen/share/v1"
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

	shareClient := sharev1.NewShareServiceClient(conn)

	withdrawSharesRes, err := shareClient.WithdrawShares(ctx, &sharev1.WithdrawSharesRequest{
		AccountId: "80a83eee-9b67-45fb-b1b2-220c5de17fb5",
		Amount: &memberv1.Money{
			CurrencyCode: "KES",
			Units:        500,
			Nanos:        0,
		},
		ReferenceId: uuid.NewString(),
		Reason:      "verify pledged shares can't be withdrawn",
	})
	if err != nil {
		slog.Error("failed to withdraw shares", "error", err)
		os.Exit(1)
	}
	slog.Info("withdraw shares successful", "txID", withdrawSharesRes.TransactionId)

}
