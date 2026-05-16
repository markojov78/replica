package service

import (
	"errors"

	"dropoutbox/internal/model"
	"dropoutbox/internal/repository"

	"gorm.io/gorm"
)

var (
	ErrInvalidRoleStatus  = errors.New("invalid role status")
	ErrInvalidPermissions = errors.New("invalid permissions")
)

type RoleList struct {
	Items []RoleDetails `json:"items"`
	Page  int           `json:"page"`
	Count int           `json:"count"`
	Total int64         `json:"total"`
}

type RolePermissionInput struct {
	Resource string `json:"resource"`
	Action   string `json:"action"`
}

type UpdateRoleInput struct {
	Name        *string
	Description *string
	Status      *string
	Permissions *[]RolePermissionInput
}

type RoleService struct {
	roles *repository.RoleRepository
}

func NewRoleService(roles *repository.RoleRepository) *RoleService {
	return &RoleService{roles: roles}
}

func (s *RoleService) Create(name, description string, permissions []RolePermissionInput) (*RoleDetails, error) {
	modelPermissions, err := validatePermissions(permissions)
	if err != nil {
		return nil, err
	}

	role := &model.Role{
		Name:        name,
		Description: description,
		Status:      model.RoleStatusActive,
	}

	if err := s.roles.Create(role); err != nil {
		return nil, err
	}

	for i := range modelPermissions {
		modelPermissions[i].RoleID = role.ID
	}
	if err := s.roles.ReplacePermissions(role.ID, modelPermissions); err != nil {
		return nil, err
	}

	role, err = s.roles.FindByID(role.ID)
	if err != nil {
		return nil, err
	}

	result := mapRoles([]model.Role{*role})
	return &result[0], nil
}

func (s *RoleService) Get(id uint) (*RoleDetails, error) {
	role, err := s.roles.FindByID(id)
	if err != nil {
		return nil, err
	}

	result := mapRoles([]model.Role{*role})
	return &result[0], nil
}

func (s *RoleService) List(page, perPage int) (*RoleList, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 20
	}
	if perPage > 100 {
		perPage = 100
	}

	roles, total, err := s.roles.List(page, perPage)
	if err != nil {
		return nil, err
	}

	return &RoleList{
		Items: mapRoles(roles),
		Page:  page,
		Count: perPage,
		Total: total,
	}, nil
}

func (s *RoleService) Update(id uint, input UpdateRoleInput) (*RoleDetails, error) {
	role, err := s.roles.FindByID(id)
	if err != nil {
		return nil, err
	}

	if input.Name != nil {
		role.Name = *input.Name
	}
	if input.Description != nil {
		role.Description = *input.Description
	}
	if input.Status != nil {
		status := model.RoleStatus(*input.Status)
		if !status.Valid() {
			return nil, ErrInvalidRoleStatus
		}
		role.Status = status
	}

	if err := s.roles.Update(role); err != nil {
		return nil, err
	}

	if input.Permissions != nil {
		modelPermissions, err := validatePermissions(*input.Permissions)
		if err != nil {
			return nil, err
		}
		for i := range modelPermissions {
			modelPermissions[i].RoleID = role.ID
		}
		if err := s.roles.ReplacePermissions(role.ID, modelPermissions); err != nil {
			return nil, err
		}
	}

	role, err = s.roles.FindByID(role.ID)
	if err != nil {
		return nil, err
	}

	result := mapRoles([]model.Role{*role})
	return &result[0], nil
}

func (s *RoleService) Delete(id uint) (*RoleDetails, error) {
	return s.Update(id, UpdateRoleInput{
		Status: stringPtr(string(model.RoleStatusDeleted)),
	})
}

func (s *RoleService) IsNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

func validatePermissions(inputs []RolePermissionInput) ([]model.Permission, error) {
	result := make([]model.Permission, 0, len(inputs))
	seen := map[string]struct{}{}

	for _, input := range inputs {
		resource := model.PermissionResource(input.Resource)
		action := model.PermissionAction(input.Action)
		if !resource.Valid() || !action.Valid() {
			return nil, ErrInvalidPermissions
		}

		key := input.Resource + ":" + input.Action
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		result = append(result, model.Permission{
			Resource: resource,
			Action:   action,
		})
	}

	return result, nil
}
