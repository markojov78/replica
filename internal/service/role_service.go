package service

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"replica/internal/model"
	"replica/internal/repository"

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
	logs  *UserLogService
}

func NewRoleService(roles *repository.RoleRepository) *RoleService {
	return &RoleService{roles: roles}
}

// WithUserLogs enables user log rows for role create, update and delete.
func (s *RoleService) WithUserLogs(logs *UserLogService) *RoleService {
	s.logs = logs
	return s
}

func (s *RoleService) transaction(fn func(roles *repository.RoleRepository, logs *UserLogWriter) error) error {
	return s.logs.Transaction(func(tx *gorm.DB, logs *UserLogWriter) error {
		if tx == nil {
			return fn(s.roles, logs)
		}
		return fn(s.roles.WithTx(tx), logs)
	})
}

func (s *RoleService) Create(name, description string, permissions []RolePermissionInput, audit AuditContext) (*RoleDetails, error) {
	var role *model.Role
	err := s.transaction(func(roles *repository.RoleRepository, logs *UserLogWriter) error {
		modelPermissions, err := validatePermissions(permissions)
		if err != nil {
			return err
		}

		role = &model.Role{
			Name:        name,
			Description: description,
			Status:      model.RoleStatusActive,
		}

		if err := roles.Create(role); err != nil {
			return err
		}

		for i := range modelPermissions {
			modelPermissions[i].RoleID = role.ID
		}
		if err := roles.ReplacePermissions(role.ID, modelPermissions); err != nil {
			return err
		}

		role, err = roles.FindByID(role.ID)
		if err != nil {
			return err
		}
		return logs.Write(UserLogEntry{
			Action:  model.UserLogActionCreate,
			Success: true,
			Message: fmt.Sprintf("created role name=%s permissions=[%s]", role.Name, strings.Join(permissionKeys(role.Permissions), ", ")),
			RoleID:  uintPtr(role.ID),
			Audit:   audit,
		})
	})
	if err != nil {
		return nil, s.logs.Failure(UserLogEntry{
			Action:  model.UserLogActionCreate,
			Message: fmt.Sprintf("create role name=%s failed: %v", name, err),
			Audit:   audit,
		}, err)
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

func (s *RoleService) Update(id uint, input UpdateRoleInput, audit AuditContext) (*RoleDetails, error) {
	return s.update(id, input, audit, model.UserLogActionUpdate)
}

func (s *RoleService) Delete(id uint, audit AuditContext) (*RoleDetails, error) {
	return s.update(id, UpdateRoleInput{
		Status: stringPtr(string(model.RoleStatusDeleted)),
	}, audit, model.UserLogActionDelete)
}

// LogPermissionDenied records a role management request rejected for missing permission.
func (s *RoleService) LogPermissionDenied(action model.UserLogAction, roleID *uint, audit AuditContext) error {
	return s.logs.Write(UserLogEntry{Action: action, Message: "permission denied", RoleID: roleID, Audit: audit})
}

func (s *RoleService) update(id uint, input UpdateRoleInput, audit AuditContext, action model.UserLogAction) (*RoleDetails, error) {
	var role *model.Role
	err := s.transaction(func(roles *repository.RoleRepository, logs *UserLogWriter) error {
		var err error
		role, err = roles.FindByID(id)
		if err != nil {
			return err
		}
		previous := *role
		previousPermissions := permissionKeys(role.Permissions)

		if input.Name != nil {
			role.Name = *input.Name
		}
		if input.Description != nil {
			role.Description = *input.Description
		}
		if input.Status != nil {
			status := model.RoleStatus(*input.Status)
			if !status.Valid() {
				return ErrInvalidRoleStatus
			}
			role.Status = status
		}

		if err := roles.Update(role); err != nil {
			return err
		}

		if input.Permissions != nil {
			modelPermissions, err := validatePermissions(*input.Permissions)
			if err != nil {
				return err
			}
			for i := range modelPermissions {
				modelPermissions[i].RoleID = role.ID
			}
			if err := roles.ReplacePermissions(role.ID, modelPermissions); err != nil {
				return err
			}
		}

		role, err = roles.FindByID(role.ID)
		if err != nil {
			return err
		}

		changes := roleChanges(&previous, role, previousPermissions)
		if action != model.UserLogActionDelete && len(changes) == 0 {
			return nil
		}
		message := "updated role"
		if action == model.UserLogActionDelete {
			message = "deleted role"
		}
		if len(changes) > 0 {
			message += ": " + strings.Join(changes, "; ")
		}
		return logs.Write(UserLogEntry{Action: action, Success: true, Message: message, RoleID: uintPtr(role.ID), Audit: audit})
	})
	if err != nil {
		verb := "update"
		if action == model.UserLogActionDelete {
			verb = "delete"
		}
		return nil, s.logs.Failure(UserLogEntry{
			Action:  action,
			Message: fmt.Sprintf("%s role %d failed: %v", verb, id, err),
			RoleID:  uintPtr(id),
			Audit:   audit,
		}, err)
	}

	result := mapRoles([]model.Role{*role})
	return &result[0], nil
}

func roleChanges(previous, current *model.Role, previousPermissions []string) []string {
	changes := []string{}
	if previous.Name != current.Name {
		changes = append(changes, fmt.Sprintf("name: %s -> %s", previous.Name, current.Name))
	}
	if previous.Description != current.Description {
		changes = append(changes, fmt.Sprintf("description: %s -> %s", previous.Description, current.Description))
	}
	if previous.Status != current.Status {
		changes = append(changes, fmt.Sprintf("status: %s -> %s", previous.Status, current.Status))
	}

	currentPermissions := permissionKeys(current.Permissions)
	added := subtractKeys(currentPermissions, previousPermissions)
	removed := subtractKeys(previousPermissions, currentPermissions)
	if len(added) > 0 {
		changes = append(changes, "permissions added: "+strings.Join(added, ", "))
	}
	if len(removed) > 0 {
		changes = append(changes, "permissions removed: "+strings.Join(removed, ", "))
	}
	return changes
}

func permissionKeys(permissions []model.Permission) []string {
	keys := make([]string, 0, len(permissions))
	for _, permission := range permissions {
		keys = append(keys, string(permission.Resource)+":"+string(permission.Action))
	}
	sort.Strings(keys)
	return keys
}

func subtractKeys(values, remove []string) []string {
	removed := make(map[string]struct{}, len(remove))
	for _, value := range remove {
		removed[value] = struct{}{}
	}
	result := []string{}
	for _, value := range values {
		if _, ok := removed[value]; !ok {
			result = append(result, value)
		}
	}
	return result
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
		if !validRolePermission(resource, action) {
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

func validRolePermission(resource model.PermissionResource, action model.PermissionAction) bool {
	if !resource.Valid() || !action.Valid() {
		return false
	}
	if resource == model.PermissionResourceSettings {
		return action == model.PermissionActionRead || action == model.PermissionActionUpdate
	}
	return true
}
