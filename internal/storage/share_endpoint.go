package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"replica/internal/apiclient"
	"replica/internal/service"
)

const shareReadPermission = "read"
const shareCreatePermission = "create"
const shareUpdatePermission = "update"
const shareDeletePermission = "delete"

var (
	errShareTokenMissing       = errors.New("missing authenticated user")
	errShareTokenInvalid       = errors.New("invalid token")
	errShareTokenForbidden     = errors.New("inactive user")
	errShareCoordinatorOffline = errors.New("coordinator unavailable")
	errShareNotFound           = errors.New("share not found")
	errShareForbidden          = errors.New("share permission denied")
	errShareFileNotFound       = errors.New("file not found")
	errShareFileUnsynchronized = errors.New("local replica file is not synchronized")
	errShareVersionConflict    = errors.New("version conflict")
	errSharePrecondition       = errors.New("missing If-Match")
	errShareMalformedIfMatch   = errors.New("malformed If-Match")
	errShareInvalidRelativeURI = errors.New("invalid relative_uri")
	errShareCreateNotAllowed   = errors.New("create not allowed for inventory of type file")
	errShareFileAlreadyExists  = errors.New("active file already exists under the same relative_uri")
	errShareLocalStorageFailed = errors.New("local storage write/delete failed")
)

type shareFileListBody struct {
	Items []apiclient.ReplicaInventoryFile `json:"items"`
	Page  int                              `json:"page"`
	Count int                              `json:"count"`
	Total int64                            `json:"total"`
}

type shareListBody struct {
	Items []apiclient.Share `json:"items"`
	Page  int               `json:"page"`
	Count int               `json:"count"`
	Total int64             `json:"total"`
}

type shareAuthMeBody struct {
	UserID   uint   `json:"user_id"`
	Username string `json:"username"`
	Status   string `json:"status"`
}

func (r *Runtime) ServeUserLoginProxy(w http.ResponseWriter, req *http.Request) {
	r.serveUserAuthProxy(w, req, r.client.ProxyUserLogin)
}

func (r *Runtime) ServeUserRefreshProxy(w http.ResponseWriter, req *http.Request) {
	r.serveUserAuthProxy(w, req, r.client.ProxyUserRefresh)
}

func (r *Runtime) serveUserAuthProxy(w http.ResponseWriter, req *http.Request, proxy func(context.Context, []byte, string) (int, http.Header, []byte, error)) {
	if req.Method != http.MethodPost {
		writeStorageShareError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		writeStorageShareError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	status, headers, responseBody, err := proxy(req.Context(), body, req.Header.Get("Content-Type"))
	if err != nil {
		writeStorageShareError(w, http.StatusServiceUnavailable, errShareCoordinatorOffline.Error())
		return
	}

	copyProxyHeaders(w.Header(), headers)
	w.WriteHeader(status)
	_, _ = w.Write(responseBody)
}

func (r *Runtime) ServeUserMe(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		writeStorageShareError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	token, err := bearerShareToken(req.Header.Get("Authorization"))
	if err != nil {
		writeStorageShareError(w, http.StatusUnauthorized, err.Error())
		return
	}
	user, err := r.validateShareAPIToken(req.Context(), token)
	if err != nil {
		writeStorageShareError(w, storageShareAuthStatus(err), err.Error())
		return
	}
	writeStorageShareJSON(w, http.StatusOK, shareAuthMeBody{
		UserID:   user.UserID,
		Username: user.Username,
		Status:   user.Status,
	})
}

func (r *Runtime) ServeAuthenticatedShares(w http.ResponseWriter, req *http.Request) {
	userID, err := r.authenticateShareUser(req)
	if err != nil {
		writeStorageShareError(w, storageShareAuthStatus(err), err.Error())
		return
	}

	switch {
	case req.Method == http.MethodGet && req.PathValue("id") == "" && req.PathValue("file_id") == "":
		list, err := r.apiShareListForUser(req, userID)
		if err != nil {
			writeStorageShareError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeStorageShareJSON(w, http.StatusOK, list)
	case req.Method == http.MethodGet && req.PathValue("id") != "" && req.PathValue("file_id") == "" && !strings.HasSuffix(req.URL.Path, "/files"):
		shareID, ok := parseSharePathUint(w, req, "id")
		if !ok {
			return
		}
		share, _, _, err := r.GetUserShare(userID, shareID)
		if err != nil {
			writeStorageShareError(w, storageShareStatus(err), err.Error())
			return
		}
		writeStorageShareJSON(w, http.StatusOK, share)
	case req.Method == http.MethodGet && req.PathValue("id") != "" && strings.HasSuffix(req.URL.Path, "/files"):
		shareID, ok := parseSharePathUint(w, req, "id")
		if !ok {
			return
		}
		list, err := r.apiUserShareFileList(req, userID, shareID)
		if err != nil {
			writeStorageShareError(w, shareListErrorStatus(err), err.Error())
			return
		}
		writeStorageShareJSON(w, http.StatusOK, apiShareFileList(list))
	case req.Method == http.MethodPost && req.PathValue("id") != "" && strings.HasSuffix(req.URL.Path, "/files"):
		shareID, ok := parseSharePathUint(w, req, "id")
		if !ok {
			return
		}
		r.createShareFile(w, req, func(relativeURI string, file io.Reader, size int64) error {
			return r.CreateUserShareFile(req.Context(), userID, shareID, relativeURI, file, size)
		})
	case req.Method == http.MethodGet && req.PathValue("id") != "" && req.PathValue("file_id") != "" && strings.HasSuffix(req.URL.Path, "/thumbnail"):
		shareID, ok := parseSharePathUint(w, req, "id")
		if !ok {
			return
		}
		fileID, ok := parseSharePathUint(w, req, "file_id")
		if !ok {
			return
		}
		_, replica, _, err := r.GetUserShare(userID, shareID)
		if err != nil {
			writeStorageShareError(w, storageShareStatus(err), err.Error())
			return
		}
		r.serveShareFileThumbnail(w, req, replica, fileID)
	case req.Method == http.MethodGet && req.PathValue("id") != "" && req.PathValue("file_id") != "" && strings.HasSuffix(req.URL.Path, "/content"):
		shareID, ok := parseSharePathUint(w, req, "id")
		if !ok {
			return
		}
		fileID, ok := parseSharePathUint(w, req, "file_id")
		if !ok {
			return
		}
		share, replica, _, err := r.GetUserShare(userID, shareID)
		if err != nil {
			writeStorageShareError(w, storageShareStatus(err), err.Error())
			return
		}
		r.serveShareFileContent(w, req, share, replica, fileID)
	case req.Method == http.MethodPut && req.PathValue("id") != "" && req.PathValue("file_id") != "":
		shareID, ok := parseSharePathUint(w, req, "id")
		if !ok {
			return
		}
		fileID, ok := parseSharePathUint(w, req, "file_id")
		if !ok {
			return
		}
		r.updateShareFileContent(w, req, func(content io.Reader, size int64) error {
			return r.ReplaceUserShareFileContent(req.Context(), userID, shareID, fileID, req.Header.Get("If-Match"), content, size)
		})
	case req.Method == http.MethodDelete && req.PathValue("id") != "" && req.PathValue("file_id") != "":
		shareID, ok := parseSharePathUint(w, req, "id")
		if !ok {
			return
		}
		fileID, ok := parseSharePathUint(w, req, "file_id")
		if !ok {
			return
		}
		r.deleteShareFile(w, req, func() error {
			return r.DeleteUserShareFile(req.Context(), userID, shareID, fileID, req.Header.Get("If-Match"))
		})
	default:
		writeStorageShareError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (r *Runtime) ServePublicShares(w http.ResponseWriter, req *http.Request) {
	linkHash := strings.TrimSpace(req.PathValue("link_hash"))
	if linkHash == "" {
		writeStorageShareError(w, http.StatusNotFound, errShareNotFound.Error())
		return
	}

	switch {
	case req.Method == http.MethodGet && req.PathValue("file_id") == "" && !strings.HasSuffix(req.URL.Path, "/files"):
		share, _, _, err := r.GetPublicShare(linkHash)
		if err != nil {
			writeStorageShareError(w, storageShareStatus(err), err.Error())
			return
		}
		writeStorageShareJSON(w, http.StatusOK, share)
	case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/files"):
		list, err := r.apiPublicShareFileList(req, linkHash)
		if err != nil {
			writeStorageShareError(w, shareListErrorStatus(err), err.Error())
			return
		}
		writeStorageShareJSON(w, http.StatusOK, apiShareFileList(list))
	case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/files"):
		r.createShareFile(w, req, func(relativeURI string, file io.Reader, size int64) error {
			return r.CreatePublicShareFile(req.Context(), linkHash, relativeURI, file, size)
		})
	case req.Method == http.MethodGet && req.PathValue("file_id") != "" && strings.HasSuffix(req.URL.Path, "/thumbnail"):
		fileID, ok := parseSharePathUint(w, req, "file_id")
		if !ok {
			return
		}
		_, replica, _, err := r.GetPublicShare(linkHash)
		if err != nil {
			writeStorageShareError(w, storageShareStatus(err), err.Error())
			return
		}
		r.serveShareFileThumbnail(w, req, replica, fileID)
	case req.Method == http.MethodGet && req.PathValue("file_id") != "" && strings.HasSuffix(req.URL.Path, "/content"):
		fileID, ok := parseSharePathUint(w, req, "file_id")
		if !ok {
			return
		}
		share, replica, _, err := r.GetPublicShare(linkHash)
		if err != nil {
			writeStorageShareError(w, storageShareStatus(err), err.Error())
			return
		}
		r.serveShareFileContent(w, req, share, replica, fileID)
	case req.Method == http.MethodPut && req.PathValue("file_id") != "":
		fileID, ok := parseSharePathUint(w, req, "file_id")
		if !ok {
			return
		}
		r.updateShareFileContent(w, req, func(content io.Reader, size int64) error {
			return r.ReplacePublicShareFileContent(req.Context(), linkHash, fileID, req.Header.Get("If-Match"), content, size)
		})
	case req.Method == http.MethodDelete && req.PathValue("file_id") != "":
		fileID, ok := parseSharePathUint(w, req, "file_id")
		if !ok {
			return
		}
		r.deleteShareFile(w, req, func() error {
			return r.DeletePublicShareFile(req.Context(), linkHash, fileID, req.Header.Get("If-Match"))
		})
	default:
		writeStorageShareError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (r *Runtime) authenticateShareUser(req *http.Request) (uint, error) {
	token, err := bearerShareToken(req.Header.Get("Authorization"))
	if err != nil {
		return 0, err
	}
	user, err := r.validateShareAPIToken(req.Context(), token)
	if err != nil {
		return 0, err
	}
	return user.UserID, nil
}

func (r *Runtime) apiShareListForUser(req *http.Request, userID uint) (shareListBody, error) {
	page, count, err := parseShareListPagination(req)
	if err != nil {
		return shareListBody{}, err
	}
	filter, err := parseShareListFilter(req)
	if err != nil {
		return shareListBody{}, err
	}

	result, err := r.ListUserShares(userID, page, count, filter)
	if err != nil {
		return shareListBody{}, err
	}
	items := make([]apiclient.Share, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, item.Share)
	}

	return shareListBody{
		Items: items,
		Page:  result.Page,
		Count: result.Count,
		Total: result.Total,
	}, nil
}

func (r *Runtime) readableSharesForUser(userID uint) []apiclient.Share {
	shares, replicas := r.shareStateSnapshot()
	result := make([]apiclient.Share, 0, len(shares))
	for _, share := range shares {
		replica, ok := replicas[share.ReplicaID]
		if !ok || !shareAvailableOnReplica(share, replica, r.client.NodeID()) {
			continue
		}
		if shareHasUserPermission(share, userID, shareReadPermission) {
			result = append(result, share)
		}
	}
	return result
}

func parseShareListPagination(req *http.Request) (int, int, error) {
	page, err := parsePositiveIntQuery(req, "page", 1)
	if err != nil {
		return 0, 0, err
	}
	count, err := parsePositiveIntQuery(req, "count", 20)
	if err != nil {
		return 0, 0, err
	}
	if count > 100 {
		count = 100
	}
	return page, count, nil
}

func parseShareListFilter(req *http.Request) (ShareListFilter, error) {
	query := req.URL.Query()
	filter := ShareListFilter{
		Status: strings.TrimSpace(query.Get("status")),
		Name:   strings.TrimSpace(query.Get("name")),
	}
	if filter.Status != "" && filter.Status != "active" && filter.Status != "deleted" {
		return ShareListFilter{}, errors.New("invalid share status")
	}
	if rawReplicaID := strings.TrimSpace(query.Get("replica_id")); rawReplicaID != "" {
		value, err := strconv.ParseUint(rawReplicaID, 10, 64)
		if err != nil {
			return ShareListFilter{}, errors.New("invalid replica_id")
		}
		filter.ReplicaID = uint(value)
	}
	return filter, nil
}

func (r *Runtime) apiUserShareFileList(req *http.Request, userID, shareID uint) (ShareFileListResult, error) {
	page, count, err := parseShareListPagination(req)
	if err != nil {
		return ShareFileListResult{}, err
	}
	filter, err := parseShareFileListFilter(req)
	if err != nil {
		return ShareFileListResult{}, err
	}
	return r.ListUserShareFiles(userID, shareID, page, count, filter)
}

func (r *Runtime) apiPublicShareFileList(req *http.Request, linkHash string) (ShareFileListResult, error) {
	page, count, err := parseShareListPagination(req)
	if err != nil {
		return ShareFileListResult{}, err
	}
	filter, err := parseShareFileListFilter(req)
	if err != nil {
		return ShareFileListResult{}, err
	}
	return r.ListPublicShareFiles(linkHash, page, count, filter)
}

func parseShareFileListFilter(req *http.Request) (ShareFileListFilter, error) {
	query := req.URL.Query()
	filter := ShareFileListFilter{
		Name:  strings.TrimSpace(query.Get("name")),
		Path:  strings.TrimSpace(query.Get("path")),
		Sort:  strings.TrimSpace(query.Get("sort")),
		Order: strings.TrimSpace(query.Get("order")),
	}
	if filter.Sort != "" {
		switch filter.Sort {
		case "id", "name", "size", "created", "modified":
		default:
			return ShareFileListFilter{}, errors.New("invalid sort")
		}
	}
	if filter.Order != "" && filter.Order != "asc" && filter.Order != "desc" {
		return ShareFileListFilter{}, errors.New("invalid order")
	}
	return filter, nil
}

func apiShareFileList(result ShareFileListResult) shareFileListBody {
	return shareFileListBody{
		Items: result.Items,
		Page:  result.Page,
		Count: result.Count,
		Total: result.Total,
	}
}

func shareListErrorStatus(err error) int {
	if strings.HasPrefix(err.Error(), "invalid ") {
		return http.StatusBadRequest
	}
	return storageShareStatus(err)
}

func parsePositiveIntQuery(req *http.Request, name string, fallback int) (int, error) {
	raw := strings.TrimSpace(req.URL.Query().Get(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 0, errors.New("invalid " + name)
	}
	return value, nil
}

func (r *Runtime) authorizedUserShare(userID, shareID uint) (apiclient.Share, apiclient.Replica, error) {
	return r.availableUserShareWithPermission(userID, shareID, shareReadPermission)
}

func (r *Runtime) availableUserShareWithPermission(userID, shareID uint, permission string) (apiclient.Share, apiclient.Replica, error) {
	shares, replicas := r.shareStateSnapshot()
	for _, share := range shares {
		if share.ID != shareID {
			continue
		}
		replica, ok := replicas[share.ReplicaID]
		if !ok || !shareAvailableOnReplica(share, replica, r.client.NodeID()) {
			return apiclient.Share{}, apiclient.Replica{}, errShareNotFound
		}
		if !shareHasUserPermission(share, userID, permission) {
			return apiclient.Share{}, apiclient.Replica{}, errShareForbidden
		}
		return share, replica, nil
	}
	return apiclient.Share{}, apiclient.Replica{}, errShareNotFound
}

func (r *Runtime) authorizedPublicShare(linkHash string) (apiclient.Share, apiclient.Replica, error) {
	return r.authorizedPublicShareWithPermission(linkHash, shareReadPermission)
}

func (r *Runtime) authorizedPublicShareWithPermission(linkHash string, permission string) (apiclient.Share, apiclient.Replica, error) {
	shares, replicas := r.shareStateSnapshot()
	for _, share := range shares {
		if share.LinkHash == nil || *share.LinkHash != linkHash {
			continue
		}
		replica, ok := replicas[share.ReplicaID]
		if !ok || !shareAvailableOnReplica(share, replica, r.client.NodeID()) {
			return apiclient.Share{}, apiclient.Replica{}, errShareNotFound
		}
		if !permissionsContain(share.AnonymousPermissions, shareReadPermission) {
			return apiclient.Share{}, apiclient.Replica{}, errShareForbidden
		}
		if !permissionsContain(share.AnonymousPermissions, permission) {
			return apiclient.Share{}, apiclient.Replica{}, errShareForbidden
		}
		return share, replica, nil
	}
	return apiclient.Share{}, apiclient.Replica{}, errShareNotFound
}

func (r *Runtime) shareStateSnapshot() ([]apiclient.Share, map[uint]apiclient.Replica) {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()

	shares := append([]apiclient.Share(nil), r.shares...)
	replicas := make(map[uint]apiclient.Replica, len(r.replicas))
	for _, replica := range r.replicas {
		replicas[replica.ID] = replica
	}
	return shares, replicas
}

func (r *Runtime) availableShareFiles(replicaID uint) []apiclient.ReplicaInventoryFile {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()

	files := r.replicaFiles[replicaID]
	result := make([]apiclient.ReplicaInventoryFile, 0, len(files))
	for _, file := range files {
		if file.InventoryStatus == "active" && file.ReplicaStatus == "synchronized" {
			result = append(result, file)
		}
	}
	return result
}

func (r *Runtime) availableShareFileList(req *http.Request, replicaID uint) (shareFileListBody, error) {
	page, count, err := parseShareListPagination(req)
	if err != nil {
		return shareFileListBody{}, err
	}

	files := r.availableShareFiles(replicaID)
	total := int64(len(files))
	start := (page - 1) * count
	if start >= len(files) {
		files = []apiclient.ReplicaInventoryFile{}
	} else {
		end := start + count
		if end > len(files) {
			end = len(files)
		}
		files = files[start:end]
	}

	return shareFileListBody{
		Items: files,
		Page:  page,
		Count: count,
		Total: total,
	}, nil
}

func (r *Runtime) createShareFile(w http.ResponseWriter, req *http.Request, create func(relativeURI string, file io.Reader, size int64) error) {
	if err := req.ParseMultipartForm(32 << 20); err != nil {
		writeStorageShareError(w, http.StatusBadRequest, "invalid multipart request")
		return
	}

	file, header, err := req.FormFile("file")
	if err != nil {
		writeStorageShareError(w, http.StatusBadRequest, "invalid multipart request")
		return
	}
	defer file.Close()

	size := int64(-1)
	if header != nil {
		size = header.Size
	}
	if err := create(req.FormValue("relative_uri"), file, size); err != nil {
		writeStorageShareError(w, storageShareStatus(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (r *Runtime) updateShareFileContent(w http.ResponseWriter, req *http.Request, replace func(content io.Reader, size int64) error) {
	if err := replace(req.Body, req.ContentLength); err != nil {
		writeStorageShareError(w, storageShareStatus(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (r *Runtime) deleteShareFile(w http.ResponseWriter, _ *http.Request, deleteFile func() error) {
	if err := deleteFile(); err != nil {
		writeStorageShareError(w, storageShareStatus(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (r *Runtime) activeShareFileExists(share apiclient.Share, replicaID uint, relativeURI string) bool {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()

	for _, file := range r.replicaFiles[replicaID] {
		if file.InventoryID == share.InventoryID && file.RelativeURI == relativeURI && file.InventoryStatus == "active" {
			return true
		}
	}
	return false
}

func (r *Runtime) shareFileForMutation(share apiclient.Share, replicaID, fileID uint) (apiclient.ReplicaInventoryFile, error) {
	_, file, ok := r.findReplicaFile(replicaID, fileID)
	if !ok || file.InventoryID != share.InventoryID || file.InventoryStatus != "active" {
		return apiclient.ReplicaInventoryFile{}, errShareFileNotFound
	}
	if file.ReplicaStatus != "synchronized" {
		return apiclient.ReplicaInventoryFile{}, errShareFileUnsynchronized
	}
	return file, nil
}

func (r *Runtime) serveShareFileContent(w http.ResponseWriter, req *http.Request, share apiclient.Share, replica apiclient.Replica, fileID uint) {
	if err := validateShareRangeHeader(req.Header.Get("Range")); err != nil {
		writeStorageShareError(w, http.StatusBadRequest, err.Error())
		return
	}

	file, content, size, err := r.openShareFileContent(req, share, replica, fileID)
	if err != nil {
		writeStorageShareError(w, storageShareStatus(err), err.Error())
		return
	}
	defer content.Close()

	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	w.Header().Set("Content-Disposition", shareContentDisposition(file.RelativeURI))
	w.Header().Set("ETag", fmt.Sprintf(`"file-%d-v%d"`, file.FileID, file.InventoryVersion))

	if seeker, ok := content.(io.ReadSeeker); ok {
		http.ServeContent(w, req, path.Base(file.RelativeURI), file.Modified, seeker)
		return
	}

	w.Header().Set("Content-Type", contentTypeByName(file.RelativeURI))
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, content)
}

func (r *Runtime) openShareFileContent(req *http.Request, share apiclient.Share, replica apiclient.Replica, fileID uint) (apiclient.ReplicaInventoryFile, io.ReadCloser, int64, error) {
	file, err := r.shareFileForReadInShare(share, replica.ID, fileID)
	if err != nil {
		return apiclient.ReplicaInventoryFile{}, nil, 0, err
	}

	reader, err := GetReader(req.Context(), replica.URI, r.GetPprofile(replica.StorageProfile))
	if err != nil {
		return apiclient.ReplicaInventoryFile{}, nil, 0, err
	}
	content, size, err := reader.Open(req.Context(), replica.URI, file.RelativeURI)
	if err != nil {
		return apiclient.ReplicaInventoryFile{}, nil, 0, err
	}
	return file, content, size, nil
}

func contentTypeByName(name string) string {
	if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
		return contentType
	}
	return "application/octet-stream"
}

func shareContentDisposition(relativeURI string) string {
	filename := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(path.Base(relativeURI))
	return fmt.Sprintf(`inline; filename="%s"`, filename)
}

func validateShareRangeHeader(header string) error {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil
	}
	if !strings.HasPrefix(header, "bytes=") {
		return errors.New("malformed Range header")
	}
	ranges := strings.Split(strings.TrimPrefix(header, "bytes="), ",")
	if len(ranges) == 0 {
		return errors.New("malformed Range header")
	}
	for _, rawRange := range ranges {
		rawRange = strings.TrimSpace(rawRange)
		start, end, ok := strings.Cut(rawRange, "-")
		if !ok {
			return errors.New("malformed Range header")
		}
		start = strings.TrimSpace(start)
		end = strings.TrimSpace(end)
		if start == "" && end == "" {
			return errors.New("malformed Range header")
		}
		if start != "" {
			parsedStart, err := strconv.ParseInt(start, 10, 64)
			if err != nil || parsedStart < 0 {
				return errors.New("malformed Range header")
			}
			if end != "" {
				parsedEnd, err := strconv.ParseInt(end, 10, 64)
				if err != nil || parsedEnd < parsedStart {
					return errors.New("malformed Range header")
				}
			}
			continue
		}
		parsedSuffix, err := strconv.ParseInt(end, 10, 64)
		if err != nil || parsedSuffix < 1 {
			return errors.New("malformed Range header")
		}
	}
	return nil
}

func (r *Runtime) serveShareFileThumbnail(w http.ResponseWriter, req *http.Request, replica apiclient.Replica, fileID uint) {
	thumbnail, cfg := r.thumbnailSnapshot()
	if thumbnail == nil {
		writeStorageShareError(w, http.StatusInternalServerError, "thumbnail service unavailable")
		return
	}

	file, err := r.shareFileForRead(replica.ID, fileID)
	if err != nil {
		writeStorageShareError(w, storageShareStatus(err), err.Error())
		return
	}

	size, err := service.ParseThumbnailSize(req.URL.Query().Get("size"), cfg.Sharing.ThumbnailDefaultSize, cfg.Sharing.ThumbnailSizes)
	if err != nil {
		writeStorageShareError(w, http.StatusBadRequest, err.Error())
		return
	}

	source, err := r.thumbnailSource(req.Context(), replica, file)
	if err != nil {
		writeStorageShareError(w, storageShareStatus(err), err.Error())
		return
	}

	result, err := thumbnail.GetOrCreateThumbnail(req.Context(), service.ThumbnailRequest{
		FileID:      file.FileID,
		FileVersion: file.InventoryVersion,
		Size:        size,
		RelativeURI: file.RelativeURI,
		Source:      source,
	})
	if err != nil {
		writeStorageShareError(w, thumbnailStatus(err), err.Error())
		return
	}

	thumbnailFile, err := os.Open(result.Path)
	if err != nil {
		writeStorageShareError(w, http.StatusInternalServerError, errShareLocalStorageFailed.Error())
		return
	}
	defer thumbnailFile.Close()

	info, err := thumbnailFile.Stat()
	if err != nil || info.IsDir() {
		writeStorageShareError(w, http.StatusInternalServerError, errShareLocalStorageFailed.Error())
		return
	}

	w.Header().Set("Content-Type", result.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("ETag", fmt.Sprintf(`"file-%d-v%d-s%d"`, file.FileID, file.InventoryVersion, size))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, thumbnailFile)
}

func (r *Runtime) shareFileForRead(replicaID, fileID uint) (apiclient.ReplicaInventoryFile, error) {
	_, file, ok := r.findReplicaFile(replicaID, fileID)
	if !ok || file.InventoryStatus != "active" {
		return apiclient.ReplicaInventoryFile{}, errShareFileNotFound
	}
	if file.ReplicaStatus != "synchronized" {
		return apiclient.ReplicaInventoryFile{}, errShareFileUnsynchronized
	}
	if file.InventoryVersion == 0 {
		return apiclient.ReplicaInventoryFile{}, errShareFileNotFound
	}
	return file, nil
}

func (r *Runtime) thumbnailSource(ctx context.Context, replica apiclient.Replica, file apiclient.ReplicaInventoryFile) (service.ThumbnailSource, error) {
	uri, err := url.Parse(replica.URI)
	if err != nil {
		return nil, err
	}

	if uri.Scheme == "s3" {
		location, key, err := resolveS3ReadKey(replica.URI, file.RelativeURI)
		if err != nil {
			return nil, err
		}

		client, err := s3Provider.Client(ctx, r.GetPprofile(replica.StorageProfile))
		if err != nil {
			return nil, err
		}
		return service.NewS3ThumbnailSource(client, location.Bucket, key, file.RelativeURI, file.Size), nil
	}

	localPath, err := resolveReplicaFilePath(replica.URI, file.RelativeURI)
	if err != nil {
		return nil, err
	}
	return service.NewLocalFileThumbnailSource(localPath, file.RelativeURI), nil
}

func shareAvailableOnReplica(share apiclient.Share, replica apiclient.Replica, nodeID string) bool {
	if share.Status != "active" || replica.Status != "active" {
		return false
	}
	if replica.NodeID != nodeID {
		return false
	}
	if share.ShareExpiration != nil && !time.Now().UTC().Before(*share.ShareExpiration) {
		return false
	}
	return true
}

func shareHasUserPermission(share apiclient.Share, userID uint, permission string) bool {
	for _, userPermission := range share.UserPermissions {
		if userPermission.UserID == userID && permissionsContain(userPermission.Permissions, permission) {
			return true
		}
	}
	return false
}

func permissionsContain(permissions []string, permission string) bool {
	for _, candidate := range permissions {
		if candidate == permission {
			return true
		}
	}
	return false
}

func bearerShareToken(header string) (string, error) {
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
		return "", errShareTokenMissing
	}
	return strings.TrimSpace(parts[1]), nil
}

func parseSharePathUint(w http.ResponseWriter, req *http.Request, name string) (uint, bool) {
	value, err := strconv.ParseUint(req.PathValue(name), 10, 64)
	if err != nil || value == 0 {
		writeStorageShareError(w, http.StatusBadRequest, "invalid "+name)
		return 0, false
	}
	return uint(value), true
}

func storageShareAuthStatus(err error) int {
	var apiErr *apiclient.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusUnauthorized:
			return http.StatusUnauthorized
		case http.StatusForbidden:
			return http.StatusForbidden
		default:
			return http.StatusServiceUnavailable
		}
	}
	if errors.Is(err, errShareTokenMissing) || errors.Is(err, errShareTokenInvalid) {
		return http.StatusUnauthorized
	}
	if errors.Is(err, errShareTokenForbidden) {
		return http.StatusForbidden
	}
	return http.StatusServiceUnavailable
}

func storageShareStatus(err error) int {
	switch {
	case errors.Is(err, errShareForbidden):
		return http.StatusForbidden
	case errors.Is(err, errShareNotFound), errors.Is(err, errShareFileNotFound):
		return http.StatusNotFound
	case errors.Is(err, errShareFileUnsynchronized), errors.Is(err, errShareVersionConflict), errors.Is(err, errShareCreateNotAllowed), errors.Is(err, errShareFileAlreadyExists):
		return http.StatusConflict
	case errors.Is(err, errSharePrecondition):
		return http.StatusPreconditionRequired
	case errors.Is(err, errShareMalformedIfMatch), errors.Is(err, errShareInvalidRelativeURI):
		return http.StatusBadRequest
	case errors.Is(err, errTransferUnsupportedURI):
		return http.StatusNotImplemented
	default:
		return http.StatusInternalServerError
	}
}

func thumbnailStatus(err error) int {
	switch {
	case errors.Is(err, service.ErrInvalidThumbnailSize):
		return http.StatusBadRequest
	case errors.Is(err, service.ErrThumbnailSourceMissing):
		return http.StatusNotFound
	case errors.Is(err, errShareCoordinatorOffline):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

func writeStorageShareJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeStorageShareError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"error":%q}`+"\n", message)
}

func copyProxyHeaders(dst http.Header, src http.Header) {
	for key, values := range src {
		if strings.EqualFold(key, "Content-Length") {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}
