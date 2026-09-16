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
	"strings"
	"syscall"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
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
	"google.golang.org/grpc/credentials/insecure"
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
	authServer := auth.NewHandlers(authStore)
	verifier, err := auth.NewVerifier(
		ctx,
		authStore,
		envOrDefault("JWKS_URL", "http://localhost:3000/api/auth/jwks"),
		envOrDefault("JWT_ISSUER", "http://localhost:3000"),
		envOrDefault("JWT_AUDIENCE", "go-api"),
	)
	exitOnError("failed to initialize authentication", err)
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := verifier.Close(closeCtx); err != nil {
			slog.Error("failed to close JWKS verifier", slog.Any("error", err))
		}
	}()

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.GRPC.Port))
	exitOnError("failed to listen for gRPC", err)

	s := grpc.NewServer(
		grpc.UnaryInterceptor(verifier.UnaryServerInterceptor),
	)
	authv1.RegisterAuthServiceServer(s, authServer)
	memberv1.RegisterMemberServiceServer(s, server)
	sharev1.RegisterShareServiceServer(s, shareServer)
	loanv1.RegisterLoanServiceServer(s, loanServer)
	loanv1.RegisterRepaymentServiceServer(s, loanServer)
	loanv1.RegisterCreditServiceServer(s, loanServer)
	contributionv1.RegisterContributionServiceServer(s, contributionServer)

	gatewayConnection, err := grpc.NewClient(
		fmt.Sprintf("127.0.0.1:%d", cfg.GRPC.Port),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	exitOnError("failed to create gateway connection", err)
	defer gatewayConnection.Close()

	gateway := runtime.NewServeMux(
		runtime.WithIncomingHeaderMatcher(func(header string) (string, bool) {
			switch strings.ToLower(header) {
			case "x-request-id", "idempotency-key":
				return strings.ToLower(header), true
			default:
				return runtime.DefaultHeaderMatcher(header)
			}
		}),
		runtime.WithOutgoingHeaderMatcher(func(header string) (string, bool) {
			if strings.EqualFold(header, "x-request-id") {
				return "X-Request-ID", true
			}
			return runtime.DefaultHeaderMatcher(header)
		}),
	)
	exitOnError("failed to register auth gateway", authv1.RegisterAuthServiceHandler(ctx, gateway, gatewayConnection))
	exitOnError("failed to register member gateway", memberv1.RegisterMemberServiceHandler(ctx, gateway, gatewayConnection))
	exitOnError("failed to register share gateway", sharev1.RegisterShareServiceHandler(ctx, gateway, gatewayConnection))
	exitOnError("failed to register loan gateway", loanv1.RegisterLoanServiceHandler(ctx, gateway, gatewayConnection))
	exitOnError("failed to register repayment gateway", loanv1.RegisterRepaymentServiceHandler(ctx, gateway, gatewayConnection))
	exitOnError("failed to register credit gateway", loanv1.RegisterCreditServiceHandler(ctx, gateway, gatewayConnection))
	exitOnError("failed to register contribution gateway", contributionv1.RegisterContributionServiceHandler(ctx, gateway, gatewayConnection))

	mux := http.NewServeMux()
	// Limit gateway request bodies because generated decoders otherwise accept an
	// unbounded body, allowing a small public request to exhaust server memory.
	mux.Handle("/api/v1/", http.MaxBytesHandler(gateway, 1<<20))
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

// envOrDefault keeps local JWT endpoints usable while allowing deployment
// overrides. Without it, local startup would require three redundant variables.
func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
