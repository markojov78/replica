package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"sort"
	"strings"
	"time"

	"replica/internal/config"
	"replica/internal/model"
	"replica/internal/repository"
	"replica/internal/security"

	"gorm.io/gorm"
)

type ShareService struct {
	repo          *repository.ShareRepository
	nodes         *NodeService
	sharingConfig func() config.SharingConfig
	logs          *ShareLogService
}

var (
	ErrInvalidShareStatus     = errors.New("invalid share status")
	ErrInvalidShareName       = errors.New("invalid share name")
	ErrInvalidShareExpiration = errors.New("invalid share expiration")
	ErrInvalidShareProperties = errors.New("invalid share properties")
	ErrShareNotFound          = errors.New("share not found")
	ErrInvalidShareFiles      = errors.New("invalid share files")
)

type ShareDetails struct {
	ID              uint                  `json:"id"`
	InventoryID     uint                  `json:"inventory_id"`
	ReplicaID       uint                  `json:"replica_id"`
	Name            string                `json:"name"`
	Status          string                `json:"status"`
	LinkHash        *string               `json:"link_hash"`
	ShareExpiration *time.Time            `json:"share_expiration"`
	Properties      model.ShareProperties `json:"properties"`
	FileCount       int64                 `json:"file_count"`
	// FileIDs is set for single-share and storage-node responses; an empty list means the whole replica is shared.
	FileIDs              *[]uint                 `json:"file_ids,omitempty"`
	UserPermissions      []UserPermissionDetails `json:"user_permissions"`
	AnonymousPermissions []string                `json:"anonymous_permissions"`
}

type ShareList struct {
	Items []ShareDetails `json:"items"`
	Page  int            `json:"page"`
	Count int            `json:"count"`
	Total int64          `json:"total"`
}

type ShareListFilter struct {
	Status      string
	InventoryID *uint
	ReplicaID   *uint
	NodeID      string
	Name        string
}

type CreateShareInput struct {
	ReplicaID            uint
	Name                 *string
	Status               *string
	ShareExpiration      *time.Time
	GenerateHash         bool
	UserPermissions      *[]UserPermissionInput
	AnonymousPermissions *[]string
	Properties           map[string]json.RawMessage
	FileIDs              *[]uint
}

type UpdateShareInput struct {
	Name                 *string
	Status               *string
	ShareExpiration      *time.Time
	ShareExpirationSet   bool
	GenerateHash         *bool
	UserPermissions      *[]UserPermissionInput
	AnonymousPermissions *[]string
	Properties           map[string]json.RawMessage
	FileIDs              *[]uint
}

func NewShareService(repo *repository.ShareRepository, nodes *NodeService, configProviders ...func() config.SharingConfig) *ShareService {
	provider := func() config.SharingConfig { return config.SharingConfig{} }
	if len(configProviders) > 0 && configProviders[0] != nil {
		provider = configProviders[0]
	}
	return &ShareService{repo: repo, nodes: nodes, sharingConfig: provider}
}

// WithShareLogs enables share log rows for share create, update and delete.
func (s *ShareService) WithShareLogs(logs *ShareLogService) *ShareService {
	s.logs = logs
	return s
}

func (s *ShareService) repoWithTx(tx *gorm.DB) *repository.ShareRepository {
	if tx == nil {
		return s.repo
	}
	return s.repo.WithTx(tx)
}

// LogPermissionDenied records a share mutation rejected by authorization.
func (s *ShareService) LogPermissionDenied(action model.ShareLogAction, shareID *uint, audit AuditContext) error {
	return s.logs.Write(ShareLogEntry{Action: action, Message: "permission denied", ShareID: shareID, Audit: audit})
}

func (s *ShareService) Start(ctx context.Context) {
	go func() {
		cleanup := func() {
			commands, err := s.repo.ExpireAnonymousAccess(time.Now().UTC())
			if err != nil {
				log.Printf("expire anonymous share access: %v", err)
				return
			}
			for i := range commands {
				s.publishCommand(&commands[i])
			}
		}
		cleanup()
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleanup()
			}
		}
	}()
}

func (s *ShareService) List() ([]model.Share, error) {
	return s.repo.List()
}

func (s *ShareService) ListPage(page, perPage int, filter ShareListFilter) (*ShareList, error) {
	if err := validateShareListFilter(&filter); err != nil {
		return nil, err
	}

	shares, total, err := s.repo.ListPage(page, perPage, repository.ShareListFilter{
		Status:      filter.Status,
		InventoryID: filter.InventoryID,
		ReplicaID:   filter.ReplicaID,
		NodeID:      filter.NodeID,
		Name:        filter.Name,
	})
	if err != nil {
		return nil, err
	}

	fileCounts, err := s.repo.FileCounts(shareIDs(shares))
	if err != nil {
		return nil, err
	}
	items := make([]ShareDetails, 0, len(shares))
	for _, share := range shares {
		details := toShareDetails(&share)
		details.FileCount = fileCounts[share.ID]
		if err := s.loadShareUserPermissions(details); err != nil {
			return nil, err
		}
		items = append(items, *details)
	}

	return &ShareList{
		Items: items,
		Page:  page,
		Count: perPage,
		Total: total,
	}, nil
}

func (s *ShareService) ListForNode(nodeID string) ([]ShareDetails, error) {
	shares, err := s.repo.ListForNode(strings.TrimSpace(nodeID))
	if err != nil {
		return nil, err
	}
	fileIDs, err := s.repo.FileIDs(shareIDs(shares))
	if err != nil {
		return nil, err
	}

	items := make([]ShareDetails, 0, len(shares))
	for _, share := range shares {
		details := toShareDetails(&share)
		setShareFileIDs(details, fileIDs[share.ID])
		if err := s.loadEffectiveSharePermissions(details); err != nil {
			return nil, err
		}
		items = append(items, *details)
	}
	return items, nil
}

func (s *ShareService) Get(id uint) (*ShareDetails, error) {
	share, err := s.repo.FindByID(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrShareNotFound
		}
		return nil, err
	}
	return s.shareDetails(share)
}

func (s *ShareService) Create(input CreateShareInput, audit AuditContext) (*ShareDetails, error) {
	share, err := s.create(input, audit)
	if err != nil {
		return nil, s.logs.Failure(ShareLogEntry{
			Action:  model.ShareLogActionCreate,
			Message: fmt.Sprintf("create share replica_id=%d failed: %v", input.ReplicaID, err),
			Audit:   audit,
		}, err)
	}
	return s.shareDetails(share)
}

func (s *ShareService) create(input CreateShareInput, audit AuditContext) (*model.Share, error) {
	if input.ShareExpiration != nil && input.ShareExpiration.Before(time.Now()) {
		return nil, ErrInvalidShareExpiration
	}
	replica, err := s.repo.FindReplicaByID(input.ReplicaID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrReplicaNotFound
		}
		return nil, err
	}
	if replica.Status == model.ReplicaStatusDeleted {
		return nil, ErrInvalidReplicaStatus
	}

	status := model.ShareStatusActive
	if input.Status != nil {
		resolvedStatus, err := resolveShareStatus(input.Status)
		if err != nil {
			return nil, err
		}
		status = resolvedStatus
	}

	name := replica.Inventory.Name
	if input.Name != nil {
		name, err = resolveShareName(input.Name)
		if err != nil {
			return nil, err
		}
	}

	share := &model.Share{
		ReplicaID:       replica.ID,
		Name:            name,
		Status:          status,
		ShareExpiration: input.ShareExpiration,
		Replica:         *replica,
	}
	properties, err := mergeShareProperties(share.Properties, input.Properties, s.sharingConfig().ThumbnailSizes)
	if err != nil {
		return nil, err
	}
	share.Properties = properties
	if input.GenerateHash {
		linkHash, err := newShareLinkHash()
		if err != nil {
			return nil, err
		}
		share.LinkHash = &linkHash
	}

	permissions, err := validateUserPermissions(input.UserPermissions)
	if err != nil {
		return nil, err
	}
	anonymousPermissions, err := validatePermissionActions(input.AnonymousPermissions)
	if err != nil {
		return nil, err
	}
	var fileIDs []uint
	if input.FileIDs != nil {
		fileIDs, err = s.validateShareFileIDs(replica.InventoryID, nil, *input.FileIDs)
		if err != nil {
			return nil, err
		}
	}
	command := newShareRefreshStateCommand(replica.NodeID)
	err = s.logs.Transaction(func(tx *gorm.DB, logs *ShareLogWriter) error {
		if err := s.repoWithTx(tx).CreateWithPermissions(share, permissions, anonymousPermissions, fileIDs, command); err != nil {
			return err
		}
		return writeShareCreateLogs(logs, share, permissions, anonymousPermissions, fileIDs, audit)
	})
	if err != nil {
		return nil, err
	}
	s.publishCommand(command)
	return share, nil
}

func (s *ShareService) Update(id uint, input UpdateShareInput, audit AuditContext) (*ShareDetails, error) {
	share, err := s.update(id, input, audit)
	if err != nil {
		return nil, s.logs.Failure(ShareLogEntry{
			Action:  model.ShareLogActionUpdate,
			Message: fmt.Sprintf("update share %d failed: %v", id, err),
			ShareID: uintPtr(id),
			Audit:   audit,
		}, err)
	}
	return s.shareDetails(share)
}

func (s *ShareService) update(id uint, input UpdateShareInput, audit AuditContext) (*model.Share, error) {
	share, err := s.repo.FindByID(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrShareNotFound
		}
		return nil, err
	}
	previous, err := s.shareSnapshot(share)
	if err != nil {
		return nil, err
	}
	permissions, err := validateUserPermissions(input.UserPermissions)
	if err != nil {
		return nil, err
	}
	anonymousPermissions, err := validatePermissionActions(input.AnonymousPermissions)
	if err != nil {
		return nil, err
	}
	var fileIDs []uint
	if input.FileIDs != nil {
		currentFileIDs, err := s.repo.FileIDs([]uint{share.ID})
		if err != nil {
			return nil, err
		}
		fileIDs, err = s.validateShareFileIDs(share.Replica.InventoryID, currentFileIDs[share.ID], *input.FileIDs)
		if err != nil {
			return nil, err
		}
	}

	if input.Name != nil {
		name, err := resolveShareName(input.Name)
		if err != nil {
			return nil, err
		}
		share.Name = name
	}
	if input.Status != nil {
		status, err := resolveShareStatus(input.Status)
		if err != nil {
			return nil, err
		}
		share.Status = status
	}
	if input.ShareExpirationSet {
		if input.ShareExpiration != nil && input.ShareExpiration.Before(time.Now()) {
			return nil, ErrInvalidShareExpiration
		}
		share.ShareExpiration = input.ShareExpiration
	}
	if input.GenerateHash != nil {
		if *input.GenerateHash {
			linkHash, err := newShareLinkHash()
			if err != nil {
				return nil, err
			}
			share.LinkHash = &linkHash
		} else {
			share.LinkHash = nil
		}
	}
	properties, err := mergeShareProperties(share.Properties, input.Properties, s.sharingConfig().ThumbnailSizes)
	if err != nil {
		return nil, err
	}
	share.Properties = properties

	command := newShareRefreshStateCommand(share.Replica.NodeID)
	err = s.logs.Transaction(func(tx *gorm.DB, logs *ShareLogWriter) error {
		if err := s.repoWithTx(tx).UpdateWithPermissions(share, permissions, input.UserPermissions != nil, anonymousPermissions, input.AnonymousPermissions != nil, fileIDs, input.FileIDs != nil, command); err != nil {
			return err
		}
		current := shareLogState{share: *share, userPermissions: previous.userPermissions, anonymousPermissions: previous.anonymousPermissions, fileIDs: previous.fileIDs}
		if input.UserPermissions != nil {
			current.userPermissions = userPermissionSets(permissions)
		}
		if input.AnonymousPermissions != nil {
			current.anonymousPermissions = anonymousPermissions
		}
		if input.FileIDs != nil {
			current.fileIDs = fileIDs
		}
		return writeShareUpdateLogs(logs, previous, current, audit)
	})
	if err != nil {
		return nil, err
	}
	s.publishCommand(command)
	return share, nil
}

func (s *ShareService) Delete(id uint, audit AuditContext) error {
	if err := s.delete(id, audit); err != nil {
		return s.logs.Failure(ShareLogEntry{
			Action:  model.ShareLogActionDelete,
			Message: fmt.Sprintf("delete share %d failed: %v", id, err),
			ShareID: uintPtr(id),
			Audit:   audit,
		}, err)
	}
	return nil
}

func (s *ShareService) delete(id uint, audit AuditContext) error {
	share, err := s.repo.FindByID(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrShareNotFound
		}
		return err
	}
	share.Status = model.ShareStatusDeleted
	command := newShareRefreshStateCommand(share.Replica.NodeID)
	err = s.logs.Transaction(func(tx *gorm.DB, logs *ShareLogWriter) error {
		if err := s.repoWithTx(tx).UpdateWithCommand(share, command); err != nil {
			return err
		}
		return logs.Write(ShareLogEntry{
			Action:  model.ShareLogActionDelete,
			Success: true,
			Message: "deleted share name=" + share.Name,
			ShareID: uintPtr(share.ID),
			Audit:   audit,
		})
	})
	if err != nil {
		return err
	}
	s.publishCommand(command)
	return nil
}

func (s *ShareService) ListFiles(shareID uint, page, perPage int, filter InventoryFileListFilter) (*InventoryFileList, error) {
	if _, err := s.findShare(shareID); err != nil {
		return nil, err
	}
	repoFilter, err := resolveInventoryFileListFilter(filter)
	if err != nil {
		return nil, err
	}
	files, total, err := s.repo.ListFilesPage(shareID, page, perPage, repoFilter)
	if err != nil {
		return nil, err
	}
	return toInventoryFileList(files, page, perPage, total), nil
}

// validateShareFileIDs returns the requested share file ids without duplicates. Every file must belong to the share
// inventory; files not already in the share must be active, so deleted files can stay in a resubmitted list.
func (s *ShareService) validateShareFileIDs(inventoryID uint, current []uint, requested []uint) ([]uint, error) {
	if slices.Contains(requested, 0) {
		return nil, ErrInvalidShareFiles
	}
	ids := uniqueFileIDs(requested)
	files, err := s.repo.FindInventoryFiles(ids)
	if err != nil {
		return nil, err
	}
	if len(files) != len(ids) {
		return nil, ErrInvalidShareFiles
	}
	for _, file := range files {
		if file.InventoryID != inventoryID {
			return nil, ErrInvalidShareFiles
		}
		if file.Status != model.InventoryFileStatusActive && !slices.Contains(current, file.ID) {
			return nil, ErrInvalidShareFiles
		}
	}
	return ids, nil
}

func (s *ShareService) IsNotFound(err error) bool {
	return errors.Is(err, ErrShareNotFound) || errors.Is(err, gorm.ErrRecordNotFound)
}

func (s *ShareService) findShare(id uint) (*model.Share, error) {
	share, err := s.repo.FindByID(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrShareNotFound
		}
		return nil, err
	}
	return share, nil
}

func (s *ShareService) shareDetails(share *model.Share) (*ShareDetails, error) {
	details := toShareDetails(share)
	fileIDs, err := s.repo.FileIDs([]uint{share.ID})
	if err != nil {
		return nil, err
	}
	setShareFileIDs(details, fileIDs[share.ID])
	if err := s.loadShareUserPermissions(details); err != nil {
		return nil, err
	}
	return details, nil
}

func setShareFileIDs(details *ShareDetails, fileIDs []uint) {
	if fileIDs == nil {
		fileIDs = []uint{}
	}
	details.FileIDs = &fileIDs
	details.FileCount = int64(len(fileIDs))
}

func shareIDs(shares []model.Share) []uint {
	ids := make([]uint, 0, len(shares))
	for _, share := range shares {
		ids = append(ids, share.ID)
	}
	return ids
}

func uniqueFileIDs(fileIDs []uint) []uint {
	seen := make(map[uint]struct{}, len(fileIDs))
	ids := make([]uint, 0, len(fileIDs))
	for _, id := range fileIDs {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func newShareRefreshStateCommand(nodeID string) *model.Command {
	return &model.Command{
		NodeID: strings.TrimSpace(nodeID),
		Type:   model.NodeCommandTypeRefreshState,
		Status: model.NodeCommandStatusPending,
	}
}

func (s *ShareService) publishCommand(command *model.Command) {
	if s.nodes == nil {
		return
	}
	s.nodes.PublishCommand(command)
}

func validateShareListFilter(filter *ShareListFilter) error {
	if filter.Status != "" {
		status, err := resolveShareStatus(&filter.Status)
		if err != nil {
			return err
		}
		filter.Status = string(status)
	}
	filter.Name = strings.TrimSpace(filter.Name)
	return nil
}

func resolveShareStatus(value *string) (model.ShareStatus, error) {
	status := model.ShareStatus(strings.TrimSpace(*value))
	if !status.Valid() {
		return "", ErrInvalidShareStatus
	}
	return status, nil
}

func resolveShareName(value *string) (string, error) {
	name := strings.TrimSpace(*value)
	if name == "" {
		return "", ErrInvalidShareName
	}
	return name, nil
}

func toShareDetails(share *model.Share) *ShareDetails {
	return &ShareDetails{
		ID:                   share.ID,
		InventoryID:          share.Replica.InventoryID,
		ReplicaID:            share.ReplicaID,
		Name:                 share.Name,
		Status:               string(share.Status),
		LinkHash:             share.LinkHash,
		ShareExpiration:      share.ShareExpiration,
		Properties:           share.Properties,
		UserPermissions:      []UserPermissionDetails{},
		AnonymousPermissions: []string{},
	}
}

func mergeShareProperties(current model.ShareProperties, values map[string]json.RawMessage, thumbnailSizes []int) (model.ShareProperties, error) {
	properties := current
	for key, raw := range values {
		switch key {
		case "view":
			value, err := nullableJSONString(raw)
			if err != nil || (value != nil && *value != "grid" && *value != "list") {
				return current, ErrInvalidShareProperties
			}
			properties.View = value
		case "page_size":
			value, err := nullableJSONInt(raw)
			if err != nil || (value != nil && *value <= 0) {
				return current, ErrInvalidShareProperties
			}
			properties.PageSize = value
		case "thumbnail_size":
			value, err := nullableJSONInt(raw)
			if err != nil || (value != nil && !slices.Contains(thumbnailSizes, *value)) {
				return current, ErrInvalidShareProperties
			}
			properties.ThumbnailSize = value
		case "theme":
			value, err := nullableJSONString(raw)
			if err != nil || (value != nil && *value != "light" && *value != "dark") {
				return current, ErrInvalidShareProperties
			}
			properties.Theme = value
		case "fuse_enabled":
			value, err := nullableJSONBool(raw)
			if err != nil {
				return current, ErrInvalidShareProperties
			}
			properties.FuseEnabled = value
		default:
			return current, ErrInvalidShareProperties
		}
	}
	return properties, nil
}

func nullableJSONString(raw json.RawMessage) (*string, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return &value, nil
}

func nullableJSONBool(raw json.RawMessage) (*bool, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return &value, nil
}

func nullableJSONInt(raw json.RawMessage) (*int, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return &value, nil
}

func (s *ShareService) loadShareUserPermissions(details *ShareDetails) error {
	permissions, err := s.repo.UserPermissions(details.ID)
	if err != nil {
		return err
	}
	details.UserPermissions = mapUserPermissionDetails(permissions)
	anonymousPermissions, err := s.repo.AnonymousPermissions(details.ID)
	if err != nil {
		return err
	}
	details.AnonymousPermissions = anonymousPermissions
	return nil
}

func (s *ShareService) loadEffectiveSharePermissions(details *ShareDetails) error {
	roleDerivedPermissions, err := s.repo.RoleDerivedPermissions()
	if err != nil {
		return err
	}
	perUserPermissions, err := s.repo.UserPermissions(details.ID)
	if err != nil {
		return err
	}
	anonymousPermissions, err := s.repo.AnonymousPermissions(details.ID)
	if err != nil {
		return err
	}

	// user_id -> permission set
	permissionsMap := make(map[uint]map[string]struct{})

	for _, p := range roleDerivedPermissions {
		if permissionsMap[p.UserID] == nil {
			permissionsMap[p.UserID] = make(map[string]struct{})
		}

		for _, permission := range p.Permissions {
			permissionsMap[p.UserID][permission] = struct{}{}
		}
	}

	for _, p := range perUserPermissions {
		if permissionsMap[p.UserID] == nil {
			permissionsMap[p.UserID] = make(map[string]struct{})
		}

		for _, permission := range p.Permissions {
			permissionsMap[p.UserID][permission] = struct{}{}
		}
	}

	permissions := make([]UserPermissionDetails, 0, len(permissionsMap))

	for userID, permissionSet := range permissionsMap {
		userPermissions := make([]string, 0, len(permissionSet))

		for permission := range permissionSet {
			userPermissions = append(userPermissions, permission)
		}

		sort.Strings(userPermissions)

		permissions = append(permissions, UserPermissionDetails{
			UserID:      userID,
			Permissions: userPermissions,
		})
	}

	sort.Slice(permissions, func(i, j int) bool {
		return permissions[i].UserID < permissions[j].UserID
	})

	details.UserPermissions = permissions
	details.AnonymousPermissions = anonymousPermissions

	return nil
}

func newShareLinkHash() (string, error) {
	return security.NewOpaqueToken()
}
