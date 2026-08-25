package contribution

import (
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// uuidToString converts sqlc's pgtype UUID into the API's string shape. Without
// it, handlers would repeat the same Valid check and risk returning zero UUIDs
// as successful identifiers.
func uuidToString(id pgtype.UUID) (string, error) {
	if !id.Valid {
		return "", ErrInvalidPayment
	}
	return uuid.UUID(id.Bytes).String(), nil
}

// stringToUUID converts API UUID strings into pgtype UUIDs. Without it, handler
// validation would leak google/uuid details into service calls.
func stringToUUID(id string) (pgtype.UUID, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return pgtype.UUID{}, err
	}
	return pgtype.UUID{Bytes: [16]byte(parsed), Valid: true}, nil
}
