package router

import (
	"context"

	"dropoutbox/internal/model"
	"dropoutbox/internal/service"

	"github.com/danielgtaylor/huma/v2"
)

func registerRoleRoutes(api huma.API, svc services) {
	huma.Get(api, "/roles", func(ctx context.Context, input *listRolesInput) (*roleListResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceUsers, model.PermissionActionRead); err != nil {
			return nil, mapPermissionError(err)
		}

		page, count := resolvePagination(input.Page, input.Count)
		roles, err := svc.roles.List(page, count)
		if err != nil {
			return nil, mapRoleError(err, svc.roles)
		}
		return &roleListResponse{Body: *roles}, nil
	})

	huma.Get(api, "/roles/{id}", func(ctx context.Context, input *getRoleInput) (*roleResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceUsers, model.PermissionActionRead); err != nil {
			return nil, mapPermissionError(err)
		}

		role, err := svc.roles.Get(input.ID)
		if err != nil {
			return nil, mapRoleError(err, svc.roles)
		}
		return &roleResponse{Body: *role}, nil
	})

	huma.Post(api, "/roles", func(ctx context.Context, input *createRoleInput) (*roleResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceUsers, model.PermissionActionCreate); err != nil {
			return nil, mapPermissionError(err)
		}

		role, err := svc.roles.Create(input.Body.Name, input.Body.Description, input.Body.Permissions)
		if err != nil {
			return nil, mapRoleError(err, svc.roles)
		}
		return &roleResponse{Body: *role}, nil
	})

	huma.Patch(api, "/roles/{id}", func(ctx context.Context, input *updateRoleInput) (*roleResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceUsers, model.PermissionActionUpdate); err != nil {
			return nil, mapPermissionError(err)
		}

		role, err := svc.roles.Update(input.ID, service.UpdateRoleInput{
			Name:        input.Body.Name,
			Description: input.Body.Description,
			Status:      input.Body.Status,
			Permissions: input.Body.Permissions,
		})
		if err != nil {
			return nil, mapRoleError(err, svc.roles)
		}
		return &roleResponse{Body: *role}, nil
	})

	huma.Delete(api, "/roles/{id}", func(ctx context.Context, input *deleteRoleInput) (*roleResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceUsers, model.PermissionActionDelete); err != nil {
			return nil, mapPermissionError(err)
		}

		role, err := svc.roles.Delete(input.ID)
		if err != nil {
			return nil, mapRoleError(err, svc.roles)
		}
		return &roleResponse{Body: *role}, nil
	})
}

type listRolesInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	Page          int    `query:"page"`
	Count         int    `query:"count"`
}

type getRoleInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            uint   `path:"id"`
}

type createRoleInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	Body          struct {
		Name        string                        `json:"name" minLength:"1"`
		Description string                        `json:"description"`
		Permissions []service.RolePermissionInput `json:"permissions"`
	}
}

type updateRoleInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            uint   `path:"id"`
	Body          struct {
		Name        *string                        `json:"name,omitempty"`
		Description *string                        `json:"description,omitempty"`
		Status      *string                        `json:"status,omitempty"`
		Permissions *[]service.RolePermissionInput `json:"permissions,omitempty"`
	}
}

type deleteRoleInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            uint   `path:"id"`
}

type roleResponse struct {
	Body service.RoleDetails
}

type roleListResponse struct {
	Body service.RoleList
}
