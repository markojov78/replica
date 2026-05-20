package security

import (
	"testing"
	"time"
)

func TestUserAccessTokenRoundTrip(t *testing.T) {
	secret := []byte("test-secret")
	expiresAt := time.Now().UTC().Add(5 * time.Minute)

	token, err := GenerateUserAccessToken(secret, 42, 9, expiresAt)
	if err != nil {
		t.Fatalf("GenerateUserAccessToken() error = %v", err)
	}

	claims, err := ParseUserAccessToken(secret, token)
	if err != nil {
		t.Fatalf("ParseUserAccessToken() error = %v", err)
	}

	if claims.Subject != "42" {
		t.Fatalf("claims.Subject = %q, want %q", claims.Subject, "42")
	}
	if claims.ID != "9" {
		t.Fatalf("claims.ID = %q, want %q", claims.ID, "9")
	}
	if claims.TokenType != "user" {
		t.Fatalf("claims.TokenType = %q, want %q", claims.TokenType, "user")
	}
}

func TestNodeAccessTokenRoundTrip(t *testing.T) {
	secret := []byte("test-secret")
	expiresAt := time.Now().UTC().Add(5 * time.Minute)

	token, err := GenerateNodeAccessToken(secret, "node-1", expiresAt)
	if err != nil {
		t.Fatalf("GenerateNodeAccessToken() error = %v", err)
	}

	claims, err := ParseNodeAccessToken(secret, token)
	if err != nil {
		t.Fatalf("ParseNodeAccessToken() error = %v", err)
	}

	if claims.Subject != "node-1" {
		t.Fatalf("claims.Subject = %q, want %q", claims.Subject, "node-1")
	}
	if claims.TokenType != "node" {
		t.Fatalf("claims.TokenType = %q, want %q", claims.TokenType, "node")
	}
}

func TestUserAndNodeTokensDoNotCrossParse(t *testing.T) {
	secret := []byte("test-secret")
	expiresAt := time.Now().UTC().Add(5 * time.Minute)

	userToken, err := GenerateUserAccessToken(secret, 42, 9, expiresAt)
	if err != nil {
		t.Fatalf("GenerateUserAccessToken() error = %v", err)
	}
	nodeToken, err := GenerateNodeAccessToken(secret, "node-1", expiresAt)
	if err != nil {
		t.Fatalf("GenerateNodeAccessToken() error = %v", err)
	}

	if _, err := ParseNodeAccessToken(secret, userToken); err == nil {
		t.Fatal("ParseNodeAccessToken(userToken) error = nil, want error")
	}
	if _, err := ParseUserAccessToken(secret, nodeToken); err == nil {
		t.Fatal("ParseUserAccessToken(nodeToken) error = nil, want error")
	}
}
