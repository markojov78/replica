package service

import (
	"errors"
	"fmt"
	"strings"

	"replica/internal/model"
	"replica/internal/repository"
	"replica/internal/security"

	"gorm.io/gorm"
)

var (
	ErrInvalidUserStatus = errors.New("invalid user status")
	ErrInvalidRoles      = errors.New("invalid roles")
)

type UserDetails struct {
	ID       uint          `json:"id"`
	Username string        `json:"username"`
	Name     string        `json:"name"`
	Status   string        `json:"status"`
	Roles    []RoleDetails `json:"roles"`
}

type UserList struct {
	Items []UserDetails `json:"items"`
	Page  int           `json:"page"`
	Count int           `json:"count"`
	Total int64         `json:"total"`
}

type UpdateUserInput struct {
	Username      *string
	Name          *string
	Password      *string
	Status        *string
	RoleIDs       *[]uint
	AddRoleIDs    *[]uint
	RemoveRoleIDs *[]uint
}

type UserService struct {
	users      *repository.UserRepository
	roles      *repository.RoleRepository
	userTokens *repository.UserTokenRepository
	logs       *UserLogService
}

func NewUserService(users *repository.UserRepository, roles *repository.RoleRepository, userTokens ...*repository.UserTokenRepository) *UserService {
	s := &UserService{users: users, roles: roles}
	if len(userTokens) > 0 {
		s.userTokens = userTokens[0]
	}
	return s
}

// WithUserLogs enables user log rows for user create, update and delete.
func (s *UserService) WithUserLogs(logs *UserLogService) *UserService {
	s.logs = logs
	return s
}

type userRepos struct {
	users      *repository.UserRepository
	roles      *repository.RoleRepository
	userTokens *repository.UserTokenRepository
}

func (s *UserService) transaction(fn func(repos userRepos, logs *UserLogWriter) error) error {
	return s.logs.Transaction(func(tx *gorm.DB, logs *UserLogWriter) error {
		repos := userRepos{users: s.users, roles: s.roles, userTokens: s.userTokens}
		if tx != nil {
			repos.users = s.users.WithTx(tx)
			repos.roles = s.roles.WithTx(tx)
			if s.userTokens != nil {
				repos.userTokens = s.userTokens.WithTx(tx)
			}
		}
		return fn(repos, logs)
	})
}

func (s *UserService) Create(username, name, password string, roleIDs []uint, audit AuditContext) (*UserDetails, error) {
	hashedPassword, err := security.HashPassword(password)
	if err != nil {
		return nil, err
	}

	user := &model.User{
		Username: username,
		Name:     name,
		Password: hashedPassword,
		Status:   model.UserStatusActive,
	}

	err = s.transaction(func(repos userRepos, logs *UserLogWriter) error {
		if err := repos.users.Create(user); err != nil {
			return err
		}
		if err := replaceUserRoles(repos, user.ID, roleIDs); err != nil {
			return err
		}
		if err := logs.Write(UserLogEntry{
			Action:  model.UserLogActionCreate,
			Success: true,
			Message: fmt.Sprintf("created user username=%s name=%s", username, name),
			UserID:  uintPtr(user.ID),
			Audit:   audit,
		}); err != nil {
			return err
		}
		created, err := repos.users.FindByID(user.ID)
		if err != nil {
			return err
		}
		user = created
		return writeUserRoleChanges(logs, user.ID, nil, user.Roles, audit)
	})
	if err != nil {
		return nil, s.logs.Failure(UserLogEntry{
			Action:  model.UserLogActionCreate,
			Message: fmt.Sprintf("create user username=%s failed: %v", username, err),
			Audit:   audit,
		}, err)
	}

	return toUserDetails(user), nil
}

func (s *UserService) Get(id uint) (*UserDetails, error) {
	user, err := s.users.FindByID(id)
	if err != nil {
		return nil, err
	}

	return toUserDetails(user), nil
}

func (s *UserService) List(page, perPage int) (*UserList, error) {
	users, total, err := s.users.List(page, perPage)
	if err != nil {
		return nil, err
	}

	items := make([]UserDetails, 0, len(users))
	for _, user := range users {
		items = append(items, *toUserDetails(&user))
	}

	return &UserList{
		Items: items,
		Page:  page,
		Count: perPage,
		Total: total,
	}, nil
}

func (s *UserService) Update(id uint, input UpdateUserInput, audit AuditContext) (*UserDetails, error) {
	return s.update(id, input, audit, model.UserLogActionUpdate)
}

func (s *UserService) Delete(id uint, audit AuditContext) (*UserDetails, error) {
	return s.update(id, UpdateUserInput{
		Status: stringPtr(string(model.UserStatusDeleted)),
	}, audit, model.UserLogActionDelete)
}

// LogPermissionDenied records a user management request rejected for missing permission.
func (s *UserService) LogPermissionDenied(action model.UserLogAction, userID *uint, audit AuditContext) error {
	return s.logs.Write(UserLogEntry{Action: action, Message: "permission denied", UserID: userID, Audit: audit})
}

func (s *UserService) update(id uint, input UpdateUserInput, audit AuditContext, action model.UserLogAction) (*UserDetails, error) {
	var user *model.User
	err := s.transaction(func(repos userRepos, logs *UserLogWriter) error {
		var err error
		user, err = repos.users.FindByID(id)
		if err != nil {
			return err
		}
		previous := *user
		previousRoles := append([]model.Role(nil), user.Roles...)

		revokeTokens := input.Password != nil
		changes := []string{}

		if input.Username != nil {
			user.Username = *input.Username
		}
		if input.Name != nil {
			user.Name = *input.Name
		}

		if input.Password != nil {
			hashedPassword, err := security.HashPassword(*input.Password)
			if err != nil {
				return err
			}
			user.Password = hashedPassword
			changes = append(changes, "password changed")
		}

		if input.Status != nil {
			status := model.UserStatus(*input.Status)
			if !status.Valid() {
				return ErrInvalidUserStatus
			}
			if status != model.UserStatusActive {
				revokeTokens = true
			}
			user.Status = status
		}

		if err := repos.users.Update(user); err != nil {
			return err
		}

		var revoked int64
		if revokeTokens && repos.userTokens != nil {
			revoked, err = repos.userTokens.DeleteByUserID(user.ID)
			if err != nil {
				return err
			}
		}

		if err := applyUserRoleUpdate(repos, user, input); err != nil {
			return err
		}

		user, err = repos.users.FindByID(user.ID)
		if err != nil {
			return err
		}

		changes = append(userFieldChanges(&previous, user), changes...)
		if action == model.UserLogActionDelete || len(changes) > 0 {
			message := "updated user"
			if action == model.UserLogActionDelete {
				message = "deleted user"
			}
			if len(changes) > 0 {
				message += ": " + strings.Join(changes, "; ")
			}
			if err := logs.Write(UserLogEntry{Action: action, Success: true, Message: message, UserID: uintPtr(user.ID), Audit: audit}); err != nil {
				return err
			}
		}
		if revoked > 0 {
			if err := logs.Write(UserLogEntry{
				Action:  model.UserLogActionTokenRevoke,
				Success: true,
				Message: fmt.Sprintf("revoked %d refresh tokens", revoked),
				UserID:  uintPtr(user.ID),
				Audit:   audit,
			}); err != nil {
				return err
			}
		}
		return writeUserRoleChanges(logs, user.ID, previousRoles, user.Roles, audit)
	})
	if err != nil {
		verb := "update"
		if action == model.UserLogActionDelete {
			verb = "delete"
		}
		return nil, s.logs.Failure(UserLogEntry{
			Action:  action,
			Message: fmt.Sprintf("%s user %d failed: %v", verb, id, err),
			UserID:  uintPtr(id),
			Audit:   audit,
		}, err)
	}

	return toUserDetails(user), nil
}

func userFieldChanges(previous, current *model.User) []string {
	changes := []string{}
	if previous.Username != current.Username {
		changes = append(changes, fmt.Sprintf("username: %s -> %s", previous.Username, current.Username))
	}
	if previous.Name != current.Name {
		changes = append(changes, fmt.Sprintf("name: %s -> %s", previous.Name, current.Name))
	}
	if previous.Status != current.Status {
		changes = append(changes, fmt.Sprintf("status: %s -> %s", previous.Status, current.Status))
	}
	return changes
}

// writeUserRoleChanges writes one update row per role added to or removed from a user.
func writeUserRoleChanges(logs *UserLogWriter, userID uint, previous, current []model.Role, audit AuditContext) error {
	previousIDs := map[uint]struct{}{}
	for _, role := range previous {
		previousIDs[role.ID] = struct{}{}
	}
	currentIDs := map[uint]struct{}{}
	for _, role := range current {
		currentIDs[role.ID] = struct{}{}
	}

	for _, role := range current {
		if _, ok := previousIDs[role.ID]; ok {
			continue
		}
		if err := logs.Write(UserLogEntry{
			Action:  model.UserLogActionUpdate,
			Success: true,
			Message: fmt.Sprintf("role added: %s", role.Name),
			UserID:  uintPtr(userID),
			RoleID:  uintPtr(role.ID),
			Audit:   audit,
		}); err != nil {
			return err
		}
	}
	for _, role := range previous {
		if _, ok := currentIDs[role.ID]; ok {
			continue
		}
		if err := logs.Write(UserLogEntry{
			Action:  model.UserLogActionUpdate,
			Success: true,
			Message: fmt.Sprintf("role removed: %s", role.Name),
			UserID:  uintPtr(userID),
			RoleID:  uintPtr(role.ID),
			Audit:   audit,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *UserService) IsNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

func toUserDetails(user *model.User) *UserDetails {
	roles := make([]RoleDetails, 0, len(user.Roles))
	for _, role := range user.Roles {
		roles = append(roles, RoleDetails{
			ID:          role.ID,
			Name:        role.Name,
			Description: role.Description,
			Status:      string(role.Status),
			Permissions: mapPermissions(role.Permissions),
		})
	}

	return &UserDetails{
		ID:       user.ID,
		Username: user.Username,
		Name:     user.Name,
		Status:   string(user.Status),
		Roles:    roles,
	}
}

func stringPtr(value string) *string {
	return &value
}

func replaceUserRoles(repos userRepos, userID uint, roleIDs []uint) error {
	roles, err := repos.roles.FindByIDs(roleIDs)
	if err != nil {
		return err
	}
	if len(roles) != len(roleIDs) {
		return ErrInvalidRoles
	}

	return repos.users.SetRoles(userID, dedupeUint(roleIDs))
}

func applyUserRoleUpdate(repos userRepos, user *model.User, input UpdateUserInput) error {
	if input.RoleIDs != nil && (input.AddRoleIDs != nil || input.RemoveRoleIDs != nil) {
		return ErrInvalidRoles
	}

	if input.RoleIDs != nil {
		return replaceUserRoles(repos, user.ID, *input.RoleIDs)
	}

	if input.AddRoleIDs == nil && input.RemoveRoleIDs == nil {
		return nil
	}

	current := make([]uint, 0, len(user.Roles))
	currentSet := map[uint]struct{}{}
	for _, role := range user.Roles {
		current = append(current, role.ID)
		currentSet[role.ID] = struct{}{}
	}

	if input.AddRoleIDs != nil {
		for _, roleID := range *input.AddRoleIDs {
			currentSet[roleID] = struct{}{}
		}
	}
	if input.RemoveRoleIDs != nil {
		for _, roleID := range *input.RemoveRoleIDs {
			delete(currentSet, roleID)
		}
	}

	merged := make([]uint, 0, len(currentSet))
	for roleID := range currentSet {
		merged = append(merged, roleID)
	}

	return replaceUserRoles(repos, user.ID, merged)
}

func mapPermissions(permissions []model.Permission) []PermissionDetail {
	result := make([]PermissionDetail, 0, len(permissions))
	for _, permission := range permissions {
		result = append(result, PermissionDetail{
			ID:       permission.ID,
			Resource: string(permission.Resource),
			Actions:  string(permission.Action),
		})
	}
	return result
}

func dedupeUint(values []uint) []uint {
	seen := map[uint]struct{}{}
	result := make([]uint, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
