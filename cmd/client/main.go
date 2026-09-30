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

	contributions := contributionv1.NewContributionServiceClient(conn)
	darajaContributionRes, err := contributions.InitiateDarajaSTKContribution(ctx, &contributionv1.InitiateDarajaSTKContributionRequest{
		IdempotencyKey: uuid.NewString(),
		MemberId:       "e4d3b882-24ed-438a-8c6f-c0364bf25b61",
		BranchId:       1,
		PhoneNumber:    "0768108321",
		Amount: &memberv1.Money{
			CurrencyCode: "KES",
			Units:       195,
			Nanos:        0,
		},
		ContributionPeriod: "2026-09-01",
		Allocations: []*contributionv1.ContributionAllocationInput{
			{
				Type: contributionv1.ContributionAllocationType_CONTRIBUTION_ALLOCATION_TYPE_LGOM,
				Amount: &memberv1.Money{
					CurrencyCode: "KES",
					Units:        30,
					Nanos:        0,
				},
			},
			{
				Type: contributionv1.ContributionAllocationType_CONTRIBUTION_ALLOCATION_TYPE_COM,
				Amount: &memberv1.Money{
					CurrencyCode: "KES",
					Units:        30,
					Nanos:        0,
				},
			},
			{
				Type:     contributionv1.ContributionAllocationType_CONTRIBUTION_ALLOCATION_TYPE_SHARE_PURCHASE,
				TargetId: "09ee704c-d1ff-42e4-85e7-bca75925c646", // every loan repayment and a share purchase must be accompanied by a target id
				Amount: &memberv1.Money{
					CurrencyCode: "KES",
					Units:       135,
					Nanos:        0,
				},
			},
		},
	})
	if err != nil {
		slog.Error("failed to start a daraja contribution", "error", err)
		os.Exit(1)
	}

	slog.Info("daraja contribution", "val", darajaContributionRes.CheckoutRequestId)

}
