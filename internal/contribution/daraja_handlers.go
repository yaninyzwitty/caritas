package contribution

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
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
	service *Service
}

// NewDarajaHandlers wires Daraja HTTP callbacks to the contribution service.
// Removing it would expose the handler struct fields or require construction
// logic in main.
func NewDarajaHandlers(service *Service) *DarajaHandlers {
	return &DarajaHandlers{service: service}
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
	if err := h.service.store.InsertDarajaCallback(r.Context(), contributionsqlc.InsertDarajaCallbackParams{
		CheckoutRequestID: checkoutID,
		Payload:           body,
		PayloadHash:       fmt.Sprintf("%x", sha256.Sum256(body)),
	}); err != nil {
		http.Error(w, "callback could not be saved", http.StatusServiceUnavailable)
		return
	}
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
