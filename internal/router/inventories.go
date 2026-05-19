package router

import (
	"context"

	"dropoutbox/internal/model"
	"dropoutbox/internal/service"

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
		inventories, err := svc.inventories.List(page, count)
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
		files, err := svc.inventories.ListFiles(input.ID, page, count)
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

	huma.Get(api, "/inventories/{id}/replicas", func(ctx context.Context, input *listInventoryReplicasInput) (*inventoryReplicaListResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceInventories, model.PermissionActionRead); err != nil {
			return nil, mapPermissionError(err)
		}

		replicas, err := svc.inventories.ListReplicas(input.ID)
		if err != nil {
			return nil, mapInventoryError(err, svc.inventories)
		}
		return &inventoryReplicaListResponse{Body: replicas}, nil
	})

	huma.Get(api, "/inventories/{id}/replicas/{replica_id}", func(ctx context.Context, input *getInventoryReplicaInput) (*inventoryReplicaResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceInventories, model.PermissionActionRead); err != nil {
			return nil, mapPermissionError(err)
		}

		replica, err := svc.inventories.GetReplica(input.ID, input.ReplicaID)
		if err != nil {
			return nil, mapInventoryError(err, svc.inventories)
		}
		return &inventoryReplicaResponse{Body: *replica}, nil
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

	huma.Post(api, "/inventories/{id}/replicas", func(ctx context.Context, input *createInventoryReplicaInput) (*inventoryReplicaResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceInventories, model.PermissionActionUpdate); err != nil {
			return nil, mapPermissionError(err)
		}

		replica, err := svc.inventories.CreateReplica(input.ID, service.CreateReplicaInput{
			NodeID: input.Body.NodeID,
			URI:    input.Body.URI,
			Type:   input.Body.Type,
		})
		if err != nil {
			return nil, mapInventoryError(err, svc.inventories)
		}
		return &inventoryReplicaResponse{Body: *replica}, nil
	})

	huma.Patch(api, "/inventories/{id}/replicas/{replica_id}", func(ctx context.Context, input *updateInventoryReplicaInput) (*inventoryReplicaResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceInventories, model.PermissionActionUpdate); err != nil {
			return nil, mapPermissionError(err)
		}

		replica, err := svc.inventories.UpdateReplica(input.ID, input.ReplicaID, service.UpdateReplicaInput{
			Type:   input.Body.Type,
			Status: input.Body.Status,
		})
		if err != nil {
			return nil, mapInventoryError(err, svc.inventories)
		}
		return &inventoryReplicaResponse{Body: *replica}, nil
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

	huma.Delete(api, "/inventories/{id}/replicas/{replica_id}", func(ctx context.Context, input *deleteInventoryReplicaInput) (*inventoryReplicaResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceInventories, model.PermissionActionUpdate); err != nil {
			return nil, mapPermissionError(err)
		}

		replica, err := svc.inventories.DeleteReplica(input.ID, input.ReplicaID)
		if err != nil {
			return nil, mapInventoryError(err, svc.inventories)
		}
		return &inventoryReplicaResponse{Body: *replica}, nil
	})
}

type listInventoriesInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	Page          int    `query:"page"`
	Count         int    `query:"count"`
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
}

type getInventoryFileInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            uint   `path:"id"`
	FileID        uint   `path:"file_id"`
}

type listInventoryReplicasInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            uint   `path:"id"`
}

type getInventoryReplicaInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            uint   `path:"id"`
	ReplicaID     uint   `path:"replica_id"`
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

type createInventoryReplicaInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            uint   `path:"id"`
	Body          struct {
		NodeID string `json:"node_id" minLength:"1"`
		URI    string `json:"uri" minLength:"1"`
		Type   string `json:"type" minLength:"1"`
	}
}

type updateInventoryReplicaInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            uint   `path:"id"`
	ReplicaID     uint   `path:"replica_id"`
	Body          struct {
		Type   *string `json:"type,omitempty"`
		Status *string `json:"status,omitempty"`
	}
}

type deleteInventoryInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            uint   `path:"id"`
}

type deleteInventoryReplicaInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            uint   `path:"id"`
	ReplicaID     uint   `path:"replica_id"`
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

type inventoryReplicaResponse struct {
	Body service.InventoryReplicaDetails
}

type inventoryReplicaListResponse struct {
	Body []service.InventoryReplicaDetails
}
