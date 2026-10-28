package contribution

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	contributionsqlc "github.com/yaninyzwitty/caritas-backend/internal/contribution/repository/sqlc"
)

// DarajaHandlers owns the unauthenticated HTTP callbacks from M-Pesa. Without
// a separate handler type, Daraja routes would have to be mixed into gRPC
// services that Safaricom cannot call.
type DarajaHandlers struct {
	service     *Service
	callbackKey string
}

// NewDarajaHandlers wires Daraja HTTP callbacks to the contribution service.
// Removing it would expose the handler struct fields or require construction
// logic in main.
func NewDarajaHandlers(service *Service, callbackKey string) *DarajaHandlers {
	return &DarajaHandlers{service: service, callbackKey: callbackKey}
}

// RegisterDarajaRoutes keeps the public callback paths in one place. Removing
// it would spread unauthenticated webhook route registration through main.
func (h *DarajaHandlers) RegisterDarajaRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /webhooks/daraja/stk", h.HandleSTKCallback)
	mux.HandleFunc("POST /webhooks/daraja/stk/{requestKey}/{signature}", h.HandleSTKCallback)
}

// HandleSTKCallback accepts Daraja STK result callbacks and delegates all
// idempotent state changes to the contribution service. If this handler tried
// to post ledger entries directly, retries could bypass receipt invariants.
func (h *DarajaHandlers) HandleSTKCallback(w http.ResponseWriter, r *http.Request) {

	var payload darajaSTKCallbackPayload
	// i<<20 is Imb
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	var body json.RawMessage
	if err := decoder.Decode(&body); err != nil {
		http.Error(w, "invalid callback payload", http.StatusBadRequest)
		return
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid callback payload", http.StatusBadRequest)
		return
	}
	var resultCode struct {
		Body struct {
			Callback struct {
				ResultCode *int `json:"ResultCode"`
			} `json:"stkCallback"`
		} `json:"Body"`
	}
	if err := json.Unmarshal(body, &resultCode); err != nil || resultCode.Body.Callback.ResultCode == nil {
		http.Error(w, "missing callback result code", http.StatusBadRequest)
		return
	}

	callback := payload.Body.STKCallback
	checkoutID := strings.TrimSpace(callback.CheckoutRequestID)
	if checkoutID == "" {
		http.Error(w, "missing checkout request id", http.StatusBadRequest)
		return
	}

	// Canonical JSON makes redelivery idempotent while retaining all provider fields.
	var canonical any
	canonicalDecoder := json.NewDecoder(strings.NewReader(string(body)))
	canonicalDecoder.UseNumber()
	if err := canonicalDecoder.Decode(&canonical); err != nil {
		http.Error(w, "invalid callback", http.StatusBadRequest)
		return
	}
	body, err := json.Marshal(canonical)
	if err != nil {
		http.Error(w, "invalid callback", http.StatusBadRequest)
		return
	}
	params := contributionsqlc.InsertDarajaCallbackParams{
		CheckoutRequestID: checkoutID,
		Payload:           body,
		PayloadHash:       fmt.Sprintf("%x", sha256.Sum256(body)),
	}
	// Bound acceptance; receipt posting runs independently after commit.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	if key := r.PathValue("requestKey"); key != "" {
		mac := hmac.New(sha256.New, []byte(h.callbackKey))
		_, _ = mac.Write([]byte("daraja-callback:" + key))
		signature, decodeErr := hex.DecodeString(r.PathValue("signature"))
		if h.callbackKey == "" || decodeErr != nil || !hmac.Equal(signature, mac.Sum(nil)) {
			http.Error(w, "invalid callback signature", http.StatusForbidden)
			return
		}
		// Bind only a signed request, atomically with callback persistence, so a lost provider response cannot orphan payment.
		err = h.service.store.ExecTx(r.Context(), func(q contributionsqlc.Querier) error {
			request, err := q.GetContributionPaymentRequestByIdempotencyKey(r.Context(), key)
			if err != nil {
				return err
			}
			request, err = q.LockDarajaRequest(r.Context(), contributionsqlc.LockDarajaRequestParams{ID: request.ID, BranchID: request.BranchID})
			if err != nil {
				return err
			}
			if request.CheckoutRequestID.Valid && request.CheckoutRequestID.String != checkoutID {
				return ErrInconsistentReceipt
			}
			if !request.CheckoutRequestID.Valid {
				if _, err := q.UpdateContributionPaymentRequestCheckoutID(r.Context(), contributionsqlc.UpdateContributionPaymentRequestCheckoutIDParams{ID: request.ID, CheckoutRequestID: pgtype.Text{String: checkoutID, Valid: true}}); err != nil {
					return err
				}
			}
			return q.InsertDarajaCallback(r.Context(), params)
		})
	} else {
		err = h.service.store.InsertDarajaCallback(r.Context(), params)
	}
	if err != nil {
		slog.Error("save daraja callback", "checkout_request_id", checkoutID, "error", err)
		http.Error(w, "callback could not be saved", http.StatusServiceUnavailable)
		return
	}
	slog.Info("daraja callback saved", "checkout_request_id", checkoutID, "result_code", callback.ResultCode)
	writeDarajaAccepted(w)
}

// darajaSTKCallbackPayload mirrors the outer Daraja STK callback envelope.
// Removing it would force loose map parsing and make missing callback fields
// indistinguishable from wrong JSON shapes.
type darajaSTKCallbackPayload struct {
	Body darajaSTKCallbackBody `json:"Body"`
}

// darajaSTKCallbackBody exists because Daraja nests the STK callback under
// Body.stkCallback. Removing it would make the payload struct lie about the
// provider's actual wire shape.
type darajaSTKCallbackBody struct {
	STKCallback darajaSTKCallback `json:"stkCallback"`
}

// darajaSTKCallback contains only fields this service needs to make idempotent
// receipt decisions. Adding the whole Daraja payload would increase coupling
// without improving processing safety.
type darajaSTKCallback struct {
	CheckoutRequestID string                 `json:"CheckoutRequestID"`
	ResultCode        int                    `json:"ResultCode"`
	ResultDesc        string                 `json:"ResultDesc"`
	CallbackMetadata  darajaCallbackMetadata `json:"CallbackMetadata"`
}

// darajaCallbackMetadata stores Daraja's name/value metadata list. Without this
// type, amount and receipt extraction would be repeated as untyped JSON scans.
type darajaCallbackMetadata struct {
	Items []darajaCallbackMetadataItem `json:"Item"`
}

// darajaCallbackMetadataItem preserves one Daraja metadata value as raw JSON so
// both numeric and string values can be read without accepting arbitrary maps.
type darajaCallbackMetadataItem struct {
	Name  string          `json:"Name"`
	Value json.RawMessage `json:"Value"`
}

// successPayment validates and normalizes a successful Daraja callback. Without
// it, HandleSTKCallback would mix wire parsing with service calls.
func (c darajaSTKCallback) successPayment() (DarajaSTKPayment, error) {
	amountText, ok := c.CallbackMetadata.value("Amount")
	if !ok {
		return DarajaSTKPayment{}, ErrInvalidReceiptAmount
	}
	var amount pgtype.Numeric
	if err := amount.Scan(amountText); err != nil {
		return DarajaSTKPayment{}, ErrInvalidReceiptAmount
	}
	receipt, ok := c.CallbackMetadata.value("MpesaReceiptNumber")
	if !ok || strings.TrimSpace(receipt) == "" {
		return DarajaSTKPayment{}, ErrReceiptReferenceRequired
	}

	receivedAt := time.Now().UTC()
	if value, ok := c.CallbackMetadata.value("TransactionDate"); ok {
		parsed, err := time.ParseInLocation("20060102150405", value, time.FixedZone("Africa/Nairobi", 3*60*60))
		if err != nil {
			return DarajaSTKPayment{}, err
		}
		receivedAt = parsed.UTC()
	}

	return DarajaSTKPayment{
		CheckoutRequestID: c.CheckoutRequestID,
		MpesaReceipt:      receipt,
		Amount:            amount,
		ReceivedAt:        receivedAt,
	}, nil
}

// value returns one Daraja metadata value as text while accepting either JSON
// strings or numbers. Removing it would make amount and receipt parsing depend
// on provider-specific value types at each call site.
func (m darajaCallbackMetadata) value(name string) (string, bool) {
	for _, item := range m.Items {
		if item.Name != name || len(item.Value) == 0 {
			continue
		}
		var textValue string
		if err := json.Unmarshal(item.Value, &textValue); err == nil {
			return strings.TrimSpace(textValue), true
		}
		return strings.TrimSpace(string(item.Value)), true
	}
	return "", false
}

// writeDarajaAccepted sends the small acknowledgement Daraja expects after the
// callback is accepted. Without a single helper, failure and success branches
// can drift into different response shapes and trigger provider retries.
func writeDarajaAccepted(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ResultCode": 0,
		"ResultDesc": "Accepted",
	})
}
