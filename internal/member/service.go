package member

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/yaninyzwitty/caritas-backend/internal/db"
	"github.com/yaninyzwitty/caritas-backend/internal/repository/sqlc"
)

type Service struct {
	store *db.Store
}

func NewService(store *db.Store) *Service {
	return &Service{store: store}
}

var (
	ErrInvalidStatusTransition = errors.New("invalid status transition")
	ErrMemberHasActiveLoans    = errors.New("member has active loans")
	ErrMemberHasShareBalance   = errors.New("member has nonzero share balance")
)

func allowedStatusTransition(from, to string) bool {
	transitions := map[string]map[string]bool{
		"pending":   {"active": true, "rejected": true},
		"active":    {"suspended": true, "closed": true},
		"suspended": {"active": true},
	}
	return transitions[from][to]
}

func (s *Service) RegisterMember(ctx context.Context, branchID int64, nationalID string, profile sqlc.CreateMemberProfileParams) (sqlc.GetMemberByIDRow, error) {
	var member sqlc.GetMemberByIDRow

	err := s.store.ExecTx(ctx, func(q sqlc.Querier) error {
		if err := q.RecoverMemberNumberCounter(ctx, branchID); err != nil {
			return fmt.Errorf("recover member number counter: %w", err)
		}

		memberNumber, err := q.IncrementMemberNumber(ctx, branchID)
		if err != nil {
			return fmt.Errorf("increment member number: %w", err)
		}

		memberID, err := q.CreateMember(ctx, sqlc.CreateMemberParams{
			BranchID:     branchID,
			MemberNumber: int64(memberNumber),
			NationalID:   nationalID,
			Status:       "pending",
		})
		if err != nil {
			return fmt.Errorf("create member: %w", err)
		}

		if memberID == 0 {
			existing, err := q.MemberExistsByBranchAndNationalID(ctx, sqlc.MemberExistsByBranchAndNationalIDParams{
				BranchID:   branchID,
				NationalID: nationalID,
			})
			if err != nil {
				return fmt.Errorf("check existing member: %w", err)
			}
			member, err = q.GetMemberByID(ctx, existing.ID)
			if err != nil {
				return fmt.Errorf("get existing member: %w", err)
			}
			return nil
		}

		profile.MemberID = memberID
		if _, err := q.CreateMemberProfile(ctx, profile); err != nil {
			return fmt.Errorf("create member profile: %w", err)
		}

		member, err = q.GetMemberByID(ctx, memberID)
		if err != nil {
			return fmt.Errorf("get created member: %w", err)
		}

		return nil
	})

	if err != nil {
		return sqlc.GetMemberByIDRow{}, err
	}

	return member, nil
}

func (s *Service) GetMember(ctx context.Context, memberID int64) (sqlc.GetMemberByIDRow, error) {
	return s.store.GetMemberByID(ctx, memberID)
}

func (s *Service) GetMemberByNationalID(ctx context.Context, branchID int64, nationalID string) (sqlc.GetMemberByIDRow, error) {
	existing, err := s.store.MemberExistsByBranchAndNationalID(ctx, sqlc.MemberExistsByBranchAndNationalIDParams{
		BranchID:   branchID,
		NationalID: nationalID,
	})
	if err != nil {
		return sqlc.GetMemberByIDRow{}, fmt.Errorf("check member existence: %w", err)
	}

	return s.store.GetMemberByID(ctx, existing.ID)
}

func (s *Service) ListMembers(ctx context.Context, branchID int64, cursor *string, limit int32, statusFilter *string) ([]sqlc.ListMembersByBranchCursorRow, error) {
	var cursorTime pgtype.Timestamptz
	var cursorID int64

	if cursor != nil {
		var unixTime int64
		_, err := fmt.Sscanf(*cursor, "%d,%d", &unixTime, &cursorID)
		if err != nil {
			return nil, fmt.Errorf("parse cursor: %w", err)
		}
		cursorTime.Time = time.Unix(unixTime, 0)
		cursorTime.Valid = true
	}

	members, err := s.store.ListMembersByBranchCursor(ctx, sqlc.ListMembersByBranchCursorParams{
		BranchID:  branchID,
		Column2:   pgtype.Timestamp{Time: cursorTime.Time, Valid: cursorTime.Valid},
		CreatedAt: cursorTime,
		Limit:     limit,
	})
	if err != nil {
		return nil, err
	}

	if statusFilter == nil || *statusFilter == "" {
		return members, nil
	}

	var filtered []sqlc.ListMembersByBranchCursorRow
	for _, m := range members {
		if m.Status == *statusFilter {
			filtered = append(filtered, m)
		}
	}

	return filtered, nil
}

func (s *Service) UpdateMemberProfile(ctx context.Context, memberID int64, profile sqlc.UpdateMemberProfileParams) error {
	return s.store.ExecTx(ctx, func(q sqlc.Querier) error {
		profile.MemberID = memberID
		return q.UpdateMemberProfile(ctx, profile)
	})
}

func (s *Service) UpdateMemberStatus(ctx context.Context, memberID int64, newStatus string, reason string) (sqlc.GetMemberByIDRow, error) {
	var member sqlc.GetMemberByIDRow

	err := s.store.ExecTx(ctx, func(q sqlc.Querier) error {
		current, err := q.GetMemberByID(ctx, memberID)
		if err != nil {
			return fmt.Errorf("get current member: %w", err)
		}

		if !allowedStatusTransition(current.Status, newStatus) {
			return fmt.Errorf("%w: cannot transition from %s to %s", ErrInvalidStatusTransition, current.Status, newStatus)
		}

		_, err = q.UpdateMemberStatus(ctx, sqlc.UpdateMemberStatusParams{
			ID:     memberID,
			Status: newStatus,
		})
		if err != nil {
			return fmt.Errorf("update member status: %w", err)
		}

		if err := q.RecordMemberStatusTransition(ctx, sqlc.RecordMemberStatusTransitionParams{
			MemberID:   memberID,
			FromStatus: pgtype.Text{String: current.Status, Valid: true},
			ToStatus:   newStatus,
			Reason:     pgtype.Text{String: reason, Valid: reason != ""},
		}); err != nil {
			return fmt.Errorf("record status transition: %w", err)
		}

		member, err = q.GetMemberByID(ctx, memberID)
		if err != nil {
			return fmt.Errorf("get updated member: %w", err)
		}

		return nil
	})

	if err != nil {
		return sqlc.GetMemberByIDRow{}, err
	}

	return member, nil
}

func (s *Service) CloseMember(ctx context.Context, memberID int64, reason string) (sqlc.DeactivateMemberRow, error) {
	member, err := s.store.GetMemberByID(ctx, memberID)
	if err != nil {
		return sqlc.DeactivateMemberRow{}, fmt.Errorf("get member: %w", err)
	}

	if member.Status == "closed" {
		return sqlc.DeactivateMemberRow{}, fmt.Errorf("member already closed")
	}

	if !allowedStatusTransition(member.Status, "closed") {
		return sqlc.DeactivateMemberRow{}, fmt.Errorf("%w: cannot close member with status %s", ErrInvalidStatusTransition, member.Status)
	}

	// TODO: Check for active loans and nonzero share balance
	// This requires shares and loans domains to be implemented
	// For now, we allow closure but this validation is required per domain spec

	var deactivated sqlc.DeactivateMemberRow

	err = s.store.ExecTx(ctx, func(q sqlc.Querier) error {
		deactivated, err = q.DeactivateMember(ctx, memberID)
		if err != nil {
			return fmt.Errorf("deactivate member: %w", err)
		}

		if err := q.RecordMemberStatusTransition(ctx, sqlc.RecordMemberStatusTransitionParams{
			MemberID:   memberID,
			FromStatus: pgtype.Text{String: member.Status, Valid: true},
			ToStatus:   "closed",
			Reason:     pgtype.Text{String: reason, Valid: reason != ""},
		}); err != nil {
			return fmt.Errorf("record status transition: %w", err)
		}

		return nil
	})

	if err != nil {
		return sqlc.DeactivateMemberRow{}, err
	}

	return deactivated, nil
}

func (s *Service) GetMemberStatusHistory(ctx context.Context, memberID int64) ([]sqlc.MemberStatusHistory, error) {
	return s.store.GetMemberStatusHistory(ctx, memberID)
}

func (s *Service) ValidateMemberActiveInBranch(ctx context.Context, memberID int64, branchID int64) (sqlc.ValidateMemberActiveInBranchRow, error) {
	return s.store.ValidateMemberActiveInBranch(ctx, sqlc.ValidateMemberActiveInBranchParams{
		ID:       memberID,
		BranchID: branchID,
	})
}