package router

import (
	"context"
	"log"
	"net/http"

	"dropoutbox/internal/service"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gorilla/websocket"
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

func registerInternalNodeWebSocketRoute(mux *http.ServeMux, svc services) {
	upgrader := websocket.Upgrader{}

	mux.Handle("/internal/nodes/ws", requireAuthenticatedNode(svc.auth, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("internal node websocket upgrade failed: %v", err)
			return
		}
		defer conn.Close()

		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})))
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
