package security

import (
	"testing"
	"time"
)

func TestUserAccessTokenRoundTrip(t *testing.T) {
	secret := []byte("test-secret")
	expiresAt := time.Now().UTC().Add(5 * time.Minute)

	token, err := NewUserAccessToken(secret, 42, 9, expiresAt)
	if err != nil {
		t.Fatalf("NewUserAccessToken() error = %v", err)
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
