package router

import (
	"context"

	"replica/internal/model"
	"replica/internal/service"

	"github.com/danielgtaylor/huma/v2"
)

func registerNodeRoutes(api huma.API, svc services) {
	huma.Get(api, "/nodes", func(ctx context.Context, input *listNodesInput) (*nodeListResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceNodes, model.PermissionActionRead); err != nil {
			return nil, mapPermissionError(err)
		}

		page, count, err := resolvePagination(input.Page, input.Count)
		if err != nil {
			return nil, err
		}
		nodes, err := svc.nodes.List(page, count)
		if err != nil {
			return nil, mapNodeError(err, svc.nodes)
		}
		return &nodeListResponse{Body: *nodes}, nil
	})

	huma.Get(api, "/nodes/{id}", func(ctx context.Context, input *getNodeInput) (*nodeResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceNodes, model.PermissionActionRead); err != nil {
			return nil, mapPermissionError(err)
		}

		node, err := svc.nodes.Get(input.ID)
		if err != nil {
			return nil, mapNodeError(err, svc.nodes)
		}
		return &nodeResponse{Body: *node}, nil
	})

	huma.Post(api, "/nodes", func(ctx context.Context, input *createNodeInput) (*nodeResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceNodes, model.PermissionActionCreate); err != nil {
			return nil, mapPermissionError(err)
		}

		node, err := svc.nodes.Create(input.Body.ID, input.Body.Secret, input.Body.Address, input.Body.Status, input.Body.SharingEnabled)
		if err != nil {
			return nil, mapNodeError(err, svc.nodes)
		}
		return &nodeResponse{Body: *node}, nil
	})

	huma.Patch(api, "/nodes/{id}", func(ctx context.Context, input *updateNodeInput) (*nodeResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceNodes, model.PermissionActionUpdate); err != nil {
			return nil, mapPermissionError(err)
		}

		node, err := svc.nodes.Update(input.ID, service.UpdateNodeInput{
			Secret:         input.Body.Secret,
			Address:        input.Body.Address,
			Status:         input.Body.Status,
			SharingEnabled: input.Body.SharingEnabled,
		})
		if err != nil {
			return nil, mapNodeError(err, svc.nodes)
		}
		return &nodeResponse{Body: *node}, nil
	})

	huma.Delete(api, "/nodes/{id}", func(ctx context.Context, input *deleteNodeInput) (*nodeResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceNodes, model.PermissionActionDelete); err != nil {
			return nil, mapPermissionError(err)
		}

		node, err := svc.nodes.Delete(input.ID)
		if err != nil {
			return nil, mapNodeError(err, svc.nodes)
		}
		return &nodeResponse{Body: *node}, nil
	})
}

type listNodesInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	Page          int    `query:"page" default:"1"`
	Count         int    `query:"count" default:"20"`
}

type getNodeInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            string `path:"id"`
}

type createNodeInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	Body          struct {
		ID             string  `json:"id" minLength:"1"`
		Secret         string  `json:"secret" minLength:"1"`
		Address        string  `json:"address,omitempty"`
		Status         *string `json:"status,omitempty"`
		SharingEnabled bool    `json:"sharing_enabled,omitempty"`
	}
}

type updateNodeInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            string `path:"id"`
	Body          struct {
		Secret         *string `json:"secret,omitempty"`
		Address        *string `json:"address,omitempty"`
		Status         *string `json:"status,omitempty"`
		SharingEnabled *bool   `json:"sharing_enabled,omitempty"`
	}
}

type deleteNodeInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            string `path:"id"`
}

type nodeResponse struct {
	Body service.NodeDetails
}

type nodeListResponse struct {
	Body service.NodeList
}
