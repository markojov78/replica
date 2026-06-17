package router

import (
	"context"

	"replica/internal/service"

	"github.com/danielgtaylor/huma/v2"
)

func registerInternalShareRoutes(api huma.API, svc services) {
	huma.Get(api, "/shares", func(ctx context.Context, input *listOwnSharesInput) (*listOwnSharesResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated node")
		}

		node, err := svc.auth.Node(accessToken)
		if err != nil {
			return nil, mapNodeMeError(err)
		}

		shares, err := svc.shares.ListForNode(node.ID)
		if err != nil {
			return nil, mapShareError(err, svc.shares)
		}

		return &listOwnSharesResponse{Body: shares}, nil
	})
}

type listOwnSharesInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
}

type listOwnSharesResponse struct {
	Body []service.ShareDetails
}
