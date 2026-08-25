package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/yaninyzwitty/caritas-backend/config"
	"github.com/yaninyzwitty/caritas-backend/internal/contribution"
)

// newDarajaClient keeps payment-provider construction in the server composition
// layer while leaving config as plain settings. If this lived in config, that
// package would need to import internal payment code and stop being a reusable
// parser for application configuration.
func newDarajaClient(cfg config.Config) (*contribution.DarajaClient, error) {
	if !cfg.Daraja.Enabled {
		return nil, nil
	}
	if strings.TrimSpace(cfg.Daraja.BaseURL) == "" {
		return nil, fmt.Errorf("daraja base_url is required when daraja is enabled")
	}
	if strings.TrimSpace(cfg.Daraja.BusinessShortCode) == "" {
		return nil, fmt.Errorf("daraja business_short_code is required when daraja is enabled")
	}
	if strings.TrimSpace(cfg.Daraja.Passkey) == "" {
		return nil, fmt.Errorf("daraja passkey is required when daraja is enabled")
	}
	if strings.TrimSpace(cfg.Daraja.CallbackURL) == "" {
		return nil, fmt.Errorf("daraja callback_url is required when daraja is enabled")
	}
	return contribution.NewDarajaClient(&http.Client{Timeout: cfg.GRPC.Timeout}, contribution.DarajaClientConfig{
		BaseURL:           cfg.Daraja.BaseURL,
		BusinessShortCode: cfg.Daraja.BusinessShortCode,
		Passkey:           cfg.Daraja.Passkey,
		CallbackURL:       cfg.Daraja.CallbackURL,
		AccountReference:  cfg.Daraja.AccountReference,
		TransactionDesc:   cfg.Daraja.TransactionDesc,
		ConsumerKey:       cfg.Daraja.ConsumerKey,
		ConsumerSecret:    cfg.Daraja.ConsumerSecret,
	}), nil
}
