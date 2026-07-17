package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
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
	ErrInvalidInventoryStatus       = errors.New("invalid inventory status")
	ErrInvalidInventoryFileStatus   = errors.New("invalid inventory file status")
	ErrInvalidInventoryType         = errors.New("invalid inventory type")
	ErrInvalidInventoryURI          = errors.New("invalid inventory uri")
	ErrInventoryFileNotFound        = errors.New("inventory file not found")
	ErrReplicaFileNotFound          = errors.New("replica file not found")
	ErrInvalidReplicaFileStatus     = errors.New("invalid replica file status")
	ErrInvalidReplicaStatus         = errors.New("invalid replica status")
	ErrInvalidReplicaType           = errors.New("invalid replica type")
	ErrInvalidReplicaFollowSymlinks = errors.New("follow_symlinks requires filesystem replica")
	ErrInvalidReplicaURI            = errors.New("invalid replica uri")
	ErrInvalidReplicaFileUpdate     = errors.New("invalid replica file update")
	ErrInvalidReplicaFileAction     = errors.New("invalid replica file action")
	ErrInvalidReplicaUpstream       = errors.New("invalid replica upstream")
	ErrReplicaNotFound              = errors.New("replica not found")
	ErrInventoryDeleted             = errors.New("inventory is deleted")
	ErrInventoryHasActiveReplica    = errors.New("inventory has active replicas")
	ErrReplicaHasActiveShare        = errors.New("replica has active shares")
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
	SyncStatus        string `json:"sync_status,omitempty"`
	Type              string `json:"type"`
	UpstreamReplicaID *uint  `json:"upstream_replica_id"`
	StorageProfile    string `json:"storage_profile"`
	FollowSymlinks    bool   `json:"follow_symlinks"`
}

type InventoryDetails struct {
	ID              uint                      `json:"id"`
	Name            string                    `json:"name"`
	Status          string                    `json:"status"`
	Type            string                    `json:"type"`
	Replicas        []InventoryReplicaDetails `json:"replicas"`
	UserPermissions []UserPermissionDetails   `json:"user_permissions"`
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

type UserPermissionInput struct {
	UserID      uint     `json:"user_id"`
	Permissions []string `json:"permissions"`
}

type UserPermissionDetails struct {
	UserID      uint     `json:"user_id"`
	Permissions []string `json:"permissions"`
}

type CreateInventoryInput struct {
	Name            string
	NodeID          string
	FolderURI       *string
	FileURIs        *[]string
	UserPermissions *[]UserPermissionInput
}

type CreateReplicaInput struct {
	InventoryID       uint
	NodeID            string
	URI               string
	Type              string
	UpstreamReplicaID *uint
	StorageProfile    string
	FollowSymlinks    bool
}

type UpdateInventoryInput struct {
	Name            *string
	Status          *string
	UserPermissions *[]UserPermissionInput
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
	StorageProfile       *string
	FollowSymlinks       *bool
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
	nodeID := strings.TrimSpace(input.NodeID)
	if name == "" || nodeID == "" {
		return nil, ErrInvalidInventoryURI
	}
	if (input.FolderURI == nil) == (input.FileURIs == nil) {
		return nil, ErrInvalidInventoryURI
	}

	inventoryType := model.InventoryTypeFolder
	replicaURI := ""
	replicaType := model.ReplicaTypeFilesystem
	var inventoryFiles []model.InventoryFile
	if input.FolderURI != nil {
		replicaURI = strings.TrimSpace(*input.FolderURI)
		if replicaURI == "" {
			return nil, ErrInvalidInventoryURI
		}
	} else {
		if len(*input.FileURIs) == 0 {
			return nil, ErrInvalidInventoryURI
		}
		inventoryType = model.InventoryTypeFile
		var relativeURIs []string
		var err error
		replicaURI, relativeURIs, err = resolveFileSetURIs(*input.FileURIs)
		if err != nil {
			return nil, ErrInvalidInventoryURI
		}
		if strings.HasPrefix(replicaURI, "s3://") {
			replicaType = model.ReplicaTypeStorage
		}
		inventoryFiles = make([]model.InventoryFile, 0, len(relativeURIs))
		for _, relativeURI := range relativeURIs {
			inventoryFiles = append(inventoryFiles, model.InventoryFile{
				RelativeURI: relativeURI,
				Status:      model.InventoryFileStatusActive,
				Version:     0,
			})
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
		Type:   replicaType,
	}
	command := &model.Command{
		NodeID: nodeID,
		Type:   model.NodeCommandTypeScanReplica,
		Status: model.NodeCommandStatusPending,
	}
	refreshCommand := &model.Command{
		NodeID: nodeID,
		Type:   model.NodeCommandTypeRefreshState,
		Status: model.NodeCommandStatusPending,
	}

	permissions, err := validateUserPermissions(input.UserPermissions)
	if err != nil {
		return nil, err
	}
	if err := s.repo.CreateWithDefaultReplica(inventory, replica, inventoryFiles, command, refreshCommand, permissions); err != nil {
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
		s.nodes.PublishCommand(refreshCommand)
	}

	inventory, err = s.repo.FindByID(inventory.ID)
	if err != nil {
		return nil, err
	}

	details := toInventoryDetails(inventory)
	if err := s.loadInventoryUserPermissions(details); err != nil {
		return nil, err
	}
	return details, nil
}

type normalizedFileURI struct {
	kind       string
	root       string
	components []string
}

func resolveFileSetURIs(values []string) (string, []string, error) {
	if len(values) == 0 {
		return "", nil, ErrInvalidInventoryURI
	}

	normalized := make([]normalizedFileURI, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		file, canonical, err := normalizeFileURI(value)
		if err != nil {
			return "", nil, err
		}
		if _, exists := seen[canonical]; exists {
			return "", nil, ErrInvalidInventoryURI
		}
		seen[canonical] = struct{}{}
		normalized = append(normalized, file)
	}

	first := normalized[0]
	commonLength := len(first.components) - 1
	for _, file := range normalized[1:] {
		if file.kind != first.kind || file.root != first.root {
			return "", nil, ErrInvalidInventoryURI
		}
		limit := commonLength
		if len(file.components)-1 < limit {
			limit = len(file.components) - 1
		}
		i := 0
		for i < limit && sameFileSetComponent(first, file, i) {
			i++
		}
		commonLength = i
	}

	replicaURI := fileSetReplicaURI(first.kind, first.root, first.components[:commonLength])
	relativeURIs := make([]string, 0, len(normalized))
	for _, file := range normalized {
		relativeURIs = append(relativeURIs, strings.Join(file.components[commonLength:], "/"))
	}
	sort.Strings(relativeURIs)
	return replicaURI, relativeURIs, nil
}

func normalizeFileURI(value string) (normalizedFileURI, string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return normalizedFileURI{}, "", ErrInvalidInventoryURI
	}

	filesystemPath := trimmed
	if !isWindowsAbsoluteFilesystemPath(trimmed) {
		parsed, err := url.Parse(trimmed)
		if err != nil {
			return normalizedFileURI{}, "", err
		}
		if parsed.Scheme == "s3" {
			if parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" || strings.HasSuffix(parsed.Path, "/") {
				return normalizedFileURI{}, "", ErrInvalidInventoryURI
			}
			key := strings.TrimPrefix(path.Clean(parsed.Path), "/")
			if key == "" || key == "." {
				return normalizedFileURI{}, "", ErrInvalidInventoryURI
			}
			components := strings.Split(key, "/")
			canonical := "s3://" + parsed.Host + "/" + strings.Join(components, "/")
			return normalizedFileURI{kind: "s3", root: parsed.Host, components: components}, canonical, nil
		}
		if parsed.Scheme != "" {
			if parsed.Scheme != "file" || (parsed.Host != "" && parsed.Host != "localhost") || parsed.RawQuery != "" || parsed.Fragment != "" {
				return normalizedFileURI{}, "", ErrInvalidInventoryURI
			}
			filesystemPath = parsed.Path
		}
	}
	root, components, err := splitAbsoluteFilesystemPath(filesystemPath)
	if err != nil {
		return normalizedFileURI{}, "", err
	}
	canonical := fileSetReplicaURI("file", root, components)
	if root != "/" {
		canonical = strings.ToLower(canonical)
	}
	return normalizedFileURI{kind: "file", root: root, components: components}, canonical, nil
}

func sameFileSetComponent(first, other normalizedFileURI, index int) bool {
	if first.kind == "file" && first.root != "/" {
		return strings.EqualFold(first.components[index], other.components[index])
	}
	return first.components[index] == other.components[index]
}

func splitAbsoluteFilesystemPath(value string) (string, []string, error) {
	normalized := strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	root := ""
	switch {
	case len(normalized) >= 4 && normalized[0] == '/' && normalized[2] == ':' && normalized[3] == '/':
		root = strings.ToUpper(normalized[1:3])
		normalized = normalized[4:]
	case strings.HasPrefix(normalized, "/"):
		root = "/"
		normalized = strings.TrimPrefix(normalized, "/")
	case len(normalized) >= 3 && normalized[1] == ':' && normalized[2] == '/':
		root = strings.ToUpper(normalized[:2])
		normalized = normalized[3:]
	default:
		return "", nil, ErrInvalidInventoryURI
	}
	if strings.HasSuffix(normalized, "/") {
		return "", nil, ErrInvalidInventoryURI
	}
	clean := path.Clean("/" + normalized)
	components := strings.Split(strings.TrimPrefix(clean, "/"), "/")
	if len(components) == 0 || components[0] == "" || components[0] == "." {
		return "", nil, ErrInvalidInventoryURI
	}
	return root, components, nil
}

func isWindowsAbsoluteFilesystemPath(value string) bool {
	normalized := strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	return len(normalized) >= 3 && normalized[1] == ':' && normalized[2] == '/'
}

func fileSetReplicaURI(kind, root string, components []string) string {
	if kind == "s3" {
		if len(components) == 0 {
			return "s3://" + root
		}
		return (&url.URL{Scheme: "s3", Host: root, Path: "/" + strings.Join(components, "/")}).String()
	}
	uriPath := "/"
	if root == "/" {
		if len(components) > 0 {
			uriPath += strings.Join(components, "/")
		}
	} else {
		uriPath += root + "/"
		if len(components) > 0 {
			uriPath += strings.Join(components, "/")
		}
	}
	return (&url.URL{Scheme: "file", Path: uriPath}).String()
}

func (s *InventoryService) Get(id uint) (*InventoryDetails, error) {
	inventory, err := s.repo.FindByID(id)
	if err != nil {
		return nil, err
	}
	details := toInventoryDetails(inventory)
	if err := s.loadInventoryUserPermissions(details); err != nil {
		return nil, err
	}
	return details, nil
}

func (s *InventoryService) List(page, perPage int, filter InventoryListFilter) (*InventoryList, error) {
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
		details := toInventoryDetails(&inventory)
		if err := s.loadInventoryUserPermissions(details); err != nil {
			return nil, err
		}
		items = append(items, *details)
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
	permissions, err := validateUserPermissions(input.UserPermissions)
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

	if err := s.repo.UpdateWithUserPermissions(inventory, permissions, input.UserPermissions != nil); err != nil {
		return nil, err
	}

	inventory, err = s.repo.FindByID(inventory.ID)
	if err != nil {
		return nil, err
	}

	details := toInventoryDetails(inventory)
	if err := s.loadInventoryUserPermissions(details); err != nil {
		return nil, err
	}
	return details, nil
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
		ID:              inventory.ID,
		Name:            inventory.Name,
		Status:          string(inventory.Status),
		Type:            string(inventory.Type),
		Replicas:        replicas,
		UserPermissions: []UserPermissionDetails{},
	}
}

func (s *InventoryService) loadInventoryUserPermissions(details *InventoryDetails) error {
	permissions, err := s.repo.UserPermissions(details.ID)
	if err != nil {
		return err
	}
	details.UserPermissions = mapUserPermissionDetails(permissions)
	return nil
}

func validateUserPermissions(input *[]UserPermissionInput) ([]repository.UserPermissionDetails, error) {
	if input == nil {
		return nil, nil
	}

	result := make([]repository.UserPermissionDetails, 0, len(*input))
	seenUsers := make(map[uint]struct{}, len(*input))
	for _, item := range *input {
		if item.UserID == 0 {
			return nil, ErrInvalidPermissions
		}
		if _, exists := seenUsers[item.UserID]; exists {
			return nil, ErrInvalidPermissions
		}
		seenUsers[item.UserID] = struct{}{}

		seenPermissions := make(map[string]struct{}, len(item.Permissions))
		permissions := make([]string, 0, len(item.Permissions))
		for _, value := range item.Permissions {
			action := model.PermissionAction(strings.TrimSpace(value))
			if !action.Valid() {
				return nil, ErrInvalidPermissions
			}
			key := string(action)
			if _, exists := seenPermissions[key]; exists {
				continue
			}
			seenPermissions[key] = struct{}{}
			permissions = append(permissions, key)
		}
		if len(permissions) == 0 {
			continue
		}
		result = append(result, repository.UserPermissionDetails{
			UserID:      item.UserID,
			Permissions: permissions,
		})
	}
	return result, nil
}

func validatePermissionActions(input *[]string) ([]string, error) {
	if input == nil {
		return nil, nil
	}

	seenPermissions := make(map[string]struct{}, len(*input))
	permissions := make([]string, 0, len(*input))
	for _, value := range *input {
		action := model.PermissionAction(strings.TrimSpace(value))
		if !action.Valid() {
			return nil, ErrInvalidPermissions
		}
		key := string(action)
		if _, exists := seenPermissions[key]; exists {
			continue
		}
		seenPermissions[key] = struct{}{}
		permissions = append(permissions, key)
	}
	return permissions, nil
}

func mapUserPermissionDetails(input []repository.UserPermissionDetails) []UserPermissionDetails {
	result := make([]UserPermissionDetails, 0, len(input))
	for _, item := range input {
		result = append(result, UserPermissionDetails{
			UserID:      item.UserID,
			Permissions: append([]string(nil), item.Permissions...),
		})
	}
	return result
}

func toInventoryReplicaDetails(replica *model.Replica) *InventoryReplicaDetails {
	return toInventoryReplicaDetailsWithSyncStatus(replica, "")
}

func toInventoryReplicaDetailsWithSyncStatus(replica *model.Replica, syncStatus string) *InventoryReplicaDetails {
	return &InventoryReplicaDetails{
		ID:                replica.ID,
		InventoryID:       replica.InventoryID,
		InventoryType:     string(replica.Inventory.Type),
		NodeID:            replica.NodeID,
		URI:               replica.URI,
		Status:            string(replica.Status),
		SyncStatus:        syncStatus,
		Type:              string(replica.Type),
		UpstreamReplicaID: replica.UpstreamReplicaID,
		StorageProfile:    replica.StorageProfile,
		FollowSymlinks:    replica.FollowSymlinks,
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
