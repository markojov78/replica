package service

import (
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"

	"replica/internal/model"
	"replica/internal/repository"

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
	SourceNodeAddress    string   `json:"source_node_address"`
	SourceNodeID         string   `json:"source_node_id"`
	SourceReplicaID      uint     `json:"source_replica_id"`
	DestinationReplicaID uint     `json:"destination_replica_id"`
	TransferToken        string   `json:"transfer_token"`
	DeleteRelativeURIs   []string `json:"delete_relative_uris,omitempty"`
}

func (s *ReplicaService) List() ([]model.Replica, error) {
	return s.repo.List()
}

func (s *ReplicaService) Create(input CreateReplicaInput) (*InventoryReplicaDetails, error) {
	inventory, err := s.inventories.FindByID(input.InventoryID)
	if err != nil {
		return nil, err
	}
	if inventory.Status == model.InventoryStatusDeleted {
		return nil, ErrInventoryDeleted
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
	return func(destination model.Replica, source repository.ReconcileSource, deleteRelativeURIs []string) (json.RawMessage, error) {
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
			DeleteRelativeURIs:   deleteRelativeURIs,
		})
	}
}

func (s *ReplicaService) EnsureReconcileCommandsForNode(nodeID string) ([]NodeCommand, error) {
	commands, err := s.repo.EnsureReconcileCommandsForNode(nodeID, s.reconcilePayloadBuilder())
	if err != nil {
		return nil, err
	}
	if s.nodes != nil {
		for i := range commands {
			s.nodes.PublishCommand(&commands[i])
		}
	}
	return toNodeCommands(commands), nil
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
	if err := validateReplicaListFilter(&filter); err != nil {
		return nil, err
	}

	replicas, err := s.repo.ListFiltered(repository.ReplicaListFilter{
		InventoryID: filter.InventoryID,
		NodeID:      filter.NodeID,
		URIPrefix:   filter.URIPrefix,
		Status:      filter.Status,
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

	if err := validateReplicaListFilter(&filter); err != nil {
		return nil, err
	}

	replicas, total, err := s.repo.ListPage(page, perPage, repository.ReplicaListFilter{
		InventoryID: filter.InventoryID,
		NodeID:      filter.NodeID,
		URIPrefix:   filter.URIPrefix,
		Status:      filter.Status,
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

func validateReplicaListFilter(filter *ReplicaListFilter) error {
	if filter.Status == "" {
		return nil
	}
	status := model.ReplicaStatus(strings.TrimSpace(filter.Status))
	if !status.Valid() {
		return ErrInvalidReplicaStatus
	}
	filter.Status = string(status)
	return nil
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
		if replica.Status == model.ReplicaStatusDeleted && status != model.ReplicaStatusDeleted {
			inventory, err := s.inventories.FindByID(replica.InventoryID)
			if err != nil {
				return nil, err
			}
			if inventory.Status == model.InventoryStatusDeleted {
				return nil, ErrInventoryDeleted
			}
			activeReplica, err := s.repo.FindActiveByLocationExcludingID(replica.NodeID, replica.URI, replica.ID)
			if err == nil {
				return nil, &ActiveReplicaLocationError{
					ReplicaID: activeReplica.ID,
					NodeID:    activeReplica.NodeID,
					URI:       activeReplica.URI,
				}
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, err
			}
		}
		replica.Status = status
	}
	if input.UpstreamReplicaIDSet || input.UpstreamReplicaID != nil {
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
	updates := make([]repository.ReplicaFileUpdate, 0, len(changes))
	for _, change := range changes {
		update, err := replicaFileUpdateFromChange(change)
		if err != nil {
			return err
		}
		updates = append(updates, update)
	}

	if len(updates) == 0 {
		return nil
	}

	var commands []model.Command
	if replica.UpstreamReplicaID != nil {
		commands, err = s.repo.ReportDownstreamFileChanges(replicaID, updates, s.reconcilePayloadBuilder())
	} else {
		commands, err = s.repo.ReportFileChanges(replicaID, updates, s.reconcilePayloadBuilder())
	}
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

func replicaFileUpdateFromChange(change ReplicaFileChangeInput) (repository.ReplicaFileUpdate, error) {
	relativeURI := strings.TrimSpace(change.RelativeURI)
	action := model.ReplicaFileAction(strings.TrimSpace(change.Action))
	fileHash := strings.TrimSpace(change.FileHash)

	if relativeURI == "" || (change.FileID != nil && *change.FileID == 0) {
		return repository.ReplicaFileUpdate{}, ErrInvalidReplicaFileUpdate
	}
	if action != "" {
		switch action {
		case model.ReplicaFileActionCreated:
			if change.FileID != nil || !hasContentReportFields(change, true) {
				return repository.ReplicaFileUpdate{}, ErrInvalidReplicaFileUpdate
			}
		case model.ReplicaFileActionUpdated:
			if change.FileID == nil || !hasContentReportFields(change, true) {
				return repository.ReplicaFileUpdate{}, ErrInvalidReplicaFileUpdate
			}
		case model.ReplicaFileActionDeleted:
			if change.FileID == nil || change.FileSizeSet || change.FileHashSet || change.CreatedTimeSet || change.ModifiedTimeSet {
				return repository.ReplicaFileUpdate{}, ErrInvalidReplicaFileUpdate
			}
		default:
			return repository.ReplicaFileUpdate{}, ErrInvalidReplicaFileAction
		}
	} else if !hasContentReportFields(change, false) {
		return repository.ReplicaFileUpdate{}, ErrInvalidReplicaFileUpdate
	}

	return repository.ReplicaFileUpdate{
		FileID:       change.FileID,
		Action:       action,
		RelativeURI:  relativeURI,
		FileSize:     change.FileSize,
		FileHash:     fileHash,
		CreatedTime:  change.CreatedTime,
		ModifiedTime: change.ModifiedTime,
	}, nil
}

func hasContentReportFields(change ReplicaFileChangeInput, requirePresence bool) bool {
	fileHash := strings.TrimSpace(change.FileHash)
	if requirePresence && (!change.FileSizeSet || !change.FileHashSet || !change.CreatedTimeSet || !change.ModifiedTimeSet) {
		return false
	}
	return change.FileSize >= 0 &&
		fileHash != "" &&
		!change.CreatedTime.IsZero() &&
		!change.ModifiedTime.IsZero()
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
	if upstream.InventoryID != inventoryID || upstream.Status != model.ReplicaStatusActive {
		return ErrInvalidReplicaUpstream
	}

	// TODO: add cycle detection if topology updates become more complex.
	return nil
}
