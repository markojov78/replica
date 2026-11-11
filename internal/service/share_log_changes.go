package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"replica/internal/model"
	"replica/internal/repository"
)

// shareLogState is the part of a share that share log rows describe.
type shareLogState struct {
	share                model.Share
	userPermissions      map[uint][]string
	anonymousPermissions []string
	// fileIDs is empty when the whole replica is shared.
	fileIDs []uint
}

// shareSnapshot captures the share state before an update. Without a log service nothing
// is compared, so only the share itself is copied.
func (s *ShareService) shareSnapshot(share *model.Share) (shareLogState, error) {
	state := shareLogState{share: *share}
	if s.logs == nil {
		return state, nil
	}
	permissions, err := s.repo.UserPermissions(share.ID)
	if err != nil {
		return state, err
	}
	state.userPermissions = userPermissionSets(permissions)
	state.anonymousPermissions, err = s.repo.AnonymousPermissions(share.ID)
	if err != nil {
		return state, err
	}
	fileIDs, err := s.repo.FileIDs([]uint{share.ID})
	if err != nil {
		return state, err
	}
	state.fileIDs = fileIDs[share.ID]
	return state, nil
}

func userPermissionSets(permissions []repository.UserPermissionDetails) map[uint][]string {
	result := make(map[uint][]string, len(permissions))
	for _, item := range permissions {
		result[item.UserID] = sortedStrings(item.Permissions)
	}
	return result
}

func writeShareCreateLogs(logs *ShareLogWriter, share *model.Share, permissions []repository.UserPermissionDetails, anonymousPermissions []string, fileIDs []uint, audit AuditContext) error {
	link := "no"
	if share.LinkHash != nil {
		link = "yes"
	}
	if err := logs.Write(ShareLogEntry{
		Action:  model.ShareLogActionCreate,
		Success: true,
		Message: fmt.Sprintf("created share name=%s replica_id=%d status=%s expiration=%s link=%s files=%s",
			share.Name, share.ReplicaID, share.Status, formatShareExpiration(share.ShareExpiration), link, formatShareFiles(fileIDs)),
		ShareID: uintPtr(share.ID),
		Audit:   audit,
	}); err != nil {
		return err
	}
	sets := userPermissionSets(permissions)
	for _, userID := range sortedUserIDs(sets) {
		if err := logs.Write(ShareLogEntry{
			Action:  model.ShareLogActionUpdate,
			Success: true,
			Message: "permissions set: " + strings.Join(sets[userID], ","),
			ShareID: uintPtr(share.ID),
			UserID:  uintPtr(userID),
			Audit:   audit,
		}); err != nil {
			return err
		}
	}
	if len(anonymousPermissions) > 0 {
		return logs.Write(ShareLogEntry{
			Action:    model.ShareLogActionUpdate,
			Success:   true,
			Message:   "permissions set: " + strings.Join(sortedStrings(anonymousPermissions), ","),
			ShareID:   uintPtr(share.ID),
			Anonymous: true,
			Audit:     audit,
		})
	}
	return nil
}

func writeShareUpdateLogs(logs *ShareLogWriter, previous, current shareLogState, audit AuditContext) error {
	shareID := uintPtr(current.share.ID)
	if changes := shareFieldChanges(previous, current); len(changes) > 0 {
		if err := logs.Write(ShareLogEntry{
			Action:  model.ShareLogActionUpdate,
			Success: true,
			Message: strings.Join(changes, "; "),
			ShareID: shareID,
			Audit:   audit,
		}); err != nil {
			return err
		}
	}

	userIDs := sortedUserIDs(previous.userPermissions)
	for _, userID := range sortedUserIDs(current.userPermissions) {
		if _, ok := previous.userPermissions[userID]; !ok {
			userIDs = append(userIDs, userID)
		}
	}
	sort.Slice(userIDs, func(i, j int) bool { return userIDs[i] < userIDs[j] })
	for _, userID := range userIDs {
		message := permissionChange(previous.userPermissions[userID], current.userPermissions[userID])
		if message == "" {
			continue
		}
		if err := logs.Write(ShareLogEntry{
			Action:  model.ShareLogActionUpdate,
			Success: true,
			Message: message,
			ShareID: shareID,
			UserID:  uintPtr(userID),
			Audit:   audit,
		}); err != nil {
			return err
		}
	}

	if message := permissionChange(previous.anonymousPermissions, current.anonymousPermissions); message != "" {
		return logs.Write(ShareLogEntry{
			Action:    model.ShareLogActionUpdate,
			Success:   true,
			Message:   message,
			ShareID:   shareID,
			Anonymous: true,
			Audit:     audit,
		})
	}
	return nil
}

// shareFieldChanges describes share field changes. The link hash value is never included.
func shareFieldChanges(previous, current shareLogState) []string {
	before, after := previous.share, current.share
	changes := []string{}
	if before.Name != after.Name {
		changes = append(changes, fmt.Sprintf("name: %s -> %s", before.Name, after.Name))
	}
	if before.Status != after.Status {
		changes = append(changes, fmt.Sprintf("status: %s -> %s", before.Status, after.Status))
	}
	if beforeExp, afterExp := formatShareExpiration(before.ShareExpiration), formatShareExpiration(after.ShareExpiration); beforeExp != afterExp {
		changes = append(changes, fmt.Sprintf("expiration: %s -> %s", beforeExp, afterExp))
	}
	switch {
	case before.LinkHash == nil && after.LinkHash != nil:
		changes = append(changes, "link created")
	case before.LinkHash != nil && after.LinkHash == nil:
		changes = append(changes, "link removed")
	case before.LinkHash != nil && after.LinkHash != nil && *before.LinkHash != *after.LinkHash:
		changes = append(changes, "link regenerated")
	}
	if keys := changedShareProperties(before.Properties, after.Properties); len(keys) > 0 {
		changes = append(changes, "properties changed: "+strings.Join(keys, ", "))
	}
	return append(changes, shareFileChanges(previous.fileIDs, current.fileIDs)...)
}

func shareFileChanges(previous, current []uint) []string {
	switch {
	case len(previous) == 0 && len(current) == 0:
		return nil
	case len(previous) == 0:
		return []string{fmt.Sprintf("files: all -> %d", len(current))}
	case len(current) == 0:
		return []string{fmt.Sprintf("files: %d -> all", len(previous))}
	}
	previousSet := make(map[uint]struct{}, len(previous))
	for _, id := range previous {
		previousSet[id] = struct{}{}
	}
	added, kept := 0, 0
	for _, id := range current {
		if _, ok := previousSet[id]; ok {
			kept++
		} else {
			added++
		}
	}
	changes := []string{}
	if added > 0 {
		changes = append(changes, fmt.Sprintf("files added: %d", added))
	}
	if removed := len(previous) - kept; removed > 0 {
		changes = append(changes, fmt.Sprintf("files removed: %d", removed))
	}
	return changes
}

// changedShareProperties returns the JSON keys of share properties whose values differ.
func changedShareProperties(previous, current model.ShareProperties) []string {
	before, errBefore := propertyValues(previous)
	after, errAfter := propertyValues(current)
	if errBefore != nil || errAfter != nil {
		return nil
	}
	keys := []string{}
	for key, value := range after {
		if !bytes.Equal(before[key], value) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func propertyValues(properties model.ShareProperties) (map[string]json.RawMessage, error) {
	data, err := json.Marshal(properties)
	if err != nil {
		return nil, err
	}
	var values map[string]json.RawMessage
	err = json.Unmarshal(data, &values)
	return values, err
}

// permissionChange describes a change of one permission set, or returns "" when nothing changed.
func permissionChange(previous, current []string) string {
	previous, current = sortedStrings(previous), sortedStrings(current)
	switch {
	case len(previous) == 0 && len(current) == 0:
		return ""
	case len(previous) == 0:
		return "access granted: " + strings.Join(current, ",")
	case len(current) == 0:
		return "access revoked"
	}
	parts := []string{}
	if added := subtractKeys(current, previous); len(added) > 0 {
		parts = append(parts, "permissions added: "+strings.Join(added, ","))
	}
	if removed := subtractKeys(previous, current); len(removed) > 0 {
		parts = append(parts, "permissions removed: "+strings.Join(removed, ","))
	}
	return strings.Join(parts, "; ")
}

func formatShareExpiration(value *time.Time) string {
	if value == nil {
		return "none"
	}
	return value.UTC().Format(time.RFC3339)
}

func formatShareFiles(fileIDs []uint) string {
	if len(fileIDs) == 0 {
		return "all"
	}
	return fmt.Sprintf("%d", len(fileIDs))
}

func sortedUserIDs(sets map[uint][]string) []uint {
	ids := make([]uint, 0, len(sets))
	for id := range sets {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
