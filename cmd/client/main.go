package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/joho/godotenv"
	"github.com/yaninyzwitty/caritas-backend/config"
	contributionv1 "github.com/yaninyzwitty/caritas-backend/gen/contribution/v1"
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

	bearerToken := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiI2ZjQ2ZDc5Mi03MDZhLTQzYmMtODFjNy02M2E3NTgwMzQ5NTEiLCJyb2xlIjoibWFuYWdlciIsImJyYW5jaF9pZCI6MSwiaXNzIjoiY2FyaXRhcy1iYWNrZW5kIiwiYXVkIjoiY2FyaXRhcy1hZG1pbiIsImlhdCI6MTc4ODE5ODE0NCwiZXhwIjoxNzg4MjAwODQ0fQ.BokuhkySlpq1JYAXsWQIIK2hh8RGKFVM9_6rmgl20eQ"
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
	// 	Email:    "jackymsoo@gmail.com",
	// 	Password: "1234567",
	// })

	// slog.Info("val", "res", loginres.AccessToken)
	contribution := contributionv1.NewContributionServiceClient(conn)

	verifyCashDeposit, err := contribution.VerifyCashDeposit(ctx, &contributionv1.VerifyCashDepositRequest{
		DepositId: "f54cd75c-de4b-4f70-b903-127810d40990",
	})

	if err != nil {
		slog.Error("verify cash deposit", "error", err)
		os.Exit(1)
	}

	slog.Info("verify cash deposit", "bankREF", verifyCashDeposit.Deposit.BankReference)
}
