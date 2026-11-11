package repository

import (
	"errors"
	"time"

	"replica/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// NodeLogRepository stores log rows uploaded by storage nodes and the per-node upload cursor.
type NodeLogRepository struct {
	db *gorm.DB
}

func NewNodeLogRepository(db *gorm.DB) *NodeLogRepository {
	return &NodeLogRepository{db: db}
}

// LastID returns the highest node log id processed for nodeID, or 0 when nothing was uploaded.
func (r *NodeLogRepository) LastID(nodeID string) (uint64, error) {
	var cursor model.NodeLogCursor
	if err := r.db.First(&cursor, "node_id = ?", nodeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return cursor.LastID, nil
}

// StoreBatch inserts share access rows that are not stored yet and advances the node cursor
// to at least lastID, in one transaction. It returns the ids of rows that already existed and
// the cursor after the update.
func (r *NodeLogRepository) StoreBatch(nodeID string, shareAccess []model.ShareAccessLog, lastID uint64, now time.Time) ([]uint64, uint64, error) {
	var duplicates []uint64
	var cursor model.NodeLogCursor
	err := r.db.Transaction(func(tx *gorm.DB) error {
		for i := range shareAccess {
			result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&shareAccess[i])
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				duplicates = append(duplicates, shareAccess[i].ID)
			}
		}

		err := tx.First(&cursor, "node_id = ?", nodeID).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			cursor = model.NodeLogCursor{NodeID: nodeID, LastID: lastID, UpdatedAt: now}
			return tx.Create(&cursor).Error
		case err != nil:
			return err
		case lastID > cursor.LastID:
			cursor.LastID = lastID
			cursor.UpdatedAt = now
			return tx.Save(&cursor).Error
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return duplicates, cursor.LastID, nil
}
