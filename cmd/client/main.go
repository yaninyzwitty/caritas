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
	memberv1 "github.com/yaninyzwitty/caritas-backend/gen/caritas/member/v1"
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
	}

	defer conn.Close()

	memberClient := memberv1.NewMemberServiceClient(conn)

	// get members
	members, err := memberClient.ListMembers(ctx, &memberv1.ListMembersRequest{
		BranchId:     1,
		PageSize:     8,
		PageToken:    "7a888aae-3349-47ac-b8f6-68518ba4443e",
		StatusFilter: memberv1.MemberStatus_MEMBER_STATUS_PENDING,
	})
	if err != nil {
		slog.Error("ListMembers", "error", err)
		os.Exit(1)
	}

	slog.Info("members", "value", len(members.Members), "nextPageToken", members.NextPageToken)

}
