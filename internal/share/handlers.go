package share

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	memberv1 "github.com/yaninyzwitty/caritas-backend/gen/caritas/member/v1"
	sharev1 "github.com/yaninyzwitty/caritas-backend/gen/caritas/share/v1"
	sharesqlc "github.com/yaninyzwitty/caritas-backend/internal/share/repository/sqlc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const defaultBranchID = 1

type Handlers struct {
	sharev1.UnimplementedShareServiceServer
	service *Service
	store   *Store
}

func NewHandlers(service *Service, store *Store) *Handlers {
	return &Handlers{service: service, store: store}
}

// resolveBranchID falls back to the seeded default branch when the caller omits
// branch_id. Without it, a zero branch_id would create/list accounts under a
// non-existent branch 0.
func resolveBranchID(branchID int64) int64 {
	if branchID == 0 {
		return defaultBranchID
	}
	return branchID
}

// encodeCursor packs a (created_at, id) cursor into an opaque page token. The
// share cursor is composite (AGENTS.md: cursor pagination on created_at, id), so
// a bare UUID is not enough to resume. Without it, paginated listing could not
// offer a next page.
func encodeCursor(t pgtype.Timestamptz, id pgtype.UUID) (string, error) {
	if !t.Valid || !id.Valid {
		return "", nil
	}
	idStr, err := uuidToString(id)
	if err != nil {
		return "", err
	}
	raw := strings.Join([]string{
		t.Time.UTC().Format(time.RFC3339Nano),
		idStr,
	}, "|")
	return base64.RawURLEncoding.EncodeToString([]byte(raw)), nil
}

// decodeCursor unpacks a page token back into (created_at, id). An empty token
// means the first page. Without it, subsequent-page requests could not be
// served.
func decodeCursor(token string) (pgtype.Timestamptz, pgtype.UUID, error) {
	var ts pgtype.Timestamptz
	var id pgtype.UUID
	if token == "" {
		return ts, id, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return ts, id, fmt.Errorf("invalid page token: %w", err)
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return ts, id, fmt.Errorf("invalid page token")
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return ts, id, fmt.Errorf("invalid page token: %w", err)
	}
	id, err = stringToUUID(parts[1])
	if err != nil {
		return ts, id, err
	}
	return pgtype.Timestamptz{Time: t, Valid: true}, id, nil
}

// OpenShareAccount handles share account creation. Without it the RPC returns
// Unimplemented and members cannot begin holding shares.
func (h *Handlers) OpenShareAccount(ctx context.Context, req *sharev1.OpenShareAccountRequest) (*sharev1.OpenShareAccountResponse, error) {
	memberID, err := stringToUUID(req.GetMemberId())
	if err != nil {
		return nil, err
	}
	account, err := h.service.OpenShareAccount(ctx, memberID, resolveBranchID(req.GetBranchId()))
	if err != nil {
		return nil, err
	}
	accountID, err := uuidToString(account.ID)
	if err != nil {
		return nil, err
	}
	return &sharev1.OpenShareAccountResponse{
		AccountId: accountID,
		Status:    accountStatusToProto(account.Status),
	}, nil
}

// GetShareAccount handles account lookup by account_id or member_id. Without it
// callers cannot inspect an account's status or identifiers.
func (h *Handlers) GetShareAccount(ctx context.Context, req *sharev1.GetShareAccountRequest) (*sharev1.GetShareAccountResponse, error) {
	var account sharesqlc.ShareAccount

	switch id := req.GetIdentifier().(type) {
	case *sharev1.GetShareAccountRequest_AccountId:
		accountID, err := stringToUUID(id.AccountId)
		if err != nil {
			return nil, fmt.Errorf("invalid uuid, %w", err)
		}

		account, err = h.store.GetAccountByID(ctx, accountID)
		if err != nil {
			return nil, status.Errorf(codes.NotFound, "failed to get account by id, %v", err)
		}
	case *sharev1.GetShareAccountRequest_MemberId:
		memberID, err := stringToUUID(id.MemberId)
		if err != nil {
			return nil, fmt.Errorf("invalid uuid, %w", err)
		}

		account, err = h.store.GetAccountByMemberID(ctx, memberID)
		if err != nil {
			return nil, status.Errorf(codes.NotFound, "failed to get account by member id, %v", err)
		}
	default:
		return nil, status.Errorf(codes.InvalidArgument, "identifier is required")

	}
	return &sharev1.GetShareAccountResponse{
		Account: convertAccountToProto(account),
	}, nil

}

// ListShareAccounts handles branch-scoped, cursor-paginated account listing.
// Without it the RPC returns Unimplemented and admins cannot browse accounts.
func (h *Handlers) ListShareAccounts(ctx context.Context, req *sharev1.ListShareAccountsRequest) (*sharev1.ListShareAccountsResponse, error) {
	const (
		defaultPageSize = 50
		maxPageSize     = 1000
	)
	limit := req.GetPageSize()
	switch {
	case limit <= 0:
		limit = defaultPageSize
	case limit > maxPageSize:
		limit = maxPageSize
	}

	cursorTS, cursorID, err := decodeCursor(req.GetPageToken())
	if err != nil {
		return nil, err
	}

	var statusFilter sharesqlc.NullShareAccountStatus
	if sf := req.GetStatusFilter(); sf != sharev1.ShareAccountStatus_SHARE_ACCOUNT_STATUS_UNSPECIFIED {
		statusFilter = sharesqlc.NullShareAccountStatus{
			ShareAccountStatus: accountStatusFromProto(sf),
			Valid:              true,
		}
	}

	accounts, err := h.store.ListAccounts(ctx, sharesqlc.ListAccountsParams{
		BranchID:     resolveBranchID(req.GetBranchId()),
		Column2:      cursorTS,
		ID:           cursorID,
		StatusFilter: statusFilter,
		Limit:        limit + 1, // fetch one extra to determine if there's a next page
	})
	if err != nil {
		return nil, err
	}

	resp := &sharev1.ListShareAccountsResponse{}

	hasMore := len(accounts) > int(limit)
	if hasMore {
		// the client receives only 'limit' rows
		last := accounts[limit-1]

		token, err := encodeCursor(last.CreatedAt, last.ID)
		if err != nil {
			return nil, fmt.Errorf("encode cursor: %w", err)
		}
		resp.NextPageToken = token
		// we then drop the look ahead row from the response
		accounts = accounts[:limit]
	}

	resp.Accounts = make([]*sharev1.ShareAccount, 0, len(accounts))

	for _, account := range accounts {
		resp.Accounts = append(resp.Accounts, convertAccountToProto(account))
	}

	return resp, nil
}

// PurchaseShares handles crediting shares to an account. Without it the SACCO
// cannot receive share-capital inflow.
func (h *Handlers) PurchaseShares(ctx context.Context, req *sharev1.PurchaseSharesRequest) (*sharev1.PurchaseSharesResponse, error) {
	accountID, err := stringToUUID(req.GetAccountId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid account_id: %v", err)
	}
	referenceID, err := stringToUUID(req.GetReferenceId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid reference_id: %v", err)
	}
	originatorID, err := stringToUUID(req.GetOriginatorId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid originator_id: %v", err)
	}
	tx, err := h.service.PurchaseShares(ctx, accountID, moneyToNumeric(req.GetAmount()), referenceID, originatorID, req.GetReason())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to purchase shares: %v", err)
	}
	txID, err := uuidToString(tx.ID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to convert transaction ID: %v", err)
	}
	return &sharev1.PurchaseSharesResponse{
		TransactionId: txID,
		BalanceAfter:  numericToMoney(tx.BalanceAfter),
	}, nil
}

// WithdrawShares handles debiting shares from an account. Without it members
// cannot exit shares or realise their value.
func (h *Handlers) WithdrawShares(ctx context.Context, req *sharev1.WithdrawSharesRequest) (*sharev1.WithdrawSharesResponse, error) {
	accountID, err := stringToUUID(req.GetAccountId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid account_id: %v", err)
	}
	referenceID, err := stringToUUID(req.GetReferenceId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid reference_id: %v", err)
	}
	originatorID, err := stringToUUID(req.GetOriginatorId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid originator_id: %v", err)
	}
	tx, err := h.service.WithdrawShares(ctx, accountID, moneyToNumeric(req.GetAmount()), referenceID, originatorID, req.GetReason())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to withdraw shares: %v", err)
	}
	txID, err := uuidToString(tx.ID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to convert transaction ID: %v", err)
	}
	return &sharev1.WithdrawSharesResponse{
		TransactionId: txID,
		BalanceAfter:  numericToMoney(tx.BalanceAfter),
	}, nil
}

// GetShareBalance handles balance reads. The account is verified first so a
// missing account is not misreported as a zero balance; without this check the
// loan domain could treat a non-existent account as zero collateral.
func (h *Handlers) GetShareBalance(ctx context.Context, req *sharev1.GetShareBalanceRequest) (*sharev1.GetShareBalanceResponse, error) {
	accountID, err := stringToUUID(req.GetAccountId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid account_id: %v", err)
	}
	if _, err := h.store.GetAccountByID(ctx, accountID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrAccountNotFound
		}
		return nil, status.Errorf(codes.Internal, "failed to get account: %v", err)
	}

	balance := &memberv1.Money{CurrencyCode: "KES"}
	if latest, err := h.store.GetLatestBalance(ctx, accountID); err == nil {
		balance = numericToMoney(latest)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Errorf(codes.Internal, "failed to get latest balance: %v", err)
	}

	accountIDStr, err := uuidToString(accountID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to convert account ID: %v", err)
	}
	return &sharev1.GetShareBalanceResponse{
		Balance:   balance,
		AccountId: accountIDStr,
	}, nil
}

// ListShareTransactions handles cursor-paginated transaction history for an
// account. Without it the ledger cannot be inspected or reconciled.
func (h *Handlers) ListShareTransactions(ctx context.Context, req *sharev1.ListShareTransactionsRequest) (*sharev1.ListShareTransactionsResponse, error) {
	const (
		defaultPageSize = 50
		maxPageSize     = 1000
	)
	limit := req.GetPageSize()
	switch {
	case limit <= 0:
		limit = defaultPageSize
	case limit > maxPageSize:
		limit = maxPageSize
	}

	accountID, err := stringToUUID(req.GetAccountId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid account_id: %v", err)
	}
	cursorTS, cursorID, err := decodeCursor(req.GetPageToken())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid page_token: %v", err)
	}

	txs, err := h.store.ListTransactions(ctx, sharesqlc.ListTransactionsParams{
		ShareAccountID: accountID,
		Column2:        cursorTS,
		ID:             cursorID,
		Limit:          limit + 1,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list transactions: %v", err)
	}

	resp := &sharev1.ListShareTransactionsResponse{}

	hasMore := len(txs) > int(limit)

	if hasMore {
		last := txs[limit-1]
		token, err := encodeCursor(last.CreatedAt, last.ID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to encode cursor: %v", err)
		}
		resp.NextPageToken = token
		txs = txs[:limit]
	}
	resp.Transactions = make([]*sharev1.ShareTransaction, 0, len(txs))
	for _, transaction := range txs {
		resp.Transactions = append(resp.Transactions, convertTransactionToProto(transaction))
	}

	return resp, nil
}

// GetShareTransaction handles single-transaction lookup. Without it individual
// ledger entries cannot be retrieved for audit.
func (h *Handlers) GetShareTransaction(ctx context.Context, req *sharev1.GetShareTransactionRequest) (*sharev1.GetShareTransactionResponse, error) {
	txID, err := stringToUUID(req.GetTransactionId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid transaction_id: %v", err)
	}
	tx, err := h.store.GetTransactionByID(ctx, txID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTransactionNotFound
		}
		return nil, status.Errorf(codes.Internal, "failed to get transaction: %v", err)
	}
	return &sharev1.GetShareTransactionResponse{Transaction: convertTransactionToProto(tx)}, nil
}

// ApproveShareAdjustment handles recording the audit approval for an adjustment
// transaction. Without it manual corrections cannot be approved or audited.
func (h *Handlers) ApproveShareAdjustment(ctx context.Context, req *sharev1.ApproveShareAdjustmentRequest) (*sharev1.ApproveShareAdjustmentResponse, error) {
	transactionID, err := stringToUUID(req.GetTransactionId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid transaction_id")
	}
	approverID, err := stringToUUID(req.GetApproverId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid approver_id")
	}
	var auditReportID pgtype.UUID
	if req.GetAuditReportId() != "" {
		auditReportID, err = stringToUUID(req.GetAuditReportId())
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid audit_report_id")
		}
	}
	adjustment, err := h.service.ApproveShareAdjustment(ctx, transactionID, approverID, req.GetReason(), auditReportID)
	if err != nil {
		return nil, mapServiceError(err)
	}
	adjustmentID, err := uuidToString(adjustment.ID)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to encode adjustment ID")
	}
	return &sharev1.ApproveShareAdjustmentResponse{AdjustmentId: adjustmentID}, nil
}
