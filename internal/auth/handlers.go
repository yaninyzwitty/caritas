package auth

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	authv1 "github.com/yaninyzwitty/caritas-backend/gen/auth/v1"
	authsqlc "github.com/yaninyzwitty/caritas-backend/internal/auth/repository/sqlc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Handlers struct {
	authv1.UnimplementedAuthServiceServer
	store *Store
}

func NewHandlers(store *Store) *Handlers {
	return &Handlers{store: store}
}

func (h *Handlers) CreateStaffUser(ctx context.Context, req *authv1.CreateStaffUserRequest) (*authv1.CreateStaffUserResponse, error) {
	slog.Info("received info", "val", "create staff user")

	email := strings.ToLower(strings.TrimSpace(req.GetEmail()))
	name := strings.TrimSpace(req.GetName())
	role := strings.TrimSpace(req.GetRole())
	authUserID := strings.TrimSpace(req.GetAuthUserId())

	if req.GetBranchId() <= 0 || email == "" || name == "" || authUserID == "" || !validRole(role) {
		return nil, status.Error(codes.InvalidArgument, "auth_user_id, branch_id, email, name, and valid role are required")
	}

	staff, err := h.store.CreateStaffUser(ctx, authsqlc.CreateStaffUserParams{
		AuthUserID: pgtype.Text{String: authUserID, Valid: true},
		Name:       name,
		BranchID:   req.GetBranchId(),
		Email:      email,
		Role:       role,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.AlreadyExists, "staff email or auth user already exists")
	}
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to create staff user")
	}

	return &authv1.CreateStaffUserResponse{StaffUser: staffUserToProto(staff)}, nil
}

func (h *Handlers) DeactivateStaffUser(ctx context.Context, req *authv1.DeactivateStaffUserRequest) (*authv1.DeactivateStaffUserResponse, error) {
	staffID, err := uuidToPG(req.GetStaffUserId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid staff_user_id")
	}

	staff, err := h.store.DeactivateStaffUser(ctx, staffID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "active staff user not found")
	}
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to deactivate staff user")
	}

	return &authv1.DeactivateStaffUserResponse{StaffUser: staffUserToProto(staff)}, nil
}

func validRole(role string) bool {
	switch role {
	case roleSystemAdmin, roleManager, roleLoanOfficer, roleCashier, roleAuditor, roleChairperson, roleSecretary:
		return true
	default:
		return false
	}
}

func staffUserToProto(staff authsqlc.StaffUser) *authv1.StaffUser {
	id, _ := uuidFromPG(staff.ID)
	return &authv1.StaffUser{
		Id:         id,
		BranchId:   staff.BranchID,
		Email:      staff.Email,
		Role:       staff.Role,
		IsActive:   staff.IsActive,
		CreatedAt:  timestamppb.New(staff.CreatedAt.Time),
		UpdatedAt:  timestamppb.New(staff.UpdatedAt.Time),
		Name:       staff.Name,
		AuthUserId: staff.AuthUserID.String,
	}
}
