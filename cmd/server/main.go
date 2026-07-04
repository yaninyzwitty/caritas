package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yaninyzwitty/caritas-backend/config"
	"github.com/yaninyzwitty/caritas-backend/database"
	"github.com/yaninyzwitty/caritas-backend/internal/db"
	grpcServer "github.com/yaninyzwitty/caritas-backend/internal/grpc"
	"github.com/yaninyzwitty/caritas-backend/internal/member"
	"github.com/yaninyzwitty/caritas-backend/gen/caritas/member/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	pool, err := database.New(ctx, *cfg)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer pool.Close()

	store := db.NewStore(pool)
	memberService := member.NewService(store)
	server := grpcServer.NewServer(memberService)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.GRPC.Port))
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	s := grpc.NewServer()
	memberv1.RegisterMemberServiceServer(s, server)
	reflection.Register(s)

	go func() {
		log.Printf("Starting gRPC server on port %d", cfg.GRPC.Port)
		if err := s.Serve(lis); err != nil {
			log.Fatalf("Failed to serve: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh

	log.Printf("Received signal: %v, initiating graceful shutdown...", sig)

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	shutdownDone := make(chan struct{})
	go func() {
		s.GracefulStop()
		close(shutdownDone)
	}()

	select {
	case <-shutdownDone:
		log.Println("Server shutdown complete")
	case <-shutdownCtx.Done():
		log.Println("Shutdown timeout, forcing exit")
		s.Stop()
	}
}