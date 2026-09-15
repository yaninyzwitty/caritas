package auth

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// Principal carries the verified Better Auth identity and its backend staff
// authorization. Without it, handlers cannot attribute writes to a staff UUID.
type Principal struct {
	ID         pgtype.UUID
	AuthUserID string
	Role       string
	BranchID   int64
}

func uuidFromPG(id pgtype.UUID) (string, error) {
	if !id.Valid {
		return "", fmt.Errorf("invalid uuid")
	}
	return uuid.UUID(id.Bytes).String(), nil
}

func uuidToPG(id string) (pgtype.UUID, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return pgtype.UUID{}, err
	}
	return pgtype.UUID{Bytes: [16]byte(parsed), Valid: true}, nil
}
