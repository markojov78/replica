package service

import (
	"errors"
	"fmt"
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
	Name     string
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
	Name          string
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
	logs            *UserLogService
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

// WithUserLogs enables user log rows for user login, token refresh and logout.
func (s *AuthService) WithUserLogs(logs *UserLogService) *AuthService {
	s.logs = logs
	return s
}

func (s *AuthService) Login(username, password string, audit AuditContext) (*TokenPair, error) {
	failure := func(userID *uint, reason string, err error) error {
		return s.logs.Failure(UserLogEntry{
			Action:  model.UserLogActionLogin,
			Message: fmt.Sprintf("failed login for username %s: %s", username, reason),
			UserID:  userID,
			Audit:   audit,
		}, err)
	}

	user, err := s.users.FindByUsername(username)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, failure(nil, "unknown user", ErrInvalidCredentials)
		}
		return nil, err
	}

	if user.Status != model.UserStatusActive {
		return nil, failure(uintPtr(user.ID), "inactive user", ErrInactiveUser)
	}

	if err := security.CheckPassword(user.Password, password); err != nil {
		return nil, failure(uintPtr(user.ID), "bad password", ErrInvalidCredentials)
	}

	var pair *TokenPair
	err = s.logs.Transaction(func(tx *gorm.DB, logs *UserLogWriter) error {
		var err error
		pair, err = s.issueTokenPair(s.tokensWithTx(tx), user.ID)
		if err != nil {
			return err
		}
		audit.ActorUserID = uintPtr(user.ID)
		return logs.Write(UserLogEntry{Action: model.UserLogActionLogin, Success: true, UserID: uintPtr(user.ID), Audit: audit})
	})
	if err != nil {
		return nil, err
	}
	return pair, nil
}

func (s *AuthService) Refresh(refreshToken string, audit AuditContext) (*TokenPair, error) {
	failure := func(action model.UserLogAction, userID *uint, reason string, err error) error {
		return s.logs.Failure(UserLogEntry{Action: action, Message: reason, UserID: userID, Audit: audit}, err)
	}

	userToken, err := s.userTokens.FindByRefreshHash(security.HashOpaqueToken(refreshToken))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, failure(model.UserLogActionTokenRefresh, nil, "invalid token", ErrInvalidToken)
		}
		return nil, err
	}

	now := time.Now().UTC()
	if userToken.RefreshExpiration.Before(now) {
		return nil, failure(model.UserLogActionTokenExpired, uintPtr(userToken.UserID), "expired refresh token", ErrExpiredToken)
	}

	user, err := s.users.FindByID(userToken.UserID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, failure(model.UserLogActionTokenRefresh, uintPtr(userToken.UserID), "invalid token: unknown user", ErrInvalidToken)
		}
		return nil, err
	}
	if user.Status != model.UserStatusActive {
		return nil, failure(model.UserLogActionTokenRefresh, uintPtr(user.ID), "inactive user", ErrInactiveUser)
	}

	var pair *TokenPair
	err = s.logs.Transaction(func(tx *gorm.DB, logs *UserLogWriter) error {
		tokens := s.tokensWithTx(tx)
		if err := tokens.DeleteByID(userToken.ID); err != nil {
			return err
		}
		var err error
		pair, err = s.issueTokenPair(tokens, userToken.UserID)
		if err != nil {
			return err
		}
		audit.ActorUserID = uintPtr(user.ID)
		return logs.Write(UserLogEntry{Action: model.UserLogActionTokenRefresh, Success: true, UserID: uintPtr(user.ID), Audit: audit})
	})
	if err != nil {
		return nil, err
	}
	return pair, nil
}

func (s *AuthService) NodeLogin(nodeID, secret string, publicKey string, version string, commit string, build string) (*NodeTokenPair, error) {
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
	node.Version = &version
	node.Commit = &commit
	node.Build = &build

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

func (s *AuthService) Logout(accessToken string, audit AuditContext) error {
	failure := func(userID *uint, err error) error {
		return s.logs.Failure(UserLogEntry{Action: model.UserLogActionLogout, Message: err.Error(), UserID: userID, Audit: audit}, err)
	}

	claims, err := s.parseAccessToken(accessToken)
	if err != nil {
		return failure(nil, err)
	}

	var userID *uint
	if id, err := parseUintClaim(claims.Subject); err == nil {
		userID = uintPtr(id)
	}

	userTokenID, err := parseUintClaim(claims.ID)
	if err != nil {
		return failure(userID, ErrInvalidToken)
	}

	return s.logs.Transaction(func(tx *gorm.DB, logs *UserLogWriter) error {
		if err := s.tokensWithTx(tx).DeleteByID(userTokenID); err != nil {
			return err
		}
		audit.ActorUserID = userID
		return logs.Write(UserLogEntry{Action: model.UserLogActionLogout, Success: true, UserID: userID, Audit: audit})
	})
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
		Username: user.Username,
		Name:     user.Name,
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
		Username:      user.Username,
		Name:          user.Name,
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

func (s *AuthService) tokensWithTx(tx *gorm.DB) *repository.UserTokenRepository {
	if tx == nil {
		return s.userTokens
	}
	return s.userTokens.WithTx(tx)
}

func (s *AuthService) issueTokenPair(userTokens *repository.UserTokenRepository, userID uint) (*TokenPair, error) {
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

	if err := userTokens.Create(userToken); err != nil {
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
