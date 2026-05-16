package router

import (
	"context"

	"dropoutbox/internal/model"
	"dropoutbox/internal/service"

	"github.com/danielgtaylor/huma/v2"
)

func registerUserRoutes(api huma.API, svc services) {
	huma.Get(api, "/users", func(ctx context.Context, input *listUsersInput) (*userListResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceUsers, model.PermissionActionRead); err != nil {
			return nil, mapPermissionError(err)
		}

		page, count := resolvePagination(input.Page, input.Count)
		users, err := svc.users.List(page, count)
		if err != nil {
			return nil, mapUserError(err, svc.users)
		}
		return &userListResponse{Body: *users}, nil
	})

	huma.Get(api, "/users/{id}", func(ctx context.Context, input *getUserInput) (*userResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceUsers, model.PermissionActionRead); err != nil {
			return nil, mapPermissionError(err)
		}

		user, err := svc.users.Get(input.ID)
		if err != nil {
			return nil, mapUserError(err, svc.users)
		}
		return &userResponse{Body: *user}, nil
	})

	huma.Post(api, "/users", func(ctx context.Context, input *createUserInput) (*userResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceUsers, model.PermissionActionCreate); err != nil {
			return nil, mapPermissionError(err)
		}

		user, err := svc.users.Create(input.Body.Name, input.Body.Password, input.Body.RoleIDs)
		if err != nil {
			return nil, mapUserError(err, svc.users)
		}
		return &userResponse{Body: *user}, nil
	})

	huma.Patch(api, "/users/{id}", func(ctx context.Context, input *updateUserInput) (*userResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceUsers, model.PermissionActionUpdate); err != nil {
			return nil, mapPermissionError(err)
		}

		user, err := svc.users.Update(input.ID, service.UpdateUserInput{
			Name:          input.Body.Name,
			Password:      input.Body.Password,
			Status:        input.Body.Status,
			RoleIDs:       input.Body.RoleIDs,
			AddRoleIDs:    input.Body.AddRoleIDs,
			RemoveRoleIDs: input.Body.RemoveRoleIDs,
		})
		if err != nil {
			return nil, mapUserError(err, svc.users)
		}
		return &userResponse{Body: *user}, nil
	})

	huma.Delete(api, "/users/{id}", func(ctx context.Context, input *deleteUserInput) (*userResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceUsers, model.PermissionActionDelete); err != nil {
			return nil, mapPermissionError(err)
		}

		user, err := svc.users.Delete(input.ID)
		if err != nil {
			return nil, mapUserError(err, svc.users)
		}
		return &userResponse{Body: *user}, nil
	})
}

type createUserInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	Body          struct {
		Name     string `json:"name" minLength:"1"`
		Password string `json:"password" minLength:"1"`
		RoleIDs  []uint `json:"role_ids,omitempty"`
	}
}

type listUsersInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	Page          int    `query:"page"`
	Count         int    `query:"count"`
}

type getUserInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            uint   `path:"id"`
}

type updateUserInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            uint   `path:"id"`
	Body          struct {
		Name          *string `json:"name,omitempty"`
		Password      *string `json:"password,omitempty"`
		Status        *string `json:"status,omitempty"`
		RoleIDs       *[]uint `json:"role_ids,omitempty"`
		AddRoleIDs    *[]uint `json:"add_role_ids,omitempty"`
		RemoveRoleIDs *[]uint `json:"remove_role_ids,omitempty"`
	}
}

type deleteUserInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            uint   `path:"id"`
}

type userResponse struct {
	Body service.UserDetails
}

type userListResponse struct {
	Body service.UserList
}
