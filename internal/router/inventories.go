package router

import (
	"context"
	"errors"

	"replica/internal/model"
	"replica/internal/service"

	"github.com/danielgtaylor/huma/v2"
)

func registerInventoryRoutes(api huma.API, svc services) {
	huma.Get(api, "/inventories", func(ctx context.Context, input *listInventoriesInput) (*inventoryListResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceInventories, model.PermissionActionRead); err != nil {
			return nil, mapPermissionError(err)
		}

		page, count, err := resolvePagination(input.Page, input.Count)
		if err != nil {
			return nil, err
		}
		inventories, err := svc.inventories.List(page, count, service.InventoryListFilter{
			Status: input.Status,
		})
		if err != nil {
			return nil, mapInventoryError(err, svc.inventories)
		}
		return &inventoryListResponse{Body: *inventories}, nil
	})

	huma.Get(api, "/inventories/{id}", func(ctx context.Context, input *getInventoryInput) (*inventoryResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if err := AuthorizeInventoryAction(svc, accessToken, input.ID, model.PermissionResourceInventories, model.PermissionActionRead); err != nil {
			return nil, mapPermissionError(err)
		}

		inventory, err := svc.inventories.Get(input.ID)
		if err != nil {
			return nil, mapInventoryError(err, svc.inventories)
		}
		return &inventoryResponse{Body: *inventory}, nil
	})

	huma.Get(api, "/inventories/{id}/files", func(ctx context.Context, input *listInventoryFilesInput) (*inventoryFileListResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if err := AuthorizeInventoryAction(svc, accessToken, input.ID, model.PermissionResourceInventories, model.PermissionActionRead); err != nil {
			return nil, mapPermissionError(err)
		}

		page, count, err := resolvePagination(input.Page, input.Count)
		if err != nil {
			return nil, err
		}
		files, err := svc.inventories.ListFiles(input.ID, page, count, service.InventoryFileListFilter{
			Status: input.Status,
		})
		if err != nil {
			return nil, mapInventoryError(err, svc.inventories)
		}
		return &inventoryFileListResponse{Body: *files}, nil
	})

	huma.Get(api, "/inventories/{id}/files/{file_id}", func(ctx context.Context, input *getInventoryFileInput) (*inventoryFileResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if err := AuthorizeInventoryAction(svc, accessToken, input.ID, model.PermissionResourceInventories, model.PermissionActionRead); err != nil {
			return nil, mapPermissionError(err)
		}

		file, err := svc.inventories.GetFile(input.ID, input.FileID)
		if err != nil {
			return nil, mapInventoryError(err, svc.inventories)
		}
		return &inventoryFileResponse{Body: *file}, nil
	})

	huma.Post(api, "/inventories", func(ctx context.Context, input *createInventoryInput) (*inventoryResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceInventories, model.PermissionActionCreate); err != nil {
			return nil, mapPermissionError(err)
		}

		inventory, err := svc.inventories.Create(service.CreateInventoryInput{
			Name:            input.Body.Name,
			NodeID:          input.Body.NodeID,
			FolderURI:       input.Body.FolderURI,
			FileURIs:        input.Body.FileURIs,
			StorageProfile:  input.Body.StorageProfile,
			FollowSymlinks:  input.Body.FollowSymlinks,
			UserPermissions: input.Body.UserPermissions,
		})
		if err != nil {
			return nil, mapInventoryError(err, svc.inventories)
		}
		return &inventoryResponse{Body: *inventory}, nil
	})

	huma.Patch(api, "/inventories/{id}", func(ctx context.Context, input *updateInventoryInput) (*inventoryResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if err := AuthorizeInventoryAction(svc, accessToken, input.ID, model.PermissionResourceInventories, model.PermissionActionUpdate); err != nil {
			return nil, mapPermissionError(err)
		}

		inventory, err := svc.inventories.Update(input.ID, service.UpdateInventoryInput{
			Name:            input.Body.Name,
			Status:          input.Body.Status,
			UserPermissions: input.Body.UserPermissions,
		})
		if err != nil {
			return nil, mapInventoryError(err, svc.inventories)
		}
		return &inventoryResponse{Body: *inventory}, nil
	})

	huma.Delete(api, "/inventories/{id}", func(ctx context.Context, input *deleteInventoryInput) (*inventoryResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if err := AuthorizeInventoryAction(svc, accessToken, input.ID, model.PermissionResourceInventories, model.PermissionActionDelete); err != nil {
			return nil, mapPermissionError(err)
		}

		inventory, err := svc.inventories.Delete(input.ID)
		if err != nil {
			return nil, mapInventoryError(err, svc.inventories)
		}
		return &inventoryResponse{Body: *inventory}, nil
	})

}

// wrapper function to authorize based on user's roles with fallback to per-user permissions
func AuthorizeInventoryAction(svc services, accessToken string, inventoryID uint, resource model.PermissionResource, action model.PermissionAction) error {
	user, err := svc.auth.Authorize(accessToken, resource, action)
	if err != nil {
		if errors.Is(err, service.ErrForbidden) {
			_, err = svc.auth.AuthorizeInventoryUser(user.ID, inventoryID, action)
		}

		if err != nil {
			return mapPermissionError(err)
		}
	}

	return nil
}

type listInventoriesInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	Page          int    `query:"page" default:"1"`
	Count         int    `query:"count" default:"20"`
	Status        string `query:"status"`
}

type getInventoryInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            uint   `path:"id"`
}

type createInventoryInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	Body          struct {
		Name            string                         `json:"name" minLength:"1"`
		NodeID          string                         `json:"node_id" minLength:"1"`
		FolderURI       *string                        `json:"folder_uri,omitempty"`
		FileURIs        *[]string                      `json:"file_uris,omitempty"`
		StorageProfile  string                         `json:"storage_profile,omitempty"`
		FollowSymlinks  bool                           `json:"follow_symlinks,omitempty"`
		UserPermissions *[]service.UserPermissionInput `json:"user_permissions,omitempty"`
	}
}

type listInventoryFilesInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            uint   `path:"id"`
	Page          int    `query:"page" default:"1"`
	Count         int    `query:"count" default:"20"`
	Status        string `query:"status"`
}

type getInventoryFileInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            uint   `path:"id"`
	FileID        uint   `path:"file_id"`
}

type updateInventoryInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            uint   `path:"id"`
	Body          struct {
		Name            *string                        `json:"name,omitempty"`
		Status          *string                        `json:"status,omitempty"`
		UserPermissions *[]service.UserPermissionInput `json:"user_permissions,omitempty"`
	}
}

type deleteInventoryInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            uint   `path:"id"`
}

type inventoryResponse struct {
	Body service.InventoryDetails
}

type inventoryListResponse struct {
	Body service.InventoryList
}

type inventoryFileResponse struct {
	Body service.InventoryFileDetails
}

type inventoryFileListResponse struct {
	Body service.InventoryFileList
}
