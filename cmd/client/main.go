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
	sharev1 "github.com/yaninyzwitty/caritas-backend/gen/share/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// TODO--check why error tx is closed
func withAccessToken(ctx context.Context, token string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
}

func main() {
	if err := godotenv.Load(); err != nil {
		slog.Error("failed to load", "error", err)
		os.Exit(1)
	}

	bearerToken := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiI4YzhmMGQ3OS05NGNjLTQzMWYtYmM1My1iMjBiNDk5NjgwZDAiLCJyb2xlIjoic3lzdGVtX2FkbWluIiwiYnJhbmNoX2lkIjoxLCJpc3MiOiJjYXJpdGFzLWJhY2tlbmQiLCJhdWQiOiJjYXJpdGFzLWFkbWluIiwiaWF0IjoxNzg3NjY3NTc3LCJleHAiOjE3ODc2NzAyNzd9._naNpEkuAl7SqbyboLnDcaCq9qvpwzFmvgIKJfCPSDc"
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

	share := sharev1.NewShareServiceClient(conn)

	shareBalanceRes, err := share.GetShareBalance(ctx, &sharev1.GetShareBalanceRequest{
		AccountId:         "adfde512-4b04-40c2-b78f-fd8d7d377ba9",
		ConsistencyStrong: true,
	})

	if err != nil {
		slog.Error("get share balance", "error", err)
		os.Exit(1)
	}

	slog.Info("get share balance", "account_id", "adfde512-4b04-40c2-b78f-fd8d7d377ba9", "balance_units", shareBalanceRes.Balance.Units)

}
