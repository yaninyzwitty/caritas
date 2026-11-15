package contribution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	contributionv1 "github.com/yaninyzwitty/caritas-backend/gen/contribution/v1"
	"github.com/yaninyzwitty/caritas-backend/internal/auth"
	"github.com/yaninyzwitty/caritas-backend/internal/ceepdb"
	sharesqlc "github.com/yaninyzwitty/caritas-backend/internal/share/repository/sqlc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

func dividendInputs(year int32, declared, rateText string) (time.Time, pgtype.Numeric, error) {
	var rate pgtype.Numeric
	if year < 2000 || year > 9998 {
		return time.Time{}, rate, status.Error(codes.InvalidArgument, "financial_year must be between 2000 and 9998")
	}
	date, err := time.Parse("2006-01-02", declared)
	today := time.Now().In(time.FixedZone("Africa/Nairobi", 3*60*60)).Format("2006-01-02")
	if err != nil || date.Format("2006-01-02") != declared || declared > today || int(year) >= date.Year() {
		return time.Time{}, rate, status.Error(codes.InvalidArgument, "declaration_date must follow the financial year and be no later than today in Nairobi")
	}
	if !regexp.MustCompile(`^\d{1,3}(\.\d{1,4})?$`).MatchString(rateText) {
		return time.Time{}, rate, status.Error(codes.InvalidArgument, "rate must be a positive percentage with at most four decimal places")
	}
	value, valid := new(big.Rat).SetString(rateText)
	if !valid || value.Sign() <= 0 || value.Cmp(big.NewRat(100, 1)) > 0 {
		return time.Time{}, rate, status.Error(codes.InvalidArgument, "rate must be greater than zero and at most 100")
	}
	if err := rate.Scan(value.FloatString(4)); err != nil {
		return time.Time{}, rate, err
	}
	return date, rate, nil
}

func dividendError(ctx context.Context, err error) error {
	if status.Code(err) != codes.Unknown {
		return err
	}
	slog.ErrorContext(ctx, "dividend operation failed", "error", err)
	return status.Error(codes.Internal, "unable to complete dividend operation")
}

// GetDividendRegister keeps financial inputs on the authenticated backend; removing
// this boundary would let the browser calculate eligibility from incomplete records.
func (h *Handlers) GetDividendRegister(ctx context.Context, req *contributionv1.GetDividendRegisterRequest) (*contributionv1.GetDividendRegisterResponse, error) {
	report, err := h.dividendRegister(ctx, req, "", "")
	if err != nil {
		return nil, err
	}
	return &contributionv1.GetDividendRegisterResponse{Report: report}, nil
}

func (h *Handlers) SaveDividendDraft(ctx context.Context, req *contributionv1.SaveDividendDraftRequest) (*contributionv1.SaveDividendDraftResponse, error) {
	actor, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "authenticated staff is required")
	}
	_, canManage := auth.CashPermissions(actor.Role)
	if !canManage {
		return nil, status.Error(codes.PermissionDenied, "only an admin or manager can edit dividend drafts")
	}
	reason := strings.TrimSpace(req.GetReason())
	if reason == "" || len(reason) > 1000 {
		return nil, status.Error(codes.InvalidArgument, "reason is required (at most 1000 characters)")
	}
	date, rate, err := dividendInputs(req.GetFinancialYear(), req.GetDeclarationDate(), req.GetRate())
	if err != nil {
		return nil, err
	}
	err = h.service.store.ExecReceiptTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1::integer, $2::integer)`, int32(actor.BranchID), req.GetFinancialYear()); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM dividend_declarations WHERE branch_id=$1 AND financial_year=$2)`, actor.BranchID, req.GetFinancialYear()).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return status.Error(codes.FailedPrecondition, "approved declaration is immutable; use an adjustment or reversal for corrections")
		}
		var beforeRate *string
		var beforeDate *time.Time
		err := tx.QueryRow(ctx, `SELECT rate::text, declaration_date FROM dividend_drafts WHERE branch_id=$1 AND financial_year=$2 FOR UPDATE`, actor.BranchID, req.GetFinancialYear()).Scan(&beforeRate, &beforeDate)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO dividend_drafts(branch_id, financial_year, declaration_date, rate, updated_by) VALUES($1,$2,$3,$4,$5)
			ON CONFLICT (branch_id, financial_year) DO UPDATE SET declaration_date=EXCLUDED.declaration_date, rate=EXCLUDED.rate, updated_by=EXCLUDED.updated_by, updated_at=NOW()`, actor.BranchID, req.GetFinancialYear(), date, rate, actor.ID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO dividend_draft_history(branch_id, financial_year, actor_id, reason, before_rate, before_declaration_date, after_rate, after_declaration_date)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, actor.BranchID, req.GetFinancialYear(), actor.ID, reason, beforeRate, beforeDate, rate, date)
		return err
	})
	if err != nil {
		return nil, dividendError(ctx, err)
	}
	report, err := h.dividendRegister(ctx, &contributionv1.GetDividendRegisterRequest{FinancialYear: req.GetFinancialYear()}, "", "")
	if err != nil {
		return nil, err
	}
	return &contributionv1.SaveDividendDraftResponse{Report: report}, nil
}

func (h *Handlers) GetDividendHistory(ctx context.Context, req *contributionv1.GetDividendHistoryRequest) (*contributionv1.GetDividendHistoryResponse, error) {
	actor, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "authenticated staff is required")
	}
	if req.GetFinancialYear() < 2000 || req.GetFinancialYear() > 9998 {
		return nil, status.Error(codes.InvalidArgument, "financial_year must be between 2000 and 9998")
	}
	var raw []byte
	err := h.service.store.ExecReceiptTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT jsonb_build_object('financialYear',$2::integer,'entries',COALESCE(jsonb_agg(jsonb_build_object(
			'id',h.id,'actorId',h.actor_id,'actor',s.full_name,'timestamp',h.changed_at,'reason',h.reason,
			'before',CASE WHEN h.before_rate IS NULL THEN NULL ELSE jsonb_build_object('rate',h.before_rate::text,'declarationDate',h.before_declaration_date) END,
			'after',jsonb_build_object('rate',h.after_rate::text,'declarationDate',h.after_declaration_date)
			) ORDER BY h.changed_at DESC,h.id DESC) FILTER (WHERE h.id IS NOT NULL),'[]'::jsonb))
			FROM dividend_draft_history h LEFT JOIN staff_users s ON s.id=h.actor_id WHERE h.branch_id=$1 AND h.financial_year=$2`, actor.BranchID, req.GetFinancialYear()).Scan(&raw)
	})
	if err != nil {
		return nil, dividendError(ctx, err)
	}
	history := &structpb.Struct{}
	if err := protojson.Unmarshal(raw, history); err != nil {
		return nil, status.Error(codes.Internal, "unable to encode dividend history")
	}
	return &contributionv1.GetDividendHistoryResponse{History: history}, nil
}

// ApproveDividends binds approval to reviewed inputs; without this endpoint a
// preview could be mistaken for an authorized payable allocation.
func (h *Handlers) ApproveDividends(ctx context.Context, req *contributionv1.ApproveDividendsRequest) (*contributionv1.ApproveDividendsResponse, error) {
	if strings.TrimSpace(req.GetPreviewHash()) == "" || strings.TrimSpace(req.GetResolutionReference()) == "" || len(req.GetResolutionReference()) > 200 {
		return nil, status.Error(codes.InvalidArgument, "review a preview and provide a resolution reference (at most 200 characters)")
	}
	report, err := h.dividendRegister(ctx, &contributionv1.GetDividendRegisterRequest{
		FinancialYear: req.GetFinancialYear(), DeclarationDate: req.GetDeclarationDate(), Rate: req.GetRate(),
	}, req.GetPreviewHash(), strings.TrimSpace(req.GetResolutionReference()))
	if err != nil {
		return nil, err
	}
	return &contributionv1.ApproveDividendsResponse{Report: report}, nil
}

// dividendRegister uses the same source query for preview and approval. Removing
// it would allow those two paths to disagree about the amount being authorized.
func (h *Handlers) dividendRegister(ctx context.Context, req *contributionv1.GetDividendRegisterRequest, previewHash, resolution string) (*structpb.Struct, error) {
	actor, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "authenticated staff is required")
	}
	_, canManage := auth.CashPermissions(actor.Role)
	if previewHash != "" && !canManage {
		return nil, status.Error(codes.PermissionDenied, "only an admin or manager can approve dividends")
	}
	if req.GetFinancialYear() < 2000 || req.GetFinancialYear() > 9998 {
		return nil, status.Error(codes.InvalidArgument, "financial_year must be between 2000 and 9998")
	}
	var raw []byte
	err := h.service.store.ExecReceiptTx(ctx, func(tx pgx.Tx) error {
		if previewHash != "" {
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1::integer, $2::integer)`, int32(actor.BranchID), req.GetFinancialYear()); err != nil {
				return err
			}
		}
		q := ceepdb.New(tx)
		lookup := ceepdb.GetDividendRegisterParams{BranchID: actor.BranchID, FinancialYear: req.GetFinancialYear()}
		var err error
		raw, err = q.GetDividendRegister(ctx, lookup)
		if err == nil {
			if previewHash != "" {
				var saved struct {
					PreviewHash string `json:"previewHash"`
				}
				if err := json.Unmarshal(raw, &saved); err != nil {
					return err
				}
				if saved.PreviewHash != previewHash {
					return status.Error(codes.AlreadyExists, "this year already has an approved declaration; reload its register")
				}
			}
			return nil // Approved declarations are immutable, including on retries.
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var savedDate time.Time
		var savedRate string
		draftErr := tx.QueryRow(ctx, `SELECT declaration_date, rate::text FROM dividend_drafts WHERE branch_id=$1 AND financial_year=$2`, actor.BranchID, req.GetFinancialYear()).Scan(&savedDate, &savedRate)
		if draftErr != nil && !errors.Is(draftErr, pgx.ErrNoRows) {
			return draftErr
		}
		if draftErr == nil {
			if previewHash != "" && (req.GetDeclarationDate() != savedDate.Format("2006-01-02") || req.GetRate() != savedRate) {
				return status.Error(codes.FailedPrecondition, "approval inputs differ from saved draft; reload the preview")
			}
			req = &contributionv1.GetDividendRegisterRequest{FinancialYear: req.GetFinancialYear(), DeclarationDate: savedDate.Format("2006-01-02"), Rate: savedRate}
		}
		nairobi := time.FixedZone("Africa/Nairobi", 3*60*60)
		today := time.Now().In(nairobi).Format("2006-01-02")
		declared := req.GetDeclarationDate()
		if declared == "" {
			declared = today
		}
		_ = today
		rateText := strings.TrimSpace(req.GetRate())
		if rateText == "" {
			rateText = "5"
		}
		date, rate, err := dividendInputs(req.GetFinancialYear(), declared, rateText)
		if err != nil {
			return err
		}
		raw, err = q.BuildDividendPreview(ctx, ceepdb.BuildDividendPreviewParams{
			BranchID: actor.BranchID, FinancialYear: req.GetFinancialYear(), DeclarationDate: pgtype.Date{Time: date, Valid: true}, Rate: rate,
		})
		if err != nil {
			return err
		}
		var report map[string]json.RawMessage
		if err := json.Unmarshal(raw, &report); err != nil {
			return err
		}
		canonical, err := json.Marshal(report)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(canonical)
		hash := hex.EncodeToString(digest[:])
		if previewHash == "" {
			report["previewHash"], _ = json.Marshal(hash)
			raw, err = json.Marshal(report)
			return err
		}
		if hash != previewHash {
			return status.Error(codes.FailedPrecondition, "sources changed; load and review a new preview")
		}
		if string(report["hasErrors"]) == "true" {
			return status.Error(codes.FailedPrecondition, "resolve negative historical balances or duplicate share accounts before approving")
		}
		var totalText string
		if err := json.Unmarshal(report["approvedTotal"], &totalText); err != nil {
			return err
		}
		var total pgtype.Numeric
		if err := total.Scan(totalText); err != nil {
			return err
		}
		id, err := q.SaveDividendDeclaration(ctx, ceepdb.SaveDividendDeclarationParams{
			BranchID: actor.BranchID, FinancialYear: req.GetFinancialYear(), DeclarationDate: pgtype.Date{Time: date, Valid: true},
			Rate: rate, ApprovedTotal: total, ResolutionReference: resolution, PreviewHash: hash, ApprovedBy: actor.ID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			raw, err = q.GetDividendRegister(ctx, lookup) // Concurrent approval won; return its frozen register.
			return err
		}
		if err != nil {
			return err
		}
		if err := q.SaveDividendAllocations(ctx, ceepdb.SaveDividendAllocationsParams{DeclarationID: id, Allocations: report["allocations"]}); err != nil {
			return err
		}
		raw, err = q.GetDividendRegister(ctx, lookup)
		return err
	})
	return dividendResponse(ctx, raw, canManage, err)
}

// RecordDividendChoice records the member's instruction separately from payment;
// removing it would make a cash preference indistinguishable from receipt of cash.
func (h *Handlers) RecordDividendChoice(ctx context.Context, req *contributionv1.RecordDividendChoiceRequest) (*contributionv1.RecordDividendChoiceResponse, error) {
	if req.GetChoice() != "cash" && req.GetChoice() != "shares" {
		return nil, status.Error(codes.InvalidArgument, "choice must be cash or shares")
	}
	report, err := h.settleDividend(ctx, req.GetAllocationId(), req.GetChoice(), "", "")
	if err != nil {
		return nil, err
	}
	return &contributionv1.RecordDividendChoiceResponse{Report: report}, nil
}

// SettleDividend confirms actual settlement; without this separate operation an
// allocation could be marked paid merely because a member selected cash.
func (h *Handlers) SettleDividend(ctx context.Context, req *contributionv1.SettleDividendRequest) (*contributionv1.SettleDividendResponse, error) {
	report, err := h.settleDividend(ctx, req.GetAllocationId(), "", strings.TrimSpace(req.GetVoucherReference()), req.GetPaymentDate())
	if err != nil {
		return nil, err
	}
	return &contributionv1.SettleDividendResponse{Report: report}, nil
}

// settleDividend serializes choice and payout on the allocation row. Removing
// that shared lock would allow concurrent cash and share settlement of one dividend.
func (h *Handlers) settleDividend(ctx context.Context, allocationID, choice, voucher, paymentDate string) (*structpb.Struct, error) {
	actor, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "authenticated staff is required")
	}
	_, canManage := auth.CashPermissions(actor.Role)
	if !canManage {
		return nil, status.Error(codes.PermissionDenied, "only an admin or manager can manage dividends")
	}
	id, err := stringToUUID(allocationID)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid allocation_id")
	}
	var raw []byte
	err = h.service.store.ExecReceiptTx(ctx, func(tx pgx.Tx) error {
		q := ceepdb.New(tx)
		allocation, err := q.LockDividendAllocation(ctx, ceepdb.LockDividendAllocationParams{ID: id, BranchID: actor.BranchID})
		if errors.Is(err, pgx.ErrNoRows) {
			return status.Error(codes.NotFound, "allocation not found in your branch")
		}
		if err != nil {
			return err
		}
		if !allocation.Eligible || numericToScale(allocation.Amount, -2).Sign() <= 0 {
			return status.Error(codes.FailedPrecondition, "allocation has no payable dividend")
		}
		if choice != "" {
			if allocation.Choice.Valid && allocation.Choice.String != choice {
				return status.Error(codes.FailedPrecondition, "member's settlement choice has already been recorded")
			}
			if !allocation.Choice.Valid {
				if err := q.RecordDividendChoice(ctx, ceepdb.RecordDividendChoiceParams{ID: id, Choice: text(choice), ChoiceBy: actor.ID}); err != nil {
					return err
				}
			}
		} else {
			if !allocation.Choice.Valid {
				return status.Error(codes.FailedPrecondition, "record the member's choice before settlement")
			}
			payment := ceepdb.SettleDividendAllocationParams{ID: id, SettledBy: actor.ID}
			if allocation.Choice.String == "cash" {
				date, err := time.Parse("2006-01-02", paymentDate)
				today := time.Now().In(time.FixedZone("Africa/Nairobi", 3*60*60)).Format("2006-01-02")
				if err != nil || voucher == "" || len(voucher) > 200 || paymentDate > today || date.Before(allocation.DeclarationDate.Time) {
					return status.Error(codes.InvalidArgument, "cash requires a signed voucher/register reference (at most 200 characters) and a payment date between declaration and today")
				}
				payment.VoucherReference, payment.PaymentDate = text(voucher), pgtype.Date{Time: date, Valid: true}
			} else if voucher != "" || paymentDate != "" {
				return status.Error(codes.InvalidArgument, "reinvestment must not contain cash payment evidence")
			}
			if allocation.SettledAt.Valid {
				if allocation.VoucherReference != payment.VoucherReference || allocation.PaymentDate != payment.PaymentDate {
					return status.Error(codes.FailedPrecondition, "allocation is already settled with different evidence")
				}
			} else {
				// Eligibility is frozen at approval. A later member status change
				// must not prevent either settlement option.
				if allocation.Choice.String == "shares" {
					posted, err := h.service.shareService.PostDividendInTx(ctx, sharesqlc.New(tx), allocation.ShareAccountID,
						allocation.MemberID, actor.BranchID, allocation.Amount, allocation.ID, actor.ID)
					if err != nil {
						return err
					}
					payment.PostedTransactionID = posted.ID
				}
				if err := q.SettleDividendAllocation(ctx, payment); err != nil {
					return err
				}
			}
		}
		raw, err = q.GetDividendRegister(ctx, ceepdb.GetDividendRegisterParams{BranchID: actor.BranchID, FinancialYear: allocation.FinancialYear})
		return err
	})
	return dividendResponse(ctx, raw, canManage, err)
}

// dividendResponse keeps database details out of transport errors and monetary
// strings intact; removing it duplicates this boundary across read and write paths.
func dividendResponse(ctx context.Context, raw []byte, canManage bool, err error) (*structpb.Struct, error) {
	if err != nil {
		var databaseError *pgconn.PgError
		if errors.As(err, &databaseError) && databaseError.Code == "23505" && databaseError.ConstraintName == "dividend_cash_voucher_branch_key" {
			return nil, status.Error(codes.AlreadyExists, "this cash payment reference has already been used; use a unique voucher number or register page and row within your branch")
		}
		if status.Code(err) != codes.Unknown {
			return nil, err
		}
		slog.ErrorContext(ctx, "dividend operation failed", "error", err)
		return nil, status.Error(codes.Internal, "unable to complete dividend operation; reload the register before retrying")
	}
	report := &structpb.Struct{}
	if err := protojson.Unmarshal(raw, report); err != nil {
		return nil, status.Error(codes.Internal, "unable to encode dividend register")
	}
	report.Fields["canManage"] = structpb.NewBoolValue(canManage)
	return report, nil
}
