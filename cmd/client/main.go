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

	bearerToken := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiI4YzhmMGQ3OS05NGNjLTQzMWYtYmM1My1iMjBiNDk5NjgwZDAiLCJyb2xlIjoic3lzdGVtX2FkbWluIiwiYnJhbmNoX2lkIjoxLCJpc3MiOiJjYXJpdGFzLWJhY2tlbmQiLCJhdWQiOiJjYXJpdGFzLWFkbWluIiwiaWF0IjoxNzg3ODM1NzY5LCJleHAiOjE3ODc4Mzg0Njl9.v25PDTFEAxl8BEwCL0FPVDG9w-8XiOncExWTmOsM-lw"
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

	// auth := authv1.NewAuthServiceClient(conn)
	// loginres, _ := auth.Login(ctx, &authv1.LoginRequest{
	// 	Email:    "brianjoseph13@gmail.com",
	// 	Password: "1234567",
	// })

	// slog.Info("val", "res", loginres.AccessToken)

	contribution := contributionv1.NewContributionServiceClient(conn)

	darajaSTKResponse, err := contribution.InitiateDarajaSTKContribution(
		ctx,
		&contributionv1.InitiateDarajaSTKContributionRequest{
			IdempotencyKey: uuid.NewString(),
			MemberId:       "63670bd6-4e7a-4e1d-bb6b-b1a7a86bfbfa",
			BranchId:       1,
			PhoneNumber:    "0768108321",
			Amount: &memberv1.Money{
				CurrencyCode: "KES",
				Units:        120,
			},
			ContributionPeriod: "2026-09-01",
			Allocations: []*contributionv1.ContributionAllocationInput{
				{
					Type: contributionv1.ContributionAllocationType_CONTRIBUTION_ALLOCATION_TYPE_COM,
					Amount: &memberv1.Money{
						CurrencyCode: "KES",
						Units:        30,
					},
				},
				{
					Type: contributionv1.ContributionAllocationType_CONTRIBUTION_ALLOCATION_TYPE_LGOM,
					Amount: &memberv1.Money{
						CurrencyCode: "KES",
						Units:        30,
					},
				},
				{
					Type:     contributionv1.ContributionAllocationType_CONTRIBUTION_ALLOCATION_TYPE_LOAN_PRINCIPAL,
					TargetId: "6a84db78-3393-479d-8123-fff16cba303f",
					Amount: &memberv1.Money{
						CurrencyCode: "KES",
						Units:        40,
					},
				},

				{
					Type:     contributionv1.ContributionAllocationType_CONTRIBUTION_ALLOCATION_TYPE_SHARE_PURCHASE,
					TargetId: "adfde512-4b04-40c2-b78f-fd8d7d377ba9",
					Amount: &memberv1.Money{
						CurrencyCode: "KES",
						Units:        20,
					},
				},
			},
		},
	)
	if err != nil {
		slog.Error("Failed", "error", err)
		os.Exit(1)
	}

	slog.Info("daraja response", "checkoutRequestID", darajaSTKResponse.CheckoutRequestId, "paymentRequestID", darajaSTKResponse.PaymentRequestId)

}
