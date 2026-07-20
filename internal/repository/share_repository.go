package repository

import (
	"errors"
	"sort"
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
	Status      string
	InventoryID *uint
	ReplicaID   *uint
	NodeID      string
	Name        string
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

func (r *ShareRepository) CreateWithPermissions(share *model.Share, userPermissions []UserPermissionDetails, anonymousPermissions []string, command *model.Command) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(share).Error; err != nil {
			return err
		}
		if err := replaceShareUserPermissions(tx, share.ID, userPermissions); err != nil {
			return err
		}
		if err := replaceShareAnonymousPermissions(tx, share.ID, anonymousPermissions); err != nil {
			return err
		}
		return createShareRefreshCommand(tx, command)
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

func (r *ShareRepository) UpdateWithPermissions(share *model.Share, userPermissions []UserPermissionDetails, replaceUserPermissions bool, anonymousPermissions []string, replaceAnonymousPermissions bool, command *model.Command) error {
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
		return createShareRefreshCommand(tx, command)
	})
}

func (r *ShareRepository) UpdateWithCommand(share *model.Share, command *model.Command) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(share).Error; err != nil {
			return err
		}
		return createShareRefreshCommand(tx, command)
	})
}

func createShareRefreshCommand(tx *gorm.DB, command *model.Command) error {
	if command == nil {
		return nil
	}
	if len(command.Payload) == 0 {
		command.Payload = []byte("{}")
	}
	return tx.Create(command).Error
}

// compile per-user permisions derived from roles
func (r *ShareRepository) RoleDerivedPermissions() ([]UserPermissionDetails, error) {
	type userPermissionRow struct {
		UserID     uint
		Permission string
	}

	var rows []userPermissionRow
	if err := r.db.Table("permissions").
		Select("user_role.user_id, permissions.actions AS permission").
		Joins("JOIN user_role ON user_role.role_id = permissions.role_id").
		Where("permissions.resource = ?", model.PermissionResourceShares).
		Order("user_role.user_id asc, permissions.actions asc").
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	permissionsByUser := make(map[uint]map[string]bool)
	for _, row := range rows {
		if permissionsByUser[row.UserID] == nil {
			permissionsByUser[row.UserID] = make(map[string]bool)
		}
		permissionsByUser[row.UserID][row.Permission] = true
	}

	userIDs := make([]uint, 0, len(permissionsByUser))
	for userID := range permissionsByUser {
		userIDs = append(userIDs, userID)
	}
	sort.Slice(userIDs, func(i, j int) bool {
		return userIDs[i] < userIDs[j]
	})

	result := make([]UserPermissionDetails, 0, len(userIDs))
	for _, userID := range userIDs {
		permissions := make([]string, 0, len(permissionsByUser[userID]))
		for permission := range permissionsByUser[userID] {
			permissions = append(permissions, permission)
		}
		sort.Strings(permissions)
		result = append(result, UserPermissionDetails{
			UserID:      userID,
			Permissions: permissions,
		})
	}
	return result, nil
}

// get per-user permissions for share
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
	if filter.InventoryID != nil || strings.TrimSpace(filter.NodeID) != "" {
		query = query.Joins("JOIN replicas ON replicas.id = shares.replica_id")
	}
	if strings.TrimSpace(filter.Status) != "" {
		query = query.Where("shares.status = ?", strings.TrimSpace(filter.Status))
	}
	if filter.InventoryID != nil {
		query = query.Where("replicas.inventory_id = ?", *filter.InventoryID)
	}
	if filter.ReplicaID != nil {
		query = query.Where("shares.replica_id = ?", *filter.ReplicaID)
	}
	if strings.TrimSpace(filter.NodeID) != "" {
		query = query.Where("replicas.node_id = ?", strings.TrimSpace(filter.NodeID))
	}
	if strings.TrimSpace(filter.Name) != "" {
		query = query.Where("shares.name = ?", strings.TrimSpace(filter.Name))
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
