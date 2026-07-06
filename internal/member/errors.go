package member

import (
	"errors"
)

var (
	ErrInvalidStatusTransition     = errors.New("invalid status transition")
	ErrMemberAlreadyClosed         = errors.New("member already closed")
	ErrInvalidIdentifier           = errors.New("invalid identifier")
	ErrBranchTransferNotSupported  = errors.New("branch transfer not supported")
)