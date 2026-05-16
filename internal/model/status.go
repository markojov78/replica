package model

type InventoryStatus string
type InventoryType string
type InventoryFileStatus string
type FileJournalAction string
type ReplicaStatus string
type ReplicaType string
type ReplicaFileStatus string
type ReplicationGroupType string
type ReplicationGroupStatus string
type ShareStatus string
type UserStatus string
type RoleStatus string
type PermissionResource string
type PermissionAction string

const (
	InventoryStatusOnline  InventoryStatus = "online"
	InventoryStatusOffline InventoryStatus = "offline"
	InventoryStatusDeleted InventoryStatus = "deleted"
)

const (
	InventoryTypeFile   InventoryType = "file"
	InventoryTypeFolder InventoryType = "folder"
)

const (
	InventoryFileStatusActive  InventoryFileStatus = "active"
	InventoryFileStatusDeleted InventoryFileStatus = "deleted"
)

const (
	FileJournalActionCreated  FileJournalAction = "created"
	FileJournalActionUpdated  FileJournalAction = "updated"
	FileJournalActionModified FileJournalAction = "modified"
	FileJournalActionDeleted  FileJournalAction = "deleted"
)

const (
	ReplicaStatusActive  ReplicaStatus = "active"
	ReplicaStatusDeleted ReplicaStatus = "deleted"
)

const (
	ReplicaTypeStorage    ReplicaType = "storage"
	ReplicaTypeFilesystem ReplicaType = "filesystem"
	ReplicaTypeRemovable  ReplicaType = "removable"
)

const (
	ReplicaFileStatusChanged      ReplicaFileStatus = "changed"
	ReplicaFileStatusPending      ReplicaFileStatus = "pending"
	ReplicaFileStatusSynchronized ReplicaFileStatus = "synchronized"
	ReplicaFileStatusConflict     ReplicaFileStatus = "conflict"
	ReplicaFileStatusError        ReplicaFileStatus = "error"
)

const (
	ReplicationGroupTypeBiDirectional ReplicationGroupType = "bi-directional"
	ReplicationGroupTypeOneWay        ReplicationGroupType = "one-way"
)

const (
	ReplicationGroupStatusActive  ReplicationGroupStatus = "active"
	ReplicationGroupStatusDeleted ReplicationGroupStatus = "deleted"
)

const (
	ShareStatusActive  ShareStatus = "active"
	ShareStatusDeleted ShareStatus = "deleted"
)

const (
	UserStatusActive  UserStatus = "active"
	UserStatusDeleted UserStatus = "deleted"
)

const (
	RoleStatusActive  RoleStatus = "active"
	RoleStatusDeleted RoleStatus = "deleted"
)

const (
	PermissionResourceUsers       PermissionResource = "users"
	PermissionResourceShares      PermissionResource = "shares"
	PermissionResourceInventories PermissionResource = "inventories"
)

const (
	PermissionActionRead   PermissionAction = "read"
	PermissionActionCreate PermissionAction = "create"
	PermissionActionUpdate PermissionAction = "update"
	PermissionActionDelete PermissionAction = "delete"
)

func (s InventoryStatus) Valid() bool {
	switch s {
	case InventoryStatusOnline, InventoryStatusOffline, InventoryStatusDeleted:
		return true
	default:
		return false
	}
}

func (t InventoryType) Valid() bool {
	switch t {
	case InventoryTypeFile, InventoryTypeFolder:
		return true
	default:
		return false
	}
}

func (s InventoryFileStatus) Valid() bool {
	switch s {
	case InventoryFileStatusActive, InventoryFileStatusDeleted:
		return true
	default:
		return false
	}
}

func (a FileJournalAction) Valid() bool {
	switch a {
	case FileJournalActionCreated, FileJournalActionUpdated, FileJournalActionModified, FileJournalActionDeleted:
		return true
	default:
		return false
	}
}

func (s ReplicaStatus) Valid() bool {
	switch s {
	case ReplicaStatusActive, ReplicaStatusDeleted:
		return true
	default:
		return false
	}
}

func (t ReplicaType) Valid() bool {
	switch t {
	case ReplicaTypeStorage, ReplicaTypeFilesystem, ReplicaTypeRemovable:
		return true
	default:
		return false
	}
}

func (s ReplicaFileStatus) Valid() bool {
	switch s {
	case ReplicaFileStatusChanged, ReplicaFileStatusPending, ReplicaFileStatusSynchronized, ReplicaFileStatusConflict, ReplicaFileStatusError:
		return true
	default:
		return false
	}
}

func (t ReplicationGroupType) Valid() bool {
	switch t {
	case ReplicationGroupTypeBiDirectional, ReplicationGroupTypeOneWay:
		return true
	default:
		return false
	}
}

func (s ReplicationGroupStatus) Valid() bool {
	switch s {
	case ReplicationGroupStatusActive, ReplicationGroupStatusDeleted:
		return true
	default:
		return false
	}
}

func (s ShareStatus) Valid() bool {
	switch s {
	case ShareStatusActive, ShareStatusDeleted:
		return true
	default:
		return false
	}
}

func (s UserStatus) Valid() bool {
	switch s {
	case UserStatusActive, UserStatusDeleted:
		return true
	default:
		return false
	}
}

func (s RoleStatus) Valid() bool {
	switch s {
	case RoleStatusActive, RoleStatusDeleted:
		return true
	default:
		return false
	}
}

func (r PermissionResource) Valid() bool {
	switch r {
	case PermissionResourceUsers, PermissionResourceShares, PermissionResourceInventories:
		return true
	default:
		return false
	}
}

func (a PermissionAction) Valid() bool {
	switch a {
	case PermissionActionRead, PermissionActionCreate, PermissionActionUpdate, PermissionActionDelete:
		return true
	default:
		return false
	}
}
