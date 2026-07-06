package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yaninyzwitty/caritas-backend/config"
	memberv1 "github.com/yaninyzwitty/caritas-backend/gen/caritas/member/v1"
	"github.com/yaninyzwitty/caritas-backend/internal/member"
	"google.golang.org/grpc"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	configPath := flag.String("config", "config.yaml", "the path to your config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	dbURL, err := config.GetDatabaseURL()
	if err != nil {
		log.Fatalf("Failed to get database URL: %v", err)
	}

	poolConfig, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		log.Fatalf("Failed to parse database URL: %v", err)
	}

	poolConfig.MaxConns = int32(cfg.Database.MaxOpenConns)
	poolConfig.MinConns = int32(cfg.Database.MaxIdleConns)
	poolConfig.MaxConnLifetime = cfg.Database.ConnMaxLifetime
	poolConfig.MaxConnIdleTime = cfg.Database.ConnMaxIdleTime

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		log.Fatalf("Failed to create database pool: %v", err)
	}
	defer pool.Close()

	pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
	defer pingCancel()
	if err := pool.Ping(pingCtx); err != nil {
		log.Fatalf("Failed to ping database: %v", err)
	}

	store := member.NewStore(pool)
	memberService := member.NewService(store)
	server := member.NewHandlers(memberService, store)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.GRPC.Port))
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	s := grpc.NewServer()
	memberv1.RegisterMemberServiceServer(s, server)

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
