package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"replica/internal/model"
	"replica/internal/service"

	"github.com/danielgtaylor/huma/v2"
)

func registerReplicaRoutes(api huma.API, svc services) {
	huma.Get(api, "/replicas", func(ctx context.Context, input *listReplicasInput) (*replicaListResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceInventories, model.PermissionActionRead); err != nil {
			return nil, mapPermissionError(err)
		}

		var inventoryID *uint
		if input.InventoryID > 0 {
			inventoryID = &input.InventoryID
		}

		page, count := resolvePagination(input.Page, input.Count)
		replicas, err := svc.replicas.ListPage(page, count, service.ReplicaListFilter{
			InventoryID: inventoryID,
			NodeID:      input.NodeID,
			URIPrefix:   input.URIPrefix,
			Status:      input.Status,
		})
		if err != nil {
			return nil, mapInventoryError(err, svc.inventories)
		}
		return &replicaListResponse{Body: *replicas}, nil
	})

	huma.Get(api, "/replicas/{id}", func(ctx context.Context, input *getReplicaInput) (*replicaResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if err := AuthorizeReplicaAction(svc, accessToken, input.ID, model.PermissionResourceInventories, model.PermissionActionRead); err != nil {
			return nil, mapPermissionError(err)
		}

		replica, err := svc.replicas.Get(input.ID)
		if err != nil {
			return nil, mapInventoryError(err, svc.inventories)
		}
		return &replicaResponse{Body: *replica}, nil
	})

	huma.Get(api, "/replicas/{id}/files", func(ctx context.Context, input *listReplicaFilesInput) (*replicaFileListResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if err := AuthorizeReplicaAction(svc, accessToken, input.ID, model.PermissionResourceInventories, model.PermissionActionRead); err != nil {
			return nil, mapPermissionError(err)
		}

		page, count := resolvePagination(input.Page, input.Count)
		var version *uint
		if input.Version > 0 {
			version = &input.Version
		}

		files, err := svc.replicas.ListFiles(input.ID, page, count, service.ReplicaFileListFilter{
			Status:  input.Status,
			Version: version,
		})
		if err != nil {
			return nil, mapInventoryError(err, svc.inventories)
		}
		return &replicaFileListResponse{Body: *files}, nil
	})

	huma.Get(api, "/replicas/{id}/files/{file_id}", func(ctx context.Context, input *getReplicaFileInput) (*replicaFileResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if err := AuthorizeReplicaAction(svc, accessToken, input.ID, model.PermissionResourceInventories, model.PermissionActionRead); err != nil {
			return nil, mapPermissionError(err)
		}

		file, err := svc.replicas.GetFile(input.ID, input.FileID)
		if err != nil {
			return nil, mapInventoryError(err, svc.inventories)
		}
		return &replicaFileResponse{Body: *file}, nil
	})

	huma.Post(api, "/replicas", func(ctx context.Context, input *createReplicaInput) (*replicaResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceInventories, model.PermissionActionUpdate); err != nil {
			return nil, mapPermissionError(err)
		}

		replica, err := svc.replicas.Create(service.CreateReplicaInput{
			InventoryID:       input.Body.InventoryID,
			NodeID:            input.Body.NodeID,
			URI:               input.Body.URI,
			Type:              input.Body.Type,
			UpstreamReplicaID: input.Body.UpstreamReplicaID,
			StorageProfile:    input.Body.StorageProfile,
		})
		if err != nil {
			return nil, mapInventoryError(err, svc.inventories)
		}
		return &replicaResponse{Body: *replica}, nil
	})

	huma.Patch(api, "/replicas/{id}", func(ctx context.Context, input *updateReplicaInput) (*replicaResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if err := AuthorizeReplicaAction(svc, accessToken, input.ID, model.PermissionResourceInventories, model.PermissionActionUpdate); err != nil {
			return nil, mapPermissionError(err)
		}

		upstreamReplicaID, upstreamReplicaIDSet, err := nullableUint(input.Body.UpstreamReplicaID)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid replica upstream")
		}
		replica, err := svc.replicas.Update(input.ID, service.UpdateReplicaInput{
			Type:                 input.Body.Type,
			Status:               input.Body.Status,
			UpstreamReplicaID:    upstreamReplicaID,
			UpstreamReplicaIDSet: upstreamReplicaIDSet,
			StorageProfile:       input.Body.StorageProfile,
		})
		if err != nil {
			return nil, mapInventoryError(err, svc.inventories)
		}
		return &replicaResponse{Body: *replica}, nil
	})

	huma.Delete(api, "/replicas/{id}", func(ctx context.Context, input *deleteReplicaInput) (*replicaResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if err := AuthorizeReplicaAction(svc, accessToken, input.ID, model.PermissionResourceInventories, model.PermissionActionUpdate); err != nil {
			return nil, mapPermissionError(err)
		}

		replica, err := svc.replicas.Delete(input.ID)
		if err != nil {
			return nil, mapInventoryError(err, svc.inventories)
		}
		return &replicaResponse{Body: *replica}, nil
	})
}

type listReplicasInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	Page          int    `query:"page"`
	Count         int    `query:"count"`
	InventoryID   uint   `query:"inventory_id"`
	NodeID        string `query:"node_id"`
	URIPrefix     string `query:"uri_prefix"`
	Status        string `query:"status"`
}

type getReplicaInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            uint   `path:"id"`
}

type createReplicaInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	Body          struct {
		InventoryID       uint   `json:"inventory_id"`
		NodeID            string `json:"node_id" minLength:"1"`
		URI               string `json:"uri" minLength:"1"`
		Type              string `json:"type" minLength:"1"`
		UpstreamReplicaID *uint  `json:"upstream_replica_id,omitempty"`
		StorageProfile    string `json:"storage_profile,omitempty"`
	}
}

type listReplicaFilesInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            uint   `path:"id"`
	Page          int    `query:"page"`
	Count         int    `query:"count"`
	Status        string `query:"status"`
	Version       uint   `query:"version"`
}

type getReplicaFileInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            uint   `path:"id"`
	FileID        uint   `path:"file_id"`
}

type updateReplicaInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            uint   `path:"id"`
	Body          struct {
		Type              *string         `json:"type,omitempty"`
		Status            *string         `json:"status,omitempty"`
		UpstreamReplicaID json.RawMessage `json:"upstream_replica_id,omitempty"`
		StorageProfile    *string         `json:"storage_profile,omitempty"`
	}
}

// wrapper function to authorize based on user's roles with fallback to per-user permissions
func AuthorizeReplicaAction(svc services, accessToken string, replicaID uint, resource model.PermissionResource, action model.PermissionAction) error {
	user, err := svc.auth.Authorize(accessToken, resource, action)
	if err != nil {
		if errors.Is(err, service.ErrForbidden) {
			replica, err := svc.replicas.Get(replicaID)
			if err == nil {
				_, err = svc.auth.AuthorizeInventoryUser(user.ID, replica.InventoryID, action)
			}

			if err != nil {
				return mapPermissionError(err)
			}
		}

		if err != nil {
			return mapPermissionError(err)
		}
	}

	return nil
}

func nullableUint(raw json.RawMessage) (*uint, bool, error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, true, nil
	}
	var value uint
	if err := json.Unmarshal(raw, &value); err != nil || value == 0 {
		return nil, true, errors.New("invalid nullable uint")
	}
	return &value, true, nil
}

type deleteReplicaInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            uint   `path:"id"`
}

type replicaResponse struct {
	Body service.InventoryReplicaDetails
}

type replicaListResponse struct {
	Body service.ReplicaList
}

type replicaFileResponse struct {
	Body service.ReplicaFileDetails
}

type replicaFileListResponse struct {
	Body service.ReplicaFileList
}
