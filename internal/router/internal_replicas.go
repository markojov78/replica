package router

import (
	"context"
	"time"

	"replica/internal/service"

	"github.com/danielgtaylor/huma/v2"
)

func registerInternalReplicaRoutes(api huma.API, svc services) {
	huma.Get(api, "/replicas", func(ctx context.Context, input *listOwnReplicasInput) (*listOwnReplicasResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated node")
		}

		node, err := svc.auth.Node(accessToken)
		if err != nil {
			return nil, mapNodeMeError(err)
		}

		replicas, err := svc.replicas.ListFiltered(service.ReplicaListFilter{
			NodeID: node.ID,
		})
		if err != nil {
			return nil, mapInventoryError(err, svc.inventories)
		}

		return &listOwnReplicasResponse{Body: replicas}, nil
	})

	huma.Get(api, "/replica/{replica_id}/files", func(ctx context.Context, input *listReplicaInventoryFilesInput) (*listReplicaInventoryFilesResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated node")
		}

		node, err := svc.auth.Node(accessToken)
		if err != nil {
			return nil, mapNodeMeError(err)
		}

		files, err := svc.replicas.ListInventoryFiles(input.ReplicaID, node.ID, service.ReplicaFileListFilter{
			Status: input.Status,
		})
		if err != nil {
			if err == service.ErrForbidden {
				return nil, huma.Error403Forbidden("replica does not belong to authenticated node")
			}
			return nil, mapInventoryError(err, svc.inventories)
		}

		return &listReplicaInventoryFilesResponse{Body: listReplicaInventoryFilesBody{Files: files}}, nil
	})

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
			change := service.ReplicaFileChangeInput{
				FileID:          file.FileID,
				Action:          file.Action,
				RelativeURI:     file.RelativeURI,
				FileSizeSet:     file.FileSize != nil,
				FileHashSet:     file.FileHash != nil,
				CreatedTimeSet:  file.CreatedTime != nil,
				ModifiedTimeSet: file.ModifiedTime != nil,
			}
			if file.FileSize != nil {
				change.FileSize = *file.FileSize
			}
			if file.FileHash != nil {
				change.FileHash = *file.FileHash
			}
			if file.CreatedTime != nil {
				change.CreatedTime = *file.CreatedTime
			}
			if file.ModifiedTime != nil {
				change.ModifiedTime = *file.ModifiedTime
			}
			changes = append(changes, change)
		}

		if err := svc.replicas.ReportFileChanges(input.ReplicaID, node.ID, changes); err != nil {
			if err == service.ErrForbidden {
				return nil, huma.Error403Forbidden("replica does not belong to authenticated node")
			}
			return nil, mapInventoryError(err, svc.inventories)
		}

		return &reportReplicaFilesResponse{Status: 204}, nil
	})

	huma.Patch(api, "/replica/{replica_id}/files/{file_id}", func(ctx context.Context, input *updateReplicaFileStatusInput) (*updateReplicaFileStatusResponse, error) {
		accessToken, err := bearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing authenticated node")
		}

		node, err := svc.auth.Node(accessToken)
		if err != nil {
			return nil, mapNodeMeError(err)
		}

		if err := svc.replicas.UpdateFileStatus(input.ReplicaID, input.FileID, node.ID, input.Body.Status, input.Body.Version, input.Body.Error); err != nil {
			if err == service.ErrForbidden {
				return nil, huma.Error403Forbidden("replica does not belong to authenticated node")
			}
			return nil, mapInventoryError(err, svc.inventories)
		}

		return &updateReplicaFileStatusResponse{Status: 204}, nil
	})
}

type listOwnReplicasInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
}

type listOwnReplicasResponse struct {
	Body []service.InventoryReplicaDetails
}

type listReplicaInventoryFilesInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ReplicaID     uint   `path:"replica_id"`
	Status        string `query:"status"`
}

type listReplicaInventoryFilesResponse struct {
	Body listReplicaInventoryFilesBody
}

type listReplicaInventoryFilesBody struct {
	Files []service.ReplicaInventoryFileDetails `json:"files"`
}

type reportReplicaFilesInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ReplicaID     uint   `path:"replica_id"`
	Body          struct {
		Files []struct {
			FileID       *uint      `json:"file_id,omitempty"`
			Action       string     `json:"action,omitempty"`
			RelativeURI  string     `json:"relative_uri" minLength:"1"`
			FileSize     *int64     `json:"file_size,omitempty"`
			FileHash     *string    `json:"file_hash,omitempty"`
			CreatedTime  *time.Time `json:"created_time,omitempty"`
			ModifiedTime *time.Time `json:"modified_time,omitempty"`
		} `json:"files"`
	}
}

type reportReplicaFilesResponse struct {
	Status int `status:"204"`
}

type updateReplicaFileStatusInput struct {
	versionHeader
	Authorization string `header:"Authorization"`
	ReplicaID     uint   `path:"replica_id"`
	FileID        uint   `path:"file_id"`
	Body          struct {
		Status  string  `json:"status" minLength:"1"`
		Version *uint   `json:"version,omitempty"`
		Error   *string `json:"error,omitempty"`
	}
}

type updateReplicaFileStatusResponse struct {
	Status int `status:"204"`
}
