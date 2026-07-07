package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"time"

	"github.com/yaninyzwitty/caritas-backend/config"
	memberv1 "github.com/yaninyzwitty/caritas-backend/gen/caritas/member/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// address not being included
// make it to return few details rather than the whole member object
// concern of inserting national ids (member request) two times, why are we doing this

// look for any methods to make the member-id, like  from (000) -> onwards

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

	// Register a new member
	member, err := memberClient.RegisterMember(ctx, &memberv1.RegisterMemberRequest{
		NationalId: "29856471",
		Profile: &memberv1.MemberProfile{
			Personal: &memberv1.PersonalInfo{
				FullName:    "Brian Otieno Ouma",
				Phone:       "+254701234567",
				Email:       "brian.ouma@example.com",
				Address:     "123 Nairobi Road, Nairobi",
				DateOfBirth: timestamppb.New(time.Date(1992, time.November, 5, 0, 0, 0, 0, time.UTC)),
			},
			Employment: &memberv1.Employment{
				Occupation: "Software Engineer",
				Employer:   "Savanna Tech Ltd",
				MonthlyIncome: &memberv1.Money{
					CurrencyCode: "KES",
					Units:        250000,
					Nanos:        0,
				},
			},
			IdDocument: &memberv1.Identification{
				Type:   "national_id",
				Number: "29856471",
			},
			NextOfKin: &memberv1.NextOfKin{
				Name:         "Alice Achieng",
				Phone:        "+254722334455",
				Relationship: memberv1.RelationshipType_RELATIONSHIP_TYPE_PARENT,
			},
		},
	})
	if err != nil {
		slog.Error("register member", "error", err)
		return
	}
	slog.Info("member", "val", member.MemberId)

}
