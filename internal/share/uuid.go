package share

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// uuidToString exposes a DB-side pgtype.UUID as the string ID carried by proto
// messages. Without it, responses could not carry account/transaction IDs.
func uuidToString(id pgtype.UUID) (string, error) {
	if !id.Valid {
		return "", fmt.Errorf("invalid uuid")
	}
	return uuid.UUID(id.Bytes).String(), nil
}

// stringToUUID parses an incoming proto string ID into pgtype.UUID for DB
// queries. Without it no request could resolve its target account/transaction.
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
