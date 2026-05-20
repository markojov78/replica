package security

import (
	"errors"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type UserAccessClaims struct {
	TokenType string `json:"token_type"`
	jwt.RegisteredClaims
}

func NewUserAccessToken(secret []byte, userID uint, userTokenID uint, expiresAt time.Time) (string, error) {
	claims := UserAccessClaims{
		TokenType: "user",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatUint(uint64(userID), 10),
			ID:        strconv.FormatUint(uint64(userTokenID), 10),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(time.Now().UTC()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(secret)
}

func ParseUserAccessToken(secret []byte, tokenString string) (*UserAccessClaims, error) {
	claims := &UserAccessClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, errors.New("unexpected signing method")
		}
		return secret, nil
	})
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, errors.New("invalid token")
	}
	if claims.TokenType != "user" || claims.Subject == "" || claims.ID == "" {
		return nil, errors.New("invalid token claims")
	}
	return claims, nil
}
