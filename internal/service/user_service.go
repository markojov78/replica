package service

import (
	"errors"

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
	ID     uint          `json:"id"`
	Name   string        `json:"name"`
	Status string        `json:"status"`
	Roles  []RoleDetails `json:"roles"`
}

type UserList struct {
	Items []UserDetails `json:"items"`
	Page  int           `json:"page"`
	Count int           `json:"count"`
	Total int64         `json:"total"`
}

type UpdateUserInput struct {
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
}

func NewUserService(users *repository.UserRepository, roles *repository.RoleRepository, userTokens ...*repository.UserTokenRepository) *UserService {
	s := &UserService{users: users, roles: roles}
	if len(userTokens) > 0 {
		s.userTokens = userTokens[0]
	}
	return s
}

func (s *UserService) Create(name, password string, roleIDs []uint) (*UserDetails, error) {
	hashedPassword, err := security.HashPassword(password)
	if err != nil {
		return nil, err
	}

	user := &model.User{
		Name:     name,
		Password: hashedPassword,
		Status:   model.UserStatusActive,
	}

	if err := s.users.Create(user); err != nil {
		return nil, err
	}

	if err := s.replaceRoles(user.ID, roleIDs); err != nil {
		return nil, err
	}

	user, err = s.users.FindByID(user.ID)
	if err != nil {
		return nil, err
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
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 20
	}
	if perPage > 100 {
		perPage = 100
	}

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

func (s *UserService) Update(id uint, input UpdateUserInput) (*UserDetails, error) {
	user, err := s.users.FindByID(id)
	if err != nil {
		return nil, err
	}

	revokeTokens := input.Password != nil

	if input.Name != nil {
		user.Name = *input.Name
	}

	if input.Password != nil {
		hashedPassword, err := security.HashPassword(*input.Password)
		if err != nil {
			return nil, err
		}
		user.Password = hashedPassword
	}

	if input.Status != nil {
		status := model.UserStatus(*input.Status)
		if !status.Valid() {
			return nil, ErrInvalidUserStatus
		}
		if status != model.UserStatusActive {
			revokeTokens = true
		}
		user.Status = status
	}

	if err := s.users.Update(user); err != nil {
		return nil, err
	}

	if revokeTokens && s.userTokens != nil {
		if err := s.userTokens.DeleteByUserID(user.ID); err != nil {
			return nil, err
		}
	}

	if err := s.applyRoleUpdate(user, input); err != nil {
		return nil, err
	}

	user, err = s.users.FindByID(user.ID)
	if err != nil {
		return nil, err
	}

	return toUserDetails(user), nil
}

func (s *UserService) Delete(id uint) (*UserDetails, error) {
	return s.Update(id, UpdateUserInput{
		Status: stringPtr(string(model.UserStatusDeleted)),
	})
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
		ID:     user.ID,
		Name:   user.Name,
		Status: string(user.Status),
		Roles:  roles,
	}
}

func stringPtr(value string) *string {
	return &value
}

func (s *UserService) replaceRoles(userID uint, roleIDs []uint) error {
	roles, err := s.roles.FindByIDs(roleIDs)
	if err != nil {
		return err
	}
	if len(roles) != len(roleIDs) {
		return ErrInvalidRoles
	}

	return s.users.SetRoles(userID, dedupeUint(roleIDs))
}

func (s *UserService) applyRoleUpdate(user *model.User, input UpdateUserInput) error {
	if input.RoleIDs != nil && (input.AddRoleIDs != nil || input.RemoveRoleIDs != nil) {
		return ErrInvalidRoles
	}

	if input.RoleIDs != nil {
		return s.replaceRoles(user.ID, *input.RoleIDs)
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

	return s.replaceRoles(user.ID, merged)
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
