package contribution

import (
	"errors"
	"testing"
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
