package router

import (
	"context"

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

		page, count := resolvePagination(input.Page, input.Count)
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
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceInventories, model.PermissionActionRead); err != nil {
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
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceInventories, model.PermissionActionRead); err != nil {
			return nil, mapPermissionError(err)
		}

		page, count := resolvePagination(input.Page, input.Count)
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
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceInventories, model.PermissionActionRead); err != nil {
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
		user, err := svc.auth.Authorize(accessToken, model.PermissionResourceInventories, model.PermissionActionCreate)
		if err != nil {
			return nil, mapPermissionError(err)
		}

		inventory, err := svc.inventories.Create(service.CreateInventoryInput{
			Name:   input.Body.Name,
			Type:   input.Body.Type,
			NodeID: input.Body.NodeID,
			URI:    input.Body.URI,
			UserID: user.ID,
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
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceInventories, model.PermissionActionUpdate); err != nil {
			return nil, mapPermissionError(err)
		}

		inventory, err := svc.inventories.Update(input.ID, service.UpdateInventoryInput{
			Name:   input.Body.Name,
			Status: input.Body.Status,
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
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceInventories, model.PermissionActionDelete); err != nil {
			return nil, mapPermissionError(err)
		}

		inventory, err := svc.inventories.Delete(input.ID)
		if err != nil {
			return nil, mapInventoryError(err, svc.inventories)
		}
		return &inventoryResponse{Body: *inventory}, nil
	})

}

type listInventoriesInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	Page          int    `query:"page"`
	Count         int    `query:"count"`
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
		Name   string `json:"name,omitempty"`
		Type   string `json:"type,omitempty"`
		NodeID string `json:"node_id" minLength:"1"`
		URI    string `json:"uri" minLength:"1"`
	}
}

type listInventoryFilesInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            uint   `path:"id"`
	Page          int    `query:"page"`
	Count         int    `query:"count"`
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
		Name   *string `json:"name,omitempty"`
		Status *string `json:"status,omitempty"`
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
