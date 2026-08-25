package share

import "errors"

var (
	ErrAccountNotFound      = errors.New("share account not found")
	ErrAccountAlreadyExists = errors.New("share account already exists for member")
	ErrAccountNotActive     = errors.New("share account is not active")
	ErrInsufficientBalance  = errors.New("insufficient share balance")
	ErrTransactionNotFound  = errors.New("share transaction not found")
	ErrAdjustmentNotFound   = errors.New("share adjustment not found")
	ErrInvalidIdentifier    = errors.New("invalid identifier: provide account_id or member_id")
	ErrAdjustmentExists     = errors.New("adjustment already approved for transaction")
	ErrAdjustmentNotPending = errors.New("share adjustment is not pending")
	ErrNotAdjustment        = errors.New("transaction is not an adjustment")
	ErrAlreadyApproved      = errors.New("transaction has already been approved")
	ErrUnauthorizedApprover = errors.New("approver is not authorized to approve this transaction")
	ErrDuplicateReference   = errors.New("duplicate reference number for share transaction")
	ErrCannotReverse        = errors.New("share transaction cannot be reversed")
	ErrAlreadyReversed      = errors.New("share transaction already reversed")
)
