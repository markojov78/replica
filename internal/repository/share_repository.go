package repository

import (
	"errors"
	"strings"

	"replica/internal/model"

	"gorm.io/gorm"
)

type ShareRepository struct {
	db *gorm.DB
}

func NewShareRepository(db *gorm.DB) *ShareRepository {
	return &ShareRepository{db: db}
}

func (r *ShareRepository) List() ([]model.Share, error) {
	var shares []model.Share
	err := r.db.Order("id asc").Find(&shares).Error
	return shares, err
}

type ShareListFilter struct {
	Status    string
	ReplicaID *uint
	Name      string
}

func (r *ShareRepository) ListPage(page, perPage int, filter ShareListFilter) ([]model.Share, int64, error) {
	query := applyShareFilters(r.db.Model(&model.Share{}), filter)

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var shares []model.Share
	err := applyShareFilters(r.db.Preload("Replica").Preload("Replica.Inventory"), filter).
		Order("shares.id asc").
		Limit(perPage).
		Offset((page - 1) * perPage).
		Find(&shares).Error
	if err != nil {
		return nil, 0, err
	}

	return shares, total, nil
}

func (r *ShareRepository) ListForNode(nodeID string) ([]model.Share, error) {
	var shares []model.Share
	err := r.db.
		Joins("JOIN replicas ON replicas.id = shares.replica_id").
		Preload("Replica").
		Preload("Replica.Inventory").
		Where("replicas.node_id = ?", nodeID).
		Order("shares.id asc").
		Find(&shares).Error
	return shares, err
}

func (r *ShareRepository) FindByID(id uint) (*model.Share, error) {
	var share model.Share
	if err := r.db.Preload("Replica").Preload("Replica.Inventory").First(&share, id).Error; err != nil {
		return nil, err
	}
	return &share, nil
}

func (r *ShareRepository) FindReplicaByID(id uint) (*model.Replica, error) {
	var replica model.Replica
	if err := r.db.Preload("Inventory").First(&replica, id).Error; err != nil {
		return nil, err
	}
	return &replica, nil
}

func (r *ShareRepository) HasActiveShareForReplica(replicaID uint, excludeShareID uint) (bool, error) {
	query := r.db.Model(&model.Share{}).
		Where("replica_id = ? AND status = ?", replicaID, model.ShareStatusActive)
	if excludeShareID > 0 {
		query = query.Where("id <> ?", excludeShareID)
	}

	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *ShareRepository) Create(share *model.Share) error {
	return r.db.Create(share).Error
}

func (r *ShareRepository) CreateWithUserPermissions(share *model.Share, permissions []UserPermissionDetails) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(share).Error; err != nil {
			return err
		}
		return replaceShareUserPermissions(tx, share.ID, permissions)
	})
}

func (r *ShareRepository) CreateWithPermissions(share *model.Share, userPermissions []UserPermissionDetails, anonymousPermissions []string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(share).Error; err != nil {
			return err
		}
		if err := replaceShareUserPermissions(tx, share.ID, userPermissions); err != nil {
			return err
		}
		return replaceShareAnonymousPermissions(tx, share.ID, anonymousPermissions)
	})
}

func (r *ShareRepository) Update(share *model.Share) error {
	return r.db.Save(share).Error
}

func (r *ShareRepository) UpdateWithUserPermissions(share *model.Share, permissions []UserPermissionDetails, replacePermissions bool) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(share).Error; err != nil {
			return err
		}
		if !replacePermissions {
			return nil
		}
		return replaceShareUserPermissions(tx, share.ID, permissions)
	})
}

func (r *ShareRepository) UpdateWithPermissions(share *model.Share, userPermissions []UserPermissionDetails, replaceUserPermissions bool, anonymousPermissions []string, replaceAnonymousPermissions bool) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(share).Error; err != nil {
			return err
		}
		if replaceUserPermissions {
			if err := replaceShareUserPermissions(tx, share.ID, userPermissions); err != nil {
				return err
			}
		}
		if replaceAnonymousPermissions {
			if err := replaceShareAnonymousPermissions(tx, share.ID, anonymousPermissions); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *ShareRepository) UserPermissions(shareID uint) ([]UserPermissionDetails, error) {
	var users []model.ShareUser
	if err := r.db.Where("share_id = ? AND anonymous = ?", shareID, false).Order("user_id asc").Find(&users).Error; err != nil {
		return nil, err
	}

	result := make([]UserPermissionDetails, 0, len(users))
	for _, user := range users {
		var permissions []model.SharePermission
		if err := r.db.Where("share_user_id = ?", user.ID).Order("permission asc").Find(&permissions).Error; err != nil {
			return nil, err
		}
		if len(permissions) == 0 {
			continue
		}
		if user.UserID == nil {
			continue
		}
		detail := UserPermissionDetails{
			UserID:      *user.UserID,
			Permissions: make([]string, 0, len(permissions)),
		}
		for _, permission := range permissions {
			detail.Permissions = append(detail.Permissions, permission.Permission)
		}
		result = append(result, detail)
	}
	return result, nil
}

func (r *ShareRepository) AnonymousPermissions(shareID uint) ([]string, error) {
	var user model.ShareUser
	if err := r.db.Where("share_id = ? AND anonymous = ?", shareID, true).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return []string{}, nil
		}
		return nil, err
	}

	var permissions []model.SharePermission
	if err := r.db.Where("share_user_id = ?", user.ID).Order("permission asc").Find(&permissions).Error; err != nil {
		return nil, err
	}
	result := make([]string, 0, len(permissions))
	for _, permission := range permissions {
		result = append(result, permission.Permission)
	}
	return result, nil
}

func applyShareFilters(query *gorm.DB, filter ShareListFilter) *gorm.DB {
	if strings.TrimSpace(filter.Status) != "" {
		query = query.Where("status = ?", strings.TrimSpace(filter.Status))
	}
	if filter.ReplicaID != nil {
		query = query.Where("replica_id = ?", *filter.ReplicaID)
	}
	if strings.TrimSpace(filter.Name) != "" {
		query = query.Where("name = ?", strings.TrimSpace(filter.Name))
	}
	return query
}

func replaceShareUserPermissions(tx *gorm.DB, shareID uint, permissions []UserPermissionDetails) error {
	var users []model.ShareUser
	if err := tx.Where("share_id = ? AND anonymous = ?", shareID, false).Find(&users).Error; err != nil {
		return err
	}
	userIDs := make([]uint, 0, len(users))
	for _, user := range users {
		userIDs = append(userIDs, user.ID)
	}
	if len(userIDs) > 0 {
		if err := tx.Where("share_user_id IN ?", userIDs).Delete(&model.SharePermission{}).Error; err != nil {
			return err
		}
		if err := tx.Where("id IN ?", userIDs).Delete(&model.ShareUser{}).Error; err != nil {
			return err
		}
	}

	for _, permission := range permissions {
		userID := permission.UserID
		shareUser := &model.ShareUser{
			UserID:    &userID,
			ShareID:   shareID,
			Anonymous: false,
		}
		if err := tx.Create(shareUser).Error; err != nil {
			return err
		}
		rows := make([]model.SharePermission, 0, len(permission.Permissions))
		for _, action := range permission.Permissions {
			rows = append(rows, model.SharePermission{
				ShareUserID: shareUser.ID,
				Permission:  action,
			})
		}
		if len(rows) > 0 {
			if err := tx.Create(&rows).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func replaceShareAnonymousPermissions(tx *gorm.DB, shareID uint, permissions []string) error {
	var users []model.ShareUser
	if err := tx.Where("share_id = ? AND anonymous = ?", shareID, true).Find(&users).Error; err != nil {
		return err
	}
	userIDs := make([]uint, 0, len(users))
	for _, user := range users {
		userIDs = append(userIDs, user.ID)
	}
	if len(userIDs) > 0 {
		if err := tx.Where("share_user_id IN ?", userIDs).Delete(&model.SharePermission{}).Error; err != nil {
			return err
		}
		if err := tx.Where("id IN ?", userIDs).Delete(&model.ShareUser{}).Error; err != nil {
			return err
		}
	}
	if len(permissions) == 0 {
		return nil
	}

	shareUser := &model.ShareUser{
		ShareID:   shareID,
		Anonymous: true,
	}
	if err := tx.Create(shareUser).Error; err != nil {
		return err
	}
	rows := make([]model.SharePermission, 0, len(permissions))
	for _, action := range permissions {
		rows = append(rows, model.SharePermission{
			ShareUserID: shareUser.ID,
			Permission:  action,
		})
	}
	return tx.Create(&rows).Error
}
