package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTP     HTTPConfig
	GRPC     GRPCConfig
	Daraja   DarajaConfig
	Database DatabaseConfig
}

type HTTPConfig struct{ Port int }
type GRPCConfig struct {
	Port    int
	Timeout time.Duration
}
type DarajaConfig struct {
	Enabled           bool
	BaseURL           string
	BusinessShortCode string
	Passkey           string
	CallbackURL       string
	AccountReference  string
	TransactionDesc   string
	ConsumerKey       string
	ConsumerSecret    string
}
type DatabaseConfig struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

func Load() (*Config, error) {
	cfg := &Config{
		HTTP: HTTPConfig{Port: 8080},
		GRPC: GRPCConfig{Port: 50051, Timeout: 30 * time.Second},
		Daraja: DarajaConfig{
			BaseURL:          "https://sandbox.safaricom.co.ke",
			AccountReference: "CARITAS",
			TransactionDesc:  "Caritas contribution",
		},
		Database: DatabaseConfig{
			MaxOpenConns:    25,
			MaxIdleConns:    5,
			ConnMaxLifetime: 5 * time.Minute,
			ConnMaxIdleTime: time.Minute,
		},
	}
	var err error
	if cfg.HTTP.Port, err = envInt("HTTP_PORT", cfg.HTTP.Port); err != nil {
		return nil, err
	}
	if cfg.GRPC.Port, err = envInt("GRPC_PORT", cfg.GRPC.Port); err != nil {
		return nil, err
	}
	if cfg.GRPC.Timeout, err = envDuration("GRPC_TIMEOUT", cfg.GRPC.Timeout); err != nil {
		return nil, err
	}
	if cfg.Daraja.Enabled, err = envBool("DARAJA_ENABLED", false); err != nil {
		return nil, err
	}
	cfg.Daraja.BaseURL = envString("DARAJA_BASE_URL", cfg.Daraja.BaseURL)
	cfg.Daraja.BusinessShortCode = os.Getenv("DARAJA_BUSINESS_SHORTCODE")
	cfg.Daraja.Passkey = os.Getenv("DARAJA_PASSKEY")
	cfg.Daraja.CallbackURL = os.Getenv("DARAJA_CALLBACK_URL")
	cfg.Daraja.AccountReference = envString("DARAJA_ACCOUNT_REFERENCE", cfg.Daraja.AccountReference)
	cfg.Daraja.TransactionDesc = envString("DARAJA_TRANSACTION_DESC", cfg.Daraja.TransactionDesc)
	cfg.Daraja.ConsumerKey = os.Getenv("DARAJA_CONSUMER_KEY")
	cfg.Daraja.ConsumerSecret = os.Getenv("DARAJA_CONSUMER_SECRET")
	if cfg.Daraja.Enabled && (cfg.Daraja.ConsumerKey == "" || cfg.Daraja.ConsumerSecret == "") {
		return nil, fmt.Errorf("DARAJA_CONSUMER_KEY and DARAJA_CONSUMER_SECRET are required when DARAJA_ENABLED=true")
	}
	if cfg.Database.MaxOpenConns, err = envInt("DATABASE_MAX_OPEN_CONNS", cfg.Database.MaxOpenConns); err != nil {
		return nil, err
	}
	if cfg.Database.MaxIdleConns, err = envInt("DATABASE_MAX_IDLE_CONNS", cfg.Database.MaxIdleConns); err != nil {
		return nil, err
	}
	if cfg.Database.ConnMaxLifetime, err = envDuration("DATABASE_CONN_MAX_LIFETIME", cfg.Database.ConnMaxLifetime); err != nil {
		return nil, err
	}
	if cfg.Database.ConnMaxIdleTime, err = envDuration("DATABASE_CONN_MAX_IDLE_TIME", cfg.Database.ConnMaxIdleTime); err != nil {
		return nil, err
	}
	if cfg.HTTP.Port < 1 || cfg.HTTP.Port > 65535 || cfg.GRPC.Port < 1 || cfg.GRPC.Port > 65535 || cfg.HTTP.Port == cfg.GRPC.Port {
		return nil, fmt.Errorf("HTTP_PORT and GRPC_PORT must be distinct ports between 1 and 65535")
	}
	if cfg.GRPC.Timeout <= 0 || cfg.Database.ConnMaxLifetime <= 0 || cfg.Database.ConnMaxIdleTime <= 0 {
		return nil, fmt.Errorf("GRPC_TIMEOUT, DATABASE_CONN_MAX_LIFETIME and DATABASE_CONN_MAX_IDLE_TIME must be positive")
	}
	if cfg.Database.MaxOpenConns < 1 || cfg.Database.MaxOpenConns > 2147483647 || cfg.Database.MaxIdleConns < 0 || cfg.Database.MaxIdleConns > cfg.Database.MaxOpenConns {
		return nil, fmt.Errorf("DATABASE_MAX_OPEN_CONNS must be between 1 and 2147483647 and DATABASE_MAX_IDLE_CONNS must be between 0 and DATABASE_MAX_OPEN_CONNS")
	}
	return cfg, nil
}

func envString(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
func envInt(name string, fallback int) (int, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", name, err)
	}
	return parsed, nil
}
func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration such as 30s or 5m: %w", name, err)
	}
	return parsed, nil
}
func envBool(name string, fallback bool) (bool, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", name, err)
	}
	return parsed, nil
}
func GetDatabaseURL() (string, error) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return "", fmt.Errorf("DATABASE_URL environment variable is required")
	}
	return url, nil
}
