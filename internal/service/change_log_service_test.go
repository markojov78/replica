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

type changeLogFixture struct {
	database    *gorm.DB
	nodes       *NodeService
	inventories *InventoryService
	replicas    *ReplicaService
	alice       *model.User
	bob         *model.User
	audit       AuditContext
}

func newChangeLogFixture(t *testing.T) changeLogFixture {
	t.Helper()
	database, err := db.Open(config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "change-logs.db")})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	if err := db.AutoMigrate(database); err != nil {
		t.Fatalf("db.AutoMigrate() error = %v", err)
	}
	hashed, err := security.HashPassword("secret")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	admin := &model.User{Username: "admin", Status: model.UserStatusActive, Password: hashed}
	alice := &model.User{Username: "alice", Status: model.UserStatusActive, Password: hashed}
	bob := &model.User{Username: "bob", Status: model.UserStatusActive, Password: hashed}
	for _, user := range []*model.User{admin, alice, bob} {
		mustCreate(t, database, user)
	}

	logs := NewChangeLogService(repository.NewChangeLogRepository(database))
	nodes := NewNodeService(repository.NewNodeRepository(database), repository.NewNodeCommandRepository(database), repository.NewNodeTokenRepository(database)).WithChangeLogs(logs)
	inventoryRepo := repository.NewInventoryRepository(database)
	return changeLogFixture{
		database:    database,
		nodes:       nodes,
		inventories: NewInventoryService(inventoryRepo, nodes).WithChangeLogs(logs),
		replicas:    NewReplicaService(repository.NewReplicaRepository(database), inventoryRepo, nodes).WithChangeLogs(logs),
		alice:       alice,
		bob:         bob,
		audit:       AuditContext{ActorUserID: &admin.ID, SourceAddress: "198.51.100.4"},
	}
}

func changeLogRows[T any](t *testing.T, database *gorm.DB) []T {
	t.Helper()
	var rows []T
	if err := database.Order("id asc").Find(&rows).Error; err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	return rows
}

func messageOf(message *string) string {
	if message == nil {
		return ""
	}
	return *message
}

func TestChangeLogsNodeLifecycle(t *testing.T) {
	f := newChangeLogFixture(t)
	if _, err := f.nodes.Create("node-a", "plain-secret", "http://node-a:8081", nil, true, f.audit); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	address := "http://node-a:9000"
	secret := "new-plain-secret"
	if _, err := f.nodes.Update("node-a", UpdateNodeInput{Address: &address, Secret: &secret}, f.audit); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if _, err := f.nodes.Update("node-a", UpdateNodeInput{Address: &address}, f.audit); err != nil {
		t.Fatalf("Update(no-op) error = %v", err)
	}
	invalid := "online"
	if _, err := f.nodes.Update("node-a", UpdateNodeInput{Status: &invalid}, f.audit); err == nil {
		t.Fatal("Update(invalid status) succeeded")
	}
	if _, err := f.nodes.Delete("node-a", f.audit); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	rows := changeLogRows[model.NodeLog](t, f.database)
	want := []struct {
		action  model.NodeLogAction
		message string
	}{
		{model.NodeLogActionCreate, "created node status=offline address=http://node-a:8081 sharing_enabled=true"},
		{model.NodeLogActionUpdate, "address: http://node-a:8081 -> http://node-a:9000; secret changed"},
		{model.NodeLogActionDelete, "deleted node status: offline -> revoked"},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows = %+v, want %d", rows, len(want))
	}
	for i, w := range want {
		row := rows[i]
		if row.Action != w.action || messageOf(row.Message) != w.message || row.NodeID != "node-a" ||
			row.ActionUserID == nil || *row.ActionUserID != *f.audit.ActorUserID || row.SourceAddress == nil || *row.SourceAddress != "198.51.100.4" {
			t.Fatalf("row %d = %+v message=%q, want %+v", i, row, messageOf(row.Message), w)
		}
		if strings.Contains(messageOf(row.Message), "plain-secret") {
			t.Fatalf("row %d leaks the node secret", i)
		}
	}
}

func TestChangeLogsInventoryAndReplicaLifecycle(t *testing.T) {
	f := newChangeLogFixture(t)
	mustCreate(t, f.database, &model.Node{ID: "node-a", Status: model.NodeStatusOffline})

	inventory, err := f.inventories.Create(CreateInventoryInput{
		Name:            "photos",
		NodeID:          "node-a",
		FolderURI:       stringPointer("/data/photos"),
		ReplicaType:     string(model.ReplicaTypeFilesystem),
		UserPermissions: &[]UserPermissionInput{{UserID: f.alice.ID, Permissions: []string{"update", "read"}}},
	}, f.audit)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	replicaID := inventory.Replicas[0].ID

	followSymlinks := true
	if _, err := f.replicas.Update(replicaID, UpdateReplicaInput{FollowSymlinks: &followSymlinks}, f.audit); err != nil {
		t.Fatalf("Update(replica) error = %v", err)
	}
	if _, err := f.replicas.Update(replicaID, UpdateReplicaInput{FollowSymlinks: &followSymlinks}, f.audit); err != nil {
		t.Fatalf("Update(replica no-op) error = %v", err)
	}

	name := "pictures"
	if _, err := f.inventories.Update(inventory.ID, UpdateInventoryInput{
		Name:            &name,
		UserPermissions: &[]UserPermissionInput{{UserID: f.alice.ID, Permissions: []string{"read"}}, {UserID: f.bob.ID, Permissions: []string{"read"}}},
	}, f.audit); err != nil {
		t.Fatalf("Update(inventory) error = %v", err)
	}
	if _, err := f.inventories.Update(inventory.ID, UpdateInventoryInput{Name: &name}, f.audit); err != nil {
		t.Fatalf("Update(inventory no-op) error = %v", err)
	}
	if _, err := f.inventories.Delete(inventory.ID, f.audit); err == nil {
		t.Fatal("Delete(inventory) with an active replica succeeded")
	}
	if _, err := f.replicas.Delete(replicaID, f.audit); err != nil {
		t.Fatalf("Delete(replica) error = %v", err)
	}
	if _, err := f.inventories.Delete(inventory.ID, f.audit); err != nil {
		t.Fatalf("Delete(inventory) error = %v", err)
	}

	inventoryRows := changeLogRows[model.InventoryLog](t, f.database)
	wantInventory := []struct {
		action  model.InventoryLogAction
		userID  uint
		message string
	}{
		{model.InventoryLogActionCreate, 0, "created inventory name=photos type=folder"},
		{model.InventoryLogActionUpdate, f.alice.ID, "permissions set: read,update"},
		{model.InventoryLogActionUpdate, 0, "name: photos -> pictures"},
		{model.InventoryLogActionUpdate, f.alice.ID, "permissions removed: update"},
		{model.InventoryLogActionUpdate, f.bob.ID, "access granted: read"},
		{model.InventoryLogActionDelete, 0, "deleted inventory name=pictures"},
	}
	if len(inventoryRows) != len(wantInventory) {
		t.Fatalf("inventory rows = %+v, want %d", inventoryRows, len(wantInventory))
	}
	for i, w := range wantInventory {
		row := inventoryRows[i]
		var userID uint
		if row.UserID != nil {
			userID = *row.UserID
		}
		if row.Action != w.action || userID != w.userID || messageOf(row.Message) != w.message || row.InventoryID != inventory.ID {
			t.Fatalf("inventory row %d = %+v message=%q, want %+v", i, row, messageOf(row.Message), w)
		}
	}

	replicaRows := changeLogRows[model.ReplicaLog](t, f.database)
	wantReplica := []struct {
		action  model.ReplicaLogAction
		message string
	}{
		{model.ReplicaLogActionCreate, "created replica node=node-a type=filesystem uri=/data/photos upstream=none storage_profile=none follow_symlinks=false"},
		{model.ReplicaLogActionUpdate, "follow_symlinks: false -> true"},
		{model.ReplicaLogActionDelete, "deleted replica uri=/data/photos"},
	}
	if len(replicaRows) != len(wantReplica) {
		t.Fatalf("replica rows = %+v, want %d", replicaRows, len(wantReplica))
	}
	for i, w := range wantReplica {
		row := replicaRows[i]
		if row.Action != w.action || messageOf(row.Message) != w.message || row.ReplicaID != replicaID ||
			row.InventoryID != inventory.ID || row.ReplicaNodeID != "node-a" {
			t.Fatalf("replica row %d = %+v message=%q, want %+v", i, row, messageOf(row.Message), w)
		}
	}
}

func TestChangeLogsWriteFailureRollsBackChange(t *testing.T) {
	f := newChangeLogFixture(t)
	if _, err := f.nodes.Create("node-a", "plain-secret", "http://node-a:8081", nil, false, f.audit); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := f.database.Migrator().DropTable(&model.NodeLog{}); err != nil {
		t.Fatalf("DropTable(node_logs) error = %v", err)
	}
	address := "http://node-a:9000"
	if _, err := f.nodes.Update("node-a", UpdateNodeInput{Address: &address}, f.audit); err == nil || !strings.Contains(err.Error(), "write change log") {
		t.Fatalf("Update() error = %v, want change log write error", err)
	}
	var node model.Node
	if err := f.database.First(&node, "id = ?", "node-a").Error; err != nil {
		t.Fatalf("First(node) error = %v", err)
	}
	if node.Address != "http://node-a:8081" {
		t.Fatalf("node address = %q, change was committed although its log row failed", node.Address)
	}
	var commands int64
	f.database.Model(&model.Command{}).Where("node_id = ? AND type = ?", "node-a", model.NodeCommandTypeRefreshConfig).Count(&commands)
	if commands != 0 {
		t.Fatalf("refresh_config commands = %d, want 0 after rollback", commands)
	}
}
