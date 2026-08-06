package auth

import (
	"github.com/jackc/pgx/v5/pgxpool"
	authsqlc "github.com/yaninyzwitty/caritas-backend/internal/auth/repository/sqlc"
)

type Store struct {
	*authsqlc.Queries
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{Queries: authsqlc.New(pool)}
}
