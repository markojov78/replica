package service

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"replica/internal/model"
	"replica/internal/repository"

	"gorm.io/gorm"
)

const userLogMessageMaxBytes = 2048

// AuditContext identifies who performed an action and where it came from.
type AuditContext struct {
	ActorUserID   *uint
	SourceAddress string
}

type sourceAddressContextKey struct{}

// ContextWithSourceAddress returns a context carrying the client address used for user logs.
// An address already present in ctx is kept, so in-process forwarded requests keep the original client.
func ContextWithSourceAddress(ctx context.Context, address string) context.Context {
	if SourceAddress(ctx) != "" {
		return ctx
	}
	return context.WithValue(ctx, sourceAddressContextKey{}, address)
}

// SourceAddress returns the client address stored by ContextWithSourceAddress.
func SourceAddress(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	address, _ := ctx.Value(sourceAddressContextKey{}).(string)
	return address
}

// UserLogEntry is a user log row before node and timestamp are assigned.
type UserLogEntry struct {
	Action  model.UserLogAction
	Success bool
	Message string
	UserID  *uint
	RoleID  *uint
	Audit   AuditContext
}

// UserLogService writes user log rows. Log writes are part of the logged action:
// when a row cannot be written, the action fails.
type UserLogService struct {
	logs   *repository.UserLogRepository
	nodeID string
	now    func() time.Time
}

func NewUserLogService(logs *repository.UserLogRepository, nodeID string) *UserLogService {
	return &UserLogService{logs: logs, nodeID: nodeID, now: time.Now}
}

// UserLogWriter writes user log rows, optionally inside a transaction.
// A nil writer discards entries.
type UserLogWriter struct {
	service *UserLogService
	logs    *repository.UserLogRepository
}

// Transaction runs fn in one database transaction and passes a writer bound to it.
// Without a log service, fn runs without a transaction and a nil tx and writer.
func (s *UserLogService) Transaction(fn func(tx *gorm.DB, logs *UserLogWriter) error) error {
	if s == nil {
		return fn(nil, nil)
	}
	return s.logs.Transaction(func(tx *gorm.DB) error {
		return fn(tx, &UserLogWriter{service: s, logs: s.logs.WithTx(tx)})
	})
}

// Write writes one entry outside of any transaction.
func (s *UserLogService) Write(entry UserLogEntry) error {
	if s == nil {
		return nil
	}
	return (&UserLogWriter{service: s, logs: s.logs}).Write(entry)
}

// Failure records a failed action and returns err, or the log write error when the entry
// cannot be written.
func (s *UserLogService) Failure(entry UserLogEntry, err error) error {
	entry.Success = false
	if logErr := s.Write(entry); logErr != nil {
		return logErr
	}
	return err
}

func (w *UserLogWriter) Write(entry UserLogEntry) error {
	if w == nil {
		return nil
	}
	row := &model.UserLog{
		Timestamp:    w.service.now().UTC(),
		Action:       entry.Action,
		Success:      entry.Success,
		Message:      optionalString(truncateUTF8(entry.Message, userLogMessageMaxBytes)),
		UserID:       entry.UserID,
		RoleID:       entry.RoleID,
		ActionUserID: entry.Audit.ActorUserID,
		NodeID:       w.service.nodeID,
	}
	if address := strings.TrimSpace(entry.Audit.SourceAddress); address != "" {
		row.SourceAddress = optionalString(truncateUTF8(address, 255))
	}
	if err := w.logs.Create(row); err != nil {
		return fmt.Errorf("write user log: %w", err)
	}
	return nil
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func truncateUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func uintPtr(value uint) *uint {
	return &value
}
