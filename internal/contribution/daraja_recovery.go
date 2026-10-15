package contribution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	contributionsqlc "github.com/yaninyzwitty/caritas-backend/internal/contribution/repository/sqlc"
)

// RecoverDaraja resumes persisted callbacks and queries after server restarts.
// Without this loop, acknowledged callbacks and missing confirmations have no recovery owner.
func (s *Service) RecoverDaraja(ctx context.Context, client *DarajaClient) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		for i := 0; i < 50 && ctx.Err() == nil; i++ {
			event, err := s.store.ClaimDarajaCallback(ctx)
			if errors.Is(err, pgx.ErrNoRows) {
				break
			}
			if err != nil {
				slog.Error("claim daraja callback", "error", err)
				break
			}
			var payload darajaSTKCallbackPayload
			err = json.Unmarshal(event.Payload, &payload)
			if err == nil {
				callback := payload.Body.STKCallback
				if callback.ResultCode != 0 {
					err = s.MarkDarajaSTKPaymentFailed(ctx, callback.CheckoutRequestID, callback.ResultDesc)
				} else {
					var payment DarajaSTKPayment
					payment, err = callback.successPayment()
					if err == nil {
						_, err = s.ProcessDarajaSTKPayment(ctx, payment)
					}
				}
			}
			message := ""
			if err != nil {
				message = err.Error()
				slog.Warn("recover daraja callback", "callback_id", event.ID.String(), "error", err)
			}
			if finishErr := s.store.FinishDarajaCallback(ctx, contributionsqlc.FinishDarajaCallbackParams{ID: event.ID, LastError: message}); finishErr != nil {
				slog.Error("finish daraja callback", "error", finishErr)
			}
		}
		if client != nil {
			for i := 0; i < 10 && ctx.Err() == nil; i++ {
				request, err := s.store.ClaimDarajaQuery(ctx)
				if errors.Is(err, pgx.ErrNoRows) {
					break
				}
				if err != nil {
					slog.Error("claim daraja query", "error", err)
					break
				}
				queryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
				code, description, queryErr := client.QuerySTK(queryCtx, request.CheckoutRequestID.String)
				cancel()
				recovery := "awaiting_confirmation"
				if request.QueryAttempts >= 8 {
					recovery = "needs_review"
				}
				if queryErr != nil {
					description = "Provider query unavailable"
					slog.Warn("query daraja checkout", "payment_request_id", request.ID.String(), "error", queryErr)
				} else if code == "0" {
					// STK query does not supply the receipt metadata required for posting.
					recovery = "received_needs_receipt"
				} else if code == "1032" || code == "1037" || code == "1" || code == "2001" {
					if err := s.MarkDarajaSTKPaymentFailed(ctx, request.CheckoutRequestID.String, description); err != nil {
						slog.Warn("record daraja result", "error", err)
					}
					recovery = "provider_failed"
				}
				if err := s.store.RecordDarajaQueryResult(ctx, contributionsqlc.RecordDarajaQueryResultParams{
					ID: request.ID, ProviderResult: pgtype.Text{String: fmt.Sprintf("%s %s", code, description), Valid: true}, RecoveryStatus: recovery,
				}); err != nil {
					slog.Error("save daraja query result", "error", err)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
