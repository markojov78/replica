package repository

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"dropoutbox/internal/model"

	"gorm.io/gorm"
)

type ReplicaRepository struct {
	db *gorm.DB
}

func NewReplicaRepository(db *gorm.DB) *ReplicaRepository {
	return &ReplicaRepository{db: db}
}

type ReplicaFileUpdate struct {
	FileID       *uint
	Action       model.ReplicaFileAction
	RelativeURI  string
	FileSize     int64
	FileHash     string
	CreatedTime  time.Time
	ModifiedTime time.Time
}

var ErrInvalidReplicaFileUpdate = errors.New("invalid replica file update")

type ReplicaInventoryFile struct {
	FileID           uint
	ReplicaID        uint
	InventoryID      uint
	RelativeURI      string
	Size             int64
	Hash             string
	InventoryStatus  string
	InventoryVersion uint
	ReplicaStatus    string
	ReplicaVersion   uint
	Created          time.Time
	Modified         time.Time
}

type ReconcileSource struct {
	ReplicaID     uint
	NodeID        string
	NodeAddress   string
	NewestVersion uint
}

type ReconcilePayloadBuilder func(destination model.Replica, source ReconcileSource) (json.RawMessage, error)

func (r *ReplicaRepository) List() ([]model.Replica, error) {
	var replicas []model.Replica
	err := r.db.Order("id asc").Find(&replicas).Error
	return replicas, err
}

func (r *ReplicaRepository) Create(replica *model.Replica) error {
	return r.db.Create(replica).Error
}

func (r *ReplicaRepository) CreateWithPendingFiles(replica *model.Replica, command *model.Command, payloadBuilders ...ReconcilePayloadBuilder) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(replica).Error; err != nil {
			return err
		}

		var files []model.InventoryFile
		if err := tx.Where("inventory_id = ?", replica.InventoryID).Order("id asc").Find(&files).Error; err != nil {
			return err
		}

		replicaFiles := make([]model.ReplicaFile, 0, len(files))
		for _, file := range files {
			replicaFiles = append(replicaFiles, model.ReplicaFile{
				FileID:    file.ID,
				ReplicaID: replica.ID,
				Version:   0,
				Status:    model.ReplicaFileStatusPending,
			})
		}
		if len(replicaFiles) > 0 {
			if err := tx.Create(&replicaFiles).Error; err != nil {
				return err
			}
		}

		if command == nil {
			return nil
		}
		command.NodeID = replica.NodeID
		if command.Type == model.NodeCommandTypeReconcileReplica {
			if len(payloadBuilders) > 0 {
				source, err := r.selectReconcileSource(tx, *replica)
				if err != nil {
					return err
				}
				payload, err := payloadBuilders[0](*replica, source)
				if err != nil {
					return err
				}
				command.Payload = payload
			} else if len(command.Payload) == 0 {
				payload, err := json.Marshal(struct {
					DestinationReplicaID uint `json:"destination_replica_id"`
				}{
					DestinationReplicaID: replica.ID,
				})
				if err != nil {
					return err
				}
				command.Payload = payload
			}
		} else if len(command.Payload) == 0 {
			command.Payload = []byte("{}")
		}
		return tx.Create(command).Error
	})
}

func (r *ReplicaRepository) selectReconcileSource(tx *gorm.DB, destination model.Replica) (ReconcileSource, error) {
	if destination.UpstreamReplicaID != nil {
		var source ReconcileSource
		err := tx.
			Table("replicas").
			Select("replicas.id AS replica_id, replicas.node_id AS node_id, nodes.address AS node_address").
			Joins("JOIN nodes ON nodes.id = replicas.node_id").
			Where("replicas.id = ?", *destination.UpstreamReplicaID).
			Scan(&source).Error
		if err != nil {
			return ReconcileSource{}, err
		}
		if source.ReplicaID == 0 {
			return ReconcileSource{}, gorm.ErrRecordNotFound
		}
		return source, nil
	}

	var pendingCount int64
	if err := tx.Model(&model.ReplicaFile{}).
		Where("replica_id = ? AND status = ?", destination.ID, model.ReplicaFileStatusPending).
		Count(&pendingCount).Error; err != nil {
		return ReconcileSource{}, err
	}
	if pendingCount == 0 {
		return ReconcileSource{}, gorm.ErrRecordNotFound
	}

	source, err := r.selectMultiDirectionalReconcileSource(tx, destination, pendingCount, true)
	if err == nil {
		return source, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return ReconcileSource{}, err
	}
	return r.selectMultiDirectionalReconcileSource(tx, destination, pendingCount, false)
}

func (r *ReplicaRepository) selectMultiDirectionalReconcileSource(tx *gorm.DB, destination model.Replica, pendingCount int64, sameNode bool) (ReconcileSource, error) {
	var source ReconcileSource
	query := tx.
		Table("replicas AS candidate").
		Select(`
			candidate.id AS replica_id,
			candidate.node_id AS node_id,
			nodes.address AS node_address,
			MAX(source_files.version) AS newest_version
		`).
		Joins("JOIN nodes ON nodes.id = candidate.node_id").
		Joins("JOIN replica_files AS source_files ON source_files.replica_id = candidate.id").
		Joins("JOIN replica_files AS destination_files ON destination_files.file_id = source_files.file_id AND destination_files.replica_id = ?", destination.ID).
		Joins("JOIN inventory_files ON inventory_files.id = source_files.file_id").
		Where("candidate.inventory_id = ?", destination.InventoryID).
		Where("candidate.upstream_replica_id IS NULL").
		Where("candidate.id <> ?", destination.ID).
		Where("candidate.status = ?", model.ReplicaStatusActive).
		Where("destination_files.status = ?", model.ReplicaFileStatusPending).
		Where("source_files.status = ?", model.ReplicaFileStatusSynchronized).
		Where("source_files.version = inventory_files.version").
		Group("candidate.id, candidate.node_id, nodes.address, nodes.last_seen").
		Having("COUNT(DISTINCT source_files.file_id) = ?", pendingCount)
	if sameNode {
		query = query.
			Where("candidate.node_id = ?", destination.NodeID).
			Order("MAX(source_files.version) DESC")
	} else {
		query = query.
			Where("candidate.node_id <> ?", destination.NodeID).
			Order("nodes.last_seen DESC")
	}
	err := query.
		Order("candidate.id ASC").
		Limit(1).
		Scan(&source).Error
	if err != nil {
		return ReconcileSource{}, err
	}
	if source.ReplicaID == 0 {
		return ReconcileSource{}, gorm.ErrRecordNotFound
	}
	return source, nil
}

func (r *ReplicaRepository) FindByID(id uint) (*model.Replica, error) {
	var replica model.Replica
	if err := r.db.First(&replica, id).Error; err != nil {
		return nil, err
	}
	return &replica, nil
}

func (r *ReplicaRepository) FindFileByID(replicaID, fileID uint) (*model.ReplicaFile, error) {
	var file model.ReplicaFile
	if err := r.db.Where("replica_id = ? AND file_id = ?", replicaID, fileID).First(&file).Error; err != nil {
		return nil, err
	}
	return &file, nil
}

type ReplicaListFilter struct {
	InventoryID *uint
	NodeID      string
	URIPrefix   string
}

type ReplicaFileListFilter struct {
	Status  string
	Version *uint
}

func (r *ReplicaRepository) ListFiltered(filter ReplicaListFilter) ([]model.Replica, error) {
	var replicas []model.Replica
	query := r.db.Order("id asc")
	if filter.InventoryID != nil {
		query = query.Where("inventory_id = ?", *filter.InventoryID)
	}
	if strings.TrimSpace(filter.NodeID) != "" {
		query = query.Where("node_id = ?", strings.TrimSpace(filter.NodeID))
	}
	if strings.TrimSpace(filter.URIPrefix) != "" {
		query = query.Where("uri LIKE ?", strings.TrimSpace(filter.URIPrefix)+"%")
	}

	err := query.Find(&replicas).Error
	return replicas, err
}

func (r *ReplicaRepository) ListPage(page, perPage int, filter ReplicaListFilter) ([]model.Replica, int64, error) {
	query := r.db.Model(&model.Replica{})
	if filter.InventoryID != nil {
		query = query.Where("inventory_id = ?", *filter.InventoryID)
	}
	if strings.TrimSpace(filter.NodeID) != "" {
		query = query.Where("node_id = ?", strings.TrimSpace(filter.NodeID))
	}
	if strings.TrimSpace(filter.URIPrefix) != "" {
		query = query.Where("uri LIKE ?", strings.TrimSpace(filter.URIPrefix)+"%")
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var replicas []model.Replica
	err := r.db.
		Scopes(func(tx *gorm.DB) *gorm.DB {
			if filter.InventoryID != nil {
				tx = tx.Where("inventory_id = ?", *filter.InventoryID)
			}
			if strings.TrimSpace(filter.NodeID) != "" {
				tx = tx.Where("node_id = ?", strings.TrimSpace(filter.NodeID))
			}
			if strings.TrimSpace(filter.URIPrefix) != "" {
				tx = tx.Where("uri LIKE ?", strings.TrimSpace(filter.URIPrefix)+"%")
			}
			return tx
		}).
		Order("id asc").
		Limit(perPage).
		Offset((page - 1) * perPage).
		Find(&replicas).Error
	if err != nil {
		return nil, 0, err
	}

	return replicas, total, nil
}

func (r *ReplicaRepository) ListFiles(replicaID uint, page, perPage int, filter ReplicaFileListFilter) ([]model.ReplicaFile, int64, error) {
	query := r.db.Model(&model.ReplicaFile{}).Where("replica_id = ?", replicaID)
	if filter.Status != "" {
		query = query.Where("status = ?", filter.Status)
	}
	if filter.Version != nil {
		query = query.Where("version = ?", *filter.Version)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var files []model.ReplicaFile
	err := r.db.
		Where("replica_id = ?", replicaID).
		Scopes(func(tx *gorm.DB) *gorm.DB {
			if filter.Status != "" {
				tx = tx.Where("status = ?", filter.Status)
			}
			if filter.Version != nil {
				tx = tx.Where("version = ?", *filter.Version)
			}
			return tx
		}).
		Order("file_id asc").
		Limit(perPage).
		Offset((page - 1) * perPage).
		Find(&files).Error
	if err != nil {
		return nil, 0, err
	}

	return files, total, nil
}

func (r *ReplicaRepository) ListInventoryFiles(replicaID uint, filters ...ReplicaFileListFilter) ([]ReplicaInventoryFile, error) {
	var filter ReplicaFileListFilter
	if len(filters) > 0 {
		filter = filters[0]
	}

	var files []ReplicaInventoryFile
	query := r.db.
		Table("replica_files").
		Select(`
			replica_files.file_id AS file_id,
			replica_files.replica_id AS replica_id,
			inventory_files.inventory_id AS inventory_id,
			inventory_files.relative_uri AS relative_uri,
			inventory_files.size AS size,
			inventory_files.hash AS hash,
			inventory_files.status AS inventory_status,
			inventory_files.version AS inventory_version,
			replica_files.status AS replica_status,
			replica_files.version AS replica_version,
			inventory_files.created AS created,
			inventory_files.modified AS modified
		`).
		Joins("JOIN inventory_files ON inventory_files.id = replica_files.file_id").
		Where("replica_files.replica_id = ?", replicaID)
	if filter.Status != "" {
		query = query.Where("replica_files.status = ?", filter.Status)
	}
	err := query.
		Order("inventory_files.relative_uri asc").
		Scan(&files).Error
	if err != nil {
		return nil, err
	}
	return files, nil
}

func (r *ReplicaRepository) Update(replica *model.Replica) error {
	return r.db.Save(replica).Error
}

func (r *ReplicaRepository) UpdateFileStatus(replicaID, fileID uint, status model.ReplicaFileStatus, version *uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		var replicaFile model.ReplicaFile
		if err := tx.Where("replica_id = ? AND file_id = ?", replicaID, fileID).First(&replicaFile).Error; err != nil {
			return err
		}

		if status == model.ReplicaFileStatusSynchronized {
			if version == nil {
				return ErrInvalidReplicaFileUpdate
			}
			var inventoryFile model.InventoryFile
			if err := tx.First(&inventoryFile, fileID).Error; err != nil {
				return err
			}
			if inventoryFile.Version != *version {
				return ErrInvalidReplicaFileUpdate
			}
			replicaFile.Version = *version
		}

		replicaFile.Status = status
		return tx.Save(&replicaFile).Error
	})
}

func (r *ReplicaRepository) ReportFileChanges(replicaID uint, updates []ReplicaFileUpdate, payloadBuilders ...ReconcilePayloadBuilder) ([]model.Command, error) {
	var commands []model.Command
	err := r.db.Transaction(func(tx *gorm.DB) error {
		var replica model.Replica
		if err := tx.First(&replica, replicaID).Error; err != nil {
			return err
		}

		affectedReplicaIDs := make(map[uint]struct{})
		for _, update := range updates {
			if update.Action == model.ReplicaFileActionDeleted {
				affected, err := r.handleDeletedReportedFile(tx, replica, update)
				if err != nil {
					return err
				}
				for _, replicaID := range affected {
					affectedReplicaIDs[replicaID] = struct{}{}
				}
				continue
			}

			handled, err := r.handleNoContentReportedFile(tx, replica, update)
			if err != nil {
				return err
			}
			if handled {
				continue
			}

			file, created, restored, err := r.resolveReportedInventoryFile(tx, replica.InventoryID, update)
			if err != nil {
				return err
			}

			oldVersion := file.Version
			if created {
				file.Version = 1
			} else {
				file.Version++
			}
			file.Status = model.InventoryFileStatusActive
			file.Created = update.CreatedTime
			file.Modified = update.ModifiedTime
			file.Size = update.FileSize
			file.Hash = update.FileHash
			if err := tx.Save(file).Error; err != nil {
				return err
			}

			action := model.FileJournalActionUpdated
			if created {
				action = model.FileJournalActionCreated
			} else if restored {
				action = model.FileJournalActionRestored
			}
			journal := model.FileJournal{
				FileID:      file.ID,
				InventoryID: file.InventoryID,
				ReplicaID:   replica.ID,
				Version:     oldVersion,
				Action:      action,
				Timestamp:   update.ModifiedTime,
			}
			if err := tx.Create(&journal).Error; err != nil {
				return err
			}

			var sourceReplicaFile model.ReplicaFile
			err = tx.Where("file_id = ? AND replica_id = ?", file.ID, replica.ID).First(&sourceReplicaFile).Error
			switch {
			case err == nil:
				sourceReplicaFile.Version = file.Version
				sourceReplicaFile.Status = model.ReplicaFileStatusSynchronized
				if err := tx.Save(&sourceReplicaFile).Error; err != nil {
					return err
				}
			case errors.Is(err, gorm.ErrRecordNotFound):
				sourceReplicaFile = model.ReplicaFile{
					FileID:    file.ID,
					ReplicaID: replica.ID,
					Version:   file.Version,
					Status:    model.ReplicaFileStatusSynchronized,
				}
				if err := tx.Create(&sourceReplicaFile).Error; err != nil {
					return err
				}
			default:
				return err
			}

			var destinationReplicas []model.Replica
			if err := tx.
				Where("inventory_id = ? AND id <> ? AND status = ?", replica.InventoryID, replica.ID, model.ReplicaStatusActive).
				Order("id asc").
				Find(&destinationReplicas).Error; err != nil {
				return err
			}
			for _, destinationReplica := range destinationReplicas {
				var destinationReplicaFile model.ReplicaFile
				err := tx.Where("file_id = ? AND replica_id = ?", file.ID, destinationReplica.ID).First(&destinationReplicaFile).Error
				switch {
				case err == nil:
					destinationReplicaFile.Status = model.ReplicaFileStatusPending
					if err := tx.Save(&destinationReplicaFile).Error; err != nil {
						return err
					}
				case errors.Is(err, gorm.ErrRecordNotFound):
					destinationReplicaFile = model.ReplicaFile{
						FileID:    file.ID,
						ReplicaID: destinationReplica.ID,
						Version:   0,
						Status:    model.ReplicaFileStatusPending,
					}
					if err := tx.Create(&destinationReplicaFile).Error; err != nil {
						return err
					}
				default:
					return err
				}
				affectedReplicaIDs[destinationReplica.ID] = struct{}{}
			}
		}

		if len(payloadBuilders) > 0 && len(affectedReplicaIDs) > 0 {
			destinationIDs := make([]uint, 0, len(affectedReplicaIDs))
			for replicaID := range affectedReplicaIDs {
				destinationIDs = append(destinationIDs, replicaID)
			}
			sort.Slice(destinationIDs, func(i, j int) bool {
				return destinationIDs[i] < destinationIDs[j]
			})

			for _, destinationID := range destinationIDs {
				var destination model.Replica
				if err := tx.First(&destination, destinationID).Error; err != nil {
					return err
				}
				source, err := r.selectReconcileSource(tx, destination)
				if err != nil {
					return err
				}
				payload, err := payloadBuilders[0](destination, source)
				if err != nil {
					return err
				}
				command := model.Command{
					NodeID:  destination.NodeID,
					Type:    model.NodeCommandTypeReconcileReplica,
					Status:  model.NodeCommandStatusPending,
					Payload: payload,
				}
				now := time.Now().UTC()
				command.CreatedAt = now
				command.UpdatedAt = now
				if err := tx.Create(&command).Error; err != nil {
					return err
				}
				commands = append(commands, command)
			}
		}

		return nil
	})
	return commands, err
}

func (r *ReplicaRepository) handleDeletedReportedFile(tx *gorm.DB, replica model.Replica, update ReplicaFileUpdate) ([]uint, error) {
	if update.FileID == nil {
		return nil, ErrInvalidReplicaFileUpdate
	}

	var file model.InventoryFile
	if err := tx.First(&file, *update.FileID).Error; err != nil {
		return nil, err
	}
	if file.InventoryID != replica.InventoryID || file.RelativeURI != update.RelativeURI {
		return nil, ErrInvalidReplicaFileUpdate
	}

	if file.Status == model.InventoryFileStatusDeleted {
		return nil, r.upsertSynchronizedReplicaFile(tx, replica.ID, file.ID, file.Version)
	}

	oldVersion := file.Version
	file.Version++
	file.Status = model.InventoryFileStatusDeleted
	if err := tx.Save(&file).Error; err != nil {
		return nil, err
	}

	journal := model.FileJournal{
		FileID:      file.ID,
		InventoryID: file.InventoryID,
		ReplicaID:   replica.ID,
		Version:     oldVersion,
		Action:      model.FileJournalActionDeleted,
		Timestamp:   time.Now().UTC(),
	}
	if err := tx.Create(&journal).Error; err != nil {
		return nil, err
	}

	if err := r.upsertSynchronizedReplicaFile(tx, replica.ID, file.ID, file.Version); err != nil {
		return nil, err
	}

	var destinationReplicas []model.Replica
	if err := tx.
		Where("inventory_id = ? AND id <> ? AND status = ?", replica.InventoryID, replica.ID, model.ReplicaStatusActive).
		Order("id asc").
		Find(&destinationReplicas).Error; err != nil {
		return nil, err
	}
	affectedReplicaIDs := make([]uint, 0, len(destinationReplicas))
	for _, destinationReplica := range destinationReplicas {
		var destinationReplicaFile model.ReplicaFile
		err := tx.Where("file_id = ? AND replica_id = ?", file.ID, destinationReplica.ID).First(&destinationReplicaFile).Error
		switch {
		case err == nil:
			destinationReplicaFile.Status = model.ReplicaFileStatusPending
			if err := tx.Save(&destinationReplicaFile).Error; err != nil {
				return nil, err
			}
		case errors.Is(err, gorm.ErrRecordNotFound):
			destinationReplicaFile = model.ReplicaFile{
				FileID:    file.ID,
				ReplicaID: destinationReplica.ID,
				Version:   0,
				Status:    model.ReplicaFileStatusPending,
			}
			if err := tx.Create(&destinationReplicaFile).Error; err != nil {
				return nil, err
			}
		default:
			return nil, err
		}
		affectedReplicaIDs = append(affectedReplicaIDs, destinationReplica.ID)
	}

	return affectedReplicaIDs, nil
}

func (r *ReplicaRepository) handleNoContentReportedFile(tx *gorm.DB, replica model.Replica, update ReplicaFileUpdate) (bool, error) {
	var file model.InventoryFile
	var err error
	if update.FileID != nil {
		err = tx.First(&file, *update.FileID).Error
	} else {
		err = tx.
			Where("inventory_id = ? AND relative_uri = ?", replica.InventoryID, update.RelativeURI).
			First(&file).Error
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if file.InventoryID != replica.InventoryID || file.RelativeURI != update.RelativeURI {
		return false, nil
	}
	if file.Status != model.InventoryFileStatusActive || !sameReportedFileContent(file, update) {
		return false, nil
	}

	var replicaFile model.ReplicaFile
	err = tx.Where("file_id = ? AND replica_id = ?", file.ID, replica.ID).First(&replicaFile).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return true, r.upsertSynchronizedReplicaFile(tx, replica.ID, file.ID, file.Version)
	}
	if err != nil {
		return false, err
	}
	replicaFile.Version = file.Version
	replicaFile.Status = model.ReplicaFileStatusSynchronized
	return true, tx.Save(&replicaFile).Error
}

func (r *ReplicaRepository) upsertSynchronizedReplicaFile(tx *gorm.DB, replicaID, fileID, version uint) error {
	var replicaFile model.ReplicaFile
	err := tx.Where("file_id = ? AND replica_id = ?", fileID, replicaID).First(&replicaFile).Error
	switch {
	case err == nil:
		replicaFile.Version = version
		replicaFile.Status = model.ReplicaFileStatusSynchronized
		return tx.Save(&replicaFile).Error
	case errors.Is(err, gorm.ErrRecordNotFound):
		replicaFile = model.ReplicaFile{
			FileID:    fileID,
			ReplicaID: replicaID,
			Version:   version,
			Status:    model.ReplicaFileStatusSynchronized,
		}
		return tx.Create(&replicaFile).Error
	default:
		return err
	}
}

func sameReportedFileContent(file model.InventoryFile, update ReplicaFileUpdate) bool {
	return file.RelativeURI == update.RelativeURI &&
		file.Size == update.FileSize &&
		file.Hash == update.FileHash
}

func (r *ReplicaRepository) resolveReportedInventoryFile(tx *gorm.DB, inventoryID uint, update ReplicaFileUpdate) (*model.InventoryFile, bool, bool, error) {
	if update.FileID != nil {
		var file model.InventoryFile
		if err := tx.First(&file, *update.FileID).Error; err != nil {
			return nil, false, false, err
		}
		if file.InventoryID != inventoryID || file.RelativeURI != update.RelativeURI {
			return nil, false, false, ErrInvalidReplicaFileUpdate
		}
		return &file, false, file.Status == model.InventoryFileStatusDeleted, nil
	}

	var existing model.InventoryFile
	err := tx.
		Where("inventory_id = ? AND relative_uri = ?", inventoryID, update.RelativeURI).
		First(&existing).Error
	switch {
	case err == nil:
		if existing.Status == model.InventoryFileStatusDeleted {
			return &existing, false, true, nil
		}
		return nil, false, false, ErrInvalidReplicaFileUpdate
	case errors.Is(err, gorm.ErrRecordNotFound):
		return &model.InventoryFile{
			InventoryID: inventoryID,
			RelativeURI: update.RelativeURI,
			Status:      model.InventoryFileStatusActive,
		}, true, false, nil
	default:
		return nil, false, false, err
	}
}
