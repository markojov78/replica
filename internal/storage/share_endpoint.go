package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"replica/internal/apiclient"
)

const shareReadPermission = "read"

var (
	errShareTokenMissing       = errors.New("missing authenticated user")
	errShareTokenInvalid       = errors.New("invalid token")
	errShareTokenForbidden     = errors.New("inactive user")
	errShareCoordinatorOffline = errors.New("coordinator unavailable")
	errShareNotFound           = errors.New("share not found")
	errShareForbidden          = errors.New("share permission denied")
	errShareFileNotFound       = errors.New("file not found")
	errShareFileUnsynchronized = errors.New("local replica file is not synchronized")
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

type shareListFilter struct {
	status    string
	replicaID uint
	name      string
}

type shareAuthMeBody struct {
	UserID uint   `json:"user_id"`
	Status string `json:"status"`
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
		UserID: user.UserID,
		Status: user.Status,
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
		list, err := r.readableShareListForUser(req, userID)
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
		share, _, err := r.authorizedUserShare(userID, shareID)
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
		_, replica, err := r.authorizedUserShare(userID, shareID)
		if err != nil {
			writeStorageShareError(w, storageShareStatus(err), err.Error())
			return
		}
		list, err := r.availableShareFileList(req, replica.ID)
		if err != nil {
			writeStorageShareError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeStorageShareJSON(w, http.StatusOK, list)
	case req.Method == http.MethodGet && req.PathValue("id") != "" && req.PathValue("file_id") != "":
		shareID, ok := parseSharePathUint(w, req, "id")
		if !ok {
			return
		}
		fileID, ok := parseSharePathUint(w, req, "file_id")
		if !ok {
			return
		}
		_, replica, err := r.authorizedUserShare(userID, shareID)
		if err != nil {
			writeStorageShareError(w, storageShareStatus(err), err.Error())
			return
		}
		r.serveShareFileContent(w, req, replica, fileID)
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
		share, _, err := r.authorizedPublicShare(linkHash)
		if err != nil {
			writeStorageShareError(w, storageShareStatus(err), err.Error())
			return
		}
		writeStorageShareJSON(w, http.StatusOK, share)
	case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/files"):
		_, replica, err := r.authorizedPublicShare(linkHash)
		if err != nil {
			writeStorageShareError(w, storageShareStatus(err), err.Error())
			return
		}
		list, err := r.availableShareFileList(req, replica.ID)
		if err != nil {
			writeStorageShareError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeStorageShareJSON(w, http.StatusOK, list)
	case req.Method == http.MethodGet && req.PathValue("file_id") != "":
		fileID, ok := parseSharePathUint(w, req, "file_id")
		if !ok {
			return
		}
		_, replica, err := r.authorizedPublicShare(linkHash)
		if err != nil {
			writeStorageShareError(w, storageShareStatus(err), err.Error())
			return
		}
		r.serveShareFileContent(w, req, replica, fileID)
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

func (r *Runtime) readableShareListForUser(req *http.Request, userID uint) (shareListBody, error) {
	page, count, err := parseShareListPagination(req)
	if err != nil {
		return shareListBody{}, err
	}
	filter, err := parseShareListFilter(req)
	if err != nil {
		return shareListBody{}, err
	}

	shares := r.readableSharesForUser(userID)
	filtered := make([]apiclient.Share, 0, len(shares))
	for _, share := range shares {
		if filter.status != "" && share.Status != filter.status {
			continue
		}
		if filter.replicaID > 0 && share.ReplicaID != filter.replicaID {
			continue
		}
		if filter.name != "" && share.Name != filter.name {
			continue
		}
		filtered = append(filtered, share)
	}

	total := int64(len(filtered))
	start := (page - 1) * count
	if start >= len(filtered) {
		filtered = []apiclient.Share{}
	} else {
		end := start + count
		if end > len(filtered) {
			end = len(filtered)
		}
		filtered = filtered[start:end]
	}

	return shareListBody{
		Items: filtered,
		Page:  page,
		Count: count,
		Total: total,
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

func parseShareListFilter(req *http.Request) (shareListFilter, error) {
	query := req.URL.Query()
	filter := shareListFilter{
		status: strings.TrimSpace(query.Get("status")),
		name:   strings.TrimSpace(query.Get("name")),
	}
	if filter.status != "" && filter.status != "active" && filter.status != "deleted" {
		return shareListFilter{}, errors.New("invalid share status")
	}
	if rawReplicaID := strings.TrimSpace(query.Get("replica_id")); rawReplicaID != "" {
		value, err := strconv.ParseUint(rawReplicaID, 10, 64)
		if err != nil {
			return shareListFilter{}, errors.New("invalid replica_id")
		}
		filter.replicaID = uint(value)
	}
	return filter, nil
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
	shares, replicas := r.shareStateSnapshot()
	for _, share := range shares {
		if share.ID != shareID {
			continue
		}
		replica, ok := replicas[share.ReplicaID]
		if !ok || !shareAvailableOnReplica(share, replica, r.client.NodeID()) {
			return apiclient.Share{}, apiclient.Replica{}, errShareNotFound
		}
		if !shareHasUserPermission(share, userID, shareReadPermission) {
			return apiclient.Share{}, apiclient.Replica{}, errShareForbidden
		}
		return share, replica, nil
	}
	return apiclient.Share{}, apiclient.Replica{}, errShareNotFound
}

func (r *Runtime) authorizedPublicShare(linkHash string) (apiclient.Share, apiclient.Replica, error) {
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

func (r *Runtime) serveShareFileContent(w http.ResponseWriter, req *http.Request, replica apiclient.Replica, fileID uint) {
	file, size, err := r.openShareFileContent(req, replica, fileID)
	if err != nil {
		writeStorageShareError(w, storageShareStatus(err), err.Error())
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, file)
}

func (r *Runtime) openShareFileContent(req *http.Request, replica apiclient.Replica, fileID uint) (io.ReadCloser, int64, error) {
	_, file, ok := r.findReplicaFile(replica.ID, fileID)
	if !ok {
		return nil, 0, errShareFileNotFound
	}
	if file.InventoryStatus != "active" {
		return nil, 0, errShareFileNotFound
	}
	if file.ReplicaStatus != "synchronized" {
		return nil, 0, errShareFileUnsynchronized
	}

	reader, err := GetReader(req.Context(), replica.URI)
	if err != nil {
		return nil, 0, err
	}
	return reader.Open(req.Context(), replica.URI, file.RelativeURI)
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
	case errors.Is(err, errShareFileUnsynchronized):
		return http.StatusConflict
	case errors.Is(err, errTransferUnsupportedURI):
		return http.StatusNotImplemented
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
