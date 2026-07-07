package member

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func uuidToString(id pgtype.UUID) (string, error) {
	if !id.Valid {
		return "", fmt.Errorf("invalid uuid")
	}
	return uuid.UUID(id.Bytes).String(), nil
}

func stringToUUID(id string) (pgtype.UUID, error) {
	if id == "" {
		return pgtype.UUID{}, fmt.Errorf("empty uuid")
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid uuid: %w", err)
	}
	return pgtype.UUID{Bytes: [16]byte(parsed), Valid: true}, nil
}