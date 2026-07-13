package service

import (
	"bytes"
	"encoding/json"
	"errors"
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
}

var (
	ErrInvalidShareStatus     = errors.New("invalid share status")
	ErrInvalidShareName       = errors.New("invalid share name")
	ErrInvalidShareExpiration = errors.New("invalid share expiration")
	ErrInvalidShareProperties = errors.New("invalid share properties")
	ErrShareNotFound          = errors.New("share not found")
	ErrShareAlreadyExists     = errors.New("share already exists")
)

type ShareDetails struct {
	ID                   uint                    `json:"id"`
	InventoryID          uint                    `json:"inventory_id"`
	ReplicaID            uint                    `json:"replica_id"`
	Name                 string                  `json:"name"`
	Status               string                  `json:"status"`
	LinkHash             *string                 `json:"link_hash"`
	ShareExpiration      *time.Time              `json:"share_expiration"`
	Properties           model.ShareProperties   `json:"properties"`
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
	Status    string
	ReplicaID *uint
	Name      string
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
}

func NewShareService(repo *repository.ShareRepository, nodes *NodeService, configProviders ...func() config.SharingConfig) *ShareService {
	provider := func() config.SharingConfig { return config.SharingConfig{} }
	if len(configProviders) > 0 && configProviders[0] != nil {
		provider = configProviders[0]
	}
	return &ShareService{repo: repo, nodes: nodes, sharingConfig: provider}
}

func (s *ShareService) List() ([]model.Share, error) {
	return s.repo.List()
}

func (s *ShareService) ListPage(page, perPage int, filter ShareListFilter) (*ShareList, error) {
	if err := validateShareListFilter(&filter); err != nil {
		return nil, err
	}

	shares, total, err := s.repo.ListPage(page, perPage, repository.ShareListFilter{
		Status:    filter.Status,
		ReplicaID: filter.ReplicaID,
		Name:      filter.Name,
	})
	if err != nil {
		return nil, err
	}

	items := make([]ShareDetails, 0, len(shares))
	for _, share := range shares {
		details := toShareDetails(&share)
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

	items := make([]ShareDetails, 0, len(shares))
	for _, share := range shares {
		details := toShareDetails(&share)
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
	details := toShareDetails(share)
	if err := s.loadShareUserPermissions(details); err != nil {
		return nil, err
	}
	return details, nil
}

func (s *ShareService) Create(input CreateShareInput) (*ShareDetails, error) {
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

	if status == model.ShareStatusActive {
		exists, err := s.repo.HasActiveShareForReplica(replica.ID, 0)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, ErrShareAlreadyExists
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
	command := newShareRefreshStateCommand(replica.NodeID)
	if err := s.repo.CreateWithPermissions(share, permissions, anonymousPermissions, command); err != nil {
		return nil, err
	}
	s.publishCommand(command)
	details := toShareDetails(share)
	if err := s.loadShareUserPermissions(details); err != nil {
		return nil, err
	}
	return details, nil
}

func (s *ShareService) Update(id uint, input UpdateShareInput) (*ShareDetails, error) {
	share, err := s.repo.FindByID(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrShareNotFound
		}
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
		if status == model.ShareStatusActive {
			exists, err := s.repo.HasActiveShareForReplica(share.ReplicaID, share.ID)
			if err != nil {
				return nil, err
			}
			if exists {
				return nil, ErrShareAlreadyExists
			}
		}
		share.Status = status
	}
	if input.ShareExpirationSet {
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
	if err := s.repo.UpdateWithPermissions(share, permissions, input.UserPermissions != nil, anonymousPermissions, input.AnonymousPermissions != nil, command); err != nil {
		return nil, err
	}
	s.publishCommand(command)
	details := toShareDetails(share)
	if err := s.loadShareUserPermissions(details); err != nil {
		return nil, err
	}
	return details, nil
}

func (s *ShareService) Delete(id uint) error {
	share, err := s.repo.FindByID(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrShareNotFound
		}
		return err
	}
	share.Status = model.ShareStatusDeleted
	command := newShareRefreshStateCommand(share.Replica.NodeID)
	if err := s.repo.UpdateWithCommand(share, command); err != nil {
		return err
	}
	s.publishCommand(command)
	return nil
}

func (s *ShareService) IsNotFound(err error) bool {
	return errors.Is(err, ErrShareNotFound) || errors.Is(err, gorm.ErrRecordNotFound)
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
