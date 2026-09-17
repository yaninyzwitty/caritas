package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDefaultsAndOverrides(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Port != 8080 || cfg.GRPC.Port != 50051 || cfg.Daraja.Enabled {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}

	t.Setenv("HTTP_PORT", "9000")
	t.Setenv("GRPC_TIMEOUT", "45s")
	t.Setenv("DATABASE_MAX_OPEN_CONNS", "10")
	t.Setenv("DARAJA_ENABLED", "true")
	t.Setenv("DARAJA_BUSINESS_SHORTCODE", "123456")
	t.Setenv("DARAJA_PASSKEY", "passkey")
	t.Setenv("DARAJA_CALLBACK_URL", "https://example.com/callback")
	t.Setenv("DARAJA_CONSUMER_KEY", "key")
	t.Setenv("DARAJA_CONSUMER_SECRET", "secret")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Port != 9000 || cfg.GRPC.Timeout != 45*time.Second || cfg.Database.MaxOpenConns != 10 ||
		!cfg.Daraja.Enabled || cfg.Daraja.BusinessShortCode != "123456" || cfg.Daraja.Passkey != "passkey" {
		t.Fatalf("environment overrides were not loaded: %+v", cfg)
	}
}

func TestLoadRejectsInvalidEnvironment(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"HTTP_PORT", "bad"},
		{"HTTP_PORT", "50051"},
		{"GRPC_TIMEOUT", "0s"},
		{"DATABASE_MAX_OPEN_CONNS", "0"},
		{"DATABASE_CONN_MAX_IDLE_TIME", "bad"},
		{"DARAJA_ENABLED", "maybe"},
	} {
		t.Run(tc.name+"="+tc.value, func(t *testing.T) {
			t.Setenv(tc.name, tc.value)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), tc.name) {
				t.Fatalf("Load() error = %v; expected %s", err, tc.name)
			}
		})
	}
}
