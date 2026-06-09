package router

import (
	"context"
	"log"
	"net/http"

	"replica/internal/service"

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
		if svc.replicas != nil {
			// replica_files.status=pending is durable truth; reconcile commands are disposable triggers.
			// Heartbeat ensures each pending replica has a pending reconcile command.
			commands, err := svc.replicas.EnsureReconcileCommandsForNode(node.ID)
			if err != nil {
				return nil, mapInventoryError(err, svc.inventories)
			}
			report.Commands = append(report.Commands, commands...)
		}

		return &reportNodeAvailabilityResponse{Body: *report}, nil
	})
}

func registerInternalNodeWebSocketRoute(mux *http.ServeMux, svc services) {
	upgrader := websocket.Upgrader{}

	mux.Handle("/internal/nodes/ws", requireAuthenticatedNode(svc.auth, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nodeID, ok := authenticatedNodeIDFromContext(r.Context())
		if !ok {
			http.Error(w, "missing authenticated node", http.StatusUnauthorized)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("internal node websocket upgrade failed: %v", err)
			return
		}
		defer conn.Close()

		commandCh, unsubscribe := svc.nodes.Subscribe(nodeID)
		defer unsubscribe()

		done := make(chan struct{})

		go func() {
			defer close(done)
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()

		for {
			select {
			case <-r.Context().Done():
				<-done
				return
			case <-done:
				return
			case command, ok := <-commandCh:
				if !ok {
					return
				}
				if err := conn.WriteJSON(command); err != nil {
					return
				}
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
