package router

import (
	"context"
	"time"

	"dropoutbox/internal/service"

	"github.com/danielgtaylor/huma/v2"
)

func registerInternalReplicaRoutes(api huma.API, svc services) {
	huma.Post(api, "/replica/{replica_id}/files", func(ctx context.Context, input *reportReplicaFilesInput) (*reportReplicaFilesResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated node")
		}

		node, err := svc.auth.Node(accessToken)
		if err != nil {
			return nil, mapNodeMeError(err)
		}

		changes := make([]service.ReplicaFileChangeInput, 0, len(input.Body.Files))
		for _, file := range input.Body.Files {
			changes = append(changes, service.ReplicaFileChangeInput{
				FileID:       file.FileID,
				FileSize:     file.FileSize,
				FileHash:     file.FileHash,
				ModifiedTime: file.ModifiedTime,
			})
		}

		if err := svc.inventories.ReportReplicaFileChanges(input.ReplicaID, node.ID, changes); err != nil {
			if err == service.ErrForbidden {
				return nil, huma.Error403Forbidden("replica does not belong to authenticated node")
			}
			return nil, mapInventoryError(err, svc.inventories)
		}

		return &reportReplicaFilesResponse{Status: 204}, nil
	})
}

type reportReplicaFilesInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ReplicaID     uint   `path:"replica_id"`
	Body          struct {
		Files []struct {
			FileID       uint      `json:"file_id"`
			FileSize     int64     `json:"file_size"`
			FileHash     string    `json:"file_hash" minLength:"1"`
			ModifiedTime time.Time `json:"modified_time"`
		} `json:"files"`
	}
}

type reportReplicaFilesResponse struct {
	Status int `status:"204"`
}
