package router

import (
	"context"

	"dropoutbox/internal/service"

	"github.com/danielgtaylor/huma/v2"
)

func registerInternalCommandRoutes(api huma.API, svc services) {
	huma.Post(api, "/commands/{command_id}/complete", func(ctx context.Context, input *completeNodeCommandInput) (*completeNodeCommandResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated node")
		}

		node, err := svc.auth.Node(accessToken)
		if err != nil {
			return nil, mapNodeMeError(err)
		}

		command, err := svc.nodes.CompleteCommand(node.ID, input.CommandID)
		if err != nil {
			return nil, mapNodeError(err, svc.nodes)
		}

		return &completeNodeCommandResponse{Body: *command}, nil
	})
}

type completeNodeCommandInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	CommandID     uint   `path:"command_id"`
}

type completeNodeCommandResponse struct {
	Body service.NodeCommand
}
