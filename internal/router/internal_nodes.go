package router

import (
	"context"

	"dropoutbox/internal/service"

	"github.com/danielgtaylor/huma/v2"
)

func registerInternalNodeRoutes(api huma.API, svc services) {
	huma.Post(api, "/nodes", func(ctx context.Context, input *reportNodeAvailabilityInput) (*reportNodeAvailabilityResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated node")
		}

		node, err := svc.auth.Node(accessToken)
		if err != nil {
			return nil, mapNodeMeError(err)
		}

		report, err := svc.nodes.ReportAvailability(node.ID, input.Body.Address)
		if err != nil {
			return nil, mapNodeError(err, svc.nodes)
		}

		return &reportNodeAvailabilityResponse{Body: *report}, nil
	})
}

type reportNodeAvailabilityInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	Body          struct {
		Address string `json:"address" minLength:"1"`
	}
}

type reportNodeAvailabilityResponse struct {
	Body service.NodeAvailabilityReport
}
