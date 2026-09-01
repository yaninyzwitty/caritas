package auth

import (
	"context"
	"strings"
	"time"

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

var methodPermissions = map[string]string{
	"/auth.v1.AuthService/CreateStaffUser":                               permissionStaffManage,
	"/auth.v1.AuthService/DeactivateStaffUser":                           permissionStaffManage,
	"/member.v1.MemberService/RegisterMember":                            permissionMemberWrite,
	"/member.v1.MemberService/UpdateMemberProfile":                       permissionMemberWrite,
	"/member.v1.MemberService/UpdateMemberStatus":                        permissionMemberWrite,
	"/member.v1.MemberService/CloseMember":                               permissionMemberWrite,
	"/share.v1.ShareService/OpenShareAccount":                            permissionShareWrite,
	"/share.v1.ShareService/PurchaseShares":                              permissionShareWrite,
	"/share.v1.ShareService/WithdrawShares":                              permissionShareWrite,
	"/share.v1.ShareService/CreateAdjustment":                            permissionShareWrite,
	"/share.v1.ShareService/ApproveShareAdjustment":                      permissionShareApprove,
	"/share.v1.ShareService/ReverseShareTransaction":                     permissionShareApprove,
	"/loan.v1.LoanService/ApplyForLoan":                                  permissionLoanWrite,
	"/loan.v1.LoanService/ApproveLoan":                                   permissionLoanApprove,
	"/loan.v1.LoanService/RejectLoan":                                    permissionLoanApprove,
	"/loan.v1.LoanService/DisburseLoan":                                  permissionLoanDisburse,
	"/loan.v1.LoanService/AddGuarantor":                                  permissionLoanWrite,
	"/loan.v1.LoanService/RemoveGuarantor":                               permissionLoanWrite,
	"/loan.v1.LoanService/ApproveGuarantor":                              permissionLoanApprove,
	"/loan.v1.RepaymentService/RecordRepayment":                          permissionRepaymentRecord,
	"/loan.v1.CreditService/RequestCreditWithdrawal":                     permissionCreditWithdraw,
	"/contribution.v1.ContributionService/InitiateDarajaSTKContribution": permissionRepaymentRecord,
	"/contribution.v1.ContributionService/OpenCashierSession":            permissionCashRecord,
	"/contribution.v1.ContributionService/CreateCashContribution":        permissionCashRecord,
	"/contribution.v1.ContributionService/CloseCashierSession":           permissionCashRecord,
	"/contribution.v1.ContributionService/AcceptCashHandover":            permissionCashApprove,
	"/contribution.v1.ContributionService/RecordCashDeposit":             permissionCashApprove,
	"/contribution.v1.ContributionService/VerifyCashDeposit":             permissionCashApprove,
}

var rolePermissions = map[string]map[string]bool{
	roleSystemAdmin: {
		permissionStaffManage:     true,
		permissionMemberWrite:     true,
		permissionShareWrite:      true,
		permissionShareApprove:    true,
		permissionLoanWrite:       true,
		permissionLoanApprove:     true,
		permissionLoanDisburse:    true,
		permissionRepaymentRecord: true,
		permissionCreditWithdraw:  true,
		permissionCashRecord:      true,
		permissionCashApprove:     true,
	},
	roleManager: {
		permissionMemberWrite:     true,
		permissionShareWrite:      true,
		permissionShareApprove:    true,
		permissionLoanWrite:       true,
		permissionLoanApprove:     true,
		permissionLoanDisburse:    true,
		permissionRepaymentRecord: true,
		permissionCreditWithdraw:  true,
		permissionCashRecord:      true,
		permissionCashApprove:     true,
	},
	roleLoanOfficer: {
		permissionMemberWrite: true,
		permissionLoanWrite:   true,
	},
	roleCashier: {
		permissionShareWrite:      true,
		permissionRepaymentRecord: true,
		permissionCashRecord:      true,
	},
	roleChairperson: {
		permissionLoanApprove:    true,
		permissionShareApprove:   true,
		permissionCreditWithdraw: true,
	},
	roleSecretary: {
		permissionMemberWrite: true,
	},
	roleAuditor: {},
}

func UnaryInterceptor(store *Store, tokenSecret string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		permission, ok := methodPermissions[info.FullMethod]
		if !ok {
			return handler(ctx, req)
		}

		token, err := bearerToken(ctx)
		if err != nil {
			return nil, err
		}

		principal, err := validateAccessToken(token, tokenSecret, time.Now().UTC())
		if err != nil {
			return nil, status.Error(codes.Unauthenticated, "invalid access token")
		}

		staff, err := store.GetActiveStaffByID(ctx, principal.ID)
		if err != nil {
			return nil, status.Error(codes.Unauthenticated, "inactive staff account")
		}
		if !rolePermissions[staff.Role][permission] {
			return nil, status.Error(codes.PermissionDenied, "permission denied")
		}

		principal.Role = staff.Role
		principal.BranchID = staff.BranchID
		return handler(contextWithPrincipal(ctx, principal), req)
	}
}

func bearerToken(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "authorization metadata is required")
	}

	values := md.Get("authorization")
	if len(values) != 1 {
		return "", status.Error(codes.Unauthenticated, "authorization bearer token is required")
	}

	value := strings.TrimSpace(values[0])
	if !strings.HasPrefix(strings.ToLower(value), "bearer ") {
		return "", status.Error(codes.Unauthenticated, "authorization bearer token is required")
	}

	token := strings.TrimSpace(value[len("bearer "):])
	if token == "" {
		return "", status.Error(codes.Unauthenticated, "authorization bearer token is required")
	}
	return token, nil
}
