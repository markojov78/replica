package security

import (
	"errors"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	TokenTypeUser = "user"
	TokenTypeNode = "node"
)

type UserAccessClaims struct {
	TokenType string `json:"token_type"`
	jwt.RegisteredClaims
}

type NodeAccessClaims struct {
	TokenType string `json:"token_type"`
	jwt.RegisteredClaims
}

func GenerateUserAccessToken(secret []byte, userID uint, userTokenID uint, expiresAt time.Time) (string, error) {
	return generateAccessToken(secret, TokenTypeUser, strconv.FormatUint(uint64(userID), 10), strconv.FormatUint(uint64(userTokenID), 10), expiresAt)
}

func NewUserAccessToken(secret []byte, userID uint, userTokenID uint, expiresAt time.Time) (string, error) {
	return GenerateUserAccessToken(secret, userID, userTokenID, expiresAt)
}

func GenerateNodeAccessToken(secret []byte, nodeID string, expiresAt time.Time) (string, error) {
	return generateAccessToken(secret, TokenTypeNode, nodeID, "", expiresAt)
}

func ParseUserAccessToken(secret []byte, tokenString string) (*UserAccessClaims, error) {
	claims, err := parseAccessToken(secret, tokenString, TokenTypeUser)
	if err != nil {
		return nil, err
	}

	userClaims := &UserAccessClaims{
		TokenType:        claims.TokenType,
		RegisteredClaims: claims.RegisteredClaims,
	}
	if userClaims.Subject == "" || userClaims.ID == "" {
		return nil, errors.New("invalid token claims")
	}
	return userClaims, nil
}

func ParseNodeAccessToken(secret []byte, tokenString string) (*NodeAccessClaims, error) {
	claims, err := parseAccessToken(secret, tokenString, TokenTypeNode)
	if err != nil {
		return nil, err
	}

	nodeClaims := &NodeAccessClaims{
		TokenType:        claims.TokenType,
		RegisteredClaims: claims.RegisteredClaims,
	}
	if nodeClaims.Subject == "" {
		return nil, errors.New("invalid token claims")
	}
	return nodeClaims, nil
}

func generateAccessToken(secret []byte, tokenType, subject, id string, expiresAt time.Time) (string, error) {
	claims := jwt.RegisteredClaims{
		Subject:   subject,
		ID:        id,
		ExpiresAt: jwt.NewNumericDate(expiresAt),
		IssuedAt:  jwt.NewNumericDate(time.Now().UTC()),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"token_type": tokenType,
		"sub":        claims.Subject,
		"jti":        claims.ID,
		"exp":        claims.ExpiresAt.Unix(),
		"iat":        claims.IssuedAt.Unix(),
	})
	return token.SignedString(secret)
}

func parseAccessToken(secret []byte, tokenString, expectedType string) (*UserAccessClaims, error) {
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
	if claims.TokenType != expectedType {
		return nil, errors.New("invalid token claims")
	}
	return claims, nil
}
