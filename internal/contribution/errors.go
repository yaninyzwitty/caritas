package contribution

import "errors"

var (
	ErrInvalidReceiptAmount     = errors.New("contribution receipt amount must be positive")
	ErrInvalidAllocationPlan    = errors.New("allocation plan is invalid")
	ErrAllocationRequired       = errors.New("at least one allocation is required")
	ErrInvalidAllocation        = errors.New("allocation is invalid")
	ErrInvalidPayment           = errors.New("payment is invalid")
	ErrDuplicateAllocation      = errors.New("allocation is duplicated")
	ErrAllocationTotalMismatch  = errors.New("allocation total must equal received amount")
	ErrReceiptReferenceRequired = errors.New("receipt reference is required")
	ErrReceiptNotFound          = errors.New("contribution receipt not found")
	ErrReceiptNotProcessable    = errors.New("contribution receipt is not processable")
	ErrAllocationNotSupported   = errors.New("contribution allocation is not supported")
	ErrOwningServiceMissing     = errors.New("owning service is not configured")
	ErrPaymentRequestNotFound   = errors.New("contribution payment request not found")
	ErrPaymentRequestInProgress = errors.New("contribution payment request is already being initiated")
	ErrPaymentAmountMismatch    = errors.New("contribution payment amount does not match request")
	ErrInconsistentReceipt      = errors.New("inconsistent contribution receipt")
	ErrDarajaClientMissing      = errors.New("daraja client is not configured")
	ErrCashierSessionNotFound   = errors.New("cashier session not found")
	ErrCashierSessionState      = errors.New("cashier session is not in the required state")
	ErrCashVarianceReason       = errors.New("cash variance requires a reason")
	ErrCashDepositInvalid       = errors.New("cash deposit is invalid")
	ErrCashSeparationOfDuties   = errors.New("cashier cannot accept their own cash handover")
	ErrCashDepositSelfVerify    = errors.New("cash deposit recorder cannot verify the same deposit")
)
