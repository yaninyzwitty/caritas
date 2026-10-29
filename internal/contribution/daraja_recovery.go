package contribution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	contributionsqlc "github.com/yaninyzwitty/caritas-backend/internal/contribution/repository/sqlc"
)

// RecoverDaraja consumes saved callbacks independently of provider latency.
// Without it, acknowledged payments would have no recovery owner after restart.
func (s *Service) RecoverDaraja(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		for i := 0; i < 50 && ctx.Err() == nil; i++ {
			attemptCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			event, err := s.store.ClaimDarajaCallback(attemptCtx)
			if err != nil {
				cancel()
				if !errors.Is(err, pgx.ErrNoRows) {
					slog.Error("claim daraja callback", "error", err)
				}
				break
			}
			var payload darajaSTKCallbackPayload
			err = json.Unmarshal(event.Payload, &payload)
			if err == nil {
				callback := payload.Body.STKCallback
				if callback.ResultCode != 0 {
					err = s.MarkDarajaSTKPaymentFailed(attemptCtx, callback.CheckoutRequestID, callback.ResultDesc)
				} else {
					var payment DarajaSTKPayment
					payment, err = callback.successPayment()
					if err == nil {
						_, err = s.ProcessDarajaSTKPayment(attemptCtx, payment)
					}
				}
			}
			cancel()
			message := ""
			if err != nil {
				message = err.Error()
				slog.Warn("recover daraja callback", "callback_id", event.ID.String(), "error", err)
				if event.Attempts >= 8 {
					slog.Error("daraja callback needs attention", "callback_id", event.ID.String(), "checkout_request_id", event.CheckoutRequestID, "error", err)
				}
			}
			finishCtx, finishCancel := context.WithTimeout(ctx, 5*time.Second)
			if err := s.store.FinishDarajaCallback(finishCtx, contributionsqlc.FinishDarajaCallbackParams{ID: event.ID, LastError: message}); err != nil {
				slog.Error("finish daraja callback", "error", err)
			}
			finishCancel()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// QueryPendingDaraja checks one checkout at a time, at least ten seconds apart.
// Keeping provider calls here prevents outages from blocking saved callbacks.
func (s *Service) QueryPendingDaraja(ctx context.Context, client *DarajaClient) {
	delay := 10 * time.Second
	for ctx.Err() == nil {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		queryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		request, err := s.store.ClaimDarajaQuery(queryCtx)
		if err != nil {
			cancel()
			if !errors.Is(err, pgx.ErrNoRows) {
				slog.Error("claim daraja query", "error", err)
			}
			continue
		}
		code, description, queryErr := client.QuerySTK(queryCtx, request.CheckoutRequestID.String)
		cancel()
		if queryErr != nil {
			slog.Warn("query daraja checkout", "payment_request_id", request.ID.String(), "error", queryErr)
			if errors.Is(queryErr, ErrDarajaQueryUnavailable) {
				delay = time.Minute
				if retryDelay := time.Until(client.queryAfter); retryDelay > delay {
					delay = retryDelay
				}
				saveCtx, saveCancel := context.WithTimeout(ctx, 5*time.Second)
				if err := s.store.DeferUnavailableDarajaQuery(saveCtx, contributionsqlc.DeferUnavailableDarajaQueryParams{ID: request.ID, QueryAttempts: request.QueryAttempts}); err != nil {
					slog.Error("defer daraja query", "payment_request_id", request.ID.String(), "error", err)
				}
				saveCancel()
				continue
			}
			description = "Provider could not verify this checkout"
		}
		recovery := "awaiting_confirmation"
		if request.QueryAttempts >= 8 {
			recovery = "needs_review"
		}
		delay = 10 * time.Second
		if code == "0" {
			// A query has no receipt metadata; confirmed money still needs reconciliation.
			recovery = "received_needs_receipt"
		}
		saveCtx, saveCancel := context.WithTimeout(ctx, 5*time.Second)
		if code == "1032" || code == "1037" || code == "1" || code == "2001" {
			if err := s.MarkDarajaSTKPaymentFailed(saveCtx, request.CheckoutRequestID.String, description); err != nil {
				slog.Warn("record daraja result", "error", err)
			}
			recovery = "provider_failed"
		}
		if err := s.store.RecordDarajaQueryResult(saveCtx, contributionsqlc.RecordDarajaQueryResultParams{
			ID: request.ID, ProviderResult: text(fmt.Sprintf("%s %s", code, description)), RecoveryStatus: recovery,
		}); err != nil {
			slog.Error("save daraja query result", "error", err)
		}
		saveCancel()
	}
}
