package service

import (
	"errors"
	"strings"

	"replica/internal/model"
	"replica/internal/repository"

	"gorm.io/gorm"
)

type ShareService struct {
	repo *repository.ShareRepository
}

var (
	ErrInvalidShareStatus = errors.New("invalid share status")
	ErrInvalidShareName   = errors.New("invalid share name")
	ErrShareNotFound      = errors.New("share not found")
	ErrShareAlreadyExists = errors.New("share already exists")
)

type ShareDetails struct {
	ID          uint   `json:"id"`
	InventoryID uint   `json:"inventory_id"`
	ReplicaID   uint   `json:"replica_id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
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
	ReplicaID uint
	Name      *string
	Status    *string
	UserID    uint
}

type UpdateShareInput struct {
	Name   *string
	Status *string
}

func NewShareService(repo *repository.ShareRepository) *ShareService {
	return &ShareService{repo: repo}
}

func (s *ShareService) List() ([]model.Share, error) {
	return s.repo.List()
}

func (s *ShareService) ListPage(page, perPage int, filter ShareListFilter) (*ShareList, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 20
	}
	if perPage > 100 {
		perPage = 100
	}

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
		items = append(items, *toShareDetails(&share))
	}

	return &ShareList{
		Items: items,
		Page:  page,
		Count: perPage,
		Total: total,
	}, nil
}

func (s *ShareService) Get(id uint) (*ShareDetails, error) {
	share, err := s.repo.FindByID(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrShareNotFound
		}
		return nil, err
	}
	return toShareDetails(share), nil
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
		ReplicaID: replica.ID,
		Name:      name,
		Status:    status,
		Replica:   *replica,
	}

	permissions := []string{
		string(model.PermissionActionRead),
		string(model.PermissionActionCreate),
		string(model.PermissionActionUpdate),
		string(model.PermissionActionDelete),
	}
	if err := s.repo.CreateWithUserPermissions(share, input.UserID, permissions); err != nil {
		return nil, err
	}
	return toShareDetails(share), nil
}

func (s *ShareService) Update(id uint, input UpdateShareInput) (*ShareDetails, error) {
	share, err := s.repo.FindByID(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrShareNotFound
		}
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

	if err := s.repo.Update(share); err != nil {
		return nil, err
	}
	return toShareDetails(share), nil
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
	return s.repo.Update(share)
}

func (s *ShareService) IsNotFound(err error) bool {
	return errors.Is(err, ErrShareNotFound) || errors.Is(err, gorm.ErrRecordNotFound)
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
		ID:          share.ID,
		InventoryID: share.Replica.InventoryID,
		ReplicaID:   share.ReplicaID,
		Name:        share.Name,
		Status:      string(share.Status),
	}
}
