package member

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/yaninyzwitty/caritas-backend/internal/repository/sqlc"
)

const DefaultBranchID = 1

var statusTransitions = map[string]map[string]bool{
	"pending":   {"active": true, "rejected": true},
	"active":    {"suspended": true, "closed": true},
	"suspended": {"active": true},
}

type Service struct {
	store *Store
}

func NewService(store *Store) *Service {
	return &Service{store: store}
}

func (s *Service) RegisterMember(ctx context.Context, branchID int64, nationalID string, profile sqlc.CreateMemberProfileParams) (sqlc.GetMemberByIDRow, error) {
	var member sqlc.GetMemberByIDRow

	err := s.store.ExecTx(ctx, func(q sqlc.Querier) error {
		memberNumber, err := q.IncrementMemberNumber(ctx, branchID)
		if err != nil {
			return fmt.Errorf("increment member number: %w", err)
		}
		slog.Info("member number", "val", memberNumber)

		memberID, err := q.CreateMember(ctx, sqlc.CreateMemberParams{
			BranchID:     branchID,
			MemberNumber: int64(memberNumber),
			NationalID:   nationalID,
			Status:       "pending",
		})
		if err != nil {
			return fmt.Errorf("create member: %w", err)
		}

		profile.MemberID = memberID
		if _, err := q.CreateMemberProfile(ctx, profile); err != nil {
			return fmt.Errorf("create member profile: %w", err)
		}

		member, err = q.GetMemberByID(ctx, memberID)
		return err
	})

	if err != nil {
		return sqlc.GetMemberByIDRow{}, err
	}

	return member, nil
}

func (s *Service) GetMember(ctx context.Context, memberID pgtype.UUID) (sqlc.GetMemberByIDRow, error) {
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

func (s *Service) ListMembers(ctx context.Context, branchID int64, cursor *pgtype.UUID, limit int32) ([]sqlc.ListMembersByBranchCursorRow, error) {
	var cursorUUID pgtype.UUID
	if cursor != nil {
		cursorUUID = *cursor
	}
	
	return s.store.ListMembersByBranchCursor(ctx, sqlc.ListMembersByBranchCursorParams{
		BranchID: branchID,
		Column2:  cursorUUID,
		Limit:    limit,
	})
}

func (s *Service) UpdateMemberProfile(ctx context.Context, memberID pgtype.UUID, profile sqlc.UpdateMemberProfileParams) error {
	profile.MemberID = memberID
	return s.store.ExecTx(ctx, func(q sqlc.Querier) error {
		if err := q.UpdateMemberProfile(ctx, profile); err != nil {
			return fmt.Errorf("update member profile: %w", err)
		}
		return nil
	})
}

func (s *Service) UpdateMemberStatus(ctx context.Context, memberID pgtype.UUID, newStatus string, reason string) (sqlc.GetMemberByIDRow, error) {
	var member sqlc.GetMemberByIDRow

	err := s.store.ExecTx(ctx, func(q sqlc.Querier) error {
		current, err := q.GetMemberByID(ctx, memberID)
		if err != nil {
			return fmt.Errorf("get current member: %w", err)
		}

		if !statusTransitions[current.Status][newStatus] {
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

func (s *Service) CloseMember(ctx context.Context, memberID pgtype.UUID, reason string) (sqlc.DeactivateMemberRow, error) {
	var deactivated sqlc.DeactivateMemberRow

	err := s.store.ExecTx(ctx, func(q sqlc.Querier) error {
		current, err := q.GetMemberByID(ctx, memberID)
		if err != nil {
			return fmt.Errorf("get member: %w", err)
		}

		if current.Status == "closed" {
			return ErrMemberAlreadyClosed
		}

		if !statusTransitions[current.Status]["closed"] {
			return fmt.Errorf("%w: cannot close member with status %s", ErrInvalidStatusTransition, current.Status)
		}

		deactivated, err = q.DeactivateMember(ctx, memberID)
		if err != nil {
			return fmt.Errorf("deactivate member: %w", err)
		}

		if err := q.RecordMemberStatusTransition(ctx, sqlc.RecordMemberStatusTransitionParams{
			MemberID:   memberID,
			FromStatus: pgtype.Text{String: current.Status, Valid: true},
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