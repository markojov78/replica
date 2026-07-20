package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"replica/internal/model"
	"replica/internal/service"

	"github.com/danielgtaylor/huma/v2"
)

func registerShareRoutes(api huma.API, svc services) {
	huma.Get(api, "/shares", func(ctx context.Context, input *listSharesInput) (*shareListResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceShares, model.PermissionActionRead); err != nil {
			return nil, mapPermissionError(err)
		}

		var replicaID *uint
		if input.ReplicaID > 0 {
			replicaID = &input.ReplicaID
		}
		var inventoryID *uint
		if input.InventoryID > 0 {
			inventoryID = &input.InventoryID
		}

		page, count, err := resolvePagination(input.Page, input.Count)
		if err != nil {
			return nil, err
		}
		shares, err := svc.shares.ListPage(page, count, service.ShareListFilter{
			Status:      input.Status,
			InventoryID: inventoryID,
			ReplicaID:   replicaID,
			NodeID:      input.NodeID,
			Name:        input.Name,
		})
		if err != nil {
			return nil, mapShareError(err, svc.shares)
		}
		return &shareListResponse{Body: *shares}, nil
	})

	huma.Get(api, "/shares/{id}", func(ctx context.Context, input *getShareInput) (*shareResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if err := AuthorizeShareAction(svc, accessToken, input.ID, model.PermissionActionRead); err != nil {
			return nil, mapPermissionError(err)
		}

		share, err := svc.shares.Get(input.ID)
		if err != nil {
			return nil, mapShareError(err, svc.shares)
		}
		return &shareResponse{Body: *share}, nil
	})

	huma.Post(api, "/shares", func(ctx context.Context, input *createShareInput) (*shareResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceShares, model.PermissionActionCreate); err != nil {
			return nil, mapPermissionError(err)
		}

		shareExpiration, _, err := nullableTime(input.Body.ShareExpiration)
		if err != nil {
			return nil, mapShareError(service.ErrInvalidShareExpiration, svc.shares)
		}

		share, err := svc.shares.Create(service.CreateShareInput{
			ReplicaID:            input.Body.ReplicaID,
			Name:                 input.Body.Name,
			Status:               input.Body.Status,
			ShareExpiration:      shareExpiration,
			GenerateHash:         input.Body.GenerateHash,
			UserPermissions:      input.Body.UserPermissions,
			AnonymousPermissions: input.Body.AnonymousPermissions,
			Properties:           input.Body.Properties,
		})
		if err != nil {
			return nil, mapShareError(err, svc.shares)
		}
		return &shareResponse{Body: *share}, nil
	})

	huma.Patch(api, "/shares/{id}", func(ctx context.Context, input *updateShareInput) (*shareResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if err := AuthorizeShareAction(svc, accessToken, input.ID, model.PermissionActionUpdate); err != nil {
			return nil, mapPermissionError(err)
		}

		shareExpiration, shareExpirationSet, err := nullableTime(input.Body.ShareExpiration)
		if err != nil {
			return nil, mapShareError(service.ErrInvalidShareExpiration, svc.shares)
		}

		share, err := svc.shares.Update(input.ID, service.UpdateShareInput{
			Name:                 input.Body.Name,
			Status:               input.Body.Status,
			ShareExpiration:      shareExpiration,
			ShareExpirationSet:   shareExpirationSet,
			GenerateHash:         input.Body.GenerateHash,
			UserPermissions:      input.Body.UserPermissions,
			AnonymousPermissions: input.Body.AnonymousPermissions,
			Properties:           input.Body.Properties,
		})
		if err != nil {
			return nil, mapShareError(err, svc.shares)
		}
		return &shareResponse{Body: *share}, nil
	})

	huma.Delete(api, "/shares/{id}", func(ctx context.Context, input *deleteShareInput) (*deleteShareResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if err := AuthorizeShareAction(svc, accessToken, input.ID, model.PermissionActionDelete); err != nil {
			return nil, mapPermissionError(err)
		}

		if err := svc.shares.Delete(input.ID); err != nil {
			return nil, mapShareError(err, svc.shares)
		}
		return &deleteShareResponse{Status: 204}, nil
	})
}

func AuthorizeShareAction(svc services, accessToken string, shareID uint, action model.PermissionAction) error {
	user, err := svc.auth.Authorize(accessToken, model.PermissionResourceShares, action)
	if err != nil {
		if errors.Is(err, service.ErrForbidden) && user != nil {
			_, err = svc.auth.AuthorizeShareUser(user.ID, shareID, action)
		}

		if err != nil {
			return mapPermissionError(err)
		}
	}

	return nil
}

type listSharesInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	Page          int    `query:"page" default:"1"`
	Count         int    `query:"count" default:"20"`
	Status        string `query:"status"`
	InventoryID   uint   `query:"inventory_id"`
	ReplicaID     uint   `query:"replica_id"`
	NodeID        string `query:"node_id"`
	Name          string `query:"name"`
}

type getShareInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            uint   `path:"id"`
}

type createShareInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	Body          struct {
		ReplicaID            uint                           `json:"replica_id"`
		Name                 *string                        `json:"name,omitempty"`
		Status               *string                        `json:"status,omitempty"`
		ShareExpiration      json.RawMessage                `json:"share_expiration,omitempty"`
		GenerateHash         bool                           `json:"generate_hash,omitempty"`
		UserPermissions      *[]service.UserPermissionInput `json:"user_permissions,omitempty"`
		AnonymousPermissions *[]string                      `json:"anonymous_permissions,omitempty"`
		Properties           map[string]json.RawMessage     `json:"properties,omitempty"`
	}
}

type updateShareInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            uint   `path:"id"`
	Body          struct {
		Name                 *string                        `json:"name,omitempty"`
		Status               *string                        `json:"status,omitempty"`
		ShareExpiration      json.RawMessage                `json:"share_expiration,omitempty"`
		GenerateHash         *bool                          `json:"generate_hash,omitempty"`
		UserPermissions      *[]service.UserPermissionInput `json:"user_permissions,omitempty"`
		AnonymousPermissions *[]string                      `json:"anonymous_permissions,omitempty"`
		Properties           map[string]json.RawMessage     `json:"properties,omitempty"`
	}
}

type deleteShareInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            uint   `path:"id"`
}

type shareResponse struct {
	Body service.ShareDetails
}

type shareListResponse struct {
	Body service.ShareList
}

type deleteShareResponse struct {
	Status int `status:"204"`
}

func nullableTime(raw json.RawMessage) (*time.Time, bool, error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte(`""`)) {
		return nil, true, nil
	}

	var value time.Time
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return nil, true, err
	}
	return &value, true, nil
}
