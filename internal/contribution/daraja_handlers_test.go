package contribution

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	contributionsqlc "github.com/yaninyzwitty/caritas-backend/internal/contribution/repository/sqlc"
)

func TestDarajaSTKCallbackSuccessPaymentParsesNumericMetadata(t *testing.T) {
	callback := darajaSTKCallback{
		CheckoutRequestID: "ws_CO_123",
		ResultCode:        0,
		CallbackMetadata: darajaCallbackMetadata{Items: []darajaCallbackMetadataItem{
			{Name: "Amount", Value: []byte(`1000`)},
			{Name: "MpesaReceiptNumber", Value: []byte(`"RCP123"`)},
			{Name: "TransactionDate", Value: []byte(`20260807121530`)},
		}},
	}

	payment, err := callback.successPayment()
	if err != nil {
		t.Fatalf("success payment: %v", err)
	}
	if payment.CheckoutRequestID != "ws_CO_123" || payment.MpesaReceipt != "RCP123" {
		t.Fatalf("payment = %+v", payment)
	}
	if numericToScale(payment.Amount, -4).String() != "10000000" {
		t.Fatalf("amount = %+v", payment.Amount)
	}
	if payment.ReceivedAt.Format(time.RFC3339) != "2026-08-07T09:15:30Z" {
		t.Fatalf("provider timestamp must use Nairobi time: %s", payment.ReceivedAt)
	}
}

// callbackPersistenceStub isolates acknowledgement from the database; without it
// a unit test cannot prove failed persistence prevents a successful response.
type callbackPersistenceStub struct {
	contributionsqlc.Querier
	err   error
	saved []contributionsqlc.InsertDarajaCallbackParams
}

// InsertDarajaCallback records attempts synchronously so a premature ACK is observable.
func (s *callbackPersistenceStub) InsertDarajaCallback(_ context.Context, params contributionsqlc.InsertDarajaCallbackParams) error {
	s.saved = append(s.saved, params)
	return s.err
}

func TestDarajaCallbackAcknowledgementRequiresDurablePersistence(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "saved", true: "database unavailable"}[fail], func(t *testing.T) {
			stub := &callbackPersistenceStub{}
			if fail {
				stub.err = errors.New("database unavailable")
			}
			h := NewDarajaHandlers(NewService(&Store{Querier: stub}, nil, nil))
			body := `{"Body":{"stkCallback":{"CheckoutRequestID":"checkout","MerchantRequestID":"merchant","ResultCode":0,"CallbackMetadata":{"Item":[{"Name":"Amount","Value":1000},{"Name":"MpesaReceiptNumber","Value":"RCP123"}]}}}}`
			response := httptest.NewRecorder()
			h.HandleSTKCallback(response, httptest.NewRequest(http.MethodPost, "/webhooks/daraja/stk", strings.NewReader(body)))
			want := http.StatusOK
			if fail {
				want = http.StatusServiceUnavailable
			}
			if response.Code != want || len(stub.saved) != 1 {
				t.Fatalf("status %d, persistence attempts %d", response.Code, len(stub.saved))
			}
			if !strings.Contains(string(stub.saved[0].Payload), `"MerchantRequestID":"merchant"`) {
				t.Fatal("original provider fields were discarded")
			}
		})
	}
}

func TestDarajaCallbackMissingResultDoesNotBecomeSuccess(t *testing.T) {
	stub := &callbackPersistenceStub{}
	h := NewDarajaHandlers(NewService(&Store{Querier: stub}, nil, nil))
	response := httptest.NewRecorder()
	h.HandleSTKCallback(response, httptest.NewRequest(http.MethodPost, "/webhooks/daraja/stk", strings.NewReader(`{"Body":{"stkCallback":{"CheckoutRequestID":"checkout"}}}`)))
	if response.Code != http.StatusBadRequest || len(stub.saved) != 0 {
		t.Fatalf("status %d, saved %d", response.Code, len(stub.saved))
	}
}

func TestDarajaSTKCallbackSuccessPaymentRequiresReceipt(t *testing.T) {
	callback := darajaSTKCallback{
		CheckoutRequestID: "ws_CO_123",
		ResultCode:        0,
		CallbackMetadata: darajaCallbackMetadata{Items: []darajaCallbackMetadataItem{
			{Name: "Amount", Value: []byte(`1000`)},
		}},
	}

	_, err := callback.successPayment()
	if !errors.Is(err, ErrReceiptReferenceRequired) {
		t.Fatalf("error = %v, want %v", err, ErrReceiptReferenceRequired)
	}
}
