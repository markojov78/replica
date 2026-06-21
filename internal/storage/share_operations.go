package storage

import (
	"context"
	"io"
	"sort"
	"strconv"
	"strings"

	"replica/internal/apiclient"
	"replica/internal/config"
)

type ShareListFilter struct {
	Status    string
	ReplicaID uint
	Name      string
}

type ShareWithPermissions struct {
	apiclient.Share
	EffectivePermissions []string
	FileCount            int64
}

type ShareListResult struct {
	Items []ShareWithPermissions
	Page  int
	Count int
	Total int64
}

type ShareFileListResult struct {
	Share                apiclient.Share
	EffectivePermissions []string
	Items                []apiclient.ReplicaInventoryFile
	Page                 int
	Count                int
	Total                int64
}

func (r *Runtime) AuthenticateShareUserAuthorization(ctx context.Context, authorization string) (*apiclient.ValidatedUserToken, error) {
	token, err := bearerShareToken(authorization)
	if err != nil {
		return nil, err
	}
	return r.validateShareAPIToken(ctx, token)
}

func (r *Runtime) ShareAuthErrorStatus(err error) int {
	return storageShareAuthStatus(err)
}

func (r *Runtime) ShareOperationErrorStatus(err error) int {
	return storageShareStatus(err)
}

func (r *Runtime) SharingConfig() config.SharingConfig {
	_, cfg := r.thumbnailSnapshot()
	return cfg.Sharing
}

func (r *Runtime) ListUserShares(userID uint, page, count int, filter ShareListFilter) (ShareListResult, error) {
	page, count = normalizeSharePagination(page, count)
	shares := r.readableSharesForUser(userID)
	items := make([]ShareWithPermissions, 0, len(shares))
	for _, share := range shares {
		if filter.Status != "" && share.Status != filter.Status {
			continue
		}
		if filter.ReplicaID > 0 && share.ReplicaID != filter.ReplicaID {
			continue
		}
		if filter.Name != "" && share.Name != filter.Name {
			continue
		}
		items = append(items, ShareWithPermissions{
			Share:                share,
			EffectivePermissions: userSharePermissions(share, userID),
			FileCount:            int64(len(r.availableShareFiles(share.ReplicaID))),
		})
	}
	total := int64(len(items))
	items = paginateShares(items, page, count)
	return ShareListResult{Items: items, Page: page, Count: count, Total: total}, nil
}

func (r *Runtime) GetUserShare(userID, shareID uint) (apiclient.Share, apiclient.Replica, []string, error) {
	share, replica, err := r.authorizedUserShare(userID, shareID)
	if err != nil {
		return apiclient.Share{}, apiclient.Replica{}, nil, err
	}
	return share, replica, userSharePermissions(share, userID), nil
}

func (r *Runtime) ListUserShareFiles(userID, shareID uint, page, count int) (ShareFileListResult, error) {
	share, replica, permissions, err := r.GetUserShare(userID, shareID)
	if err != nil {
		return ShareFileListResult{}, err
	}
	return r.shareFileList(share, replica.ID, permissions, page, count), nil
}

func (r *Runtime) GetUserShareFile(userID, shareID, fileID uint) (apiclient.Share, apiclient.Replica, apiclient.ReplicaInventoryFile, error) {
	share, replica, _, err := r.GetUserShare(userID, shareID)
	if err != nil {
		return apiclient.Share{}, apiclient.Replica{}, apiclient.ReplicaInventoryFile{}, err
	}
	file, err := r.shareFileForReadInShare(share, replica.ID, fileID)
	if err != nil {
		return apiclient.Share{}, apiclient.Replica{}, apiclient.ReplicaInventoryFile{}, err
	}
	return share, replica, file, nil
}

func (r *Runtime) CreateUserShareFile(ctx context.Context, userID, shareID uint, relativeURI string, file io.Reader, size int64) error {
	share, replica, err := r.availableUserShareWithPermission(userID, shareID, shareCreatePermission)
	if err != nil {
		return err
	}
	return r.createShareFileOperation(ctx, share, replica, relativeURI, file, size)
}

func (r *Runtime) ReplaceUserShareFileContent(ctx context.Context, userID, shareID, fileID uint, ifMatch string, content io.Reader, size int64) error {
	share, replica, err := r.availableUserShareWithPermission(userID, shareID, shareUpdatePermission)
	if err != nil {
		return err
	}
	return r.replaceShareFileContentOperation(ctx, share, replica, fileID, ifMatch, content, size)
}

func (r *Runtime) DeleteUserShareFile(ctx context.Context, userID, shareID, fileID uint, ifMatch string) error {
	share, replica, err := r.availableUserShareWithPermission(userID, shareID, shareDeletePermission)
	if err != nil {
		return err
	}
	return r.deleteShareFileOperation(ctx, share, replica, fileID, ifMatch)
}

func (r *Runtime) GetPublicShare(linkHash string) (apiclient.Share, apiclient.Replica, []string, error) {
	share, replica, err := r.authorizedPublicShare(linkHash)
	if err != nil {
		return apiclient.Share{}, apiclient.Replica{}, nil, err
	}
	return share, replica, append([]string(nil), share.AnonymousPermissions...), nil
}

func (r *Runtime) ListPublicShareFiles(linkHash string, page, count int) (ShareFileListResult, error) {
	share, replica, permissions, err := r.GetPublicShare(linkHash)
	if err != nil {
		return ShareFileListResult{}, err
	}
	return r.shareFileList(share, replica.ID, permissions, page, count), nil
}

func (r *Runtime) GetPublicShareFile(linkHash string, fileID uint) (apiclient.Share, apiclient.Replica, apiclient.ReplicaInventoryFile, error) {
	share, replica, _, err := r.GetPublicShare(linkHash)
	if err != nil {
		return apiclient.Share{}, apiclient.Replica{}, apiclient.ReplicaInventoryFile{}, err
	}
	file, err := r.shareFileForReadInShare(share, replica.ID, fileID)
	if err != nil {
		return apiclient.Share{}, apiclient.Replica{}, apiclient.ReplicaInventoryFile{}, err
	}
	return share, replica, file, nil
}

func (r *Runtime) CreatePublicShareFile(ctx context.Context, linkHash, relativeURI string, file io.Reader, size int64) error {
	share, replica, err := r.authorizedPublicShareWithPermission(linkHash, shareCreatePermission)
	if err != nil {
		return err
	}
	return r.createShareFileOperation(ctx, share, replica, relativeURI, file, size)
}

func (r *Runtime) ReplacePublicShareFileContent(ctx context.Context, linkHash string, fileID uint, ifMatch string, content io.Reader, size int64) error {
	share, replica, err := r.authorizedPublicShareWithPermission(linkHash, shareUpdatePermission)
	if err != nil {
		return err
	}
	return r.replaceShareFileContentOperation(ctx, share, replica, fileID, ifMatch, content, size)
}

func (r *Runtime) DeletePublicShareFile(ctx context.Context, linkHash string, fileID uint, ifMatch string) error {
	share, replica, err := r.authorizedPublicShareWithPermission(linkHash, shareDeletePermission)
	if err != nil {
		return err
	}
	return r.deleteShareFileOperation(ctx, share, replica, fileID, ifMatch)
}

func (r *Runtime) shareFileList(share apiclient.Share, replicaID uint, permissions []string, page, count int) ShareFileListResult {
	page, count = normalizeSharePagination(page, count)
	files := r.availableShareFiles(replicaID)
	total := int64(len(files))
	files = paginateFiles(files, page, count)
	return ShareFileListResult{
		Share:                share,
		EffectivePermissions: permissions,
		Items:                files,
		Page:                 page,
		Count:                count,
		Total:                total,
	}
}

func (r *Runtime) createShareFileOperation(ctx context.Context, share apiclient.Share, replica apiclient.Replica, relativeURI string, file io.Reader, size int64) error {
	if replica.InventoryType != "folder" {
		return errShareCreateNotAllowed
	}
	cleanRelativeURI, err := cleanWriteRelativeURI(relativeURI)
	if err != nil {
		return errShareInvalidRelativeURI
	}
	if r.activeShareFileExists(share, replica.ID, cleanRelativeURI) {
		return errShareFileAlreadyExists
	}
	writer, err := GetWriter(ctx, replica.URI)
	if err != nil {
		return err
	}
	if err := writer.Save(ctx, replica.URI, cleanRelativeURI, file, size); err != nil {
		return errShareLocalStorageFailed
	}
	return nil
}

func (r *Runtime) replaceShareFileContentOperation(ctx context.Context, share apiclient.Share, replica apiclient.Replica, fileID uint, ifMatch string, content io.Reader, size int64) error {
	file, err := r.shareFileForMutation(share, replica.ID, fileID)
	if err != nil {
		return err
	}
	if err := validateShareIfMatchValue(ifMatch, file.InventoryVersion); err != nil {
		return err
	}
	writer, err := GetWriter(ctx, replica.URI)
	if err != nil {
		return err
	}
	if err := writer.Save(ctx, replica.URI, file.RelativeURI, content, size); err != nil {
		return errShareLocalStorageFailed
	}
	return nil
}

func (r *Runtime) deleteShareFileOperation(ctx context.Context, share apiclient.Share, replica apiclient.Replica, fileID uint, ifMatch string) error {
	file, err := r.shareFileForMutation(share, replica.ID, fileID)
	if err != nil {
		return err
	}
	if err := validateShareIfMatchValue(ifMatch, file.InventoryVersion); err != nil {
		return err
	}
	writer, err := GetWriter(ctx, replica.URI)
	if err != nil {
		return err
	}
	if err := writer.Delete(ctx, replica.URI, file.RelativeURI); err != nil {
		return errShareLocalStorageFailed
	}
	return nil
}

func validateShareIfMatchValue(raw string, expectedVersion uint) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errSharePrecondition
	}
	value, err := strconv.Unquote(raw)
	if err != nil || strings.TrimSpace(value) == "" {
		return errShareMalformedIfMatch
	}
	parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed == 0 {
		return errShareMalformedIfMatch
	}
	if uint(parsed) != expectedVersion {
		return errShareVersionConflict
	}
	return nil
}

func (r *Runtime) shareFileForReadInShare(share apiclient.Share, replicaID, fileID uint) (apiclient.ReplicaInventoryFile, error) {
	_, file, ok := r.findReplicaFile(replicaID, fileID)
	if !ok || file.InventoryID != share.InventoryID || file.InventoryStatus != "active" {
		return apiclient.ReplicaInventoryFile{}, errShareFileNotFound
	}
	if file.ReplicaStatus != "synchronized" {
		return apiclient.ReplicaInventoryFile{}, errShareFileUnsynchronized
	}
	return file, nil
}

func normalizeSharePagination(page, count int) (int, int) {
	if page < 1 {
		page = 1
	}
	if count < 1 {
		count = 20
	}
	if count > 100 {
		count = 100
	}
	return page, count
}

func paginateShares(items []ShareWithPermissions, page, count int) []ShareWithPermissions {
	start, end := pageBounds(page, count, len(items))
	if start >= len(items) {
		return []ShareWithPermissions{}
	}
	return items[start:end]
}

func paginateFiles(items []apiclient.ReplicaInventoryFile, page, count int) []apiclient.ReplicaInventoryFile {
	start, end := pageBounds(page, count, len(items))
	if start >= len(items) {
		return []apiclient.ReplicaInventoryFile{}
	}
	return items[start:end]
}

func pageBounds(page, count, length int) (int, int) {
	start := (page - 1) * count
	if start >= length {
		return start, start
	}
	end := start + count
	if end > length {
		end = length
	}
	return start, end
}

func userSharePermissions(share apiclient.Share, userID uint) []string {
	for _, userPermission := range share.UserPermissions {
		if userPermission.UserID == userID {
			permissions := append([]string(nil), userPermission.Permissions...)
			sort.Strings(permissions)
			return permissions
		}
	}
	return nil
}

func PermissionAllowed(permissions []string, permission string) bool {
	for _, candidate := range permissions {
		if strings.EqualFold(candidate, permission) {
			return true
		}
	}
	return false
}
