package service

import (
	"errors"
	"strconv"
	"time"

	"replica/internal/model"
	"replica/internal/repository"
	"replica/internal/security"

	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"
)

var (
	ErrInvalidCredentials     = errors.New("invalid username or password")
	ErrInvalidNodeCredentials = errors.New("invalid node credentials")
	ErrInactiveUser           = errors.New("inactive user")
	ErrDisabledNode           = errors.New("disabled node")
	ErrRevokedNode            = errors.New("revoked node")
	ErrInvalidToken           = errors.New("invalid token")
	ErrExpiredToken           = errors.New("expired token")
	ErrRevokedToken           = errors.New("revoked token")
	ErrForbidden              = errors.New("forbidden")
)

type TokenPair struct {
	UserID                uint
	AccessToken           string
	RefreshToken          string
	AccessTokenExpiresAt  time.Time
	RefreshTokenExpiresAt time.Time
}

type NodeTokenPair struct {
	NodeID                 string
	AccessToken            string
	RefreshToken           string
	AccessTokenExpiresAt   time.Time
	RefreshTokenExpiresAt  time.Time
	TransferTokenPublicKey string
}

type AuthenticatedUser struct {
	ID       uint
	Username string
	Status   string
	Roles    []RoleDetails
}

type AuthenticatedNode struct {
	ID             string
	Status         string
	PublicKey      string
	SharingEnabled bool
}

type ValidatedUserToken struct {
	UserID        uint
	Username      string
	Status        string
	AccessExpires time.Time
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
	userTokens      *repository.UserTokenRepository
	nodes           *repository.NodeRepository
	nodeTokens      *repository.NodeTokenRepository
	settings        *SettingService
	jwtSecret       []byte
	accessTokenTTL  time.Duration
	refreshTokenTTL time.Duration
}

func NewAuthService(
	users *repository.UserRepository,
	userTokens *repository.UserTokenRepository,
	nodes *repository.NodeRepository,
	nodeTokens *repository.NodeTokenRepository,
	jwtSecret string,
	accessTokenTTL, refreshTokenTTL time.Duration,
	settingServices ...*SettingService,
) *AuthService {
	var settings *SettingService
	if len(settingServices) > 0 {
		settings = settingServices[0]
	}

	return &AuthService{
		users:           users,
		userTokens:      userTokens,
		nodes:           nodes,
		nodeTokens:      nodeTokens,
		settings:        settings,
		jwtSecret:       []byte(jwtSecret),
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
	userToken, err := s.userTokens.FindByRefreshHash(security.HashOpaqueToken(refreshToken))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidToken
		}
		return nil, err
	}

	now := time.Now().UTC()
	if userToken.RefreshExpiration.Before(now) {
		return nil, ErrExpiredToken
	}

	user, err := s.users.FindByID(userToken.UserID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidToken
		}
		return nil, err
	}
	if user.Status != model.UserStatusActive {
		return nil, ErrInactiveUser
	}

	if err := s.userTokens.DeleteByID(userToken.ID); err != nil {
		return nil, err
	}

	return s.issueTokenPair(userToken.UserID)
}

func (s *AuthService) NodeLogin(nodeID, secret string, publicKey string) (*NodeTokenPair, error) {
	node, err := s.nodes.FindByID(nodeID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidNodeCredentials
		}
		return nil, err
	}

	if err := validateNodeAuthStatus(node.Status); err != nil {
		return nil, err
	}

	if err := security.CheckPassword(node.Secret, secret); err != nil {
		return nil, ErrInvalidNodeCredentials
	}

	node.PublicKey = publicKey
	if err := s.nodes.Update(node); err != nil {
		return nil, err
	}

	return s.issueNodeTokenPair(node.ID)
}

func (s *AuthService) NodeRefresh(refreshToken string) (*NodeTokenPair, error) {
	nodeToken, err := s.nodeTokens.FindByRefreshHash(security.HashOpaqueToken(refreshToken))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidToken
		}
		return nil, err
	}

	if nodeToken.RevokedAt != nil {
		return nil, ErrRevokedToken
	}

	now := time.Now().UTC()
	if nodeToken.RefreshExpiration.Before(now) {
		return nil, ErrExpiredToken
	}

	if err := validateNodeAuthStatus(nodeToken.Node.Status); err != nil {
		return nil, err
	}

	if err := s.nodeTokens.DeleteByNodeID(nodeToken.NodeID); err != nil {
		return nil, err
	}

	return s.issueNodeTokenPair(nodeToken.NodeID)
}

func (s *AuthService) Logout(accessToken string) error {
	claims, err := s.parseAccessToken(accessToken)
	if err != nil {
		return err
	}

	userTokenID, err := parseUintClaim(claims.ID)
	if err != nil {
		return ErrInvalidToken
	}

	return s.userTokens.DeleteByID(userTokenID)
}

func (s *AuthService) Me(accessToken string) (*AuthenticatedUser, error) {
	claims, err := s.parseAccessToken(accessToken)
	if err != nil {
		return nil, err
	}

	userID, err := parseUintClaim(claims.Subject)
	if err != nil {
		return nil, ErrInvalidToken
	}

	user, err := s.users.FindByID(userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidToken
		}
		return nil, err
	}

	return &AuthenticatedUser{
		ID:       user.ID,
		Username: user.Name,
		Status:   string(user.Status),
		Roles:    mapRoles(user.Roles),
	}, nil
}

func (s *AuthService) ValidateUserAccessToken(accessToken string) (*ValidatedUserToken, error) {
	claims, err := s.parseAccessToken(accessToken)
	if err != nil {
		return nil, err
	}

	userID, err := parseUintClaim(claims.Subject)
	if err != nil {
		return nil, ErrInvalidToken
	}
	if _, err := parseUintClaim(claims.ID); err != nil {
		return nil, ErrInvalidToken
	}
	if claims.ExpiresAt == nil {
		return nil, ErrInvalidToken
	}

	user, err := s.users.FindByID(userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidToken
		}
		return nil, err
	}
	if user.Status != model.UserStatusActive {
		return nil, ErrInactiveUser
	}

	return &ValidatedUserToken{
		UserID:        user.ID,
		Username:      user.Name,
		Status:        string(user.Status),
		AccessExpires: claims.ExpiresAt.Time,
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

	return user, ErrForbidden
}

func (s *AuthService) AuthorizeInventoryUser(userID uint, inventoryID uint, action model.PermissionAction) (bool, error) {
	permissions, err := s.users.GetInventoryPermissions(userID, inventoryID)

	if err != nil {
		return false, err
	}

	for _, permission := range permissions {
		if permission.Permission == string(action) {
			return true, nil
		}
	}

	return false, ErrForbidden
}

func (s *AuthService) AuthorizeShareUser(userID uint, shareID uint, action model.PermissionAction) (bool, error) {
	permissions, err := s.users.GetSharePermissions(userID, shareID)

	if err != nil {
		return false, err
	}

	for _, permission := range permissions {
		if permission.Permission == string(action) {
			return true, nil
		}
	}

	return false, ErrForbidden
}

func (s *AuthService) Node(accessToken string) (*AuthenticatedNode, error) {
	claims, err := s.parseNodeAccessToken(accessToken)
	if err != nil {
		return nil, err
	}

	node, err := s.nodes.FindByID(claims.Subject)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidToken
		}
		return nil, err
	}

	if err := validateNodeAuthStatus(node.Status); err != nil {
		return nil, err
	}

	return &AuthenticatedNode{
		ID:             node.ID,
		Status:         string(node.Status),
		PublicKey:      node.PublicKey,
		SharingEnabled: node.Sharing,
	}, nil
}

func (s *AuthService) issueTokenPair(userID uint) (*TokenPair, error) {
	refreshToken, err := security.NewOpaqueToken()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	pair := &TokenPair{
		UserID:                userID,
		RefreshToken:          refreshToken,
		AccessTokenExpiresAt:  now.Add(s.accessTokenTTL),
		RefreshTokenExpiresAt: now.Add(s.refreshTokenTTL),
	}

	userToken := &model.UserToken{
		UserID:            userID,
		RefreshHash:       security.HashOpaqueToken(pair.RefreshToken),
		RefreshExpiration: pair.RefreshTokenExpiresAt,
	}

	if err := s.userTokens.Create(userToken); err != nil {
		return nil, err
	}

	pair.AccessToken, err = security.NewUserAccessToken(s.jwtSecret, userID, userToken.ID, pair.AccessTokenExpiresAt)
	if err != nil {
		return nil, err
	}

	return pair, nil
}

func (s *AuthService) issueNodeTokenPair(nodeID string) (*NodeTokenPair, error) {
	refreshToken, err := security.NewOpaqueToken()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	pair := &NodeTokenPair{
		NodeID:                nodeID,
		RefreshToken:          refreshToken,
		AccessTokenExpiresAt:  now.Add(s.accessTokenTTL),
		RefreshTokenExpiresAt: now.Add(s.refreshTokenTTL),
	}

	nodeToken := &model.NodeToken{
		NodeID:            nodeID,
		RefreshHash:       security.HashOpaqueToken(pair.RefreshToken),
		RefreshExpiration: pair.RefreshTokenExpiresAt,
		RevokedAt:         nil,
	}

	if err := s.nodeTokens.Save(nodeToken); err != nil {
		return nil, err
	}

	pair.AccessToken, err = security.GenerateNodeAccessToken(s.jwtSecret, nodeID, pair.AccessTokenExpiresAt)
	if err != nil {
		return nil, err
	}

	if s.settings != nil {
		pair.TransferTokenPublicKey, err = s.settings.TransferPublicKey()
		if err != nil {
			return nil, err
		}
	}

	return pair, nil
}

func (s *AuthService) parseAccessToken(accessToken string) (*security.UserAccessClaims, error) {
	claims, err := security.ParseUserAccessToken(s.jwtSecret, accessToken)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrExpiredToken
		}
		return nil, ErrInvalidToken
	}

	return claims, nil
}

func (s *AuthService) parseNodeAccessToken(accessToken string) (*security.NodeAccessClaims, error) {
	claims, err := security.ParseNodeAccessToken(s.jwtSecret, accessToken)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrExpiredToken
		}
		return nil, ErrInvalidToken
	}

	return claims, nil
}

func parseUintClaim(value string) (uint, error) {
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, err
	}
	return uint(parsed), nil
}

func validateNodeAuthStatus(status model.NodeStatus) error {
	switch status {
	case model.NodeStatusDisabled:
		return ErrDisabledNode
	case model.NodeStatusRevoked:
		return ErrRevokedNode
	default:
		return nil
	}
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
