package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	"replica/internal/model"
	"replica/internal/repository"

	"gorm.io/gorm"
)

type InventoryService struct {
	repo  *repository.InventoryRepository
	nodes *NodeService
}

var (
	ErrInvalidInventoryStatus     = errors.New("invalid inventory status")
	ErrInvalidInventoryFileStatus = errors.New("invalid inventory file status")
	ErrInvalidInventoryType       = errors.New("invalid inventory type")
	ErrInvalidInventoryURI        = errors.New("invalid inventory uri")
	ErrInventoryFileNotFound      = errors.New("inventory file not found")
	ErrReplicaFileNotFound        = errors.New("replica file not found")
	ErrInvalidReplicaFileStatus   = errors.New("invalid replica file status")
	ErrInvalidReplicaStatus       = errors.New("invalid replica status")
	ErrInvalidReplicaType         = errors.New("invalid replica type")
	ErrInvalidReplicaURI          = errors.New("invalid replica uri")
	ErrInvalidReplicaFileUpdate   = errors.New("invalid replica file update")
	ErrInvalidReplicaFileAction   = errors.New("invalid replica file action")
	ErrInvalidReplicaUpstream     = errors.New("invalid replica upstream")
	ErrReplicaNotFound            = errors.New("replica not found")
	ErrInventoryDeleted           = errors.New("inventory is deleted")
	ErrInventoryHasActiveReplica  = errors.New("inventory has active replicas")
)

type ActiveReplicaLocationError struct {
	ReplicaID uint
	NodeID    string
	URI       string
}

func (e *ActiveReplicaLocationError) Error() string {
	return fmt.Sprintf("Active replica %d on %s is already using location %s", e.ReplicaID, e.NodeID, e.URI)
}

type InventoryReplicaDetails struct {
	ID                uint   `json:"id"`
	InventoryID       uint   `json:"inventory_id"`
	InventoryType     string `json:"inventory_type"`
	NodeID            string `json:"node_id"`
	URI               string `json:"uri"`
	Status            string `json:"status"`
	Type              string `json:"type"`
	UpstreamReplicaID *uint  `json:"upstream_replica_id"`
}

type InventoryDetails struct {
	ID       uint                      `json:"id"`
	Name     string                    `json:"name"`
	Status   string                    `json:"status"`
	Type     string                    `json:"type"`
	Replicas []InventoryReplicaDetails `json:"replicas"`
}

type InventoryFileDetails struct {
	ID          uint      `json:"id"`
	InventoryID uint      `json:"inventory_id"`
	RelativeURI string    `json:"relative_uri"`
	Status      string    `json:"status"`
	Size        int64     `json:"size"`
	Hash        string    `json:"hash"`
	Version     uint      `json:"version"`
	Created     time.Time `json:"created"`
	Modified    time.Time `json:"modified"`
}

type InventoryFileList struct {
	Items []InventoryFileDetails `json:"items"`
	Page  int                    `json:"page"`
	Count int                    `json:"count"`
	Total int64                  `json:"total"`
}

type ReplicaFileDetails struct {
	ID        uint   `json:"id"`
	FileID    uint   `json:"file_id"`
	ReplicaID uint   `json:"replica_id"`
	Version   uint   `json:"version"`
	Status    string `json:"status"`
}

type ReplicaInventoryFileDetails struct {
	FileID           uint      `json:"file_id"`
	ReplicaID        uint      `json:"replica_id"`
	InventoryID      uint      `json:"inventory_id"`
	RelativeURI      string    `json:"relative_uri"`
	Size             int64     `json:"size"`
	Hash             string    `json:"hash"`
	InventoryStatus  string    `json:"inventory_status"`
	InventoryVersion uint      `json:"inventory_version"`
	ReplicaStatus    string    `json:"replica_status"`
	ReplicaVersion   uint      `json:"replica_version"`
	Created          time.Time `json:"created"`
	Modified         time.Time `json:"modified"`
}

type ReplicaFileList struct {
	Items []ReplicaFileDetails `json:"items"`
	Page  int                  `json:"page"`
	Count int                  `json:"count"`
	Total int64                `json:"total"`
}

type ReplicaFileListFilter struct {
	Status  string
	Version *uint
}

type InventoryListFilter struct {
	Status string
}

type InventoryFileListFilter struct {
	Status string
}

type InventoryList struct {
	Items []InventoryDetails `json:"items"`
	Page  int                `json:"page"`
	Count int                `json:"count"`
	Total int64              `json:"total"`
}

type ReplicaList struct {
	Items []InventoryReplicaDetails `json:"items"`
	Page  int                       `json:"page"`
	Count int                       `json:"count"`
	Total int64                     `json:"total"`
}

type CreateInventoryInput struct {
	Name   string
	Type   string
	NodeID string
	URI    string
	UserID uint
}

type CreateReplicaInput struct {
	InventoryID       uint
	NodeID            string
	URI               string
	Type              string
	UpstreamReplicaID *uint
}

type UpdateInventoryInput struct {
	Name   *string
	Status *string
}

type ReplicaListFilter struct {
	InventoryID *uint
	NodeID      string
	URIPrefix   string
	Status      string
}

type UpdateReplicaInput struct {
	Type                 *string
	Status               *string
	UpstreamReplicaID    *uint
	UpstreamReplicaIDSet bool
}

type ReplicaFileChangeInput struct {
	FileID          *uint
	Action          string
	RelativeURI     string
	FileSize        int64
	FileSizeSet     bool
	FileHash        string
	FileHashSet     bool
	CreatedTime     time.Time
	CreatedTimeSet  bool
	ModifiedTime    time.Time
	ModifiedTimeSet bool
}

func NewInventoryService(repo *repository.InventoryRepository, nodeServices ...*NodeService) *InventoryService {
	service := &InventoryService{repo: repo}
	if len(nodeServices) > 0 {
		service.nodes = nodeServices[0]
	}
	return service
}

func (s *InventoryService) Create(input CreateInventoryInput) (*InventoryDetails, error) {
	name := strings.TrimSpace(input.Name)
	uri := strings.TrimSpace(input.URI)
	nodeID := strings.TrimSpace(input.NodeID)
	if uri == "" || nodeID == "" {
		return nil, ErrInvalidInventoryURI
	}

	if name == "" {
		name = inventoryNameFromURI(uri)
	}
	if name == "" {
		return nil, ErrInvalidInventoryURI
	}

	inventoryType := model.InventoryType(strings.TrimSpace(input.Type))
	if inventoryType == "" {
		inventoryType = model.InventoryTypeFolder
	}
	if !inventoryType.Valid() {
		return nil, ErrInvalidInventoryType
	}

	replicaURI := uri
	var inventoryFile *model.InventoryFile
	if inventoryType == model.InventoryTypeFile {
		var relativeURI string
		replicaURI, relativeURI = splitFileInventoryURI(uri)
		if replicaURI == "" || relativeURI == "" {
			return nil, ErrInvalidInventoryURI
		}
		inventoryFile = &model.InventoryFile{
			RelativeURI: relativeURI,
			Status:      model.InventoryFileStatusActive,
			Version:     0,
		}
	}

	inventory := &model.Inventory{
		Name:   name,
		Status: model.InventoryStatusActive,
		Type:   inventoryType,
	}
	replica := &model.Replica{
		NodeID: nodeID,
		URI:    replicaURI,
		Status: model.ReplicaStatusActive,
		Type:   model.ReplicaTypeFilesystem,
	}
	command := &model.Command{
		NodeID: nodeID,
		Type:   model.NodeCommandTypeScanReplica,
		Status: model.NodeCommandStatusPending,
	}

	permissions := []string{
		string(model.PermissionActionRead),
		string(model.PermissionActionCreate),
		string(model.PermissionActionUpdate),
		string(model.PermissionActionDelete),
	}
	if err := s.repo.CreateWithDefaultReplica(inventory, replica, inventoryFile, command, input.UserID, permissions); err != nil {
		return nil, err
	}

	commandPayload, err := json.Marshal(map[string]uint{
		"replica_id": replica.ID,
	})
	if err != nil {
		return nil, err
	}
	command.Payload = commandPayload
	if s.nodes != nil {
		s.nodes.PublishCommand(command)
	}

	inventory, err = s.repo.FindByID(inventory.ID)
	if err != nil {
		return nil, err
	}

	return toInventoryDetails(inventory), nil
}

func (s *InventoryService) Get(id uint) (*InventoryDetails, error) {
	inventory, err := s.repo.FindByID(id)
	if err != nil {
		return nil, err
	}
	return toInventoryDetails(inventory), nil
}

func (s *InventoryService) List(page, perPage int, filter InventoryListFilter) (*InventoryList, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 20
	}
	if perPage > 100 {
		perPage = 100
	}

	if filter.Status != "" {
		status := model.InventoryStatus(strings.TrimSpace(filter.Status))
		if !status.Valid() {
			return nil, ErrInvalidInventoryStatus
		}
		filter.Status = string(status)
	}

	inventories, total, err := s.repo.List(page, perPage, repository.InventoryListFilter{
		Status: filter.Status,
	})
	if err != nil {
		return nil, err
	}

	items := make([]InventoryDetails, 0, len(inventories))
	for _, inventory := range inventories {
		items = append(items, *toInventoryDetails(&inventory))
	}

	return &InventoryList{
		Items: items,
		Page:  page,
		Count: perPage,
		Total: total,
	}, nil
}

func (s *InventoryService) GetFile(inventoryID, fileID uint) (*InventoryFileDetails, error) {
	if _, err := s.repo.FindByID(inventoryID); err != nil {
		return nil, err
	}

	file, err := s.repo.FindFileByID(inventoryID, fileID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInventoryFileNotFound
		}
		return nil, err
	}

	return toInventoryFileDetails(file), nil
}

func (s *InventoryService) ListFiles(inventoryID uint, page, perPage int, filter InventoryFileListFilter) (*InventoryFileList, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 20
	}
	if perPage > 100 {
		perPage = 100
	}

	if _, err := s.repo.FindByID(inventoryID); err != nil {
		return nil, err
	}

	if filter.Status != "" {
		status := model.InventoryFileStatus(strings.TrimSpace(filter.Status))
		if !status.Valid() {
			return nil, ErrInvalidInventoryFileStatus
		}
		filter.Status = string(status)
	}

	files, total, err := s.repo.ListFiles(inventoryID, page, perPage, repository.InventoryFileListFilter{
		Status: filter.Status,
	})
	if err != nil {
		return nil, err
	}

	items := make([]InventoryFileDetails, 0, len(files))
	for _, file := range files {
		items = append(items, *toInventoryFileDetails(&file))
	}

	return &InventoryFileList{
		Items: items,
		Page:  page,
		Count: perPage,
		Total: total,
	}, nil
}

func (s *InventoryService) Update(id uint, input UpdateInventoryInput) (*InventoryDetails, error) {
	inventory, err := s.repo.FindByID(id)
	if err != nil {
		return nil, err
	}

	if input.Name != nil {
		inventory.Name = strings.TrimSpace(*input.Name)
	}
	if input.Status != nil {
		status := model.InventoryStatus(strings.TrimSpace(*input.Status))
		if !status.Valid() {
			return nil, ErrInvalidInventoryStatus
		}
		if status == model.InventoryStatusDeleted {
			for _, replica := range inventory.Replicas {
				if replica.Status != model.ReplicaStatusDeleted {
					return nil, ErrInventoryHasActiveReplica
				}
			}
		}
		inventory.Status = status
	}

	if err := s.repo.Update(inventory); err != nil {
		return nil, err
	}

	inventory, err = s.repo.FindByID(inventory.ID)
	if err != nil {
		return nil, err
	}

	return toInventoryDetails(inventory), nil
}

func (s *InventoryService) Delete(id uint) (*InventoryDetails, error) {
	return s.Update(id, UpdateInventoryInput{
		Status: stringPtr(string(model.InventoryStatusDeleted)),
	})
}

func (s *InventoryService) IsNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

func toInventoryDetails(inventory *model.Inventory) *InventoryDetails {
	replicas := make([]InventoryReplicaDetails, 0, len(inventory.Replicas))
	for _, replica := range inventory.Replicas {
		details := toInventoryReplicaDetails(&replica)
		details.InventoryType = string(inventory.Type)
		replicas = append(replicas, *details)
	}

	return &InventoryDetails{
		ID:       inventory.ID,
		Name:     inventory.Name,
		Status:   string(inventory.Status),
		Type:     string(inventory.Type),
		Replicas: replicas,
	}
}

func toInventoryReplicaDetails(replica *model.Replica) *InventoryReplicaDetails {
	return &InventoryReplicaDetails{
		ID:                replica.ID,
		InventoryID:       replica.InventoryID,
		InventoryType:     string(replica.Inventory.Type),
		NodeID:            replica.NodeID,
		URI:               replica.URI,
		Status:            string(replica.Status),
		Type:              string(replica.Type),
		UpstreamReplicaID: replica.UpstreamReplicaID,
	}
}

func toInventoryFileDetails(file *model.InventoryFile) *InventoryFileDetails {
	return &InventoryFileDetails{
		ID:          file.ID,
		InventoryID: file.InventoryID,
		RelativeURI: file.RelativeURI,
		Status:      string(file.Status),
		Size:        file.Size,
		Hash:        file.Hash,
		Version:     file.Version,
		Created:     file.Created,
		Modified:    file.Modified,
	}
}

func toReplicaFileDetails(file *model.ReplicaFile) *ReplicaFileDetails {
	return &ReplicaFileDetails{
		ID:        file.FileID,
		FileID:    file.FileID,
		ReplicaID: file.ReplicaID,
		Version:   file.Version,
		Status:    string(file.Status),
	}
}

func splitFileInventoryURI(value string) (string, string) {
	clean := strings.TrimSpace(value)
	if !isAbsoluteFilesystemPath(clean) || strings.HasSuffix(clean, "/") || strings.HasSuffix(clean, "\\") {
		return "", ""
	}
	separator := strings.LastIndexAny(clean, `/\`)
	if separator < 0 || separator == len(clean)-1 {
		return "", ""
	}
	prefix := clean[:separator]
	if separator == 0 || (separator == 2 && len(clean) >= 3 && clean[1] == ':') {
		prefix = clean[:separator+1]
	}
	relativeURI := clean[separator+1:]
	if prefix == "" || relativeURI == "" {
		return "", ""
	}
	return prefix, normalizeInventoryRelativeURI(relativeURI)
}

func isAbsoluteFilesystemPath(value string) bool {
	return strings.HasPrefix(value, "/") ||
		strings.HasPrefix(value, `\\`) ||
		(len(value) >= 3 && value[1] == ':' && (value[2] == '/' || value[2] == '\\'))
}

func normalizeInventoryRelativeURI(value string) string {
	return strings.ReplaceAll(value, "\\", "/")
}

func inventoryNameFromURI(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}

	if parsed, err := url.Parse(trimmed); err == nil && parsed.Scheme != "" {
		candidate := path.Base(strings.TrimRight(parsed.Path, "/"))
		if candidate != "" && candidate != "." && candidate != "/" {
			return candidate
		}
		if parsed.Host != "" {
			return parsed.Host
		}
	}

	normalized := strings.ReplaceAll(trimmed, "\\", "/")
	candidate := path.Base(strings.TrimRight(normalized, "/"))
	if candidate == "." || candidate == "/" {
		return ""
	}
	return candidate
}
