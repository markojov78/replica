package service

import (
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"

	"dropoutbox/internal/model"
	"dropoutbox/internal/repository"

	"gorm.io/gorm"
)

type ReplicaService struct {
	repo        *repository.ReplicaRepository
	inventories *repository.InventoryRepository
	nodes       *NodeService
	settings    *SettingService
}

func NewReplicaService(repo *repository.ReplicaRepository, inventoryRepo *repository.InventoryRepository, optionalServices ...any) *ReplicaService {
	service := &ReplicaService{repo: repo, inventories: inventoryRepo}
	for _, optional := range optionalServices {
		switch svc := optional.(type) {
		case *NodeService:
			service.nodes = svc
		case *SettingService:
			service.settings = svc
		}
	}
	return service
}

type ReconcileReplicaCommandPayload struct {
	SourceNodeAddress    string `json:"source_node_address"`
	SourceNodeID         string `json:"source_node_id"`
	SourceReplicaID      uint   `json:"source_replica_id"`
	DestinationReplicaID uint   `json:"destination_replica_id"`
	TransferToken        string `json:"transfer_token"`
}

func (s *ReplicaService) List() ([]model.Replica, error) {
	return s.repo.List()
}

func (s *ReplicaService) Create(input CreateReplicaInput) (*InventoryReplicaDetails, error) {
	if _, err := s.inventories.FindByID(input.InventoryID); err != nil {
		return nil, err
	}

	nodeID := strings.TrimSpace(input.NodeID)
	uri := strings.TrimSpace(input.URI)
	if nodeID == "" || uri == "" {
		return nil, ErrInvalidReplicaURI
	}

	replicaType := model.ReplicaType(strings.TrimSpace(input.Type))
	if !replicaType.Valid() {
		return nil, ErrInvalidReplicaType
	}
	if err := s.validateUpstreamReplica(input.InventoryID, 0, input.UpstreamReplicaID); err != nil {
		return nil, err
	}

	replica := &model.Replica{
		InventoryID:       input.InventoryID,
		NodeID:            nodeID,
		URI:               uri,
		Status:            model.ReplicaStatusActive,
		Type:              replicaType,
		UpstreamReplicaID: input.UpstreamReplicaID,
	}
	command := &model.Command{
		NodeID: nodeID,
		Type:   model.NodeCommandTypeReconcileReplica,
		Status: model.NodeCommandStatusPending,
	}
	if err := s.repo.CreateWithPendingFiles(replica, command, s.reconcilePayloadBuilder()); err != nil {
		return nil, err
	}
	if s.nodes != nil {
		s.nodes.PublishCommand(command)
	}

	return toInventoryReplicaDetails(replica), nil
}

func (s *ReplicaService) reconcilePayloadBuilder() repository.ReconcilePayloadBuilder {
	return func(destination model.Replica, source repository.ReconcileSource) (json.RawMessage, error) {
		if s.settings == nil {
			return nil, ErrTransferPrivateKeyUnset
		}

		token, err := s.settings.NewReplicaTransferToken(TransferTokenInput{
			SourceReplicaID:      source.ReplicaID,
			DestinationReplicaID: destination.ID,
			SourceNodeID:         source.NodeID,
			DestinationNodeID:    destination.NodeID,
			ExpiresIn:            15 * time.Minute,
		})
		if err != nil {
			return nil, err
		}

		return json.Marshal(ReconcileReplicaCommandPayload{
			SourceNodeAddress:    source.NodeAddress,
			SourceNodeID:         source.NodeID,
			SourceReplicaID:      source.ReplicaID,
			DestinationReplicaID: destination.ID,
			TransferToken:        token,
		})
	}
}

func (s *ReplicaService) Get(replicaID uint) (*InventoryReplicaDetails, error) {
	replica, err := s.repo.FindByID(replicaID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrReplicaNotFound
		}
		return nil, err
	}

	return toInventoryReplicaDetails(replica), nil
}

func (s *ReplicaService) GetFile(replicaID, fileID uint) (*ReplicaFileDetails, error) {
	if _, err := s.repo.FindByID(replicaID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrReplicaNotFound
		}
		return nil, err
	}

	file, err := s.repo.FindFileByID(replicaID, fileID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrReplicaFileNotFound
		}
		return nil, err
	}

	return toReplicaFileDetails(file), nil
}

func (s *ReplicaService) ListFiltered(filter ReplicaListFilter) ([]InventoryReplicaDetails, error) {
	replicas, err := s.repo.ListFiltered(repository.ReplicaListFilter{
		InventoryID: filter.InventoryID,
		NodeID:      filter.NodeID,
		URIPrefix:   filter.URIPrefix,
	})
	if err != nil {
		return nil, err
	}

	result := make([]InventoryReplicaDetails, 0, len(replicas))
	for _, replica := range replicas {
		result = append(result, *toInventoryReplicaDetails(&replica))
	}

	return result, nil
}

func (s *ReplicaService) ListPage(page, perPage int, filter ReplicaListFilter) (*ReplicaList, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 20
	}
	if perPage > 100 {
		perPage = 100
	}

	replicas, total, err := s.repo.ListPage(page, perPage, repository.ReplicaListFilter{
		InventoryID: filter.InventoryID,
		NodeID:      filter.NodeID,
		URIPrefix:   filter.URIPrefix,
	})
	if err != nil {
		return nil, err
	}

	items := make([]InventoryReplicaDetails, 0, len(replicas))
	for _, replica := range replicas {
		items = append(items, *toInventoryReplicaDetails(&replica))
	}

	return &ReplicaList{
		Items: items,
		Page:  page,
		Count: perPage,
		Total: total,
	}, nil
}

func (s *ReplicaService) ListFiles(replicaID uint, page, perPage int, filter ReplicaFileListFilter) (*ReplicaFileList, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 20
	}
	if perPage > 100 {
		perPage = 100
	}

	if _, err := s.repo.FindByID(replicaID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrReplicaNotFound
		}
		return nil, err
	}

	if filter.Status != "" {
		status := model.ReplicaFileStatus(strings.TrimSpace(filter.Status))
		if !status.Valid() {
			return nil, ErrInvalidReplicaFileStatus
		}
		filter.Status = string(status)
	}

	files, total, err := s.repo.ListFiles(replicaID, page, perPage, repository.ReplicaFileListFilter{
		Status:  filter.Status,
		Version: filter.Version,
	})
	if err != nil {
		return nil, err
	}

	items := make([]ReplicaFileDetails, 0, len(files))
	for _, file := range files {
		items = append(items, *toReplicaFileDetails(&file))
	}

	return &ReplicaFileList{
		Items: items,
		Page:  page,
		Count: perPage,
		Total: total,
	}, nil
}

func (s *ReplicaService) ListInventoryFiles(replicaID uint, nodeID string, filters ...ReplicaFileListFilter) ([]ReplicaInventoryFileDetails, error) {
	replica, err := s.repo.FindByID(replicaID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrReplicaNotFound
		}
		return nil, err
	}
	if strings.TrimSpace(nodeID) == "" || replica.NodeID != strings.TrimSpace(nodeID) {
		return nil, ErrForbidden
	}

	var filter ReplicaFileListFilter
	if len(filters) > 0 {
		filter = filters[0]
	}
	if filter.Status != "" {
		status := model.ReplicaFileStatus(strings.TrimSpace(filter.Status))
		if !status.Valid() {
			return nil, ErrInvalidReplicaFileStatus
		}
		filter.Status = string(status)
	}

	files, err := s.repo.ListInventoryFiles(replicaID, repository.ReplicaFileListFilter{
		Status: filter.Status,
	})
	if err != nil {
		return nil, err
	}

	result := make([]ReplicaInventoryFileDetails, 0, len(files))
	for _, file := range files {
		result = append(result, ReplicaInventoryFileDetails{
			FileID:           file.FileID,
			ReplicaID:        file.ReplicaID,
			InventoryID:      file.InventoryID,
			RelativeURI:      file.RelativeURI,
			Size:             file.Size,
			Hash:             file.Hash,
			InventoryStatus:  file.InventoryStatus,
			InventoryVersion: file.InventoryVersion,
			ReplicaStatus:    file.ReplicaStatus,
			ReplicaVersion:   file.ReplicaVersion,
			Created:          file.Created,
			Modified:         file.Modified,
		})
	}

	return result, nil
}

func (s *ReplicaService) Update(replicaID uint, input UpdateReplicaInput) (*InventoryReplicaDetails, error) {
	replica, err := s.repo.FindByID(replicaID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrReplicaNotFound
		}
		return nil, err
	}

	if input.Type != nil {
		replicaType := model.ReplicaType(strings.TrimSpace(*input.Type))
		if !replicaType.Valid() {
			return nil, ErrInvalidReplicaType
		}
		replica.Type = replicaType
	}
	if input.Status != nil {
		status := model.ReplicaStatus(strings.TrimSpace(*input.Status))
		if !status.Valid() {
			return nil, ErrInvalidReplicaStatus
		}
		replica.Status = status
	}
	if input.UpstreamReplicaID != nil {
		if err := s.validateUpstreamReplica(replica.InventoryID, replica.ID, input.UpstreamReplicaID); err != nil {
			return nil, err
		}
		replica.UpstreamReplicaID = input.UpstreamReplicaID
	}

	if err := s.repo.Update(replica); err != nil {
		return nil, err
	}

	return toInventoryReplicaDetails(replica), nil
}

func (s *ReplicaService) Delete(replicaID uint) (*InventoryReplicaDetails, error) {
	return s.Update(replicaID, UpdateReplicaInput{
		Status: stringPtr(string(model.ReplicaStatusDeleted)),
	})
}

func (s *ReplicaService) ReportFileChanges(replicaID uint, nodeID string, changes []ReplicaFileChangeInput) error {
	replica, err := s.repo.FindByID(replicaID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrReplicaNotFound
		}
		return err
	}

	if strings.TrimSpace(nodeID) == "" || replica.NodeID != strings.TrimSpace(nodeID) {
		return ErrForbidden
	}
	if replica.UpstreamReplicaID != nil {
		return ErrInvalidReplicaFileUpdate
	}

	updates := make([]repository.ReplicaFileUpdate, 0, len(changes))
	for _, change := range changes {
		relativeURI := strings.TrimSpace(change.RelativeURI)
		fileHash := strings.TrimSpace(change.FileHash)
		if relativeURI == "" || fileHash == "" || change.FileSize < 0 || change.CreatedTime.IsZero() || change.ModifiedTime.IsZero() ||
			(change.FileID != nil && *change.FileID == 0) {
			return ErrInvalidReplicaFileUpdate
		}
		updates = append(updates, repository.ReplicaFileUpdate{
			FileID:       change.FileID,
			RelativeURI:  relativeURI,
			FileSize:     change.FileSize,
			FileHash:     fileHash,
			CreatedTime:  change.CreatedTime,
			ModifiedTime: change.ModifiedTime,
		})
	}

	if len(updates) == 0 {
		return nil
	}

	commands, err := s.repo.ReportFileChanges(replicaID, updates, s.reconcilePayloadBuilder())
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrInventoryFileNotFound
		}
		if errors.Is(err, repository.ErrInvalidReplicaFileUpdate) {
			return ErrInvalidReplicaFileUpdate
		}
		return err
	}
	if s.nodes != nil {
		for i := range commands {
			s.nodes.PublishCommand(&commands[i])
		}
	}

	return nil
}

func (s *ReplicaService) UpdateFileStatus(replicaID, fileID uint, nodeID, statusValue string, version *uint, errorMessage *string) error {
	replica, err := s.repo.FindByID(replicaID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrReplicaNotFound
		}
		return err
	}

	if strings.TrimSpace(nodeID) == "" || replica.NodeID != strings.TrimSpace(nodeID) {
		return ErrForbidden
	}

	status := model.ReplicaFileStatus(strings.TrimSpace(statusValue))
	if !status.Valid() {
		return ErrInvalidReplicaFileStatus
	}

	if errorMessage != nil && strings.TrimSpace(*errorMessage) != "" {
		log.Printf("replica file status update reported error replica_id=%d file_id=%d status=%s error=%s", replicaID, fileID, status, strings.TrimSpace(*errorMessage))
	}

	if err := s.repo.UpdateFileStatus(replicaID, fileID, status, version); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrReplicaFileNotFound
		}
		if errors.Is(err, repository.ErrInvalidReplicaFileUpdate) {
			return ErrInvalidReplicaFileUpdate
		}
		return err
	}

	return nil
}

func (s *ReplicaService) IsNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

func (s *ReplicaService) validateUpstreamReplica(inventoryID, replicaID uint, upstreamReplicaID *uint) error {
	if upstreamReplicaID == nil {
		return nil
	}
	if *upstreamReplicaID == 0 || *upstreamReplicaID == replicaID {
		return ErrInvalidReplicaUpstream
	}

	upstream, err := s.repo.FindByID(*upstreamReplicaID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrInvalidReplicaUpstream
		}
		return err
	}
	if upstream.InventoryID != inventoryID {
		return ErrInvalidReplicaUpstream
	}

	// TODO: add cycle detection if topology updates become more complex.
	return nil
}
