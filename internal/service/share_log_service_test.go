package service

import (
	"path/filepath"
	"strings"
	"testing"

	"replica/internal/config"
	"replica/internal/db"
	"replica/internal/model"
	"replica/internal/repository"
	"replica/internal/security"

	"gorm.io/gorm"
)

type shareLogFixture struct {
	database *gorm.DB
	shares   *ShareService
	replica  *model.Replica
	files    []model.InventoryFile
	alice    *model.User
	bob      *model.User
}

func newShareLogFixture(t *testing.T) shareLogFixture {
	t.Helper()
	database, err := db.Open(config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "share-logs.db")})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}
	inventory := &model.Inventory{Name: "photos", Status: model.InventoryStatusActive, Type: model.InventoryTypeFolder}
	mustCreate(t, database, inventory)
	mustCreate(t, database, &model.Node{ID: "node-a", Status: model.NodeStatusOnline, Address: "https://node-a.example"})
	replica := &model.Replica{InventoryID: inventory.ID, NodeID: "node-a", URI: "/data/a", Status: model.ReplicaStatusActive, Type: model.ReplicaTypeFilesystem}
	mustCreate(t, database, replica)
	files := []model.InventoryFile{}
	for _, name := range []string{"a.jpg", "b.jpg", "c.jpg"} {
		file := model.InventoryFile{InventoryID: inventory.ID, RelativeURI: name, Status: model.InventoryFileStatusActive, Version: 1, Size: 1, Hash: name}
		mustCreate(t, database, &file)
		files = append(files, file)
	}
	hashed, err := security.HashPassword("secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	alice := &model.User{Username: "alice", Status: model.UserStatusActive, Password: hashed}
	bob := &model.User{Username: "bob", Status: model.UserStatusActive, Password: hashed}
	mustCreate(t, database, alice)
	mustCreate(t, database, bob)

	logs := NewShareLogService(repository.NewShareLogRepository(database), "node-test")
	shares := NewShareService(repository.NewShareRepository(database), nil).WithShareLogs(logs)
	return shareLogFixture{database: database, shares: shares, replica: replica, files: files, alice: alice, bob: bob}
}

func shareLogRows(t *testing.T, database *gorm.DB) []model.ShareLog {
	t.Helper()
	var rows []model.ShareLog
	if err := database.Order("id asc").Find(&rows).Error; err != nil {
		t.Fatalf("Find(share_logs) error = %v", err)
	}
	return rows
}

func shareLogMessage(row model.ShareLog) string {
	if row.Message == nil {
		return ""
	}
	return *row.Message
}

func TestShareLogsLifecycle(t *testing.T) {
	f := newShareLogFixture(t)
	actor := uint(99)
	audit := AuditContext{ActorUserID: &actor, SourceAddress: "203.0.113.7"}

	name := "holiday"
	share, err := f.shares.Create(CreateShareInput{
		ReplicaID:            f.replica.ID,
		Name:                 &name,
		GenerateHash:         true,
		UserPermissions:      &[]UserPermissionInput{{UserID: f.alice.ID, Permissions: []string{"read", "update"}}},
		AnonymousPermissions: &[]string{"read"},
		FileIDs:              &[]uint{f.files[0].ID, f.files[1].ID},
	}, audit)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	rows := shareLogRows(t, f.database)
	if len(rows) != 3 {
		t.Fatalf("create rows = %d, want 3: %+v", len(rows), rows)
	}
	if rows[0].Action != model.ShareLogActionCreate || !rows[0].Success || uintValue(rows[0].ShareID) != share.ID ||
		uintValue(rows[0].ActionUserID) != actor || rows[0].SourceAddress == nil || *rows[0].SourceAddress != "203.0.113.7" || rows[0].NodeID != "node-test" ||
		shareLogMessage(rows[0]) != "created share name=holiday replica_id=1 status=active expiration=none link=yes files=2" {
		t.Fatalf("create row = %+v message=%q", rows[0], shareLogMessage(rows[0]))
	}
	if uintValue(rows[1].UserID) != f.alice.ID || shareLogMessage(rows[1]) != "permissions set: read,update" {
		t.Fatalf("user permission row = %+v message=%q", rows[1], shareLogMessage(rows[1]))
	}
	if !rows[2].Anonymous || rows[2].UserID != nil || shareLogMessage(rows[2]) != "permissions set: read" {
		t.Fatalf("anonymous permission row = %+v message=%q", rows[2], shareLogMessage(rows[2]))
	}

	// A no-op update writes no row.
	if _, err := f.shares.Update(share.ID, UpdateShareInput{Name: &name}, audit); err != nil {
		t.Fatalf("Update(no-op) error = %v", err)
	}
	if got := len(shareLogRows(t, f.database)); got != 3 {
		t.Fatalf("no-op update wrote %d rows", got-3)
	}

	renamed := "summer"
	regenerate := true
	if _, err := f.shares.Update(share.ID, UpdateShareInput{
		Name:                 &renamed,
		GenerateHash:         &regenerate,
		UserPermissions:      &[]UserPermissionInput{{UserID: f.alice.ID, Permissions: []string{"read"}}, {UserID: f.bob.ID, Permissions: []string{"read"}}},
		AnonymousPermissions: &[]string{},
		FileIDs:              &[]uint{f.files[1].ID, f.files[2].ID},
	}, audit); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	rows = shareLogRows(t, f.database)[3:]
	want := []struct {
		userID    uint
		anonymous bool
		message   string
	}{
		{0, false, "name: holiday -> summer; link regenerated; files added: 1; files removed: 1"},
		{f.alice.ID, false, "permissions removed: update"},
		{f.bob.ID, false, "access granted: read"},
		{0, true, "access revoked"},
	}
	if len(rows) != len(want) {
		t.Fatalf("update rows = %d, want %d: %+v", len(rows), len(want), rows)
	}
	for i, w := range want {
		row := rows[i]
		if row.Action != model.ShareLogActionUpdate || !row.Success || uintValue(row.ShareID) != share.ID || uintValue(row.UserID) != w.userID || row.Anonymous != w.anonymous || shareLogMessage(row) != w.message {
			t.Fatalf("update row %d = %+v message=%q, want %+v", i, row, shareLogMessage(row), w)
		}
	}

	if err := f.shares.Delete(share.ID, audit); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	all := shareLogRows(t, f.database)
	last := all[len(all)-1]
	if last.Action != model.ShareLogActionDelete || !last.Success || shareLogMessage(last) != "deleted share name=summer" {
		t.Fatalf("delete row = %+v message=%q", last, shareLogMessage(last))
	}

	var stored model.Share
	if err := f.database.First(&stored, share.ID).Error; err != nil {
		t.Fatalf("First(share) error = %v", err)
	}
	for _, row := range all {
		if stored.LinkHash != nil && strings.Contains(shareLogMessage(row), *stored.LinkHash) {
			t.Fatalf("row %d leaks link hash: %q", row.ID, shareLogMessage(row))
		}
	}
}

func TestShareLogsFailureIsLoggedAndRolledBack(t *testing.T) {
	f := newShareLogFixture(t)
	share, err := f.shares.Create(CreateShareInput{ReplicaID: f.replica.ID}, AuditContext{})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	before := len(shareLogRows(t, f.database))

	renamed := "renamed"
	if _, err := f.shares.Update(share.ID, UpdateShareInput{Name: &renamed, FileIDs: &[]uint{999}}, AuditContext{}); err == nil {
		t.Fatal("Update() with an unknown file succeeded")
	}
	rows := shareLogRows(t, f.database)
	if len(rows) != before+1 {
		t.Fatalf("failed update wrote %d rows, want 1", len(rows)-before)
	}
	last := rows[len(rows)-1]
	if last.Success || last.Action != model.ShareLogActionUpdate || uintValue(last.ShareID) != share.ID ||
		!strings.HasPrefix(shareLogMessage(last), "update share 1 failed: ") {
		t.Fatalf("failure row = %+v message=%q", last, shareLogMessage(last))
	}

	if _, err := f.shares.Create(CreateShareInput{ReplicaID: 999}, AuditContext{}); err == nil {
		t.Fatal("Create() with an unknown replica succeeded")
	}
	rows = shareLogRows(t, f.database)
	last = rows[len(rows)-1]
	if last.Success || last.Action != model.ShareLogActionCreate || last.ShareID != nil || !strings.HasPrefix(shareLogMessage(last), "create share replica_id=999 failed: ") {
		t.Fatalf("create failure row = %+v message=%q", last, shareLogMessage(last))
	}
}

func TestShareLogsWriteFailureFailsAction(t *testing.T) {
	f := newShareLogFixture(t)
	share, err := f.shares.Create(CreateShareInput{ReplicaID: f.replica.ID}, AuditContext{})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := f.database.Migrator().DropTable(&model.ShareLog{}); err != nil {
		t.Fatalf("DropTable(share_logs) error = %v", err)
	}

	renamed := "renamed"
	if _, err := f.shares.Update(share.ID, UpdateShareInput{Name: &renamed}, AuditContext{}); err == nil || !strings.Contains(err.Error(), "write share log") {
		t.Fatalf("Update() error = %v, want share log write error", err)
	}
	var stored model.Share
	if err := f.database.First(&stored, share.ID).Error; err != nil {
		t.Fatalf("First(share) error = %v", err)
	}
	if stored.Name == renamed {
		t.Fatal("share update was committed although its log row failed")
	}
}
