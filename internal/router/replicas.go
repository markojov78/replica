package router

import (
	"context"

	"dropoutbox/internal/model"
	"dropoutbox/internal/service"

	"github.com/danielgtaylor/huma/v2"
)

func registerReplicaRoutes(api huma.API, svc services) {
	huma.Get(api, "/replica", func(ctx context.Context, input *listReplicasInput) (*replicaListResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceInventories, model.PermissionActionRead); err != nil {
			return nil, mapPermissionError(err)
		}

		replicas, err := svc.inventories.ListReplicas(service.ReplicaListFilter{
			InventoryID: input.InventoryID,
			NodeID:      input.NodeID,
			URIPrefix:   input.URIPrefix,
		})
		if err != nil {
			return nil, mapInventoryError(err, svc.inventories)
		}
		return &replicaListResponse{Body: replicas}, nil
	})

	huma.Get(api, "/replica/{id}", func(ctx context.Context, input *getReplicaInput) (*replicaResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceInventories, model.PermissionActionRead); err != nil {
			return nil, mapPermissionError(err)
		}

		replica, err := svc.inventories.GetReplica(input.ID)
		if err != nil {
			return nil, mapInventoryError(err, svc.inventories)
		}
		return &replicaResponse{Body: *replica}, nil
	})

	huma.Post(api, "/replica", func(ctx context.Context, input *createReplicaInput) (*replicaResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceInventories, model.PermissionActionUpdate); err != nil {
			return nil, mapPermissionError(err)
		}

		replica, err := svc.inventories.CreateReplica(service.CreateReplicaInput{
			InventoryID: input.Body.InventoryID,
			NodeID:      input.Body.NodeID,
			URI:         input.Body.URI,
			Type:        input.Body.Type,
		})
		if err != nil {
			return nil, mapInventoryError(err, svc.inventories)
		}
		return &replicaResponse{Body: *replica}, nil
	})

	huma.Patch(api, "/replica/{id}", func(ctx context.Context, input *updateReplicaInput) (*replicaResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceInventories, model.PermissionActionUpdate); err != nil {
			return nil, mapPermissionError(err)
		}

		replica, err := svc.inventories.UpdateReplica(input.ID, service.UpdateReplicaInput{
			Type:   input.Body.Type,
			Status: input.Body.Status,
		})
		if err != nil {
			return nil, mapInventoryError(err, svc.inventories)
		}
		return &replicaResponse{Body: *replica}, nil
	})

	huma.Delete(api, "/replica/{id}", func(ctx context.Context, input *deleteReplicaInput) (*replicaResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceInventories, model.PermissionActionUpdate); err != nil {
			return nil, mapPermissionError(err)
		}

		replica, err := svc.inventories.DeleteReplica(input.ID)
		if err != nil {
			return nil, mapInventoryError(err, svc.inventories)
		}
		return &replicaResponse{Body: *replica}, nil
	})
}

type listReplicasInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	InventoryID   *uint  `query:"inventory_id"`
	NodeID        string `query:"node_id"`
	URIPrefix     string `query:"uri_prefix"`
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
		InventoryID uint   `json:"inventory_id"`
		NodeID      string `json:"node_id" minLength:"1"`
		URI         string `json:"uri" minLength:"1"`
		Type        string `json:"type" minLength:"1"`
	}
}

type updateReplicaInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            uint   `path:"id"`
	Body          struct {
		Type   *string `json:"type,omitempty"`
		Status *string `json:"status,omitempty"`
	}
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
	Body []service.InventoryReplicaDetails
}
