package model

import "testing"

func TestDocumentedEnumsAreValid(t *testing.T) {
	tests := []struct {
		name  string
		valid bool
	}{
		{name: string(InventoryStatusActive), valid: InventoryStatusActive.Valid()},
		{name: string(InventoryTypeFolder), valid: InventoryTypeFolder.Valid()},
		{name: string(InventoryFileStatusActive), valid: InventoryFileStatusActive.Valid()},
		{name: string(FileJournalActionModified), valid: FileJournalActionModified.Valid()},
		{name: string(FileJournalActionRestored), valid: FileJournalActionRestored.Valid()},
		{name: string(ReplicaStatusActive), valid: ReplicaStatusActive.Valid()},
		{name: string(ReplicaTypeFilesystem), valid: ReplicaTypeFilesystem.Valid()},
		{name: string(ReplicaFileStatusConflict), valid: ReplicaFileStatusConflict.Valid()},
		{name: string(ShareStatusActive), valid: ShareStatusActive.Valid()},
		{name: string(NodeStatusOnline), valid: NodeStatusOnline.Valid()},
		{name: string(NodeCommandStatusPending), valid: NodeCommandStatusPending.Valid()},
		{name: string(NodeCommandTypeRefreshState), valid: NodeCommandTypeRefreshState.Valid()},
		{name: string(PermissionResourceNodes), valid: PermissionResourceNodes.Valid()},
		{name: string(PermissionResourceSettings), valid: PermissionResourceSettings.Valid()},
	}

	for _, test := range tests {
		if !test.valid {
			t.Fatalf("%s should be valid", test.name)
		}
	}
}

func TestInvalidStatusesFailValidation(t *testing.T) {
	if InventoryStatus("invalid").Valid() {
		t.Fatal("invalid inventory status should fail")
	}
	if ReplicaFileStatus("invalid").Valid() {
		t.Fatal("invalid replica file status should fail")
	}
	if NodeStatus("invalid").Valid() {
		t.Fatal("invalid node status should fail")
	}
	if CommandStatus("invalid").Valid() {
		t.Fatal("invalid node command status should fail")
	}
	if CommandType("invalid").Valid() {
		t.Fatal("invalid node command type should fail")
	}
	if PermissionResource("invalid").Valid() {
		t.Fatal("invalid permission resource should fail")
	}
}
