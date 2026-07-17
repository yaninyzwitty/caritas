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
	"github.com/yaninyzwitty/caritas-backend/config"
	sharev1 "github.com/yaninyzwitty/caritas-backend/gen/caritas/share/v1"
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
	defer conn.Close()

	shareClient := sharev1.NewShareServiceClient(conn)

	// reference_id is the idempotency key (NOT NULL, part of the unique
	// constraint on share_transactions) so the client must supply it; a fresh
	// UUID per run makes each purchase a distinct, retry-safe operation.
	shareAdjustmentRes, err := shareClient.ApproveShareAdjustment(ctx, &sharev1.ApproveShareAdjustmentRequest{
		TransactionId: "370621df-5e8c-4457-bf83-d2f0158f9c37",
		ApproverId:    "126ec801-fa06-4a2c-9d0b-e3524016d17f",
		Reason:        "They made a purchase of 1000",
		AuditReportId: uuid.NewString(),
	})

	if err != nil {
		slog.Error("approve share adjustment failed", "error", err)
		os.Exit(1)
	}

	slog.Info("share Adjustment", "adjustment_id", shareAdjustmentRes.AdjustmentId)

}
