package contribution

import "errors"

var (
	ErrInvalidReceiptAmount     = errors.New("contribution receipt amount must be positive")
	ErrInvalidAllocationPlan    = errors.New("allocation plan is invalid")
	ErrAllocationRequired       = errors.New("at least one allocation is required")
	ErrInvalidAllocation        = errors.New("allocation is invalid")
	ErrDuplicateAllocation      = errors.New("allocation is duplicated")
	ErrAllocationTotalMismatch  = errors.New("allocation total must equal received amount")
	ErrReceiptReferenceRequired = errors.New("external transaction id or checkout request id is required")
	ErrReceiptNotFound          = errors.New("contribution receipt not found")
)
