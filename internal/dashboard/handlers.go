package dashboard

import (
	"context"
	"log/slog"

	dashboardv1 "github.com/yaninyzwitty/caritas-backend/gen/dashboard/v1"
	"github.com/yaninyzwitty/caritas-backend/internal/ceepdb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Handlers serves a read-only branch aggregate without loading directory records.
type Handlers struct {
	dashboardv1.UnimplementedDashboardServiceServer
	queries *ceepdb.Queries
}

// NewHandlers reuses the reporting query layer for the dashboard route.
func NewHandlers(queries *ceepdb.Queries) *Handlers {
	return &Handlers{queries: queries}
}

// GetSummary returns the metrics needed by the main dashboard in one database read.
func (h *Handlers) GetSummary(ctx context.Context, req *dashboardv1.GetSummaryRequest) (*dashboardv1.GetSummaryResponse, error) {
	branchID := req.GetBranchId()
	if branchID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "branch_id must be positive")
	}

	row, err := h.queries.GetDashboardSummary(ctx, branchID)
	if err != nil {
		slog.ErrorContext(ctx, "dashboard summary failed", "branch_id", branchID, "error", err)
		return nil, status.Error(codes.Internal, "dashboard summary unavailable")
	}
	principal, err := row.OutstandingPrincipal.Value()
	if err != nil {
		return nil, status.Error(codes.Internal, "dashboard summary unavailable")
	}
	amount, ok := principal.(string)
	if !ok {
		return nil, status.Error(codes.Internal, "dashboard summary unavailable")
	}
	shareCapital, err := row.ShareCapital.Value()
	if err != nil {
		return nil, status.Error(codes.Internal, "dashboard summary unavailable")
	}
	shares, ok := shareCapital.(string)
	if !ok {
		return nil, status.Error(codes.Internal, "dashboard summary unavailable")
	}
	monthlyCollections, err := row.CollectionsThisMonth.Value()
	if err != nil {
		return nil, status.Error(codes.Internal, "dashboard summary unavailable")
	}
	collections, ok := monthlyCollections.(string)
	if !ok {
		return nil, status.Error(codes.Internal, "dashboard summary unavailable")
	}
	return &dashboardv1.GetSummaryResponse{
		ActiveMembers:        row.ActiveMembers,
		ActiveLoans:          row.ActiveLoans,
		PendingLoans:         row.PendingLoans,
		DelinquentLoans:      row.DelinquentLoans,
		ClosedLoans:          row.ClosedLoans,
		OutstandingPrincipal: amount,
		ShareCapital:         shares,
		CollectionsThisMonth: collections,
	}, nil
}
