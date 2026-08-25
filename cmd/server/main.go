package main

import (
	"context"
	"flag"
	"fmt"
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
	contributionv1 "github.com/yaninyzwitty/caritas-backend/gen/contribution/v1"
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
	exitOnError("failed to load config", err)

	dbURL, err := config.GetDatabaseURL()
	exitOnError("failed to get database URL", err)
	authTokenSecret, err := config.GetAuthTokenSecret()
	exitOnError("failed to get auth token secret", err)

	poolConfig, err := pgxpool.ParseConfig(dbURL)
	exitOnError("failed to parse database URL", err)

	if cfg.Database.MaxOpenConns > 2147483647 {
		slog.Error("database max_open_conns exceeds int32 max", "value", cfg.Database.MaxOpenConns)
		os.Exit(1)
	}
	if cfg.Database.MaxIdleConns > 2147483647 {
		slog.Error("database max_idle_conns exceeds int32 max", "value", cfg.Database.MaxIdleConns)
		os.Exit(1)
	}

	poolConfig.MaxConns = int32(cfg.Database.MaxOpenConns)
	poolConfig.MinConns = int32(cfg.Database.MaxIdleConns)
	poolConfig.MaxConnLifetime = cfg.Database.ConnMaxLifetime
	poolConfig.MaxConnIdleTime = cfg.Database.ConnMaxIdleTime

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	exitOnError("failed to create database pool", err)
	defer pool.Close()
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
			slog.Error("failed to connect to PostgreSQL", "attempts", attempt, "error", err)
			os.Exit(1)
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
	darajaClient, err := newDarajaClient(*cfg)
	exitOnError("failed to configure Daraja STK initiator", err)
	contributionServer := contribution.NewHandlers(contributionService, darajaClient)
	darajaHandlers := contribution.NewDarajaHandlers(ctx, contributionService)
	authStore := auth.NewStore(pool)
	authServer := auth.NewHandlers(authStore, authTokenSecret)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.GRPC.Port))
	exitOnError("failed to listen for gRPC", err)

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
	contributionv1.RegisterContributionServiceServer(s, contributionServer)

	mux := http.NewServeMux()
	darajaHandlers.RegisterDarajaRoutes(mux)
	httpServer := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.HTTP.Port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		slog.Info("starting gRPC server", "port", cfg.GRPC.Port)
		exitOnError("failed to serve gRPC", s.Serve(lis))
	}()

	go func() {
		slog.Info("starting HTTP server", "port", cfg.HTTP.Port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			exitOnError("failed to serve HTTP", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh

	slog.Info("received shutdown signal", "signal", sig)

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
		slog.Info("server shutdown complete")
	case <-shutdownCtx.Done():
		slog.Warn("shutdown timeout, forcing exit")
		s.Stop()
	}
}
