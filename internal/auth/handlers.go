package auth

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	authv1 "github.com/yaninyzwitty/caritas-backend/gen/auth/v1"
	authsqlc "github.com/yaninyzwitty/caritas-backend/internal/auth/repository/sqlc"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Handlers struct {
	authv1.UnimplementedAuthServiceServer
	store       *Store
	tokenSecret string
}

func NewHandlers(store *Store, tokenSecret string) *Handlers {
	return &Handlers{store: store, tokenSecret: tokenSecret}
}

func (h *Handlers) Login(ctx context.Context, req *authv1.LoginRequest) (*authv1.LoginResponse, error) {
	started := time.Now()

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	slog.Info(
		"starting staff lookup",
		"email", req.GetEmail(),
		"parent_ctx_error", ctx.Err(),
		"child_ctx_error", reqCtx.Err(),
	)

	email := strings.ToLower(strings.TrimSpace(req.GetEmail()))
	if email == "" || req.GetPassword() == "" {
		return nil, status.Error(codes.InvalidArgument, "email and password are required")
	}

	slog.Info("starting staff lookup", "email", email)

	staff, err := h.store.GetActiveStaffByEmail(reqCtx, email)
	slog.Info(
		"staff lookup returned",
		"duration", time.Since(started),
	)
	if err != nil {
		slog.Error(
			"staff lookup failed",
			"error", err,
			"parent_ctx_error", ctx.Err(),
			"child_ctx_error", reqCtx.Err(),
			"parent_cause", context.Cause(ctx),
			"child_cause", context.Cause(reqCtx),
		)

		return nil, status.Error(codes.Unauthenticated, "invalid credentials")
	}
	slog.Info("[password_hash]", "Val", staff.PasswordHash)
	if err := bcrypt.CompareHashAndPassword([]byte(staff.PasswordHash), []byte(req.GetPassword())); err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid credentials")
	}

	token, err := createAccessToken(staff, h.tokenSecret, time.Now().UTC())
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to create access token")
	}

	return &authv1.LoginResponse{
		AccessToken:      token,
		ExpiresInSeconds: int64(tokenTTL.Seconds()),
	}, nil
}

func (h *Handlers) CreateStaffUser(ctx context.Context, req *authv1.CreateStaffUserRequest) (*authv1.CreateStaffUserResponse, error) {

	email := strings.ToLower(strings.TrimSpace(req.GetEmail()))
	name := strings.TrimSpace(req.GetName())
	role := strings.TrimSpace(req.GetRole())

	if req.GetBranchId() <= 0 || email == "" || name == "" || req.GetPassword() == "" || !validRole(role) {
		return nil, status.Error(codes.InvalidArgument, "branch_id, email, name, password, and valid role are required")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.GetPassword()), bcrypt.DefaultCost)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to hash password")
	}

	staff, err := h.store.CreateStaffUser(ctx, authsqlc.CreateStaffUserParams{
		Name:         name,
		BranchID:     req.GetBranchId(),
		Email:        email,
		PasswordHash: string(hash),
		Role:         role,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.AlreadyExists, "staff email already exists")
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
	_, ok := rolePermissions[role]
	return ok
}

func staffUserToProto(staff authsqlc.StaffUser) *authv1.StaffUser {
	id, _ := uuidFromPG(staff.ID)
	return &authv1.StaffUser{
		Id:        id,
		BranchId:  staff.BranchID,
		Email:     staff.Email,
		Role:      staff.Role,
		IsActive:  staff.IsActive,
		CreatedAt: timestamppb.New(staff.CreatedAt.Time),
		UpdatedAt: timestamppb.New(staff.UpdatedAt.Time),
		Name:      staff.Name,
	}
}
