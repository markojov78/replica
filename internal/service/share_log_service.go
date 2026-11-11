package service

import (
	"fmt"
	"strings"
	"time"

	"replica/internal/model"
	"replica/internal/repository"

	"gorm.io/gorm"
)

// ShareLogEntry is a share log row before node and timestamp are assigned.
type ShareLogEntry struct {
	Action    model.ShareLogAction
	Success   bool
	Message   string
	ShareID   *uint
	UserID    *uint
	Anonymous bool
	Audit     AuditContext
}

// ShareLogService writes share log rows. Log writes are part of the logged action:
// when a row cannot be written, the action fails.
type ShareLogService struct {
	logs   *repository.ShareLogRepository
	nodeID string
	now    func() time.Time
}

func NewShareLogService(logs *repository.ShareLogRepository, nodeID string) *ShareLogService {
	return &ShareLogService{logs: logs, nodeID: nodeID, now: time.Now}
}

// ShareLogWriter writes share log rows, optionally inside a transaction.
// A nil writer discards entries.
type ShareLogWriter struct {
	service *ShareLogService
	logs    *repository.ShareLogRepository
}

// Transaction runs fn in one database transaction and passes a writer bound to it.
// Without a log service, fn runs without a transaction and a nil tx and writer.
func (s *ShareLogService) Transaction(fn func(tx *gorm.DB, logs *ShareLogWriter) error) error {
	if s == nil {
		return fn(nil, nil)
	}
	return s.logs.Transaction(func(tx *gorm.DB) error {
		return fn(tx, &ShareLogWriter{service: s, logs: s.logs.WithTx(tx)})
	})
}

// Write writes one entry outside of any transaction.
func (s *ShareLogService) Write(entry ShareLogEntry) error {
	if s == nil {
		return nil
	}
	return (&ShareLogWriter{service: s, logs: s.logs}).Write(entry)
}

// Failure records a failed action and returns err, or the log write error when the entry
// cannot be written.
func (s *ShareLogService) Failure(entry ShareLogEntry, err error) error {
	entry.Success = false
	if logErr := s.Write(entry); logErr != nil {
		return logErr
	}
	return err
}

func (w *ShareLogWriter) Write(entry ShareLogEntry) error {
	if w == nil {
		return nil
	}
	row := &model.ShareLog{
		Timestamp:    w.service.now().UTC(),
		Action:       entry.Action,
		Success:      entry.Success,
		Message:      optionalString(truncateUTF8(entry.Message, userLogMessageMaxBytes)),
		ShareID:      entry.ShareID,
		UserID:       entry.UserID,
		Anonymous:    entry.Anonymous,
		ActionUserID: entry.Audit.ActorUserID,
		NodeID:       w.service.nodeID,
	}
	if nodeID := strings.TrimSpace(entry.Audit.NodeID); nodeID != "" {
		row.NodeID = nodeID
	}
	if address := strings.TrimSpace(entry.Audit.SourceAddress); address != "" {
		row.SourceAddress = optionalString(truncateUTF8(address, 255))
	}
	if err := w.logs.Create(row); err != nil {
		return fmt.Errorf("write share log: %w", err)
	}
	return nil
}
