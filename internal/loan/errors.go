package loan

import "errors"

var (
	ErrLoanNotFound             = errors.New("loan not found")
	ErrGuarantorNotFound        = errors.New("guarantor not found")
	ErrInvalidLoanAmount        = errors.New("invalid loan amount")
	ErrInvalidInterestRate      = errors.New("invalid interest rate")
	ErrInvalidRepaymentPeriod   = errors.New("invalid repayment period")
	ErrInvalidGuaranteedAmount  = errors.New("invalid guaranteed amount")
	ErrInvalidStatusTransition  = errors.New("invalid loan status transition")
	ErrInvalidGuarantorStatus   = errors.New("invalid guarantor status")
	ErrSelfGuarantee            = errors.New("member cannot guarantee own loan")
	ErrTooManyGuarantors        = errors.New("loan cannot have more than 20 guarantors")
	ErrInsufficientGuarantors   = errors.New("loan requires at least one approved guarantor")
	ErrInsufficientGuarantee    = errors.New("approved guarantees do not cover principal")
	ErrPaymentNotAllowed        = errors.New("loan status does not allow repayment")
	ErrGatewayTransactionID     = errors.New("payment gateway transaction id is required")
	ErrUnsupportedLoanOperation = errors.New("loan operation is not supported by current schema")
)
