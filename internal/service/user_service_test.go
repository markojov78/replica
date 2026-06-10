package service

import (
	"encoding/json"
	"strings"
	"testing"

	"replica/internal/model"
)

func TestUserDetailsResponsesOmitPassword(t *testing.T) {
	details := toUserDetails(&model.User{
		ID:       1,
		Name:     "jsmith",
		Password: "hashed-password",
		Status:   model.UserStatusActive,
	})

	for name, response := range map[string]any{
		"single": details,
		"list":   UserList{Items: []UserDetails{*details}},
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(response)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			if strings.Contains(string(encoded), "password") || strings.Contains(string(encoded), "hashed-password") {
				t.Fatalf("response contains password: %s", encoded)
			}
		})
	}
}
