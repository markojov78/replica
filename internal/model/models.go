package model

import (
	"encoding/json"
	"time"
)

type Inventory struct {
	ID       uint            `gorm:"primaryKey" json:"id"`
	Name     string          `gorm:"size:255" json:"name"`
	Status   InventoryStatus `gorm:"size:32;not null" json:"status"`
	Type     InventoryType   `gorm:"size:32;not null" json:"type"`
	Replicas []Replica       `gorm:"foreignKey:InventoryID" json:"replicas,omitempty"`
}

func (Inventory) TableName() string {
	return "inventories"
}

type InventoryFile struct {
	ID          uint                `gorm:"primaryKey" json:"id"`
	InventoryID uint                `gorm:"index;not null" json:"inventory_id"`
	RelativeURI string              `gorm:"size:2048;not null" json:"relative_uri"`
	Status      InventoryFileStatus `gorm:"size:32;not null" json:"status"`
	Size        int64               `json:"size"`
	Hash        string              `gorm:"size:255" json:"hash"`
	Version     uint                `gorm:"not null;default:0" json:"version"`
	Created     time.Time           `json:"created"`
	Modified    time.Time           `json:"modified"`
	Inventory   Inventory           `gorm:"constraint:OnUpdate:CASCADE,OnDelete:RESTRICT;" json:"-"`
}

func (InventoryFile) TableName() string {
	return "inventory_files"
}

type FileJournal struct {
	ID          uint              `gorm:"primaryKey" json:"id"`
	FileID      uint              `gorm:"index;not null" json:"file_id"`
	InventoryID uint              `gorm:"index;not null" json:"inventory_id"`
	ReplicaID   uint              `gorm:"index;not null" json:"replica_id"`
	Version     uint              `gorm:"not null" json:"version"`
	Action      FileJournalAction `gorm:"size:32;not null" json:"action"`
	Timestamp   time.Time         `gorm:"not null" json:"timestamp"`
}

func (FileJournal) TableName() string {
	return "file_journal"
}

type Node struct {
	ID       string     `gorm:"primaryKey;size:255" json:"id"`
	Status   NodeStatus `gorm:"size:32;not null" json:"status"`
	Secret   string     `gorm:"size:255;not null" json:"-"`
	Address  string     `gorm:"size:2048" json:"address"`
	Interval *float64   `json:"interval"`
	LastSeen *time.Time `json:"last_seen"`
	Token    *NodeToken `gorm:"foreignKey:NodeID;references:ID" json:"token,omitempty"`
	Commands []Command  `gorm:"foreignKey:NodeID;references:ID" json:"commands,omitempty"`
	Replicas []Replica  `gorm:"foreignKey:NodeID;references:ID" json:"replicas,omitempty"`
}

func (Node) TableName() string {
	return "nodes"
}

type NodeToken struct {
	NodeID            string     `gorm:"primaryKey;size:255" json:"node_id"`
	RefreshHash       string     `gorm:"column:refresh_hash;size:255;not null;uniqueIndex" json:"-"`
	RefreshExpiration time.Time  `gorm:"column:refresh_expiration;not null;index" json:"refresh_expiration"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	RevokedAt         *time.Time `json:"revoked_at,omitempty"`
	Node              Node       `gorm:"foreignKey:NodeID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
}

func (NodeToken) TableName() string {
	return "node_tokens"
}

type Setting struct {
	Key   string `gorm:"primaryKey;size:255" json:"key"`
	Value string `gorm:"type:text;not null" json:"value"`
}

func (Setting) TableName() string {
	return "settings"
}

type Command struct {
	ID        uint            `gorm:"primaryKey" json:"id"`
	NodeID    string          `gorm:"size:255;index;not null" json:"node_id"`
	Type      CommandType     `gorm:"size:64;not null" json:"type"`
	Status    CommandStatus   `gorm:"size:32;not null;index" json:"status"`
	Payload   json.RawMessage `gorm:"type:json" json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	LastError *string         `gorm:"size:2048" json:"last_error,omitempty"`
	Node      Node            `gorm:"foreignKey:NodeID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
}

func (Command) TableName() string {
	return "commands"
}

type Replica struct {
	ID                uint          `gorm:"primaryKey" json:"id"`
	InventoryID       uint          `gorm:"index;not null" json:"inventory_id"`
	NodeID            string        `gorm:"size:255;index;not null" json:"node_id"`
	URI               string        `gorm:"size:2048;not null" json:"uri"`
	Status            ReplicaStatus `gorm:"size:32;not null" json:"status"`
	Type              ReplicaType   `gorm:"size:32;not null" json:"type"`
	UpstreamReplicaID *uint         `gorm:"index" json:"upstream_replica_id,omitempty"`
	StorageProfile    string        `gorm:"size:255" json:"storage_profile,omitempty"`
	Inventory         Inventory     `gorm:"foreignKey:InventoryID;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT;" json:"-"`
	Node              Node          `gorm:"foreignKey:NodeID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT;" json:"-"`
	UpstreamReplica   *Replica      `gorm:"foreignKey:UpstreamReplicaID;constraint:OnUpdate:CASCADE,OnDelete:SET NULL;" json:"-"`
}

func (Replica) TableName() string {
	return "replicas"
}

type ReplicaFile struct {
	FileID    uint              `gorm:"primaryKey;autoIncrement:false;column:file_id" json:"file_id"`
	ReplicaID uint              `gorm:"primaryKey;autoIncrement:false;column:replica_id" json:"replica_id"`
	Version   uint              `gorm:"not null;default:0" json:"version"`
	Status    ReplicaFileStatus `gorm:"size:32;not null" json:"status"`
	File      InventoryFile     `gorm:"foreignKey:FileID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	Replica   Replica           `gorm:"foreignKey:ReplicaID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
}

func (ReplicaFile) TableName() string {
	return "replica_files"
}

type Share struct {
	ID              uint        `gorm:"primaryKey" json:"id"`
	ReplicaID       uint        `gorm:"index;not null" json:"replica_id"`
	Name            string      `gorm:"size:255" json:"name"`
	Status          ShareStatus `gorm:"size:32;not null" json:"status"`
	LinkHash        *string     `gorm:"size:255" json:"link_hash"`
	ShareExpiration *time.Time  `json:"share_expiration"`
	Replica         Replica     `gorm:"foreignKey:ReplicaID;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT;" json:"-"`
}

func (Share) TableName() string {
	return "shares"
}

type User struct {
	ID       uint        `gorm:"primaryKey" json:"id"`
	Name     string      `gorm:"size:255;uniqueIndex;not null" json:"name"`
	Status   UserStatus  `gorm:"size:32;not null" json:"status"`
	Password string      `gorm:"size:255;not null" json:"-"`
	Tokens   []UserToken `json:"tokens,omitempty"`
	Roles    []Role      `gorm:"many2many:user_role;" json:"roles,omitempty"`
}

func (User) TableName() string {
	return "users"
}

type UserToken struct {
	ID                uint      `gorm:"primaryKey" json:"id"`
	UserID            uint      `gorm:"not null;index" json:"user_id"`
	RefreshHash       string    `gorm:"column:refresh_hash;size:255;not null;uniqueIndex" json:"-"`
	RefreshExpiration time.Time `gorm:"column:refresh_expiration;not null;index" json:"refresh_expiration"`
	User              User      `gorm:"foreignKey:UserID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
}

func (UserToken) TableName() string {
	return "user_tokens"
}

type UserRole struct {
	UserID uint `gorm:"primaryKey;column:user_id" json:"user_id"`
	RoleID uint `gorm:"primaryKey;column:role_id" json:"role_id"`
}

func (UserRole) TableName() string {
	return "user_role"
}

type Role struct {
	ID          uint         `gorm:"primaryKey" json:"id"`
	Name        string       `gorm:"size:255;uniqueIndex;not null" json:"name"`
	Description string       `gorm:"size:1024" json:"description"`
	Status      RoleStatus   `gorm:"size:32;not null" json:"status"`
	Permissions []Permission `gorm:"foreignKey:RoleID" json:"permissions,omitempty"`
}

func (Role) TableName() string {
	return "roles"
}

type Permission struct {
	ID       uint               `gorm:"primaryKey" json:"id"`
	RoleID   uint               `gorm:"not null;index" json:"role_id"`
	Resource PermissionResource `gorm:"size:64;not null" json:"resource"`
	Action   PermissionAction   `gorm:"column:actions;size:64;not null" json:"actions"`
	Role     Role               `gorm:"foreignKey:RoleID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
}

func (Permission) TableName() string {
	return "permissions"
}

type ShareUser struct {
	ID        uint  `gorm:"primaryKey" json:"id"`
	UserID    *uint `gorm:"index" json:"user_id"`
	ShareID   uint  `gorm:"index;not null" json:"share_id"`
	Anonymous bool  `gorm:"not null;default:false;check:share_users_user_anonymous_check,(anonymous = false AND user_id IS NOT NULL) OR (anonymous = true AND user_id IS NULL)" json:"anonymous"`
	User      User  `gorm:"foreignKey:UserID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	Share     Share `gorm:"foreignKey:ShareID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
}

func (ShareUser) TableName() string {
	return "share_users"
}

type InventoryUser struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	UserID      uint      `gorm:"index;not null" json:"user_id"`
	InventoryID uint      `gorm:"index;not null" json:"inventory_id"`
	User        User      `gorm:"foreignKey:UserID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	Inventory   Inventory `gorm:"foreignKey:InventoryID;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT;" json:"-"`
}

func (InventoryUser) TableName() string {
	return "inventory_users"
}

type SharePermission struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	ShareUserID uint      `gorm:"index;not null" json:"share_user_id"`
	Permission  string    `gorm:"size:64;not null" json:"permission"`
	ShareUser   ShareUser `gorm:"foreignKey:ShareUserID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
}

func (SharePermission) TableName() string {
	return "share_permissions"
}

type InventoryPermission struct {
	ID              uint          `gorm:"primaryKey" json:"id"`
	InventoryUserID uint          `gorm:"index;not null" json:"inventory_user_id"`
	Permission      string        `gorm:"size:64;not null" json:"permission"`
	InventoryUser   InventoryUser `gorm:"foreignKey:InventoryUserID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
}

func (InventoryPermission) TableName() string {
	return "inventory_permissions"
}

func AllModels() []any {
	return []any{
		&Inventory{},
		&InventoryFile{},
		&FileJournal{},
		&Node{},
		&NodeToken{},
		&Setting{},
		&Command{},
		&Replica{},
		&ReplicaFile{},
		&Share{},
		&User{},
		&UserRole{},
		&Role{},
		&Permission{},
		&ShareUser{},
		&InventoryUser{},
		&SharePermission{},
		&InventoryPermission{},
		&UserToken{},
	}
}
