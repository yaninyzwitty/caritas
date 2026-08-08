package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yaninyzwitty/caritas-backend/config"
	authv1 "github.com/yaninyzwitty/caritas-backend/gen/auth/v1"
	loanv1 "github.com/yaninyzwitty/caritas-backend/gen/loan/v1"
	memberv1 "github.com/yaninyzwitty/caritas-backend/gen/member/v1"
	sharev1 "github.com/yaninyzwitty/caritas-backend/gen/share/v1"
	"github.com/yaninyzwitty/caritas-backend/internal/auth"
	"github.com/yaninyzwitty/caritas-backend/internal/contribution"
	"github.com/yaninyzwitty/caritas-backend/internal/loan"
	"github.com/yaninyzwitty/caritas-backend/internal/member"
	"github.com/yaninyzwitty/caritas-backend/internal/share"
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
	authTokenSecret, err := config.GetAuthTokenSecret()
	if err != nil {
		log.Fatalf("Failed to get auth token secret: %v", err)
	}

	poolConfig, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		log.Fatalf("Failed to parse database URL: %v", err)
	}

	if cfg.Database.MaxOpenConns > 2147483647 {
		log.Fatalf("Database.MaxOpenConns exceeds int32 max: %d", cfg.Database.MaxOpenConns)
	}
	if cfg.Database.MaxIdleConns > 2147483647 {
		log.Fatalf("Database.MaxIdleConns exceeds int32 max: %d", cfg.Database.MaxIdleConns)
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

	// TODO-move the postgres logic into its own separate file
	// retry on startup to prevent: context timeout deadline or whatever
	backoff := time.Second

	for attempt := 1; attempt <= 5; attempt++ {
		pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := pool.Ping(pingCtx)
		cancel()

		if err == nil {
			slog.Info("Connected to database successfully")
			break
		}

		if attempt == 5 {
			log.Fatalf("Failed to connect to PostgreSQL after %d attempts: %v", attempt, err)
		}

		slog.Warn(
			"Database not ready, retrying...",
			slog.Int("attempt", attempt),
			slog.Any("error", err),
		)

		time.Sleep(backoff)
		backoff *= 2
	}

	store := member.NewStore(pool)
	memberService := member.NewService(store)
	server := member.NewHandlers(memberService, store)
	shareStore := share.NewStore(pool)
	shareService := share.NewService(shareStore)
	shareServer := share.NewHandlers(shareService, shareStore)
	loanStore := loan.NewStore(pool)
	loanService := loan.NewService(loanStore, memberService)
	loanServer := loan.NewHandlers(loanStore, loanService)
	contributionStore := contribution.NewStore(pool)
	contributionService := contribution.NewService(contributionStore, shareService, loanService)
	darajaHandlers := contribution.NewDarajaHandlers(contributionService)
	authStore := auth.NewStore(pool)
	authServer := auth.NewHandlers(authStore, authTokenSecret)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.GRPC.Port))
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	s := grpc.NewServer(
		grpc.UnaryInterceptor(
			auth.UnaryInterceptor(authStore, authTokenSecret),
		),
	)
	authv1.RegisterAuthServiceServer(s, authServer)
	memberv1.RegisterMemberServiceServer(s, server)
	sharev1.RegisterShareServiceServer(s, shareServer)
	loanv1.RegisterLoanServiceServer(s, loanServer)
	loanv1.RegisterRepaymentServiceServer(s, loanServer)
	loanv1.RegisterCreditServiceServer(s, loanServer)

	mux := http.NewServeMux()
	darajaHandlers.RegisterDarajaRoutes(mux)
	httpServer := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.HTTP.Port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("Starting gRPC server on port %d", cfg.GRPC.Port)
		if err := s.Serve(lis); err != nil {
			log.Fatalf("Failed to serve: %v", err)
		}
	}()

	go func() {
		log.Printf("Starting HTTP server on port %d", cfg.HTTP.Port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to serve HTTP: %v", err)
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
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			slog.Error("HTTP server shutdown", "error", err)
		}
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
