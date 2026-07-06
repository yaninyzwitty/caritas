package member

import (
	"context"
	"fmt"
	"math/big"

	"github.com/jackc/pgx/v5/pgtype"
	memberv1 "github.com/yaninyzwitty/caritas-backend/gen/caritas/member/v1"
	"github.com/yaninyzwitty/caritas-backend/internal/repository/sqlc"
	timestamppb "google.golang.org/protobuf/types/known/timestamppb"
)

type Handlers struct {
	memberv1.UnimplementedMemberServiceServer
	memberService *Service
	store         *Store
}

func NewHandlers(memberService *Service, store *Store) *Handlers {
	return &Handlers{memberService: memberService, store: store}
}

func resolveBranchID(branchID int64) int64 {
	if branchID == 0 {
		return DefaultBranchID
	}
	return branchID
}

func (h *Handlers) RegisterMember(ctx context.Context, req *memberv1.RegisterMemberRequest) (*memberv1.RegisterMemberResponse, error) {
	branchID := resolveBranchID(req.BranchId)

	var dateOfBirth pgtype.Date
	if req.GetProfile().GetPersonal().GetDateOfBirth() != nil {
		dateOfBirth = pgtype.Date{Time: req.GetProfile().GetPersonal().GetDateOfBirth().AsTime(), Valid: true}
	}

	units := req.GetProfile().GetEmployment().GetMonthlyIncome().GetUnits()
	nanos := req.GetProfile().GetEmployment().GetMonthlyIncome().GetNanos()
	total := new(big.Int).Mul(big.NewInt(units), big.NewInt(1_000_000_000))
	total.Add(total, big.NewInt(int64(nanos)))

	profile := sqlc.CreateMemberProfileParams{
		FullName:              req.GetProfile().GetPersonal().GetFullName(),
		Phone:                 req.GetProfile().GetPersonal().GetPhone(),
		Email:                 req.GetProfile().GetPersonal().GetEmail(),
		Address:               pgtype.Text{String: req.GetProfile().GetPersonal().GetAddress(), Valid: req.GetProfile().GetPersonal().GetAddress() != ""},
		DateOfBirth:           dateOfBirth,
		Occupation:            pgtype.Text{String: req.GetProfile().GetEmployment().GetOccupation(), Valid: req.GetProfile().GetEmployment().GetOccupation() != ""},
		Employer:              pgtype.Text{String: req.GetProfile().GetEmployment().GetEmployer(), Valid: req.GetProfile().GetEmployment().GetEmployer() != ""},
		MonthlyIncome:         pgtype.Numeric{Int: total, Exp: -9, Valid: true},
		IDDocumentType:        pgtype.Text{String: req.GetProfile().GetIdDocument().GetType(), Valid: req.GetProfile().GetIdDocument().GetType() != ""},
		IDDocumentNumber:      pgtype.Text{String: req.GetProfile().GetIdDocument().GetNumber(), Valid: req.GetProfile().GetIdDocument().GetNumber() != ""},
		NextOfKinName:         pgtype.Text{String: req.GetProfile().GetNextOfKin().GetName(), Valid: req.GetProfile().GetNextOfKin().GetName() != ""},
		NextOfKinPhone:        pgtype.Text{String: req.GetProfile().GetNextOfKin().GetPhone(), Valid: req.GetProfile().GetNextOfKin().GetPhone() != ""},
		NextOfKinRelationship: pgtype.Text{String: req.GetProfile().GetNextOfKin().GetRelationship().String(), Valid: req.GetProfile().GetNextOfKin().GetRelationship() != memberv1.RelationshipType_RELATIONSHIP_TYPE_UNSPECIFIED},
	}

	member, err := h.memberService.RegisterMember(ctx, branchID, req.NationalId, profile)
	if err != nil {
		return nil, fmt.Errorf("register member: %w", err)
	}

	return &memberv1.RegisterMemberResponse{
		Member: convertMemberFromRow(member),
	}, nil
}

func (h *Handlers) GetMember(ctx context.Context, req *memberv1.GetMemberRequest) (*memberv1.GetMemberResponse, error) {
	var member sqlc.GetMemberByIDRow
	var err error

	switch identifier := req.Identifier.(type) {
	case *memberv1.GetMemberRequest_MemberId:
		member, err = h.memberService.GetMember(ctx, identifier.MemberId)
	case *memberv1.GetMemberRequest_NationalId:
		member, err = h.memberService.GetMemberByNationalID(ctx, resolveBranchID(req.BranchId), identifier.NationalId)
	default:
		return nil, fmt.Errorf("%w: must provide member_id or national_id", ErrInvalidIdentifier)
	}

	if err != nil {
		return nil, fmt.Errorf("get member: %w", err)
	}

	return &memberv1.GetMemberResponse{
		Member: convertMemberFromRow(member),
	}, nil
}

func (h *Handlers) ListMembers(ctx context.Context, req *memberv1.ListMembersRequest) (*memberv1.ListMembersResponse, error) {
	const maxPageSize = 1000

	limit := req.PageSize
	if limit <= 0 {
		limit = 50
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}

	branchID := resolveBranchID(req.BranchId)

	var pageToken *string
	if req.PageToken != "" {
		pageToken = &req.PageToken
	}

	members, err := h.memberService.ListMembers(ctx, branchID, pageToken, limit)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}

	var protoMembers []*memberv1.Member
	var nextToken string

	if len(members) > 0 {
		protoMembers = make([]*memberv1.Member, len(members))
		for i, m := range members {
			protoMembers[i] = convertListMemberFromRow(m)
		}
		lastMember := members[len(members)-1]
		nextToken = fmt.Sprintf("%d,%d", lastMember.CreatedAt.Time.Unix(), lastMember.ID)
	}

	return &memberv1.ListMembersResponse{
		Members:       protoMembers,
		NextPageToken: nextToken,
	}, nil
}

func (h *Handlers) UpdateMemberProfile(ctx context.Context, req *memberv1.UpdateMemberProfileRequest) (*memberv1.UpdateMemberProfileResponse, error) {
	var dateOfBirth pgtype.Date
	if req.GetProfile().GetPersonal().GetDateOfBirth() != nil {
		dateOfBirth = pgtype.Date{Time: req.GetProfile().GetPersonal().GetDateOfBirth().AsTime(), Valid: true}
	}

	units := req.GetProfile().GetEmployment().GetMonthlyIncome().GetUnits()
	nanos := req.GetProfile().GetEmployment().GetMonthlyIncome().GetNanos()
	total := new(big.Int).Mul(big.NewInt(units), big.NewInt(1_000_000_000))
	total.Add(total, big.NewInt(int64(nanos)))

	profile := sqlc.UpdateMemberProfileParams{
		FullName:              req.GetProfile().GetPersonal().GetFullName(),
		Phone:                 req.GetProfile().GetPersonal().GetPhone(),
		Email:                 req.GetProfile().GetPersonal().GetEmail(),
		Address:               pgtype.Text{String: req.GetProfile().GetPersonal().GetAddress(), Valid: req.GetProfile().GetPersonal().GetAddress() != ""},
		DateOfBirth:           dateOfBirth,
		Occupation:            pgtype.Text{String: req.GetProfile().GetEmployment().GetOccupation(), Valid: req.GetProfile().GetEmployment().GetOccupation() != ""},
		Employer:              pgtype.Text{String: req.GetProfile().GetEmployment().GetEmployer(), Valid: req.GetProfile().GetEmployment().GetEmployer() != ""},
		MonthlyIncome:         pgtype.Numeric{Int: total, Exp: -9, Valid: true},
		IDDocumentType:        pgtype.Text{String: req.GetProfile().GetIdDocument().GetType(), Valid: req.GetProfile().GetIdDocument().GetType() != ""},
		IDDocumentNumber:      pgtype.Text{String: req.GetProfile().GetIdDocument().GetNumber(), Valid: req.GetProfile().GetIdDocument().GetNumber() != ""},
		NextOfKinName:         pgtype.Text{String: req.GetProfile().GetNextOfKin().GetName(), Valid: req.GetProfile().GetNextOfKin().GetName() != ""},
		NextOfKinPhone:        pgtype.Text{String: req.GetProfile().GetNextOfKin().GetPhone(), Valid: req.GetProfile().GetNextOfKin().GetPhone() != ""},
		NextOfKinRelationship: pgtype.Text{String: req.GetProfile().GetNextOfKin().GetRelationship().String(), Valid: req.GetProfile().GetNextOfKin().GetRelationship() != memberv1.RelationshipType_RELATIONSHIP_TYPE_UNSPECIFIED},
	}

	if err := h.memberService.UpdateMemberProfile(ctx, req.MemberId, profile); err != nil {
		return nil, fmt.Errorf("update member profile: %w", err)
	}

	member, err := h.memberService.GetMember(ctx, req.MemberId)
	if err != nil {
		return nil, fmt.Errorf("get updated member: %w", err)
	}

	return &memberv1.UpdateMemberProfileResponse{
		Member: convertMemberFromRow(member),
	}, nil
}

func (h *Handlers) UpdateMemberStatus(ctx context.Context, req *memberv1.UpdateMemberStatusRequest) (*memberv1.UpdateMemberStatusResponse, error) {
	status := statusProtoToString(req.NewStatus)
	member, err := h.memberService.UpdateMemberStatus(ctx, req.MemberId, status, req.Reason)
	if err != nil {
		return nil, fmt.Errorf("update member status: %w", err)
	}

	return &memberv1.UpdateMemberStatusResponse{
		Member: convertMemberFromRow(member),
	}, nil
}

func (h *Handlers) CloseMember(ctx context.Context, req *memberv1.CloseMemberRequest) (*memberv1.CloseMemberResponse, error) {
	_, err := h.memberService.CloseMember(ctx, req.MemberId, req.Reason)
	if err != nil {
		return nil, fmt.Errorf("close member: %w", err)
	}

	return &memberv1.CloseMemberResponse{
		Success: true,
	}, nil
}

func (h *Handlers) GetMemberStatusHistory(ctx context.Context, req *memberv1.GetMemberStatusHistoryRequest) (*memberv1.GetMemberStatusHistoryResponse, error) {
	history, err := h.store.GetMemberStatusHistory(ctx, req.MemberId)
	if err != nil {
		return nil, fmt.Errorf("get member status history: %w", err)
	}

	var transitions []*memberv1.StatusTransition
	for _, transition := range history {
		var occurredAt *timestamppb.Timestamp
		if transition.CreatedAt.Valid {
			occurredAt = timestamppb.New(transition.CreatedAt.Time)
		}

		transitions = append(transitions, &memberv1.StatusTransition{
			FromStatus: transition.FromStatus.String,
			ToStatus:   transition.ToStatus,
			Reason:     transition.Reason.String,
			OccurredAt: occurredAt,
		})
	}

	return &memberv1.GetMemberStatusHistoryResponse{
		Transitions: transitions,
	}, nil
}
