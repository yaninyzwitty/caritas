package member

import (
	"math/big"

	"github.com/jackc/pgx/v5/pgtype"
	memberv1 "github.com/yaninyzwitty/caritas-backend/gen/caritas/member/v1"
	timestamppb "google.golang.org/protobuf/types/known/timestamppb"
)

func convertMember(id, branchID, memberNumber int64, nationalID, status string, createdAt, updatedAt pgtype.Timestamptz,
	fullName, phone, email, address string, dateOfBirth pgtype.Date,
	occupation, employer string, monthlyIncome *big.Int,
	idDocumentType, idDocumentNumber string,
	nextOfKinName, nextOfKinPhone, nextOfKinRelationship string) *memberv1.Member {

	var units int64
	if monthlyIncome != nil {
		units = monthlyIncome.Int64()
	}

	var dob *timestamppb.Timestamp
	if dateOfBirth.Valid {
		dob = timestamppb.New(dateOfBirth.Time)
	}

	var registeredAt, lastUpdated *timestamppb.Timestamp
	if createdAt.Valid {
		registeredAt = timestamppb.New(createdAt.Time)
	}
	if updatedAt.Valid {
		lastUpdated = timestamppb.New(updatedAt.Time)
	}

	return &memberv1.Member{
		Id:           id,
		BranchId:     branchID,
		MemberNumber: memberNumber,
		NationalId:   nationalID,
		Status:       statusStringToProto(status),
		Profile: &memberv1.MemberProfile{
			Personal: &memberv1.PersonalInfo{
				FullName:    fullName,
				Phone:       phone,
				Email:       email,
				DateOfBirth: dob,
				Address:     address,
			},
			Employment: &memberv1.Employment{
				Occupation: occupation,
				Employer:   employer,
				MonthlyIncome: &memberv1.Money{
					CurrencyCode: "KES",
					Units:        units,
					Nanos:        0,
				},
			},
			IdDocument: &memberv1.Identification{
				Type:   idDocumentType,
				Number: idDocumentNumber,
			},
			NextOfKin: &memberv1.NextOfKin{
				Name:         nextOfKinName,
				Phone:        nextOfKinPhone,
				Relationship: relationshipStringToProto(nextOfKinRelationship),
			},
		},
		RegisteredAt: registeredAt,
		LastUpdated:  lastUpdated,
	}
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
