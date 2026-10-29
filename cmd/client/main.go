package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"github.com/yaninyzwitty/caritas-backend/config"
	contributionv1 "github.com/yaninyzwitty/caritas-backend/gen/contribution/v1"
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

	contributionClient := contributionv1.NewContributionServiceClient(conn)
	shareClient := sharev1.NewShareServiceClient(conn)
	const memberID = "129fc5b8-6b23-4f18-bb72-17e332208dae"
	const period = "2026-10-01"
	account, err := shareClient.GetShareAccount(ctx, &sharev1.GetShareAccountRequest{
		Identifier: &sharev1.GetShareAccountRequest_MemberId{MemberId: memberID},
		BranchId:   1,
	})
	if err != nil {
		log.Fatalf("GetShareAccount: %v", err)
	}
	quote, err := contributionClient.GetContributionQuote(ctx, &contributionv1.GetContributionQuoteRequest{
		MemberId: memberID, ContributionPeriod: period,
	})
	if err != nil {
		log.Fatalf("GetContributionQuote: %v", err)
	}
	amount := &memberv1.Money{CurrencyCode: "KES", Units: 50}
	for _, fee := range quote.GetFees() {
		money := fee.GetAmount()
		if money.GetCurrencyCode() != "KES" {
			log.Fatalf("unexpected fee currency: %q", money.GetCurrencyCode())
		}
		amount.Units += money.GetUnits()
		amount.Nanos += money.GetNanos()
		amount.Units += int64(amount.Nanos / 1_000_000_000)
		amount.Nanos %= 1_000_000_000
	}

	response, err := contributionClient.InitiateDarajaSTKContribution(ctx, &contributionv1.InitiateDarajaSTKContributionRequest{
		IdempotencyKey:     uuid.NewString(),
		MemberId:           memberID,
		BranchId:           1,
		PhoneNumber:        "0768108321",
		Amount:             amount,
		ContributionPeriod: period,
		Allocations: []*contributionv1.ContributionAllocationInput{
			{
				Type:     contributionv1.ContributionAllocationType_CONTRIBUTION_ALLOCATION_TYPE_SHARE_PURCHASE,
				TargetId: account.GetAccount().GetId(),
				Amount:   &memberv1.Money{CurrencyCode: "KES", Units: 50},
			},
		},
	})
	if err != nil {
		log.Fatalf("InitiateDarajaSTKContribution: %v", err)
	}
	slog.Info("STK contribution initiated", "payment_request_id", response.GetPaymentRequestId(), "checkout_request_id", response.GetCheckoutRequestId(), "status", response.GetStatus())
}
