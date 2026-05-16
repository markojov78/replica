package service

import (
	"errors"
	"time"

	"dropoutbox/internal/model"
	"dropoutbox/internal/repository"
	"dropoutbox/internal/security"

	"gorm.io/gorm"
)

var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrInactiveUser       = errors.New("inactive user")
	ErrInvalidToken       = errors.New("invalid token")
	ErrExpiredToken       = errors.New("expired token")
	ErrForbidden          = errors.New("forbidden")
)

type TokenPair struct {
	UserID                uint
	AccessToken           string
	RefreshToken          string
	AccessTokenExpiresAt  time.Time
	RefreshTokenExpiresAt time.Time
}

type AuthenticatedUser struct {
	ID       uint
	Username string
	Status   string
	Roles    []RoleDetails
}

type RoleDetails struct {
	ID          uint               `json:"id"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Status      string             `json:"status"`
	Permissions []PermissionDetail `json:"permissions"`
}

type PermissionDetail struct {
	ID       uint   `json:"id"`
	Resource string `json:"resource"`
	Actions  string `json:"actions"`
}

type AuthService struct {
	users           *repository.UserRepository
	tokens          *repository.TokenRepository
	accessTokenTTL  time.Duration
	refreshTokenTTL time.Duration
}

func NewAuthService(users *repository.UserRepository, tokens *repository.TokenRepository, accessTokenTTL, refreshTokenTTL time.Duration) *AuthService {
	return &AuthService{
		users:           users,
		tokens:          tokens,
		accessTokenTTL:  accessTokenTTL,
		refreshTokenTTL: refreshTokenTTL,
	}
}

func (s *AuthService) Login(username, password string) (*TokenPair, error) {
	user, err := s.users.FindByName(username)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}

	if user.Status != model.UserStatusActive {
		return nil, ErrInactiveUser
	}

	if err := security.CheckPassword(user.Password, password); err != nil {
		return nil, ErrInvalidCredentials
	}

	return s.issueTokenPair(user.ID)
}

func (s *AuthService) Refresh(refreshToken string) (*TokenPair, error) {
	token, err := s.tokens.FindByRefresh(refreshToken)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidToken
		}
		return nil, err
	}

	now := time.Now().UTC()
	if token.RefreshExpiration.Before(now) {
		return nil, ErrExpiredToken
	}

	if err := s.tokens.DeleteByID(token.ID); err != nil {
		return nil, err
	}

	return s.issueTokenPair(token.UserID)
}

func (s *AuthService) Logout(accessToken string) error {
	token, err := s.tokens.FindByAccess(accessToken)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrInvalidToken
		}
		return err
	}
	return s.tokens.DeleteByID(token.ID)
}

func (s *AuthService) Me(accessToken string) (*AuthenticatedUser, error) {
	token, err := s.validAccessToken(accessToken)
	if err != nil {
		return nil, err
	}

	return &AuthenticatedUser{
		ID:       token.User.ID,
		Username: token.User.Name,
		Status:   string(token.User.Status),
		Roles:    mapRoles(token.User.Roles),
	}, nil
}

func (s *AuthService) Authorize(accessToken string, resource model.PermissionResource, action model.PermissionAction) (*AuthenticatedUser, error) {
	user, err := s.Me(accessToken)
	if err != nil {
		return nil, err
	}

	for _, role := range user.Roles {
		if role.Status != string(model.RoleStatusActive) {
			continue
		}

		for _, permission := range role.Permissions {
			if permission.Resource == string(resource) && permission.Actions == string(action) {
				return user, nil
			}
		}
	}

	return nil, ErrForbidden
}

func (s *AuthService) issueTokenPair(userID uint) (*TokenPair, error) {
	accessToken, err := security.NewOpaqueToken()
	if err != nil {
		return nil, err
	}

	refreshToken, err := security.NewOpaqueToken()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	pair := &TokenPair{
		UserID:                userID,
		AccessToken:           accessToken,
		RefreshToken:          refreshToken,
		AccessTokenExpiresAt:  now.Add(s.accessTokenTTL),
		RefreshTokenExpiresAt: now.Add(s.refreshTokenTTL),
	}

	token := &model.Token{
		UserID:            userID,
		Access:            pair.AccessToken,
		Refresh:           pair.RefreshToken,
		AccessExpiration:  pair.AccessTokenExpiresAt,
		RefreshExpiration: pair.RefreshTokenExpiresAt,
	}

	if err := s.tokens.Create(token); err != nil {
		return nil, err
	}

	return pair, nil
}

func (s *AuthService) validAccessToken(accessToken string) (*model.Token, error) {
	token, err := s.tokens.FindByAccess(accessToken)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidToken
		}
		return nil, err
	}

	if token.AccessExpiration.Before(time.Now().UTC()) {
		return nil, ErrExpiredToken
	}

	return token, nil
}

func mapRoles(roles []model.Role) []RoleDetails {
	result := make([]RoleDetails, 0, len(roles))
	for _, role := range roles {
		roleDetails := RoleDetails{
			ID:          role.ID,
			Name:        role.Name,
			Description: role.Description,
			Status:      string(role.Status),
			Permissions: make([]PermissionDetail, 0, len(role.Permissions)),
		}

		for _, permission := range role.Permissions {
			roleDetails.Permissions = append(roleDetails.Permissions, PermissionDetail{
				ID:       permission.ID,
				Resource: string(permission.Resource),
				Actions:  string(permission.Action),
			})
		}

		result = append(result, roleDetails)
	}

	return result
}
