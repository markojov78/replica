package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"replica/internal/apiclient"
	"replica/internal/config"
)

type ShareListFilter struct {
	Status    string
	ReplicaID uint
	Name      string
}

type ShareFileListFilter struct {
	Name  string
	Path  string
	Sort  string
	Order string
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

type ShareTokenPair struct {
	UserID                uint      `json:"user_id"`
	AccessToken           string    `json:"access_token"`
	RefreshToken          string    `json:"refresh_token"`
	AccessTokenExpiresAt  time.Time `json:"access_token_expires_at"`
	RefreshTokenExpiresAt time.Time `json:"refresh_token_expires_at"`
}

func (r *Runtime) AuthenticateShareUserAuthorization(ctx context.Context, authorization string) (*apiclient.ValidatedUserToken, error) {
	token, err := bearerShareToken(authorization)
	if err != nil {
		return nil, err
	}
	return r.validateShareAPIToken(ctx, token)
}

func (r *Runtime) LoginShareUser(ctx context.Context, username string, password string) (ShareTokenPair, int, error) {
	body, err := json.Marshal(map[string]string{
		"username": username,
		"password": password,
	})
	if err != nil {
		return ShareTokenPair{}, http.StatusInternalServerError, err
	}
	return r.proxyShareUserTokenPair(ctx, body, r.client.ProxyUserLogin)
}

func (r *Runtime) RefreshShareUser(ctx context.Context, refreshToken string) (ShareTokenPair, int, error) {
	body, err := json.Marshal(map[string]string{
		"refresh_token": refreshToken,
	})
	if err != nil {
		return ShareTokenPair{}, http.StatusInternalServerError, err
	}
	return r.proxyShareUserTokenPair(ctx, body, r.client.ProxyUserRefresh)
}

func (r *Runtime) proxyShareUserTokenPair(ctx context.Context, body []byte, proxy func(context.Context, []byte, string) (int, http.Header, []byte, error)) (ShareTokenPair, int, error) {
	status, _, responseBody, err := proxy(ctx, body, "application/json")
	if err != nil {
		return ShareTokenPair{}, http.StatusServiceUnavailable, err
	}
	if status < 200 || status >= 300 {
		return ShareTokenPair{}, status, errors.New(proxyShareError(responseBody))
	}
	var pair ShareTokenPair
	if err := json.NewDecoder(bytes.NewReader(responseBody)).Decode(&pair); err != nil {
		return ShareTokenPair{}, http.StatusServiceUnavailable, err
	}
	return pair, status, nil
}

func proxyShareError(body []byte) string {
	var problem map[string]any
	if err := json.Unmarshal(body, &problem); err == nil {
		for _, key := range []string{"error", "detail", "title"} {
			if value, ok := problem[key].(string); ok && strings.TrimSpace(value) != "" {
				return value
			}
		}
	}
	if message := strings.TrimSpace(string(body)); message != "" {
		return message
	}
	return "auth request failed"
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

func (r *Runtime) SharingEnabled() bool {
	return r.SharingConfig().Enabled
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

func (r *Runtime) ListUserShareFiles(userID, shareID uint, page, count int, filter ShareFileListFilter) (ShareFileListResult, error) {
	share, replica, permissions, err := r.GetUserShare(userID, shareID)
	if err != nil {
		return ShareFileListResult{}, err
	}
	return r.shareFileList(share, replica.ID, permissions, page, count, filter), nil
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

func (r *Runtime) ServeUserShareFileContent(w http.ResponseWriter, req *http.Request, userID, shareID, fileID uint) {
	share, replica, _, err := r.GetUserShare(userID, shareID)
	if err != nil {
		writeStorageShareError(w, storageShareStatus(err), err.Error())
		return
	}
	r.serveShareFileContent(w, req, share, replica, fileID)
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

func (r *Runtime) ListPublicShareFiles(linkHash string, page, count int, filter ShareFileListFilter) (ShareFileListResult, error) {
	share, replica, permissions, err := r.GetPublicShare(linkHash)
	if err != nil {
		return ShareFileListResult{}, err
	}
	return r.shareFileList(share, replica.ID, permissions, page, count, filter), nil
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

func (r *Runtime) ServePublicShareFileContent(w http.ResponseWriter, req *http.Request, linkHash string, fileID uint) {
	share, replica, _, err := r.GetPublicShare(linkHash)
	if err != nil {
		writeStorageShareError(w, storageShareStatus(err), err.Error())
		return
	}
	r.serveShareFileContent(w, req, share, replica, fileID)
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

func (r *Runtime) shareFileList(share apiclient.Share, replicaID uint, permissions []string, page, count int, filter ShareFileListFilter) ShareFileListResult {
	page, count = normalizeSharePagination(page, count)
	files := r.availableShareFiles(replicaID)
	files = filterShareFiles(share, files, filter)
	sortShareFiles(files, filter)
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

func filterShareFiles(share apiclient.Share, files []apiclient.ReplicaInventoryFile, filter ShareFileListFilter) []apiclient.ReplicaInventoryFile {
	name := strings.ToLower(strings.TrimSpace(filter.Name))
	pathFilter := strings.ToLower(strings.TrimSpace(filter.Path))
	result := make([]apiclient.ReplicaInventoryFile, 0, len(files))
	for _, file := range files {
		if share.InventoryID != 0 && file.InventoryID != share.InventoryID {
			continue
		}
		filename := strings.ToLower(path.Base(file.RelativeURI))
		if name != "" && !strings.Contains(filename, name) {
			continue
		}
		dir := path.Dir(file.RelativeURI)
		if dir == "." {
			dir = ""
		}
		if pathFilter != "" && !strings.Contains(strings.ToLower(dir), pathFilter) {
			continue
		}
		result = append(result, file)
	}
	return result
}

func sortShareFiles(files []apiclient.ReplicaInventoryFile, filter ShareFileListFilter) {
	sortBy := filter.Sort
	if sortBy == "" {
		sortBy = "id"
	}
	desc := filter.Order == "desc"
	sort.SliceStable(files, func(i, j int) bool {
		less := false
		switch sortBy {
		case "name":
			left := strings.ToLower(path.Base(files[i].RelativeURI))
			right := strings.ToLower(path.Base(files[j].RelativeURI))
			if left == right {
				return false
			}
			less = left < right
		case "size":
			if files[i].Size == files[j].Size {
				return false
			}
			less = files[i].Size < files[j].Size
		case "created":
			if files[i].Created.Equal(files[j].Created) {
				return false
			}
			less = files[i].Created.Before(files[j].Created)
		case "modified":
			if files[i].Modified.Equal(files[j].Modified) {
				return false
			}
			less = files[i].Modified.Before(files[j].Modified)
		default:
			if files[i].FileID == files[j].FileID {
				return false
			}
			less = files[i].FileID < files[j].FileID
		}
		if desc {
			return !less
		}
		return less
	})
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
	profile, err := r.GetPprofile(replica.StorageProfile)
	if err != nil {
		return err
	}
	writer, err := GetWriter(ctx, replica.URI, profile)
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
	profile, err := r.GetPprofile(replica.StorageProfile)
	if err != nil {
		return err
	}
	writer, err := GetWriter(ctx, replica.URI, profile)
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
	profile, err := r.GetPprofile(replica.StorageProfile)
	if err != nil {
		return err
	}
	writer, err := GetWriter(ctx, replica.URI, profile)
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
