package service

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"replica/internal/config"
	"replica/internal/db"
	"replica/internal/model"
	"replica/internal/repository"

	"gorm.io/gorm"
)

func newNodeLogTestService(t *testing.T) (*NodeLogService, *gorm.DB) {
	t.Helper()
	database, err := db.Open(config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "node-logs.db")})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}
	return NewNodeLogService(repository.NewNodeLogRepository(database)), database
}

func shareAccessLine(id int, extra string) string {
	return fmt.Sprintf(`{"id":%d,"type":"share_access","timestamp":"2026-11-11T21:44:49Z","action":"read","success":true,"share_id":4,"file_id":41,"path":"docs/a.pdf","anonymous":true,"source":"http","bytes":10,"source_address":"203.0.113.5"%s}`, id, extra)
}

func TestNodeLogIngestStoresValidLinesAndRejectsOthers(t *testing.T) {
	svc, database := newNodeLogTestService(t)

	body := strings.Join([]string{
		shareAccessLine(1, `,"node_id":"spoofed"`),
		`not json`,
		strings.Replace(shareAccessLine(2, ""), `"action":"read"`, `"action":"preview"`, 1),
		`{"id":3,"type":"future_type"}`,
		shareAccessLine(4, ""),
		shareAccessLine(4, ""),
	}, "\n") + "\n"
	result, err := svc.Ingest("node-a", strings.NewReader(body))
	if err != nil {
		t.Fatalf("Ingest() error = %v", err)
	}
	if result.LastID != 4 {
		t.Fatalf("LastID = %d, want 4", result.LastID)
	}
	wantRejected := []NodeLogRejection{
		{ID: 0, Line: 2, Error: "invalid JSON"},
		{ID: 2, Line: 3, Error: `invalid action "preview"`},
		{ID: 3, Line: 4, Error: `unknown type "future_type"`},
		{ID: 4, Line: 6, Error: "duplicate id"},
	}
	if fmt.Sprint(result.Rejected) != fmt.Sprint(wantRejected) {
		t.Fatalf("Rejected = %+v, want %+v", result.Rejected, wantRejected)
	}

	var rows []model.ShareAccessLog
	if err := database.Order("id asc").Find(&rows).Error; err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	if len(rows) != 2 || rows[0].ID != 1 || rows[1].ID != 4 {
		t.Fatalf("rows = %+v, want ids 1 and 4", rows)
	}
	row := rows[0]
	if row.NodeID != "node-a" || row.Action != model.ShareAccessLogActionRead || !row.Success || !row.Anonymous ||
		row.Source != model.ShareAccessLogSourceHTTP || row.ShareID == nil || *row.ShareID != 4 || row.Path == nil || *row.Path != "docs/a.pdf" ||
		row.Bytes == nil || *row.Bytes != 10 || row.SourceAddress == nil || *row.SourceAddress != "203.0.113.5" || row.ReceivedAt.IsZero() {
		t.Fatalf("row = %+v", row)
	}

	// Re-sending a batch rejects stored lines as duplicates and keeps the cursor.
	result, err = svc.Ingest("node-a", strings.NewReader(shareAccessLine(1, "")+"\n"))
	if err != nil {
		t.Fatalf("Ingest(resend) error = %v", err)
	}
	if result.LastID != 4 || len(result.Rejected) != 1 || result.Rejected[0].Error != "duplicate id" {
		t.Fatalf("resend result = %+v", result)
	}

	// The same ids from another node are distinct rows.
	if _, err := svc.Ingest("node-b", strings.NewReader(shareAccessLine(1, "")+"\n")); err != nil {
		t.Fatalf("Ingest(node-b) error = %v", err)
	}
	if last, err := svc.LastID("node-b"); err != nil || last != 1 {
		t.Fatalf("LastID(node-b) = %d, %v", last, err)
	}
	if last, err := svc.LastID("node-c"); err != nil || last != 0 {
		t.Fatalf("LastID(node-c) = %d, %v", last, err)
	}
}

func TestNodeLogIngestRejectsOversizedBatch(t *testing.T) {
	svc, _ := newNodeLogTestService(t)
	var body strings.Builder
	for i := 1; i <= NodeLogMaxLines+1; i++ {
		body.WriteString(`{"id":1}` + "\n")
	}
	if _, err := svc.Ingest("node-a", strings.NewReader(body.String())); !errors.Is(err, ErrNodeLogBatchTooLarge) {
		t.Fatalf("Ingest() error = %v, want ErrNodeLogBatchTooLarge", err)
	}
}
