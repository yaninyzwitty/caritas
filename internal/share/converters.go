package share

import (
	"context"
	"errors"
	"math/big"

	"github.com/jackc/pgx/v5/pgtype"
	memberv1 "github.com/yaninyzwitty/caritas-backend/gen/caritas/member/v1"
	sharev1 "github.com/yaninyzwitty/caritas-backend/gen/caritas/share/v1"
	sharesqlc "github.com/yaninyzwitty/caritas-backend/internal/share/repository/sqlc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	timestamppb "google.golang.org/protobuf/types/known/timestamppb"
)

// accountStatusToProto maps the DB enum string to the proto enum. Without it,
// the account status would surface as an opaque string instead of the typed
// ShareAccountStatus the API contract promises.
func accountStatusToProto(s sharesqlc.ShareAccountStatus) sharev1.ShareAccountStatus {
	switch s {
	case sharesqlc.ShareAccountStatusActive:
		return sharev1.ShareAccountStatus_SHARE_ACCOUNT_STATUS_ACTIVE
	case sharesqlc.ShareAccountStatusDormant:
		return sharev1.ShareAccountStatus_SHARE_ACCOUNT_STATUS_DORMANT
	case sharesqlc.ShareAccountStatusClosed:
		return sharev1.ShareAccountStatus_SHARE_ACCOUNT_STATUS_CLOSED
	default:
		return sharev1.ShareAccountStatus_SHARE_ACCOUNT_STATUS_UNSPECIFIED
	}
}

// accountStatusFromProto is the inverse of accountStatusToProto, used to turn a
// request's status_filter into the nullable DB enum the ListAccounts query
// expects. UNSPECIFIED maps to an empty string so the caller can build a
// NullShareAccountStatus with Valid=false and skip the filter.
func accountStatusFromProto(s sharev1.ShareAccountStatus) sharesqlc.ShareAccountStatus {
	switch s {
	case sharev1.ShareAccountStatus_SHARE_ACCOUNT_STATUS_ACTIVE:
		return sharesqlc.ShareAccountStatusActive
	case sharev1.ShareAccountStatus_SHARE_ACCOUNT_STATUS_DORMANT:
		return sharesqlc.ShareAccountStatusDormant
	case sharev1.ShareAccountStatus_SHARE_ACCOUNT_STATUS_CLOSED:
		return sharesqlc.ShareAccountStatusClosed
	default:
		return ""
	}
}

// transactionTypeToProto maps the DB enum string to the proto enum. Without it,
// transaction types could not be represented in responses.
func transactionTypeToProto(t sharesqlc.ShareTransactionType) sharev1.ShareTransactionType {
	switch t {
	case sharesqlc.ShareTransactionTypePurchase:
		return sharev1.ShareTransactionType_SHARE_TRANSACTION_TYPE_PURCHASE
	case sharesqlc.ShareTransactionTypeWithdrawal:
		return sharev1.ShareTransactionType_SHARE_TRANSACTION_TYPE_WITHDRAWAL
	case sharesqlc.ShareTransactionTypeDividend:
		return sharev1.ShareTransactionType_SHARE_TRANSACTION_TYPE_DIVIDEND
	case sharesqlc.ShareTransactionTypeReversal:
		return sharev1.ShareTransactionType_SHARE_TRANSACTION_TYPE_REVERSAL
	case sharesqlc.ShareTransactionTypeAdjustment:
		return sharev1.ShareTransactionType_SHARE_TRANSACTION_TYPE_ADJUSTMENT
	default:
		return sharev1.ShareTransactionType_SHARE_TRANSACTION_TYPE_UNSPECIFIED
	}
}

// moneyToNumeric collapses a proto Money (units + nanos) into a single pgtype
// Numeric at scale 1e-9 so Postgres stores it in NUMERIC(19,4) without float
// rounding. Without it, share amounts would lose precision on write.
func moneyToNumeric(m *memberv1.Money) pgtype.Numeric {
	if m == nil {
		return pgtype.Numeric{}
	}
	total := new(big.Int).Mul(big.NewInt(m.GetUnits()), big.NewInt(1_000_000_000))
	total.Add(total, big.NewInt(int64(m.GetNanos())))
	return pgtype.Numeric{Int: total, Exp: -9, Valid: true}
}

// numericToNanos normalises a pgtype.Numeric (value = Int * 10^Exp) into an
// integer count of nanos. Postgres reads NUMERIC(19,4) back with Exp=-4, not
// -9, so the raw Int cannot be added/subtracted directly; normalising to a
// common scale is the only correct way to do ledger math in Go.
func numericToNanos(n pgtype.Numeric) *big.Int {
	v := new(big.Int)
	if n.Int == nil {
		return v
	}
	v.Set(n.Int)
	shift := int(n.Exp) + 9
	if shift > 0 {
		v.Mul(v, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(shift)), nil))
	} else if shift < 0 {
		v.Quo(v, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-shift)), nil))
	}
	return v
}

// numericToMoney is the inverse of moneyToNumeric, splitting the normalised
// nanos back into proto Money units + nanos. Without it, balance/amount
// responses would be malformed.
func numericToMoney(n pgtype.Numeric) *memberv1.Money {
	nanos := numericToNanos(n)
	billion := big.NewInt(1_000_000_000)
	units := new(big.Int).Quo(nanos, billion)
	frac := new(big.Int).Mod(nanos, billion)
	return &memberv1.Money{CurrencyCode: "KES", Units: units.Int64(), Nanos: int32(frac.Int64())}
}

// timestampToProto converts a DB timestamptz to the proto type, returning nil
// for NULL so optional timestamps stay absent rather than zero-time. Without it,
// NULL opened_at/created_at would surface as the Unix epoch.
func timestampToProto(t pgtype.Timestamptz) *timestamppb.Timestamp {
	if !t.Valid {
		return nil
	}
	return timestamppb.New(t.Time)
}

// convertAccountToProto maps a share_accounts row to the proto message. The ID
// conversion error is ignored because the column is NOT NULL, so a row read from
// the DB always carries a valid UUID; failing here would mean the row itself is
// corrupt, which the caller cannot recover from anyway.
func convertAccountToProto(a sharesqlc.ShareAccount) *sharev1.ShareAccount {
	id, _ := uuidToString(a.ID)
	memberID, _ := uuidToString(a.MemberID)
	return &sharev1.ShareAccount{
		Id:        id,
		MemberId:  memberID,
		BranchId:  a.BranchID,
		Status:    accountStatusToProto(a.Status),
		OpenedAt:  timestampToProto(a.OpenedAt),
		CreatedAt: timestampToProto(a.CreatedAt),
		UpdatedAt: timestampToProto(a.UpdatedAt),
	}
}

// convertTransactionToProto maps a share_transactions row to the proto message.
// Nullable UUIDs (reversal_of, originator_id) gracefully become empty strings;
// without this helper every read/list response would have to repeat the
// field-by-field mapping.
func convertTransactionToProto(t sharesqlc.ShareTransaction) *sharev1.ShareTransaction {
	id, _ := uuidToString(t.ID)
	accountID, _ := uuidToString(t.ShareAccountID)
	referenceID, _ := uuidToString(t.ReferenceID)
	reversalOf, _ := uuidToString(t.ReversalOf)
	originatorID, _ := uuidToString(t.OriginatorID)
	return &sharev1.ShareTransaction{
		Id:             id,
		ShareAccountId: accountID,
		Type:           transactionTypeToProto(t.Type),
		Amount:         numericToMoney(t.Amount),
		BalanceAfter:   numericToMoney(t.BalanceAfter),
		ReferenceId:    referenceID,
		ReversalOf:     reversalOf,
		Reason:         t.Reason.String,
		OriginatorId:   originatorID,
		CreatedAt:      timestampToProto(t.CreatedAt),
	}
}

func mapServiceError(err error) error {
	switch {
	case errors.Is(err, ErrNotAdjustment):
		return status.Error(codes.NotFound, "share adjustment not found")

	case errors.Is(err, ErrAlreadyApproved):
		return status.Error(codes.FailedPrecondition, "share adjustment already approved")

	case errors.Is(err, ErrUnauthorizedApprover):
		return status.Error(codes.PermissionDenied, "approver is not authorized")

	case errors.Is(err, ErrDuplicateReference):
		return status.Error(codes.AlreadyExists, "duplicate reference")

	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())

	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())

	default:
		return status.Error(codes.Internal, "internal server error")
	}
}
