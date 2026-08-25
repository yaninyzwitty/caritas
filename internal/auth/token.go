package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	authsqlc "github.com/yaninyzwitty/caritas-backend/internal/auth/repository/sqlc"
)

const (
	tokenIssuer   = "caritas-backend"
	tokenAudience = "caritas-admin"
	tokenTTL      = 45 * time.Minute
)

var ErrInvalidToken = errors.New("invalid token")

type tokenClaims struct {
	Subject  string `json:"sub"`
	Role     string `json:"role"`
	BranchID int64  `json:"branch_id"`
	Issuer   string `json:"iss"`
	Audience string `json:"aud"`
	IssuedAt int64  `json:"iat"`
	Expires  int64  `json:"exp"`
}

type Principal struct {
	ID       pgtype.UUID
	Role     string
	BranchID int64
}

func createAccessToken(staff authsqlc.StaffUser, secret string, now time.Time) (string, error) {
	staffID, err := uuidFromPG(staff.ID)
	if err != nil {
		return "", err
	}

	header := map[string]string{"alg": "HS256", "typ": "JWT"}
	claims := tokenClaims{
		Subject:  staffID,
		Role:     staff.Role,
		BranchID: staff.BranchID,
		Issuer:   tokenIssuer,
		Audience: tokenAudience,
		IssuedAt: now.Unix(),
		Expires:  now.Add(tokenTTL).Unix(),
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}

	unsigned := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	return unsigned + "." + sign(unsigned, secret), nil
}

func validateAccessToken(token string, secret string, now time.Time) (Principal, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Principal{}, ErrInvalidToken
	}

	unsigned := parts[0] + "." + parts[1]
	if !hmac.Equal([]byte(parts[2]), []byte(sign(unsigned, secret))) {
		return Principal{}, ErrInvalidToken
	}

	var header struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Principal{}, ErrInvalidToken
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return Principal{}, ErrInvalidToken
	}
	if header.Alg != "HS256" || header.Typ != "JWT" {
		return Principal{}, ErrInvalidToken
	}

	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Principal{}, ErrInvalidToken
	}

	var claims tokenClaims
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return Principal{}, ErrInvalidToken
	}
	if claims.Issuer != tokenIssuer || claims.Audience != tokenAudience || claims.Expires <= now.Unix() {
		return Principal{}, ErrInvalidToken
	}

	id, err := uuidToPG(claims.Subject)
	if err != nil {
		return Principal{}, ErrInvalidToken
	}

	return Principal{ID: id, Role: claims.Role, BranchID: claims.BranchID}, nil
}

func sign(unsigned string, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(unsigned))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
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
