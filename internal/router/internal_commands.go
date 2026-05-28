package router

import (
	"context"

	"dropoutbox/internal/service"

	"github.com/danielgtaylor/huma/v2"
)

func registerInternalCommandRoutes(api huma.API, svc services) {
	huma.Patch(api, "/commands/{command_id}", func(ctx context.Context, input *updateNodeCommandInput) (*updateNodeCommandResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated node")
		}

		node, err := svc.auth.Node(accessToken)
		if err != nil {
			return nil, mapNodeMeError(err)
		}

		command, err := svc.nodes.UpdateCommand(node.ID, input.CommandID, service.UpdateNodeCommandInput{
			Status: input.Body.Status,
			Error:  input.Body.Error,
		})
		if err != nil {
			return nil, mapNodeError(err, svc.nodes)
		}

		return &updateNodeCommandResponse{Body: *command}, nil
	})
}

type updateNodeCommandInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	CommandID     uint   `path:"command_id"`
	Body          struct {
		Status string  `json:"status" minLength:"1"`
		Error  *string `json:"error,omitempty"`
	}
}

type updateNodeCommandResponse struct {
	Body service.NodeCommand
}
