package storage

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"replica/internal/apiclient"

	"github.com/golang-jwt/jwt/v5"
)

const transferTokenPurpose = "replica_file_transfer"

var (
	errTransferTokenMissing     = errors.New("missing transfer token")
	errTransferTokenInvalid     = errors.New("invalid transfer token")
	errTransferTokenForbidden   = errors.New("transfer token does not authorize this file")
	errTransferKeyMissing       = errors.New("transfer token public key is not configured")
	errTransferReplicaNotFound  = errors.New("replica not found in local state")
	errTransferFileNotFound     = errors.New("file not found in local state")
	errTransferVersionConflict  = errors.New("local replica file version is not synchronized")
	errTransferUnsupportedURI   = errors.New("replica uri is not a local filesystem path")
	errTransferInvalidLocalPath = errors.New("resolved file path escapes replica root")
)

var getTransferReader = GetReader

type transferTokenClaims struct {
	Purpose              string `json:"purpose"`
	SourceReplicaID      uint   `json:"source_replica_id"`
	DestinationReplicaID uint   `json:"destination_replica_id"`
	TargetReplicaID      uint   `json:"target_replica_id,omitempty"`
	SourceNodeID         string `json:"source_node_id"`
	DestinationNodeID    string `json:"destination_node_id"`
	jwt.RegisteredClaims
}

func (r *Runtime) ServeReplicaFileContent(w http.ResponseWriter, req *http.Request) {
	replicaID, err := parseUintPathValue(req, "replica_id")
	if err != nil {
		writeStorageTransferError(w, http.StatusBadRequest, "invalid replica_id")
		return
	}

	fileID, err := parseUintPathValue(req, "file_id")
	if err != nil {
		writeStorageTransferError(w, http.StatusBadRequest, "invalid file_id")
		return
	}

	version, err := parseUintQueryValue(req, "version")
	if err != nil {
		writeStorageTransferError(w, http.StatusBadRequest, "invalid version")
		return
	}

	token, err := bearerTransferToken(req.Header.Get("Authorization"))
	if err != nil {
		writeStorageTransferError(w, http.StatusUnauthorized, errTransferTokenMissing.Error())
		return
	}

	file, size, err := r.openReplicaFileContent(req, token, replicaID, fileID, version)
	if err != nil {
		writeStorageTransferError(w, storageTransferStatus(err), err.Error())
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, file)
}

func (r *Runtime) openReplicaFileContent(req *http.Request, token string, replicaID, fileID, version uint) (io.ReadCloser, int64, error) {
	claims, err := r.verifyTransferToken(token, replicaID)
	if err != nil {
		return nil, 0, err
	}

	replica, replicaFile, ok := r.findReplicaFile(replicaID, fileID)
	if !ok {
		return nil, 0, errTransferFileNotFound
	}
	if replica.NodeID != r.client.NodeID() {
		return nil, 0, errTransferReplicaNotFound
	}
	if claims.DestinationReplicaID == 0 && claims.TargetReplicaID == 0 {
		return nil, 0, errTransferTokenForbidden
	}
	if replicaFile.ReplicaVersion != version || replicaFile.ReplicaStatus != "synchronized" {
		return nil, 0, errTransferVersionConflict
	}

	reader, err := getTransferReader(req.Context(), replica.URI, r.GetPprofile(replica.StorageProfile))
	if err != nil {
		return nil, 0, err
	}
	return reader.Open(req.Context(), replica.URI, replicaFile.RelativeURI)
}

func (r *Runtime) verifyTransferToken(tokenString string, replicaID uint) (*transferTokenClaims, error) {
	publicKeyPEM := r.transferTokenPublicKey()
	if strings.TrimSpace(publicKeyPEM) == "" {
		return nil, errTransferKeyMissing
	}

	publicKey, err := parseRSAPublicKeyPEM(publicKeyPEM)
	if err != nil {
		return nil, err
	}

	claims := &transferTokenClaims{}
	token, err := jwt.ParseWithClaims(
		tokenString,
		claims,
		func(token *jwt.Token) (any, error) {
			if token.Method != jwt.SigningMethodRS256 {
				return nil, errors.New("unexpected signing method")
			}
			return publicKey, nil
		},
		jwt.WithAudience(r.client.NodeID()),
		jwt.WithIssuer("coordinator"),
		jwt.WithIssuedAt(),
	)
	if err != nil || !token.Valid {
		return nil, errTransferTokenInvalid
	}

	if claims.Purpose != transferTokenPurpose ||
		claims.SourceReplicaID != replicaID ||
		claims.Subject == "" {
		return nil, errTransferTokenForbidden
	}

	return claims, nil
}

func (r *Runtime) findReplicaFile(replicaID, fileID uint) (apiclient.Replica, apiclient.ReplicaInventoryFile, bool) {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()

	var replica apiclient.Replica
	foundReplica := false
	for _, candidate := range r.replicas {
		if candidate.ID == replicaID {
			replica = candidate
			foundReplica = true
			break
		}
	}
	if !foundReplica {
		return apiclient.Replica{}, apiclient.ReplicaInventoryFile{}, false
	}

	for _, file := range r.replicaFiles[replicaID] {
		if file.FileID == fileID {
			return replica, file, true
		}
	}
	return apiclient.Replica{}, apiclient.ReplicaInventoryFile{}, false
}

func parseRSAPublicKeyPEM(value string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(value))
	if block == nil {
		return nil, errTransferTokenInvalid
	}

	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, errTransferTokenInvalid
	}

	publicKey, ok := key.(*rsa.PublicKey)
	if !ok {
		return nil, errTransferTokenInvalid
	}
	return publicKey, nil
}

func resolveReplicaFilePath(rootURI, relativeURI string) (string, error) {
	if strings.TrimSpace(relativeURI) == "" {
		return "", errTransferInvalidLocalPath
	}

	cleanRelative := path.Clean("/" + relativeURI)
	cleanRelative = strings.TrimPrefix(cleanRelative, "/")
	if cleanRelative == "." || path.IsAbs(relativeURI) || hasParentPathSegment(relativeURI) {
		return "", errTransferInvalidLocalPath
	}

	rootPath, err := localFilesystemPath(rootURI)
	if err != nil {
		return "", err
	}

	root, err := resolveFilesystemRoot(rootPath)
	if err != nil {
		return "", err
	}

	if root.singleFile {
		targetRelative := normalizeRelativeURI(filepath.Base(root.targetPath))
		if cleanRelative != targetRelative {
			return "", errTransferFileNotFound
		}
		return root.targetPath, nil
	}

	fullPath := filepath.Join(root.scanPath, filepath.FromSlash(cleanRelative))
	rel, err := filepath.Rel(root.scanPath, fullPath)
	if err != nil {
		return "", err
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errTransferInvalidLocalPath
	}
	return fullPath, nil
}

// localFilesystemPath converts a local path or file:// URI into a filesystem path.
// Only local filesystem locations are supported; remote or unsupported URI schemes return an error.
func localFilesystemPath(rootURI string) (string, error) {
	parsed, err := url.Parse(rootURI)
	if err != nil {
		return "", err
	}
	switch parsed.Scheme {
	case "":
		return rootURI, nil
	case "file":
		if parsed.Host != "" && parsed.Host != "localhost" {
			return "", errTransferUnsupportedURI
		}
		if runtime.GOOS == "windows" && len(parsed.Path) >= 3 && parsed.Path[0] == '/' && parsed.Path[2] == ':' {
			return strings.TrimPrefix(parsed.Path, "/"), nil
		}
		return parsed.Path, nil
	default:
		return "", errTransferUnsupportedURI
	}
}

func parseUintPathValue(req *http.Request, name string) (uint, error) {
	value, err := strconv.ParseUint(req.PathValue(name), 10, 64)
	if err != nil || value == 0 {
		return 0, fmt.Errorf("invalid %s", name)
	}
	return uint(value), nil
}

func parseUintQueryValue(req *http.Request, name string) (uint, error) {
	raw := req.URL.Query().Get(name)
	if raw == "" {
		return 0, fmt.Errorf("invalid %s", name)
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s", name)
	}
	return uint(value), nil
}

func hasParentPathSegment(value string) bool {
	for _, part := range strings.Split(filepath.ToSlash(value), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

func bearerTransferToken(header string) (string, error) {
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
		return "", errTransferTokenMissing
	}
	return strings.TrimSpace(parts[1]), nil
}

func storageTransferStatus(err error) int {
	switch {
	case errors.Is(err, errTransferTokenMissing), errors.Is(err, errTransferTokenInvalid), errors.Is(err, errTransferKeyMissing):
		return http.StatusUnauthorized
	case errors.Is(err, errTransferTokenForbidden):
		return http.StatusForbidden
	case errors.Is(err, errTransferReplicaNotFound), errors.Is(err, errTransferFileNotFound):
		return http.StatusNotFound
	case errors.Is(err, errTransferVersionConflict):
		return http.StatusConflict
	case errors.Is(err, errTransferUnsupportedURI):
		return http.StatusNotImplemented
	default:
		return http.StatusInternalServerError
	}
}

func writeStorageTransferError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"error":%q}`+"\n", message)
}
