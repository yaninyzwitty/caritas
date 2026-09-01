package contribution

import (
	"context"
	"errors"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	contributionv1 "github.com/yaninyzwitty/caritas-backend/gen/contribution/v1"
	memberv1 "github.com/yaninyzwitty/caritas-backend/gen/member/v1"
	"github.com/yaninyzwitty/caritas-backend/internal/auth"
	contributionsqlc "github.com/yaninyzwitty/caritas-backend/internal/contribution/repository/sqlc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	timestamppb "google.golang.org/protobuf/types/known/timestamppb"
)

type Handlers struct {
	contributionv1.UnimplementedContributionServiceServer
	service   *Service
	initiator DarajaSTKInitiator
}

// NewHandlers wires authenticated contribution RPCs to the service. Without
// this constructor, main would have to expose handler fields or use package
// globals for the Daraja initiator.
func NewHandlers(service *Service, initiator DarajaSTKInitiator) *Handlers {
	return &Handlers{service: service, initiator: initiator}
}

// InitiateDarajaSTKContribution starts one member contribution STK request.
// Without this RPC, clients would need the unauthenticated Daraja webhook HTTP
// surface to initiate payments, bypassing the existing gRPC auth interceptor.
func (h *Handlers) InitiateDarajaSTKContribution(ctx context.Context, req *contributionv1.InitiateDarajaSTKContributionRequest) (*contributionv1.InitiateDarajaSTKContributionResponse, error) {
	actor, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "authenticated admin is required")
	}
	memberID, err := stringToUUID(req.GetMemberId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid member_id")
	}
	period, err := time.Parse("2006-01-02", req.GetContributionPeriod())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid contribution_period")
	}
	allocations, err := allocationsFromProto(req.GetAllocations())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	branchID := req.GetBranchId()
	if branchID == 0 {
		branchID = actor.BranchID
	}

	actualPhoneNumber, err := normalizePhoneNumber(req.PhoneNumber)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "normalize phone number: %v", err)
	}

	paymentRequest, err := h.service.InitiateDarajaSTKPayment(ctx, InitiateDarajaSTKPaymentParams{
		IdempotencyKey:     req.GetIdempotencyKey(),
		PhoneNumber:        actualPhoneNumber,
		MemberID:           memberID,
		BranchID:           branchID,
		ContributionPeriod: pgtype.Date{Time: period, Valid: true},
		Amount:             moneyToNumeric(req.GetAmount()),
		Allocations:        allocations,
		RequestedBy:        actor.ID,
	}, h.initiator)
	if err != nil {
		return nil, mapContributionError(err)
	}
	return paymentRequestToProto(paymentRequest)
}

// OpenCashierSession starts the custody boundary that every cash receipt must
// join. Without it, received notes cannot be assigned to one accountable till.
func (h *Handlers) OpenCashierSession(ctx context.Context, _ *contributionv1.OpenCashierSessionRequest) (*contributionv1.OpenCashierSessionResponse, error) {
	actor, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "authenticated cashier is required")
	}
	session, err := h.service.OpenCashierSession(ctx, actor.BranchID, actor.ID)
	if err != nil {
		return nil, mapContributionError(err)
	}
	result, err := cashierSessionToProto(session)
	return &contributionv1.OpenCashierSessionResponse{Session: result}, err
}

// CreateCashContribution records accepted cash before applying its immutable
// allocation plan. Without this endpoint, cashiers can only use the STK path
// and physical receipts remain outside reconciliation.
func (h *Handlers) CreateCashContribution(ctx context.Context, req *contributionv1.CreateCashContributionRequest) (*contributionv1.CreateCashContributionResponse, error) {
	actor, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "authenticated cashier is required")
	}
	sessionID, err := stringToUUID(req.GetSessionId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid session_id")
	}
	memberID, err := stringToUUID(req.GetMemberId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid member_id")
	}
	period, err := time.Parse("2006-01-02", req.GetContributionPeriod())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid contribution_period")
	}
	allocations, err := allocationsFromProto(req.GetAllocations())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	result, err := h.service.CreateCashReceipt(ctx, contributionsqlc.InsertContributionReceiptParams{
		IdempotencyKey:     text(req.GetIdempotencyKey()),
		CashierSessionID:   sessionID,
		MemberID:           memberID,
		BranchID:           actor.BranchID,
		ContributionPeriod: pgtype.Date{Time: period, Valid: true},
		ReceivedAmount:     moneyToNumeric(req.GetAmount()),
		ReceivedBy:         actor.ID,
	}, allocations)
	if err != nil {
		return nil, mapContributionError(err)
	}
	id, err := uuidToString(result.Receipt.ID)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to encode receipt id")
	}
	session, err := uuidToString(result.Receipt.CashierSessionID)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to encode cashier session id")
	}
	response := &contributionv1.CashContributionReceipt{
		Id:                       id,
		InternalReceiptReference: result.Receipt.InternalReceiptReference.String,
		SessionId:                session,
		Status:                   string(result.Receipt.Status),
		Amount:                   numericToMoney(result.Receipt.ReceivedAmount),
	}
	if result.Receipt.ReceivedAt.Valid {
		response.ReceivedAt = timestamppb.New(result.Receipt.ReceivedAt.Time)
	}
	return &contributionv1.CreateCashContributionResponse{Receipt: response}, nil
}

// CloseCashierSession snapshots expected cash while the session row is locked.
// Without it, a receipt could race the count and create an unexplained variance.
func (h *Handlers) CloseCashierSession(ctx context.Context, req *contributionv1.CloseCashierSessionRequest) (*contributionv1.CloseCashierSessionResponse, error) {
	actor, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "authenticated cashier is required")
	}
	id, err := stringToUUID(req.GetSessionId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid session_id")
	}
	session, err := h.service.CloseCashierSession(ctx, id, actor.ID, moneyToNumeric(req.GetCountedAmount()), req.GetVarianceReason())
	if err != nil {
		return nil, mapContributionError(err)
	}
	result, err := cashierSessionToProto(session)
	return &contributionv1.CloseCashierSessionResponse{Session: result}, err
}

// AcceptCashHandover records the second person who takes custody from the
// cashier. Removing it collapses collection and handover into one actor.
func (h *Handlers) AcceptCashHandover(ctx context.Context, req *contributionv1.AcceptCashHandoverRequest) (*contributionv1.AcceptCashHandoverResponse, error) {
	actor, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "authenticated manager is required")
	}
	id, err := stringToUUID(req.GetSessionId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid session_id")
	}
	session, err := h.service.AcceptCashHandover(ctx, id, actor.ID, actor.BranchID)
	if err != nil {
		return nil, mapContributionError(err)
	}
	result, err := cashierSessionToProto(session)
	return &contributionv1.AcceptCashHandoverResponse{Session: result}, err
}

// RecordCashDeposit joins handed-over sessions to one bank reference. Without
// the join, the system can prove collection but not where the cash went.
func (h *Handlers) RecordCashDeposit(ctx context.Context, req *contributionv1.RecordCashDepositRequest) (*contributionv1.RecordCashDepositResponse, error) {
	actor, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "authenticated manager is required")
	}
	ids := make([]pgtype.UUID, 0, len(req.GetSessionIds()))
	for _, value := range req.GetSessionIds() {
		id, err := stringToUUID(value)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid session_id")
		}
		ids = append(ids, id)
	}
	deposit, err := h.service.RecordCashDeposit(ctx, ids, moneyToNumeric(req.GetAmount()), req.GetBankReference(), actor.BranchID, actor.ID)
	if err != nil {
		return nil, mapContributionError(err)
	}
	result, err := cashDepositToProto(deposit)
	return &contributionv1.RecordCashDepositResponse{Deposit: result}, err
}

// VerifyCashDeposit records bank-side confirmation separately from the person
// who entered the slip. Without it, a typed bank reference is treated as proof.
func (h *Handlers) VerifyCashDeposit(ctx context.Context, req *contributionv1.VerifyCashDepositRequest) (*contributionv1.VerifyCashDepositResponse, error) {
	actor, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "authenticated manager is required")
	}
	id, err := stringToUUID(req.GetDepositId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid deposit_id")
	}
	deposit, err := h.service.VerifyCashDeposit(ctx, id, actor.ID, actor.BranchID)
	if err != nil {
		return nil, mapContributionError(err)
	}
	result, err := cashDepositToProto(deposit)
	return &contributionv1.VerifyCashDepositResponse{Deposit: result}, err
}

// allocationsFromProto maps API allocation rows to service inputs in one pass.
// Without it, the RPC body would mix enum conversion, UUID parsing and request
// orchestration, making invalid target handling easy to miss.
func allocationsFromProto(rows []*contributionv1.ContributionAllocationInput) ([]AllocationInput, error) {
	allocations := make([]AllocationInput, 0, len(rows))
	for _, row := range rows {
		allocation := AllocationInput{
			Type:   allocationTypeFromProto(row.GetType()),
			Amount: moneyToNumeric(row.GetAmount()),
		}
		if allocation.Type == "" {
			return nil, ErrInvalidAllocation
		}
		if strings.TrimSpace(row.GetTargetId()) != "" {
			targetID, err := stringToUUID(row.GetTargetId())
			if err != nil {
				return nil, ErrInvalidAllocation
			}
			allocation.TargetID = targetID
		}
		allocations = append(allocations, allocation)
	}
	return allocations, nil
}

// allocationTypeFromProto keeps proto enum names out of the service layer.
// Removing it would couple persistence validation to generated API constants.
func allocationTypeFromProto(value contributionv1.ContributionAllocationType) contributionsqlc.ContributionAllocationType {
	switch value {
	case contributionv1.ContributionAllocationType_CONTRIBUTION_ALLOCATION_TYPE_COM:
		return contributionsqlc.ContributionAllocationTypeCom
	case contributionv1.ContributionAllocationType_CONTRIBUTION_ALLOCATION_TYPE_LGOM:
		return contributionsqlc.ContributionAllocationTypeLgom
	case contributionv1.ContributionAllocationType_CONTRIBUTION_ALLOCATION_TYPE_SHARE_PURCHASE:
		return contributionsqlc.ContributionAllocationTypeSharePurchase
	case contributionv1.ContributionAllocationType_CONTRIBUTION_ALLOCATION_TYPE_LOAN_PRINCIPAL:
		return contributionsqlc.ContributionAllocationTypeLoanPrincipal
	case contributionv1.ContributionAllocationType_CONTRIBUTION_ALLOCATION_TYPE_LOAN_INTEREST:
		return contributionsqlc.ContributionAllocationTypeLoanInterest
	case contributionv1.ContributionAllocationType_CONTRIBUTION_ALLOCATION_TYPE_PENALTY:
		return contributionsqlc.ContributionAllocationTypePenalty
	case contributionv1.ContributionAllocationType_CONTRIBUTION_ALLOCATION_TYPE_OTHER_CHARGE:
		return contributionsqlc.ContributionAllocationTypeOtherCharge
	case contributionv1.ContributionAllocationType_CONTRIBUTION_ALLOCATION_TYPE_OVERPAYMENT_CREDIT:
		return contributionsqlc.ContributionAllocationTypeOverpaymentCredit
	default:
		return ""
	}
}

var kenyanPhone = regexp.MustCompile(`^254[17]\d{8}$`)

// normalizes phone numbers, ie from 07 - 254 the required format
func normalizePhoneNumber(phone string) (string, error) {
	// Remove common formatting
	phone = strings.TrimSpace(phone)
	phone = strings.ReplaceAll(phone, " ", "")
	phone = strings.ReplaceAll(phone, "-", "")
	phone = strings.TrimPrefix(phone, "+")

	switch {
	case strings.HasPrefix(phone, "0"):
		phone = "254" + phone[1:]

	case strings.HasPrefix(phone, "7"),
		strings.HasPrefix(phone, "1"):
		phone = "254" + phone

	}

	if !kenyanPhone.MatchString(phone) {
		return "", errors.New("invalid Kenyan phone number")
	}
	return phone, nil
}

// moneyToNumeric collapses proto Money into pgtype.Numeric without using
// floats. Without it, contribution amounts could round differently before the
// callback compares expected and received money.
func moneyToNumeric(m *memberv1.Money) pgtype.Numeric {
	if m == nil {
		return pgtype.Numeric{}
	}
	total := new(big.Int).Mul(big.NewInt(m.GetUnits()), big.NewInt(1_000_000_000))
	total.Add(total, big.NewInt(int64(m.GetNanos())))
	return pgtype.Numeric{Int: total, Exp: -9, Valid: true}
}

// numericToMoney is shared by receipt, session, and deposit responses. Without
// one scale conversion, NUMERIC(19,4) values can be rendered differently at
// each custody boundary.
func numericToMoney(n pgtype.Numeric) *memberv1.Money {
	if !n.Valid || n.Int == nil {
		return nil
	}
	nanos := numericToScale(n, -9)
	units := new(big.Int)
	fraction := new(big.Int)
	units.QuoRem(nanos, big.NewInt(1_000_000_000), fraction)
	return &memberv1.Money{CurrencyCode: "KES", Units: units.Int64(), Nanos: int32(fraction.Int64())}
}

// cashierSessionToProto keeps the three session transitions on one response
// shape. Removing it would duplicate nullable money and timestamp handling in
// open, close, and handover RPCs.
func cashierSessionToProto(row contributionsqlc.CashierSession) (*contributionv1.CashierSession, error) {
	id, err := uuidToString(row.ID)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to encode cashier session id")
	}
	cashierID, err := uuidToString(row.CashierID)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to encode cashier id")
	}
	result := &contributionv1.CashierSession{
		Id:             id,
		BranchId:       row.BranchID,
		CashierId:      cashierID,
		Status:         string(row.Status),
		ExpectedAmount: numericToMoney(row.ExpectedAmount),
		CountedAmount:  numericToMoney(row.CountedAmount),
		Variance:       numericToMoney(row.Variance),
		VarianceReason: row.VarianceReason.String,
	}
	if row.OpenedAt.Valid {
		result.OpenedAt = timestamppb.New(row.OpenedAt.Time)
	}
	if row.ClosedAt.Valid {
		result.ClosedAt = timestamppb.New(row.ClosedAt.Time)
	}
	if row.HandedOverAt.Valid {
		result.HandedOverAt = timestamppb.New(row.HandedOverAt.Time)
	}
	if row.DepositedAt.Valid {
		result.DepositedAt = timestamppb.New(row.DepositedAt.Time)
	}
	return result, nil
}

// cashDepositToProto gives record and verification RPCs the same immutable
// deposit identity. Without it, those endpoints can disagree on bank evidence.
func cashDepositToProto(row contributionsqlc.CashDeposit) (*contributionv1.CashDeposit, error) {
	id, err := uuidToString(row.ID)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to encode cash deposit id")
	}
	result := &contributionv1.CashDeposit{
		Id:            id,
		BranchId:      row.BranchID,
		Amount:        numericToMoney(row.Amount),
		BankReference: row.BankReference,
		Status:        string(row.Status),
	}
	if row.RecordedAt.Valid {
		result.RecordedAt = timestamppb.New(row.RecordedAt.Time)
	}
	if row.VerifiedAt.Valid {
		result.VerifiedAt = timestamppb.New(row.VerifiedAt.Time)
	}
	return result, nil
}

// paymentRequestToProto maps the sqlc row returned by initiation to the RPC
// response. Without it, the handler would repeat nullable checkout/status/time
// translation every time this request state is returned.
func paymentRequestToProto(row contributionsqlc.ContributionPaymentRequest) (*contributionv1.InitiateDarajaSTKContributionResponse, error) {
	id, err := uuidToString(row.ID)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to encode payment request id")
	}
	resp := &contributionv1.InitiateDarajaSTKContributionResponse{
		PaymentRequestId: id,
		Status:           paymentRequestStatusToProto(row.Status),
	}
	if row.CheckoutRequestID.Valid {
		resp.CheckoutRequestId = row.CheckoutRequestID.String
	}
	if row.CreatedAt.Valid {
		resp.CreatedAt = timestamppb.New(row.CreatedAt.Time)
	}
	return resp, nil
}

// paymentRequestStatusToProto maps DB status strings to the public enum.
// Without it, callers would receive storage enum text instead of the typed API
// contract used by generated clients.
func paymentRequestStatusToProto(status contributionsqlc.ContributionPaymentRequestStatus) contributionv1.ContributionPaymentRequestStatus {
	switch status {
	case contributionsqlc.ContributionPaymentRequestStatusPending:
		return contributionv1.ContributionPaymentRequestStatus_CONTRIBUTION_PAYMENT_REQUEST_STATUS_PENDING
	case contributionsqlc.ContributionPaymentRequestStatusCompleted:
		return contributionv1.ContributionPaymentRequestStatus_CONTRIBUTION_PAYMENT_REQUEST_STATUS_COMPLETED
	case contributionsqlc.ContributionPaymentRequestStatusFailed:
		return contributionv1.ContributionPaymentRequestStatus_CONTRIBUTION_PAYMENT_REQUEST_STATUS_FAILED
	default:
		return contributionv1.ContributionPaymentRequestStatus_CONTRIBUTION_PAYMENT_REQUEST_STATUS_UNSPECIFIED
	}
}

// mapContributionError translates domain errors to gRPC status codes. Without
// it, callers would see every validation or idempotency outcome as an internal
// server failure.
func mapContributionError(err error) error {
	switch {
	case errors.Is(err, ErrInvalidPayment),
		errors.Is(err, ErrInvalidReceiptAmount),
		errors.Is(err, ErrInvalidAllocationPlan),
		errors.Is(err, ErrInvalidAllocation),
		errors.Is(err, ErrAllocationRequired),
		errors.Is(err, ErrDuplicateAllocation),
		errors.Is(err, ErrAllocationTotalMismatch):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, ErrPaymentRequestInProgress):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, ErrCashierSessionNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, ErrCashierSessionState),
		errors.Is(err, ErrCashSeparationOfDuties),
		errors.Is(err, ErrCashDepositSelfVerify):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, ErrCashVarianceReason),
		errors.Is(err, ErrCashDepositInvalid),
		errors.Is(err, ErrReceiptReferenceRequired),
		errors.Is(err, ErrInconsistentReceipt):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, ErrDarajaClientMissing):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	default:
		return status.Error(codes.Internal, "internal server error")
	}
}
