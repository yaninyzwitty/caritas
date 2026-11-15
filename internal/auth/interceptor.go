package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/lestrrat-go/httprc/v3"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	jwksStartupTimeout = 20 * time.Second

	roleSystemAdmin = "system_admin"
	roleManager     = "manager"
	roleLoanOfficer = "loan_officer"
	roleCashier     = "cashier"
	roleAuditor     = "auditor"
	roleChairperson = "chairperson"
	roleSecretary   = "secretary"

	permissionStaffManage     = "staff.manage"
	permissionMemberRead      = "member.read"
	permissionMemberWrite     = "member.write"
	permissionMemberStatus    = "member.status"
	permissionShareRead       = "share.read"
	permissionShareWrite      = "share.write"
	permissionShareApprove    = "share.approve"
	permissionLoanRead        = "loan.read"
	permissionLoanWrite       = "loan.write"
	permissionLoanApprove     = "loan.approve"
	permissionLoanDisburse    = "loan.disburse"
	permissionRepaymentRecord = "repayment.record"
	permissionCreditWithdraw  = "credit.withdraw"
	permissionCashRecord      = "cash.record"
	permissionCashApprove     = "cash.approve"
	permissionCashRead        = "cash.read"
	permissionCreditRead      = "credit.read"
	permissionCreditAccess    = "credit.access"
	permissionCEEPRead        = "ceep.read"
	permissionDividendRead    = "dividend.read"
	permissionDashboardRead   = "dashboard.read"
	permissionAuthenticated   = "authenticated"
)

// Verifier owns the Better Auth key cache and staff lookup used by the gRPC
// boundary. Without one shared verifier, every request would download signing
// keys and handlers would receive unauthenticated, audit-incompatible actors.
type Verifier struct {
	cache    *jwk.Cache
	keyset   jwk.Set
	issuer   string
	audience string
	store    *Store
}

// NewVerifier initializes the shared JWKS cache before serving traffic. Without
// the eager fetch, an invalid issuer configuration would surface only after the
// first protected operation reached the server.
func NewVerifier(ctx context.Context, store *Store, jwksURL, issuer, audience string) (*Verifier, error) {
	cache, err := jwk.NewCache(ctx, httprc.NewClient())
	if err != nil {
		return nil, fmt.Errorf("create JWKS cache: %w", err)
	}
	readyCtx, cancel := context.WithTimeout(ctx, jwksStartupTimeout)
	defer cancel()

	if err := cache.Register(
		readyCtx,
		jwksURL,
		jwk.WithConstantInterval(5*time.Minute),
		jwk.WithWaitReady(true)); err != nil {
		_ = cache.Shutdown(context.Background())
		return nil, fmt.Errorf("fetch JWKS from %q within %s: %w", jwksURL, jwksStartupTimeout, err)
	}
	keyset, err := cache.CachedSet(jwksURL)
	if err != nil {
		_ = cache.Shutdown(context.Background())
		return nil, fmt.Errorf("create cached JWKS set: %w", err)
	}
	return &Verifier{cache: cache, keyset: keyset, issuer: issuer, audience: audience, store: store}, nil
}

// Close stops the verifier's background key refresh. Without it, shutdown can
// leave the refresh client running until the process is forcibly terminated.
func (v *Verifier) Close(ctx context.Context) error {
	return v.cache.Shutdown(ctx)
}

// UnaryServerInterceptor authenticates every non-health RPC and enforces the
// coarse method permission before domain code runs. Without this boundary,
// direct gRPC callers could bypass the HTTP gateway and its access controls.
func (v *Verifier) UnaryServerInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if info.FullMethod == "/grpc.health.v1.Health/Check" {
		return handler(ctx, req)
	}

	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "authentication required")
	}
	requestID := ""
	if values := md.Get("x-request-id"); len(values) > 0 {
		requestID = values[0]
	}

	if requestID == "" {
		requestID = uuid.NewString()
	}

	authorization := md.Get("authorization")
	if len(authorization) != 1 {
		return nil, status.Error(codes.Unauthenticated, "authentication required")
	}

	parts := strings.Fields(authorization[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return nil, status.Error(codes.Unauthenticated, "authentication required")
	}

	token, err := jwt.Parse(
		[]byte(parts[1]),
		jwt.WithKeySet(v.keyset),
		jwt.WithIssuer(v.issuer),
		jwt.WithAudience(v.audience),
		jwt.WithAcceptableSkew(30*time.Second),
	)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "authentication required")
	}
	subject, exists := token.Subject()
	if !exists || subject == "" {
		return nil, status.Error(codes.Unauthenticated, "authentication required")
	}

	staff, err := v.store.GetActiveStaffByAuthUserID(ctx, pgtype.Text{String: subject, Valid: true})
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "active staff account required")
	}
	permission := permissionForMethod(info.FullMethod)
	if permission == "" {
		return nil, status.Error(codes.PermissionDenied, "This operation is unavailable to staff. Contact a system administrator.")
	}
	if !roleHasPermission(staff.Role, permission) {
		action := "perform this action"
		switch permission {
		case permissionMemberRead:
			action = "view member records"
		case permissionStaffManage:
			action = "manage staff access"
		case permissionMemberWrite:
			action = "change member records"
		case permissionMemberStatus:
			action = "change member status or close membership"
		case permissionShareRead:
			action = "view share records"
		case permissionShareWrite:
			action = "record share transactions"
		case permissionShareApprove:
			action = "approve share changes"
		case permissionLoanRead:
			action = "view loan records"
		case permissionLoanWrite:
			action = "prepare loans"
		case permissionLoanApprove:
			action = "approve loans"
		case permissionLoanDisburse:
			action = "disburse loans"
		case permissionRepaymentRecord:
			action = "record repayments or M-Pesa contributions"
		case permissionCreditWithdraw:
			action = "approve credit withdrawals"
		case permissionCashRecord:
			action = "record cash transactions"
		case permissionCashApprove:
			action = "approve cash transactions"
		case permissionCashRead:
			action = "view cash records"
		case permissionCreditRead:
			action = "view credit records"
		case permissionCreditAccess:
			action = "manage member credit"
		case permissionCEEPRead:
			action = "view CEEP reports"
		case permissionDividendRead:
			action = "view dividend records"
		case permissionDashboardRead:
			action = "view the dashboard"
		}
		return nil, status.Errorf(codes.PermissionDenied, "Your %s role cannot %s. Ask an authorized staff member.", strings.ReplaceAll(staff.Role, "_", " "), action)
	}

	_ = grpc.SetHeader(ctx, metadata.Pairs("x-request-id", requestID))
	return handler(contextWithPrincipal(ctx, Principal{
		ID:         staff.ID,
		AuthUserID: subject,
		Role:       staff.Role,
		BranchID:   staff.BranchID,
	}), req)
}

// permissionForMethod keeps transport methods mapped to stable domain
// permissions. Without it, authentication would grant every staff role write
// access to every RPC.
func permissionForMethod(method string) string {
	switch method {
	case "/dashboard.v1.DashboardService/GetSummary":
		return permissionDashboardRead
	case "/member.v1.MemberService/GetMember", "/member.v1.MemberService/ListMembers", "/member.v1.MemberService/GetMemberStatusHistory":
		return permissionMemberRead
	case "/share.v1.ShareService/GetShareAccount", "/share.v1.ShareService/ListShareAccounts", "/share.v1.ShareService/GetShareBalance", "/share.v1.ShareService/ListShareTransactions", "/share.v1.ShareService/GetShareTransaction":
		return permissionShareRead
	case "/loan.v1.LoanService/GetLoan", "/loan.v1.LoanService/ListLoans", "/loan.v1.LoanService/GetLoanStatus", "/loan.v1.LoanService/ListGuarantors", "/loan.v1.RepaymentService/GetRepaymentSchedule", "/loan.v1.RepaymentService/GetInstallmentDetails", "/loan.v1.RepaymentService/GetPaymentHistory":
		return permissionLoanRead
	case "/loan.v1.CreditService/ListCreditOperations", "/loan.v1.CreditService/GetCreditBalance":
		return permissionCreditRead
	case "/contribution.v1.ContributionService/GetCashContext", "/auth.v1.AuthService/GetCurrentStaff":
		return permissionAuthenticated
	case "/contribution.v1.ContributionService/GetCashReceipt", "/contribution.v1.ContributionService/ListCashierSessions", "/contribution.v1.ContributionService/ListCashDeposits", "/contribution.v1.ContributionService/GetCashDeposit", "/contribution.v1.ContributionService/ListContributionCharges":
		return permissionCashRead
	case "/contribution.v1.ContributionService/GetCEEPReport":
		return permissionCEEPRead
	case "/contribution.v1.ContributionService/GetDividendHistory", "/contribution.v1.ContributionService/GetDividendRegister":
		return permissionDividendRead
	case "/contribution.v1.ContributionService/ApproveDividends", "/contribution.v1.ContributionService/SaveDividendDraft", "/contribution.v1.ContributionService/RecordDividendChoice", "/contribution.v1.ContributionService/SettleDividend":
		return permissionCashApprove
	case "/auth.v1.AuthService/CreateStaffUser", "/auth.v1.AuthService/DeactivateStaffUser", "/auth.v1.AuthService/ListStaffUsers":
		return permissionStaffManage
	case "/member.v1.MemberService/RegisterMember", "/member.v1.MemberService/UpdateMemberProfile":
		return permissionMemberWrite
	case "/member.v1.MemberService/UpdateMemberStatus", "/member.v1.MemberService/CloseMember":
		return permissionMemberStatus
	case "/share.v1.ShareService/OpenShareAccount", "/share.v1.ShareService/PurchaseShares", "/share.v1.ShareService/WithdrawShares", "/share.v1.ShareService/CreateAdjustment":
		return permissionShareWrite
	case "/share.v1.ShareService/ApproveShareAdjustment", "/share.v1.ShareService/ReverseShareTransaction":
		return permissionShareApprove
	case "/loan.v1.LoanService/ApplyForLoan", "/loan.v1.LoanService/AddGuarantor", "/loan.v1.LoanService/RemoveGuarantor":
		return permissionLoanWrite
	case "/loan.v1.LoanService/ApproveLoan", "/loan.v1.LoanService/RejectLoan", "/loan.v1.LoanService/ApproveGuarantor":
		return permissionLoanApprove
	case "/loan.v1.LoanService/DisburseLoan":
		return permissionLoanDisburse
	case "/loan.v1.RepaymentService/RecordRepayment", "/contribution.v1.ContributionService/InitiateDarajaSTKContribution", "/contribution.v1.ContributionService/GetContributionPayment", "/contribution.v1.ContributionService/GetContributionQuote":
		return permissionRepaymentRecord
	case "/contribution.v1.ContributionService/ListDarajaPayments", "/contribution.v1.ContributionService/RecoverDarajaPayment":
		return permissionRepaymentRecord
	case "/loan.v1.CreditService/RequestCreditWithdrawal":
		return permissionRepaymentRecord
	case "/loan.v1.CreditService/ManageCredit":
		// The handler distinguishes recording from refund approval permissions.
		return permissionCreditAccess
	case "/contribution.v1.ContributionService/CreateContributionCharge", "/contribution.v1.ContributionService/OpenCashierSession", "/contribution.v1.ContributionService/CreateCashContribution", "/contribution.v1.ContributionService/CloseCashierSession":
		return permissionCashRecord
	case "/contribution.v1.ContributionService/FinalizeCEEPReport", "/contribution.v1.ContributionService/AcceptCashHandover", "/contribution.v1.ContributionService/RecordCashDeposit", "/contribution.v1.ContributionService/VerifyCashDeposit":
		return permissionCashApprove
	default:
		return ""
	}
}

// roleHasPermission centralizes the existing role matrix without mutable
// package maps. Without it, permission decisions would be duplicated across
// handlers and drift as services change.
func roleHasPermission(role, permission string) bool {
	switch role {
	case roleSystemAdmin:
		return true
	case roleManager:
		return permission != permissionStaffManage
	case roleLoanOfficer:
		return permission == permissionAuthenticated || permission == permissionDashboardRead || permission == permissionMemberRead || permission == permissionMemberWrite || permission == permissionShareRead || permission == permissionLoanRead || permission == permissionLoanWrite || permission == permissionCreditRead
	case roleCashier:
		return permission == permissionAuthenticated || permission == permissionDashboardRead || permission == permissionMemberRead || permission == permissionShareRead || permission == permissionShareWrite || permission == permissionLoanRead || permission == permissionCreditRead || permission == permissionCreditAccess || permission == permissionCashRead || permission == permissionRepaymentRecord || permission == permissionCashRecord
	case roleChairperson:
		return permission == permissionAuthenticated || permission == permissionDashboardRead || permission == permissionMemberRead || permission == permissionShareRead || permission == permissionLoanRead || permission == permissionCreditRead || permission == permissionCreditAccess || permission == permissionLoanApprove || permission == permissionShareApprove || permission == permissionCreditWithdraw
	case roleSecretary:
		return permission == permissionAuthenticated || permission == permissionDashboardRead || permission == permissionMemberRead || permission == permissionMemberWrite || permission == permissionShareRead || permission == permissionLoanRead
	case roleAuditor:
		return permission == permissionAuthenticated || permission == permissionDashboardRead || permission == permissionMemberRead || permission == permissionShareRead || permission == permissionLoanRead || permission == permissionCreditRead || permission == permissionCashRead || permission == permissionCEEPRead || permission == permissionDividendRead
	default:
		return false
	}
}

// CashPermissions reuses the interceptor's role matrix for visible cash controls.
// Without it, the UI's advertised actions could drift from RPC authorization.
func CashPermissions(role string) (record, approve bool) {
	return roleHasPermission(role, permissionCashRecord), roleHasPermission(role, permissionCashApprove)
}

// CreditApprovalAllowed keeps refund review on the established credit permission.
// Without it, the combined credit endpoint would let cashiers approve their own refunds.
func CreditApprovalAllowed(role string) bool {
	return roleHasPermission(role, permissionCreditWithdraw)
}

// CanAccessBranch keeps record access within the actor's branch while allowing managers and system admins to work across branches; without it, handlers can disagree on branch access.
func CanAccessBranch(actor Principal, branchID int64) bool {
	return actor.Role == roleManager || actor.Role == roleSystemAdmin || actor.BranchID == branchID
}
