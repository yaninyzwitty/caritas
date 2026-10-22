package contribution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	contributionv1 "github.com/yaninyzwitty/caritas-backend/gen/contribution/v1"
	"github.com/yaninyzwitty/caritas-backend/internal/auth"
	"github.com/yaninyzwitty/caritas-backend/internal/ceepdb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

// GetCEEPReport is the authenticated read boundary; removing it leaves previews
// and historical exports without branch isolation. Money in the report is text.
func (h *Handlers) GetCEEPReport(ctx context.Context, req *contributionv1.GetCEEPReportRequest) (*contributionv1.GetCEEPReportResponse, error) {
	report, err := h.ceepReport(ctx, req, false, "")
	if err != nil {
		return nil, err
	}
	return &contributionv1.GetCEEPReportResponse{Report: report}, nil
}

// FinalizeCEEPReport exposes finalization separately from reads so a download
// cannot accidentally finalize a month or bypass the approval permission.
func (h *Handlers) FinalizeCEEPReport(ctx context.Context, req *contributionv1.FinalizeCEEPReportRequest) (*contributionv1.FinalizeCEEPReportResponse, error) {
	report, err := h.ceepReport(ctx, &contributionv1.GetCEEPReportRequest{Period: req.GetPeriod(), Cutoff: req.GetCutoff(),
		Title: req.GetTitle(), Parish: req.GetParish(), PaymentDate: req.GetPaymentDate()}, true, req.GetPreviewHash())
	if err != nil {
		return nil, err
	}
	return &contributionv1.FinalizeCEEPReportResponse{Report: report}, nil
}

// ceepReport keeps preview and finalization on identical source rules. Without
// this shared core, the figures approved could differ from the figures saved.
func (h *Handlers) ceepReport(ctx context.Context, req *contributionv1.GetCEEPReportRequest, finalize bool, previewHash string) (*structpb.Struct, error) {
	actor, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "authenticated staff is required")
	}
	_, canFinalize := auth.CashPermissions(actor.Role)
	if finalize && !canFinalize {
		return nil, status.Error(codes.PermissionDenied, "CEEP finalization requires cash approval permission")
	}
	period, err := time.Parse("2006-01-02", req.GetPeriod())
	if err != nil || period.Day() != 1 || period.Year() < 1 {
		return nil, status.Error(codes.InvalidArgument, "period must be YYYY-MM-01")
	}
	// A fixed offset avoids a deployment dependency on the operating system's zoneinfo.
	nairobi := time.FixedZone("Africa/Nairobi", 3*60*60)
	now := time.Now().UTC()
	month := time.Date(now.In(nairobi).Year(), now.In(nairobi).Month(), 1, 0, 0, 0, 0, time.UTC)
	if period.After(month) {
		return nil, status.Error(codes.InvalidArgument, "future reporting months are not allowed")
	}
	cutoff := now.Truncate(time.Microsecond)
	if req.GetCutoff() != "" {
		cutoff, err = time.Parse(time.RFC3339Nano, req.GetCutoff())
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "cutoff must include a timezone")
		}
		cutoff = cutoff.UTC().Truncate(time.Microsecond)
	}
	starts := time.Date(period.Year(), period.Month(), 1, 0, 0, 0, 0, nairobi)
	if cutoff.After(now) || cutoff.Before(starts) {
		return nil, status.Error(codes.InvalidArgument, "cutoff must be within or after the reporting month and no later than now")
	}
	if finalize && (req.GetCutoff() == "" || previewHash == "") {
		return nil, status.Error(codes.InvalidArgument, "review a preview before finalizing")
	}
	title, parish := strings.TrimSpace(req.GetTitle()), strings.TrimSpace(req.GetParish())
	if len(title) > 160 || len(parish) > 160 {
		return nil, status.Error(codes.InvalidArgument, "report headings must be at most 160 characters")
	}
	if title == "" {
		title = fmt.Sprintf("Branch %d", actor.BranchID)
	}
	if req.GetPaymentDate() != "" {
		if _, err := time.Parse("2006-01-02", req.GetPaymentDate()); err != nil {
			return nil, status.Error(codes.InvalidArgument, "payment date must be YYYY-MM-DD or empty for multiple dates")
		}
	}
	lookup := ceepdb.GetCEEPReportParams{BranchID: actor.BranchID, Period: pgtype.Date{Time: period, Valid: true}}
	var report map[string]any
	// Reuse the existing transaction boundary directly: CEEP adds no service,
	// repository or interface between the handler and generated sqlc queries.
	err = h.service.store.ExecReceiptTx(ctx, func(tx pgx.Tx) error {
		q := ceepdb.New(tx)
		saved, err := q.GetCEEPReport(ctx, lookup)
		if err == nil {
			return json.Unmarshal(saved, &report)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		raw, err := q.BuildCEEPReport(ctx, ceepdb.BuildCEEPReportParams{BranchID: actor.BranchID, Period: lookup.Period,
			Cutoff: pgtype.Timestamptz{Time: cutoff, Valid: true}})
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &report); err != nil {
			return err
		}
		report["title"], report["parish"], report["paymentDate"] = title, parish, req.GetPaymentDate()
		report["status"] = "draft"
		// Marshal maps in key order before hashing. Generation time and actor
		// permissions are excluded because they do not change financial facts.
		canonical, err := json.Marshal(report)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(canonical)
		hash := hex.EncodeToString(digest[:])
		report["previewHash"], report["generatedAt"] = hash, now.Format(time.RFC3339Nano)
		if !finalize {
			return nil
		}
		if cutoff.Before(starts.AddDate(0, 1, 0)) {
			return status.Error(codes.FailedPrecondition, "finalize only after the reporting month has ended")
		}
		if report["hasErrors"] == true {
			return status.Error(codes.FailedPrecondition, "resolve reconciliation errors before finalizing")
		}
		if hash != previewHash {
			return status.Error(codes.FailedPrecondition, "report sources changed; load and review a new preview")
		}
		report["status"], report["finalizedBy"], report["finalizedAt"] = "finalized", actor.ID.String(), now.Format(time.RFC3339Nano)
		raw, err = json.Marshal(report)
		if err != nil {
			return err
		}
		if err := q.SaveCEEPReport(ctx, ceepdb.SaveCEEPReportParams{BranchID: actor.BranchID, Period: lookup.Period, OfficerID: actor.ID, Report: raw}); err != nil {
			return err
		}
		// A concurrent finalizer may have won. Return the stored winner so every
		// retry and export sees exactly the same immutable report.
		saved, err = q.GetCEEPReport(ctx, lookup)
		if err != nil {
			return err
		}
		return json.Unmarshal(saved, &report)
	})
	if err != nil {
		if status.Code(err) != codes.Unknown {
			return nil, err
		}
		slog.ErrorContext(ctx, "generate CEEP report", "branch_id", actor.BranchID, "period", req.GetPeriod(), "error", err)
		return nil, status.Error(codes.Internal, "unable to generate CEEP report")
	}
	report["canFinalize"] = canFinalize && report["status"] == "draft" && !cutoff.Before(starts.AddDate(0, 1, 0))
	raw, err := json.Marshal(report)
	if err != nil {
		return nil, status.Error(codes.Internal, "unable to encode CEEP report")
	}
	response := &structpb.Struct{}
	if err := protojson.Unmarshal(raw, response); err != nil {
		return nil, status.Error(codes.Internal, "unable to encode CEEP report")
	}
	return response, nil
}
