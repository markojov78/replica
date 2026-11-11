package service

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"replica/internal/model"
)

func inventoryChanges(previous, current model.Inventory) []string {
	changes := []string{}
	if previous.Name != current.Name {
		changes = append(changes, fmt.Sprintf("name: %s -> %s", previous.Name, current.Name))
	}
	if previous.Status != current.Status {
		changes = append(changes, fmt.Sprintf("status: %s -> %s", previous.Status, current.Status))
	}
	return changes
}

// writeInventoryPermissionLogs writes one inventory_logs row per user whose inventory
// permissions changed. On create, every user gets a "permissions set" row.
func writeInventoryPermissionLogs(logs *ChangeLogWriter, inventoryID uint, previous, current map[uint][]string, created bool, audit AuditContext) error {
	userIDs := sortedUserIDs(current)
	for _, userID := range sortedUserIDs(previous) {
		if _, ok := current[userID]; !ok {
			userIDs = append(userIDs, userID)
		}
	}
	sort.Slice(userIDs, func(i, j int) bool { return userIDs[i] < userIDs[j] })
	for _, userID := range userIDs {
		message := permissionChange(previous[userID], current[userID])
		if created {
			message = "permissions set: " + strings.Join(current[userID], ",")
		}
		if message == "" {
			continue
		}
		if err := logs.Inventory(InventoryLogEntry{
			Action:      model.InventoryLogActionUpdate,
			Message:     message,
			InventoryID: inventoryID,
			UserID:      uintPtr(userID),
			Audit:       audit,
		}); err != nil {
			return err
		}
	}
	return nil
}

func replicaCreatedMessage(replica *model.Replica) string {
	return fmt.Sprintf("created replica node=%s type=%s uri=%s upstream=%s storage_profile=%s follow_symlinks=%t",
		replica.NodeID, replica.Type, replica.URI, formatOptionalUint(replica.UpstreamReplicaID),
		formatOptionalString(replica.StorageProfile), replica.FollowSymlinks)
}

func replicaChanges(previous, current model.Replica) []string {
	changes := []string{}
	if previous.Type != current.Type {
		changes = append(changes, fmt.Sprintf("type: %s -> %s", previous.Type, current.Type))
	}
	if previous.Status != current.Status {
		changes = append(changes, fmt.Sprintf("status: %s -> %s", previous.Status, current.Status))
	}
	if before, after := formatOptionalUint(previous.UpstreamReplicaID), formatOptionalUint(current.UpstreamReplicaID); before != after {
		changes = append(changes, fmt.Sprintf("upstream: %s -> %s", before, after))
	}
	if previous.StorageProfile != current.StorageProfile {
		changes = append(changes, fmt.Sprintf("storage_profile: %s -> %s", formatOptionalString(previous.StorageProfile), formatOptionalString(current.StorageProfile)))
	}
	if previous.FollowSymlinks != current.FollowSymlinks {
		changes = append(changes, fmt.Sprintf("follow_symlinks: %t -> %t", previous.FollowSymlinks, current.FollowSymlinks))
	}
	return changes
}

// nodeChanges describes admin changes of a node. The secret value is never included.
func nodeChanges(previous, current model.Node, secretChanged bool) []string {
	changes := []string{}
	if previous.Status != current.Status {
		changes = append(changes, fmt.Sprintf("status: %s -> %s", previous.Status, current.Status))
	}
	if previous.Address != current.Address {
		changes = append(changes, fmt.Sprintf("address: %s -> %s", previous.Address, current.Address))
	}
	if secretChanged {
		changes = append(changes, "secret changed")
	}
	if previous.Sharing != current.Sharing {
		changes = append(changes, fmt.Sprintf("sharing_enabled: %t -> %t", previous.Sharing, current.Sharing))
	}
	return changes
}

func formatOptionalUint(value *uint) string {
	if value == nil {
		return "none"
	}
	return strconv.FormatUint(uint64(*value), 10)
}

func formatOptionalString(value string) string {
	if value == "" {
		return "none"
	}
	return value
}
