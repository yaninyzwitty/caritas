package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/httprc/v3"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	roleSystemAdmin = "system_admin"
	roleManager     = "manager"
	roleLoanOfficer = "loan_officer"
	roleCashier     = "cashier"
	roleAuditor     = "auditor"
	roleChairperson = "chairperson"
	roleSecretary   = "secretary"

	permissionStaffManage     = "staff.manage"
	permissionMemberWrite     = "member.write"
	permissionShareWrite      = "share.write"
	permissionShareApprove    = "share.approve"
	permissionLoanWrite       = "loan.write"
	permissionLoanApprove     = "loan.approve"
	permissionLoanDisburse    = "loan.disburse"
	permissionRepaymentRecord = "repayment.record"
	permissionCreditWithdraw  = "credit.withdraw"
	permissionCashRecord      = "cash.record"
	permissionCashApprove     = "cash.approve"
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
	if err := cache.Register(ctx, jwksURL, jwk.WithConstantInterval(5*time.Minute), jwk.WithWaitReady(true)); err != nil {
		_ = cache.Shutdown(context.Background())
		return nil, fmt.Errorf("register JWKS URL: %w", err)
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
	if subject, exists := token.Subject(); !exists || subject == "" {
		return nil, status.Error(codes.Unauthenticated, "authentication required")
	}
	var email string
	if err := token.Get("email", &email); err != nil || email == "" {
		return nil, status.Error(codes.Unauthenticated, "authentication required")
	}

	staff, err := v.store.GetActiveStaffByEmail(ctx, email)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "active staff account required")
	}
	permission := permissionForMethod(info.FullMethod)
	if permission != "" && !roleHasPermission(staff.Role, permission) {
		return nil, status.Error(codes.PermissionDenied, "permission denied")
	}

	requestID := ""
	if values := md.Get("x-request-id"); len(values) > 0 {
		requestID = values[0]
	}
	if requestID == "" {
		requestID = uuid.NewString()
	}
	_ = grpc.SetHeader(ctx, metadata.Pairs("x-request-id", requestID))
	return handler(contextWithPrincipal(ctx, Principal{
		ID:       staff.ID,
		Role:     staff.Role,
		BranchID: staff.BranchID,
	}), req)
}

// permissionForMethod keeps transport methods mapped to stable domain
// permissions. Without it, authentication would grant every staff role write
// access to every RPC.
func permissionForMethod(method string) string {
	switch method {
	case "/auth.v1.AuthService/CreateStaffUser", "/auth.v1.AuthService/DeactivateStaffUser":
		return permissionStaffManage
	case "/member.v1.MemberService/RegisterMember", "/member.v1.MemberService/UpdateMemberProfile", "/member.v1.MemberService/UpdateMemberStatus", "/member.v1.MemberService/CloseMember":
		return permissionMemberWrite
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
	case "/loan.v1.RepaymentService/RecordRepayment", "/contribution.v1.ContributionService/InitiateDarajaSTKContribution":
		return permissionRepaymentRecord
	case "/loan.v1.CreditService/RequestCreditWithdrawal":
		return permissionCreditWithdraw
	case "/contribution.v1.ContributionService/OpenCashierSession", "/contribution.v1.ContributionService/CreateCashContribution", "/contribution.v1.ContributionService/CloseCashierSession":
		return permissionCashRecord
	case "/contribution.v1.ContributionService/AcceptCashHandover", "/contribution.v1.ContributionService/RecordCashDeposit", "/contribution.v1.ContributionService/VerifyCashDeposit":
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
		return permission == permissionMemberWrite || permission == permissionLoanWrite
	case roleCashier:
		return permission == permissionShareWrite || permission == permissionRepaymentRecord || permission == permissionCashRecord
	case roleChairperson:
		return permission == permissionLoanApprove || permission == permissionShareApprove || permission == permissionCreditWithdraw
	case roleSecretary:
		return permission == permissionMemberWrite
	case roleAuditor:
		return false
	default:
		return false
	}
}
