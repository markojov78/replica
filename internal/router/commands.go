package router

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"replica/internal/model"
	"replica/internal/service"

	"github.com/danielgtaylor/huma/v2"
)

func registerCommandRoutes(api huma.API, svc services) {
	huma.Register(api, huma.Operation{
		OperationID:   "create-command",
		Method:        http.MethodPost,
		Path:          "/commands",
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, input *createCommandInput) (*commandResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		// Each supported command defines its payload validation, target authorization and creation.
		switch model.CommandType(input.Body.Type) {
		case model.NodeCommandTypeScanReplica:
			payload, err := service.ParseScanReplicaCommandPayload(input.Body.Payload)
			if err != nil {
				return nil, huma.Error400BadRequest("invalid command payload")
			}
			if err := AuthorizeReplicaAction(svc, accessToken, payload.ReplicaID, model.PermissionResourceInventories, model.PermissionActionUpdate); err != nil {
				return nil, mapPermissionError(err)
			}
			command, err := svc.replicas.CreateScanCommand(payload)
			if err != nil {
				return nil, mapInventoryError(err, svc.inventories)
			}
			return &commandResponse{Body: *command}, nil
		default:
			return nil, huma.Error400BadRequest("unsupported command type")
		}
	})

	huma.Get(api, "/commands", func(ctx context.Context, input *listCommandsInput) (*commandListResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceNodes, model.PermissionActionRead); err != nil {
			return nil, mapPermissionError(err)
		}

		createdAfter, err := parseCommandDateFilter(input.CreatedAfter)
		if err != nil {
			return nil, mapNodeError(service.ErrInvalidNodeCommandDateFilter, svc.nodes)
		}
		createdBefore, err := parseCommandDateFilter(input.CreatedBefore)
		if err != nil {
			return nil, mapNodeError(service.ErrInvalidNodeCommandDateFilter, svc.nodes)
		}
		page, count, err := resolvePagination(input.Page, input.Count)
		if err != nil {
			return nil, err
		}
		commands, err := svc.nodes.ListCommands(page, count, service.NodeCommandListFilter{
			NodeID: input.NodeID, Type: input.Type, Status: input.Status,
			CreatedAfter: createdAfter, CreatedBefore: createdBefore,
			Sort: input.Sort, Order: input.Order,
		})
		if err != nil {
			return nil, mapNodeError(err, svc.nodes)
		}
		return &commandListResponse{Body: *commands}, nil
	})

	huma.Get(api, "/commands/{id}", func(ctx context.Context, input *getCommandInput) (*commandResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceNodes, model.PermissionActionRead); err != nil {
			return nil, mapPermissionError(err)
		}
		command, err := svc.nodes.GetCommand(input.ID)
		if err != nil {
			return nil, mapNodeError(err, svc.nodes)
		}
		return &commandResponse{Body: *command}, nil
	})

	huma.Patch(api, "/commands/{id}", func(ctx context.Context, input *updateAdminCommandInput) (*commandResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated user")
		}
		if _, err := svc.auth.Authorize(accessToken, model.PermissionResourceNodes, model.PermissionActionUpdate); err != nil {
			return nil, mapPermissionError(err)
		}
		command, err := svc.nodes.UpdateCommandStatus(input.ID, input.Body.Status)
		if err != nil {
			return nil, mapNodeError(err, svc.nodes)
		}
		return &commandResponse{Body: *command}, nil
	})
}

func parseCommandDateFilter(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, err
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

type listCommandsInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	Page          int    `query:"page" default:"1"`
	Count         int    `query:"count" default:"20"`
	NodeID        string `query:"node_id"`
	Type          string `query:"type"`
	Status        string `query:"status"`
	CreatedAfter  string `query:"created_after"`
	CreatedBefore string `query:"created_before"`
	Sort          string `query:"sort" default:"id"`
	Order         string `query:"order" default:"asc"`
}

type getCommandInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            uint   `path:"id"`
}

type updateAdminCommandInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ID            uint   `path:"id"`
	Body          struct {
		Status string `json:"status" minLength:"1"`
	}
}

type commandResponse struct {
	Body service.NodeCommand
}

type commandListResponse struct {
	Body service.NodeCommandList
}

type createCommandInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	Body          struct {
		Type    string          `json:"type" doc:"Command type; currently only scan_replica can be created."`
		Payload json.RawMessage `json:"payload" doc:"Command-specific payload. scan_replica requires a positive replica_id and accepts an optional force_rescan boolean (default false)."`
	}
}
