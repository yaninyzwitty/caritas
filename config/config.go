package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	HTTP     HTTPConfig     `yaml:"http"`
	GRPC     GRPCConfig     `yaml:"grpc"`
	Daraja   DarajaConfig   `yaml:"daraja"`
	Temporal TemporalConfig `yaml:"temporal"`
	Database DatabaseConfig `yaml:"database"`
	Log      LogConfig      `yaml:"log"`
}

// HTTPConfig exists for public webhook ingress. Without it, Daraja would have
// to share the gRPC listener even though callbacks are plain HTTP.
type HTTPConfig struct {
	Port int `yaml:"port"`
}

type GRPCConfig struct {
	Port    int           `yaml:"port"`
	Timeout time.Duration `yaml:"timeout"`
}

// DarajaConfig carries provider settings from config/env into main. Without
// this explicit config object, STK initiation would either use globals or bury
// production credentials inside the contribution package.
type DarajaConfig struct {
	Enabled           bool   `yaml:"enabled"`
	BaseURL           string `yaml:"base_url"`
	BusinessShortCode string `yaml:"business_short_code"`
	Passkey           string `yaml:"passkey"`
	CallbackURL       string `yaml:"callback_url"`
	AccountReference  string `yaml:"account_reference"`
	TransactionDesc   string `yaml:"transaction_desc"`
	ConsumerKeyEnv    string `yaml:"consumer_key_env"`
	ConsumerSecretEnv string `yaml:"consumer_secret_env"`
	ConsumerKey       string `yaml:"-"`
	ConsumerSecret    string `yaml:"-"`
}

type TemporalConfig struct {
	Host                     string        `yaml:"host"`
	Namespace                string        `yaml:"namespace"`
	TaskQueue                string        `yaml:"task_queue"`
	WorkflowExecutionTimeout time.Duration `yaml:"workflow_execution_timeout"`
	ActivityTimeout          time.Duration `yaml:"activity_timeout"`
}

type DatabaseConfig struct {
	MaxOpenConns    int           `yaml:"max_open_conns"`
	MaxIdleConns    int           `yaml:"max_idle_conns"`
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`
	ConnMaxIdleTime time.Duration `yaml:"conn_max_idle_time"`
}

type LogConfig struct {
	Level     string `yaml:"level"`
	Format    string `yaml:"format"`
	AddSource bool   `yaml:"add_source"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}
	if cfg.Daraja.Enabled {
		keyEnv := cfg.Daraja.ConsumerKeyEnv
		if keyEnv == "" {
			keyEnv = "DARAJA_CONSUMER_KEY"
		}
		secretEnv := cfg.Daraja.ConsumerSecretEnv
		if secretEnv == "" {
			secretEnv = "DARAJA_CONSUMER_SECRET"
		}

		businessShortCode := cfg.Daraja.BusinessShortCode
		if businessShortCode == "" {
			businessShortCode = os.Getenv("DARAJA_BUSINESS_SHORTCODE")
		}

		darajaPassKey := cfg.Daraja.Passkey

		if darajaPassKey == "" {
			darajaPassKey = os.Getenv("DARAJA_PASSKEY")
		}

		cfg.Daraja.ConsumerKey = os.Getenv(keyEnv)
		cfg.Daraja.ConsumerSecret = os.Getenv(secretEnv)
		if cfg.Daraja.ConsumerKey == "" || cfg.Daraja.ConsumerSecret == "" {
			return nil, fmt.Errorf("%s and %s environment variables are required", keyEnv, secretEnv)
		}
	}

	return &cfg, nil
}

func GetDatabaseURL() (string, error) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return "", fmt.Errorf("DATABASE_URL environment variable is required")
	}
	return url, nil
}

func GetAuthTokenSecret() (string, error) {
	secret := os.Getenv("AUTH_TOKEN_SECRET")
	if secret == "" {
		return "", fmt.Errorf("AUTH_TOKEN_SECRET environment variable is required")
	}
	return secret, nil
}
