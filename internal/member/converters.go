package member

import (
	"math/big"

	"github.com/jackc/pgx/v5/pgtype"
	memberv1 "github.com/yaninyzwitty/caritas-backend/gen/caritas/member/v1"
	"github.com/yaninyzwitty/caritas-backend/internal/repository/sqlc"
	timestamppb "google.golang.org/protobuf/types/known/timestamppb"
)

func convertMemberFromRow(row sqlc.GetMemberByIDRow) *memberv1.Member {
	id, _ := uuidToString(row.ID)

	var units int64
	var nanos int32

	if row.MonthlyIncome.Int != nil && row.MonthlyIncome.Int.Sign() != 0 {
		total := new(big.Int).Set(row.MonthlyIncome.Int)
		unitsBig := new(big.Int).Div(total, big.NewInt(1_000_000_000))
		nanosBig := new(big.Int).Mod(total, big.NewInt(1_000_000_000))
		units = unitsBig.Int64()
		nanos = int32(nanosBig.Int64())
	}

	return &memberv1.Member{
		Id:           id,
		BranchId:     row.BranchID,
		MemberNumber: row.MemberNumber,
		NationalId:   row.NationalID,
		Status:       statusStringToProto(row.Status),
		Profile: &memberv1.MemberProfile{
			Personal: &memberv1.PersonalInfo{
				FullName:    row.FullName.String,
				Phone:       row.Phone.String,
				Email:       row.Email.String,
				DateOfBirth: dateToProto(row.DateOfBirth),
				Address:     row.Address.String,
			},
			Employment: &memberv1.Employment{
				Occupation: row.Occupation.String,
				Employer:   row.Employer.String,
				MonthlyIncome: &memberv1.Money{
					CurrencyCode: "KES",
					Units:        units,
					Nanos:        nanos,
				},
			},
			IdDocument: &memberv1.Identification{
				Type:   row.IDDocumentType.String,
				Number: row.IDDocumentNumber.String,
			},
			NextOfKin: &memberv1.NextOfKin{
				Name:         row.NextOfKinName.String,
				Phone:        row.NextOfKinPhone.String,
				Relationship: relationshipStringToProto(row.NextOfKinRelationship.String),
			},
		},
		RegisteredAt: timestampToProto(row.CreatedAt),
		LastUpdated:  timestampToProto(row.UpdatedAt),
	}
}

func convertListMemberFromRow(row sqlc.ListMembersByBranchCursorRow) *memberv1.Member {
	id, _ := uuidToString(row.ID)

	var units int64
	var nanos int32

	if row.MonthlyIncome.Int != nil && row.MonthlyIncome.Int.Sign() != 0 {
		total := new(big.Int).Set(row.MonthlyIncome.Int)
		unitsBig := new(big.Int).Div(total, big.NewInt(1_000_000_000))
		nanosBig := new(big.Int).Mod(total, big.NewInt(1_000_000_000))
		units = unitsBig.Int64()
		nanos = int32(nanosBig.Int64())
	}

	return &memberv1.Member{
		Id:           id,
		BranchId:     row.BranchID,
		MemberNumber: row.MemberNumber,
		NationalId:   row.NationalID,
		Status:       statusStringToProto(row.Status),
		Profile: &memberv1.MemberProfile{
			Personal: &memberv1.PersonalInfo{
				FullName:    row.FullName.String,
				Phone:       row.Phone.String,
				Email:       row.Email.String,
				DateOfBirth: dateToProto(row.DateOfBirth),
				Address:     row.Address.String,
			},
			Employment: &memberv1.Employment{
				Occupation: row.Occupation.String,
				Employer:   row.Employer.String,
				MonthlyIncome: &memberv1.Money{
					CurrencyCode: "KES",
					Units:        units,
					Nanos:        nanos,
				},
			},
			IdDocument: &memberv1.Identification{
				Type:   row.IDDocumentType.String,
				Number: row.IDDocumentNumber.String,
			},
			NextOfKin: &memberv1.NextOfKin{
				Name:         row.NextOfKinName.String,
				Phone:        row.NextOfKinPhone.String,
				Relationship: relationshipStringToProto(row.NextOfKinRelationship.String),
			},
		},
		RegisteredAt: timestampToProto(row.CreatedAt),
		LastUpdated:  timestampToProto(row.UpdatedAt),
	}
}

func dateToProto(d pgtype.Date) *timestamppb.Timestamp {
	if !d.Valid {
		return nil
	}
	return timestamppb.New(d.Time)
}

func timestampToProto(t pgtype.Timestamptz) *timestamppb.Timestamp {
	if !t.Valid {
		return nil
	}
	return timestamppb.New(t.Time)
}

func statusStringToProto(status string) memberv1.MemberStatus {
	switch status {
	case "pending":
		return memberv1.MemberStatus_MEMBER_STATUS_PENDING
	case "active":
		return memberv1.MemberStatus_MEMBER_STATUS_ACTIVE
	case "suspended":
		return memberv1.MemberStatus_MEMBER_STATUS_SUSPENDED
	case "closed":
		return memberv1.MemberStatus_MEMBER_STATUS_CLOSED
	case "rejected":
		return memberv1.MemberStatus_MEMBER_STATUS_REJECTED
	default:
		return memberv1.MemberStatus_MEMBER_STATUS_UNSPECIFIED
	}
}

func statusProtoToString(status memberv1.MemberStatus) string {
	switch status {
	case memberv1.MemberStatus_MEMBER_STATUS_PENDING:
		return "pending"
	case memberv1.MemberStatus_MEMBER_STATUS_ACTIVE:
		return "active"
	case memberv1.MemberStatus_MEMBER_STATUS_SUSPENDED:
		return "suspended"
	case memberv1.MemberStatus_MEMBER_STATUS_CLOSED:
		return "closed"
	case memberv1.MemberStatus_MEMBER_STATUS_REJECTED:
		return "rejected"
	default:
		return "pending"
	}
}

func relationshipStringToProto(rel string) memberv1.RelationshipType {
	switch rel {
	case "spouse":
		return memberv1.RelationshipType_RELATIONSHIP_TYPE_SPOUSE
	case "child":
		return memberv1.RelationshipType_RELATIONSHIP_TYPE_CHILD
	case "parent":
		return memberv1.RelationshipType_RELATIONSHIP_TYPE_PARENT
	case "sibling":
		return memberv1.RelationshipType_RELATIONSHIP_TYPE_SIBLING
	case "friend":
		return memberv1.RelationshipType_RELATIONSHIP_TYPE_FRIEND
	case "other":
		return memberv1.RelationshipType_RELATIONSHIP_TYPE_OTHER
	default:
		return memberv1.RelationshipType_RELATIONSHIP_TYPE_UNSPECIFIED
	}
}