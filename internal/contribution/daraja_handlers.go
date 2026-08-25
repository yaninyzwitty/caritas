package contribution

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// DarajaHandlers owns the unauthenticated HTTP callbacks from M-Pesa. Without
// a separate handler type, Daraja routes would have to be mixed into gRPC
// services that Safaricom cannot call.
type DarajaHandlers struct {
	ctx     context.Context
	service *Service
}

// NewDarajaHandlers wires Daraja HTTP callbacks to the contribution service.
// Removing it would expose the handler struct fields or require construction
// logic in main.
func NewDarajaHandlers(ctx context.Context, service *Service) *DarajaHandlers {
	return &DarajaHandlers{ctx: ctx, service: service}
}

// RegisterDarajaRoutes keeps the public callback paths in one place. Removing
// it would spread unauthenticated webhook route registration through main.
func (h *DarajaHandlers) RegisterDarajaRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /webhooks/daraja/stk", h.HandleSTKCallback)
}

// HandleSTKCallback accepts Daraja STK result callbacks and delegates all
// idempotent state changes to the contribution service. If this handler tried
// to post ledger entries directly, retries could bypass receipt invariants.
func (h *DarajaHandlers) HandleSTKCallback(w http.ResponseWriter, r *http.Request) {

	var payload darajaSTKCallbackPayload
	// i<<20 is Imb
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := decoder.Decode(&payload); err != nil {
		http.Error(w, "invalid callback payload", http.StatusBadRequest)
		return
	}

	callback := payload.Body.STKCallback
	checkoutID := strings.TrimSpace(callback.CheckoutRequestID)
	if checkoutID == "" {
		http.Error(w, "missing checkout request id", http.StatusBadRequest)
		return
	}

	if callback.ResultCode != 0 {
		go h.markSTKPaymentFailed(checkoutID, callback.ResultDesc)
		writeDarajaAccepted(w)
		return
	}

	payment, err := callback.successPayment()
	if err != nil {
		http.Error(w, "invalid successful callback metadata", http.StatusBadRequest)
		return
	}

	go h.processSTKPayment(checkoutID, payment)
	writeDarajaAccepted(w)
}

func (h *DarajaHandlers) processSTKPayment(checkoutID string, payment DarajaSTKPayment) {
	// TODO: Move Daraja callback processing to Temporal so accepted callbacks
	// survive process restarts and retry with workflow history.
	ctx, cancel := context.WithTimeout(h.ctx, 2*time.Minute)
	defer cancel()

	if _, err := h.service.ProcessDarajaSTKPayment(ctx, payment); err != nil {
		if errors.Is(err, ErrPaymentRequestNotFound) {
			slog.Warn("daraja stk callback without payment request", "checkout_request_id", checkoutID)
		} else {
			slog.Error("process daraja stk payment", "checkout_request_id", checkoutID, "error", err)
		}
	}
}

func (h *DarajaHandlers) markSTKPaymentFailed(checkoutID, reason string) {
	// TODO: Move Daraja callback processing to Temporal so accepted callbacks
	// survive process restarts and retry with workflow history.
	ctx, cancel := context.WithTimeout(h.ctx, 2*time.Minute)
	defer cancel()

	if err := h.service.MarkDarajaSTKPaymentFailed(ctx, checkoutID, reason); err != nil {
		slog.Warn("record daraja stk failure", "checkout_request_id", checkoutID, "error", err)
	}
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
		parsed, err := time.ParseInLocation("20060102150405", value, time.Local)
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
