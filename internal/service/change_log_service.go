package service

import (
	"fmt"
	"strings"
	"time"

	"replica/internal/model"
	"replica/internal/repository"

	"gorm.io/gorm"
)

// InventoryLogEntry is an inventory_logs row before the timestamp is assigned.
type InventoryLogEntry struct {
	Action      model.InventoryLogAction
	Message     string
	InventoryID uint
	UserID      *uint
	Audit       AuditContext
}

// ReplicaLogEntry is a replica_logs row before the timestamp is assigned.
type ReplicaLogEntry struct {
	Action      model.ReplicaLogAction
	Message     string
	ReplicaID   uint
	InventoryID uint
	NodeID      string
	Audit       AuditContext
}

// NodeLogEntry is a node_logs row before the timestamp is assigned.
type NodeLogEntry struct {
	Action  model.NodeLogAction
	Message string
	NodeID  string
	Audit   AuditContext
}

// ChangeLogService records successful inventory, replica and node changes made through the
// admin API. Rows are written in the transaction of the change: when a row cannot be written,
// the change fails.
type ChangeLogService struct {
	logs *repository.ChangeLogRepository
	now  func() time.Time
}

func NewChangeLogService(logs *repository.ChangeLogRepository) *ChangeLogService {
	return &ChangeLogService{logs: logs, now: time.Now}
}

// ChangeLogWriter writes change log rows inside a transaction. A nil writer discards entries.
type ChangeLogWriter struct {
	service *ChangeLogService
	logs    *repository.ChangeLogRepository
}

// Transaction runs fn in one database transaction and passes a writer bound to it.
// Without a log service, fn runs without a transaction and a nil tx and writer.
func (s *ChangeLogService) Transaction(fn func(tx *gorm.DB, logs *ChangeLogWriter) error) error {
	if s == nil {
		return fn(nil, nil)
	}
	return s.logs.Transaction(func(tx *gorm.DB) error {
		return fn(tx, &ChangeLogWriter{service: s, logs: s.logs.WithTx(tx)})
	})
}

func (w *ChangeLogWriter) Inventory(entry InventoryLogEntry) error {
	if w == nil {
		return nil
	}
	return w.create(&model.InventoryLog{
		Timestamp:     w.service.now().UTC(),
		Action:        entry.Action,
		Message:       changeLogMessage(entry.Message),
		InventoryID:   entry.InventoryID,
		UserID:        entry.UserID,
		ActionUserID:  entry.Audit.ActorUserID,
		SourceAddress: changeLogAddress(entry.Audit),
	})
}

func (w *ChangeLogWriter) Replica(entry ReplicaLogEntry) error {
	if w == nil {
		return nil
	}
	return w.create(&model.ReplicaLog{
		Timestamp:     w.service.now().UTC(),
		Action:        entry.Action,
		Message:       changeLogMessage(entry.Message),
		ReplicaID:     entry.ReplicaID,
		InventoryID:   entry.InventoryID,
		ReplicaNodeID: entry.NodeID,
		ActionUserID:  entry.Audit.ActorUserID,
		SourceAddress: changeLogAddress(entry.Audit),
	})
}

func (w *ChangeLogWriter) Node(entry NodeLogEntry) error {
	if w == nil {
		return nil
	}
	return w.create(&model.NodeLog{
		Timestamp:     w.service.now().UTC(),
		Action:        entry.Action,
		Message:       changeLogMessage(entry.Message),
		NodeID:        entry.NodeID,
		ActionUserID:  entry.Audit.ActorUserID,
		SourceAddress: changeLogAddress(entry.Audit),
	})
}

func (w *ChangeLogWriter) create(row any) error {
	if err := w.logs.Create(row); err != nil {
		return fmt.Errorf("write change log: %w", err)
	}
	return nil
}

func changeLogMessage(message string) *string {
	return optionalString(truncateUTF8(message, userLogMessageMaxBytes))
}

func changeLogAddress(audit AuditContext) *string {
	return optionalString(truncateUTF8(strings.TrimSpace(audit.SourceAddress), 255))
}
