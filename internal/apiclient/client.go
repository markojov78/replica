package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"replica/internal/config"
)

const apiVersion = "1"
const defaultAPIRequestTimeout = 15 * time.Second
const defaultFileTransferTimeout = 30 * time.Minute

var (
	ErrMissingNodeID         = errors.New("missing node id")
	ErrMissingCoordinatorURL = errors.New("missing coordinator url")
	ErrMissingNodeSecret     = errors.New("missing node secret")
	ErrMissingNodeAddress    = errors.New("missing node address")
)

type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("coordinator api error: status=%d", e.StatusCode)
	}
	return fmt.Sprintf("coordinator api error: status=%d body=%s", e.StatusCode, e.Body)
}

type Client struct {
	nodeID            string
	coordinatorURL    string
	nodeSecret        string
	nodeAddress       string
	heartbeatInterval time.Duration
	httpClient        *http.Client
	transferClient    *http.Client
	now               func() time.Time

	mu                    sync.Mutex
	accessToken           string
	refreshToken          string
	accessTokenExpiresAt  time.Time
	refreshTokenExpiresAt time.Time
	transferPublicKey     string
}

type NodeTokenPair struct {
	NodeID                 string    `json:"node_id"`
	AccessToken            string    `json:"access_token"`
	RefreshToken           string    `json:"refresh_token"`
	AccessTokenExpiresAt   time.Time `json:"access_token_expires_at"`
	RefreshTokenExpiresAt  time.Time `json:"refresh_token_expires_at"`
	TransferTokenPublicKey string    `json:"transfer_token_public_key"`
}

type Replica struct {
	ID                uint   `json:"id"`
	InventoryID       uint   `json:"inventory_id"`
	InventoryType     string `json:"inventory_type"`
	NodeID            string `json:"node_id"`
	URI               string `json:"uri"`
	Status            string `json:"status"`
	Type              string `json:"type"`
	UpstreamReplicaID *uint  `json:"upstream_replica_id"`
}

type ReplicaFile struct {
	ID        uint   `json:"id"`
	FileID    uint   `json:"file_id"`
	ReplicaID uint   `json:"replica_id"`
	Version   uint   `json:"version"`
	Status    string `json:"status"`
}

type ReplicaInventoryFile struct {
	FileID           uint      `json:"file_id"`
	ReplicaID        uint      `json:"replica_id"`
	InventoryID      uint      `json:"inventory_id"`
	RelativeURI      string    `json:"relative_uri"`
	Size             int64     `json:"size"`
	Hash             string    `json:"hash"`
	InventoryStatus  string    `json:"inventory_status"`
	InventoryVersion uint      `json:"inventory_version"`
	ReplicaStatus    string    `json:"replica_status"`
	ReplicaVersion   uint      `json:"replica_version"`
	Created          time.Time `json:"created"`
	Modified         time.Time `json:"modified"`
}

type ReplicaInventoryFileList struct {
	Files []ReplicaInventoryFile `json:"files"`
}

type ReplicaFileReport struct {
	FileID       *uint      `json:"file_id,omitempty"`
	Action       string     `json:"action,omitempty"`
	RelativeURI  string     `json:"relative_uri"`
	FileSize     *int64     `json:"file_size,omitempty"`
	FileHash     *string    `json:"file_hash,omitempty"`
	CreatedTime  *time.Time `json:"created_time,omitempty"`
	ModifiedTime *time.Time `json:"modified_time,omitempty"`
}

type ReplicaFileList struct {
	Items []ReplicaFile `json:"items"`
	Page  int           `json:"page"`
	Count int           `json:"count"`
	Total int64         `json:"total"`
}

type Command struct {
	ID        uint            `json:"id"`
	NodeID    string          `json:"node_id"`
	Type      string          `json:"type"`
	Status    string          `json:"status"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt string          `json:"created_at"`
	UpdatedAt string          `json:"updated_at"`
	LastError *string         `json:"last_error,omitempty"`
}

type AvailabilityReport struct {
	NodeID   string    `json:"node_id"`
	Address  string    `json:"address"`
	LastSeen string    `json:"last_seen"`
	Commands []Command `json:"commands"`
}

type UserPermission struct {
	UserID      uint     `json:"user_id"`
	Permissions []string `json:"permissions"`
}

type Share struct {
	ID                   uint             `json:"id"`
	InventoryID          uint             `json:"inventory_id"`
	ReplicaID            uint             `json:"replica_id"`
	Name                 string           `json:"name"`
	Status               string           `json:"status"`
	LinkHash             *string          `json:"link_hash"`
	ShareExpiration      *time.Time       `json:"share_expiration"`
	UserPermissions      []UserPermission `json:"user_permissions"`
	AnonymousPermissions []string         `json:"anonymous_permissions"`
}

type ConfigItem struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

type ValidatedUserToken struct {
	UserID               uint      `json:"user_id"`
	Username             string    `json:"username"`
	Status               string    `json:"status"`
	AccessTokenExpiresAt time.Time `json:"access_token_expires_at"`
}

func New(cfg config.Config) (*Client, error) {
	if strings.TrimSpace(cfg.App.NodeID) == "" {
		return nil, ErrMissingNodeID
	}
	if strings.TrimSpace(cfg.App.CoordinatorURL) == "" {
		return nil, ErrMissingCoordinatorURL
	}
	if strings.TrimSpace(cfg.Auth.NodeSecret) == "" {
		return nil, ErrMissingNodeSecret
	}
	if strings.TrimSpace(cfg.App.NodeAddress) == "" {
		return nil, ErrMissingNodeAddress
	}

	apiRequestTimeout := cfg.App.APIRequestTimeout
	if apiRequestTimeout <= 0 {
		apiRequestTimeout = defaultAPIRequestTimeout
	}
	fileTransferTimeout := cfg.App.FileTransferTimeout
	if fileTransferTimeout <= 0 {
		fileTransferTimeout = defaultFileTransferTimeout
	}

	return &Client{
		nodeID:            strings.TrimSpace(cfg.App.NodeID),
		coordinatorURL:    strings.TrimRight(strings.TrimSpace(cfg.App.CoordinatorURL), "/"),
		nodeSecret:        cfg.Auth.NodeSecret,
		nodeAddress:       strings.TrimSpace(cfg.App.NodeAddress),
		heartbeatInterval: cfg.App.HeartbeatInterval,
		httpClient: &http.Client{
			Timeout: apiRequestTimeout,
		},
		transferClient: &http.Client{
			Timeout: fileTransferTimeout,
		},
		now: func() time.Time {
			return time.Now().UTC()
		},
	}, nil
}

func (c *Client) NodeID() string {
	return c.nodeID
}

func (c *Client) TransferTokenPublicKey() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.transferPublicKey
}

func (c *Client) AccessToken(ctx context.Context) (string, error) {
	return c.ensureAccessToken(ctx)
}

func (c *Client) WebSocketURL(path string) (string, error) {
	base, err := url.Parse(c.coordinatorURL)
	if err != nil {
		return "", err
	}

	switch base.Scheme {
	case "http":
		base.Scheme = "ws"
	case "https":
		base.Scheme = "wss"
	default:
		return "", fmt.Errorf("unsupported coordinator url scheme %q", base.Scheme)
	}

	base.Path = path
	base.RawQuery = ""
	base.Fragment = ""
	return base.String(), nil
}

func (c *Client) Authenticate(ctx context.Context) (*NodeTokenPair, error) {
	reqBody := map[string]string{
		"node_id": c.nodeID,
		"secret":  c.nodeSecret,
	}

	var pair NodeTokenPair
	if err := c.doJSON(ctx, http.MethodPost, "/node/auth/login", reqBody, "", &pair); err != nil {
		return nil, err
	}

	c.storeTokenPair(pair)
	return &pair, nil
}

func (c *Client) Refresh(ctx context.Context) (*NodeTokenPair, error) {
	c.mu.Lock()
	refreshToken := c.refreshToken
	c.mu.Unlock()

	if refreshToken == "" {
		return c.Authenticate(ctx)
	}

	reqBody := map[string]string{
		"refresh_token": refreshToken,
	}

	var pair NodeTokenPair
	if err := c.doJSON(ctx, http.MethodPost, "/node/auth/refresh", reqBody, "", &pair); err != nil {
		return nil, err
	}

	c.storeTokenPair(pair)
	return &pair, nil
}

func (c *Client) ReportAvailability(ctx context.Context) (*AvailabilityReport, error) {
	accessToken, err := c.ensureAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	reqBody := map[string]any{
		"address":  c.nodeAddress,
		"interval": c.heartbeatInterval.Seconds(),
	}

	var report AvailabilityReport
	if err := c.doAuthenticatedJSON(ctx, http.MethodPost, "/node/nodes", reqBody, accessToken, &report); err != nil {
		if apiErr, ok := err.(*APIError); ok && apiErr.StatusCode == http.StatusUnauthorized {
			accessToken, err = c.refreshOrAuthenticate(ctx)
			if err != nil {
				return nil, err
			}
			if err := c.doAuthenticatedJSON(ctx, http.MethodPost, "/node/nodes", reqBody, accessToken, &report); err != nil {
				return nil, err
			}
			return &report, nil
		}
		return nil, err
	}

	return &report, nil
}

func (c *Client) ProxyUserLogin(ctx context.Context, body []byte, contentType string) (int, http.Header, []byte, error) {
	return c.proxyAdminAuth(ctx, "/api/admin/auth/login", body, contentType)
}

func (c *Client) ProxyUserRefresh(ctx context.Context, body []byte, contentType string) (int, http.Header, []byte, error) {
	return c.proxyAdminAuth(ctx, "/api/admin/auth/refresh", body, contentType)
}

func (c *Client) proxyAdminAuth(ctx context.Context, path string, body []byte, contentType string) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.coordinatorURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("X-API-Version", apiVersion)
	if strings.TrimSpace(contentType) != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, nil, err
	}
	return resp.StatusCode, resp.Header.Clone(), data, nil
}

func (c *Client) ListOwnReplicas(ctx context.Context) ([]Replica, error) {
	accessToken, err := c.ensureAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	var replicas []Replica
	if err := c.doAuthenticatedJSON(ctx, http.MethodGet, "/node/replicas", nil, accessToken, &replicas); err != nil {
		if apiErr, ok := err.(*APIError); ok && apiErr.StatusCode == http.StatusUnauthorized {
			accessToken, err = c.refreshOrAuthenticate(ctx)
			if err != nil {
				return nil, err
			}
			if err := c.doAuthenticatedJSON(ctx, http.MethodGet, "/node/replicas", nil, accessToken, &replicas); err != nil {
				return nil, err
			}
			return replicas, nil
		}
		return nil, err
	}

	return replicas, nil
}

func (c *Client) ListOwnShares(ctx context.Context) ([]Share, error) {
	accessToken, err := c.ensureAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	var shares []Share
	if err := c.doAuthenticatedJSON(ctx, http.MethodGet, "/node/shares", nil, accessToken, &shares); err != nil {
		if apiErr, ok := err.(*APIError); ok && apiErr.StatusCode == http.StatusUnauthorized {
			accessToken, err = c.refreshOrAuthenticate(ctx)
			if err != nil {
				return nil, err
			}
			if err := c.doAuthenticatedJSON(ctx, http.MethodGet, "/node/shares", nil, accessToken, &shares); err != nil {
				return nil, err
			}
			return shares, nil
		}
		return nil, err
	}

	return shares, nil
}

func (c *Client) GetConfig(ctx context.Context) ([]ConfigItem, error) {
	accessToken, err := c.ensureAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	var items []ConfigItem
	if err := c.doAuthenticatedJSON(ctx, http.MethodGet, "/node/config", nil, accessToken, &items); err != nil {
		if apiErr, ok := err.(*APIError); ok && apiErr.StatusCode == http.StatusUnauthorized {
			accessToken, err = c.refreshOrAuthenticate(ctx)
			if err != nil {
				return nil, err
			}
			if err := c.doAuthenticatedJSON(ctx, http.MethodGet, "/node/config", nil, accessToken, &items); err != nil {
				return nil, err
			}
			return items, nil
		}
		return nil, err
	}

	return items, nil
}

func (c *Client) ValidateUserToken(ctx context.Context, accessToken string) (*ValidatedUserToken, error) {
	nodeAccessToken, err := c.ensureAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	reqBody := map[string]string{"access_token": accessToken}
	var token ValidatedUserToken
	if err := c.doAuthenticatedJSON(ctx, http.MethodPost, "/node/auth/validate-user-token", reqBody, nodeAccessToken, &token); err != nil {
		if apiErr, ok := err.(*APIError); ok && apiErr.StatusCode == http.StatusUnauthorized {
			nodeAccessToken, err = c.refreshOrAuthenticate(ctx)
			if err != nil {
				return nil, err
			}
			if err := c.doAuthenticatedJSON(ctx, http.MethodPost, "/node/auth/validate-user-token", reqBody, nodeAccessToken, &token); err != nil {
				return nil, err
			}
			return &token, nil
		}
		return nil, err
	}

	return &token, nil
}

func (c *Client) ListReplicaFiles(ctx context.Context, replicaID uint, page, count int) (*ReplicaFileList, error) {
	accessToken, err := c.ensureAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	query := url.Values{}
	if page > 0 {
		query.Set("page", strconv.Itoa(page))
	}
	if count > 0 {
		query.Set("count", strconv.Itoa(count))
	}

	path := fmt.Sprintf("/api/admin/replicas/%d/files", replicaID)
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}

	var files ReplicaFileList
	if err := c.doAuthenticatedJSON(ctx, http.MethodGet, path, nil, accessToken, &files); err != nil {
		if apiErr, ok := err.(*APIError); ok && apiErr.StatusCode == http.StatusUnauthorized {
			accessToken, err = c.refreshOrAuthenticate(ctx)
			if err != nil {
				return nil, err
			}
			if err := c.doAuthenticatedJSON(ctx, http.MethodGet, path, nil, accessToken, &files); err != nil {
				return nil, err
			}
			return &files, nil
		}
		return nil, err
	}

	return &files, nil
}

func (c *Client) ListReplicaInventoryFiles(ctx context.Context, replicaID uint, statuses ...string) ([]ReplicaInventoryFile, error) {
	accessToken, err := c.ensureAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	path := fmt.Sprintf("/node/replica/%d/files", replicaID)
	if len(statuses) > 0 && strings.TrimSpace(statuses[0]) != "" {
		query := url.Values{}
		query.Set("status", strings.TrimSpace(statuses[0]))
		path += "?" + query.Encode()
	}

	var fileList ReplicaInventoryFileList
	if err := c.doAuthenticatedJSON(ctx, http.MethodGet, path, nil, accessToken, &fileList); err != nil {
		if apiErr, ok := err.(*APIError); ok && apiErr.StatusCode == http.StatusUnauthorized {
			accessToken, err = c.refreshOrAuthenticate(ctx)
			if err != nil {
				return nil, err
			}
			if err := c.doAuthenticatedJSON(ctx, http.MethodGet, path, nil, accessToken, &fileList); err != nil {
				return nil, err
			}
			return fileList.Files, nil
		}
		return nil, err
	}

	return fileList.Files, nil
}

func (c *Client) UpdateReplicaFileStatus(ctx context.Context, replicaID, fileID uint, status string, version *uint, lastError *string) error {
	accessToken, err := c.ensureAccessToken(ctx)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/node/replica/%d/files/%d", replicaID, fileID)
	reqBody := map[string]any{
		"status": status,
	}
	if version != nil {
		reqBody["version"] = *version
	}
	if lastError != nil {
		reqBody["error"] = *lastError
	}

	if err := c.doAuthenticatedJSON(ctx, http.MethodPatch, path, reqBody, accessToken, nil); err != nil {
		if apiErr, ok := err.(*APIError); ok && apiErr.StatusCode == http.StatusUnauthorized {
			accessToken, err = c.refreshOrAuthenticate(ctx)
			if err != nil {
				return err
			}
			if err := c.doAuthenticatedJSON(ctx, http.MethodPatch, path, reqBody, accessToken, nil); err != nil {
				return err
			}
			return nil
		}
		return err
	}

	return nil
}

func (c *Client) TransferReplicaFileContent(ctx context.Context, sourceNodeAddress string, replicaID, fileID, version uint, transferToken string) (io.ReadCloser, error) {
	base := strings.TrimRight(strings.TrimSpace(sourceNodeAddress), "/")
	if base == "" {
		return nil, errors.New("missing source node address")
	}
	if strings.TrimSpace(transferToken) == "" {
		return nil, errors.New("missing transfer token")
	}

	query := url.Values{}
	query.Set("version", strconv.FormatUint(uint64(version), 10))
	requestURL := fmt.Sprintf("%s/transfer/replicas/%d/files/%d/content?%s", base, replicaID, fileID, query.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Version", apiVersion)
	req.Header.Set("Authorization", "Bearer "+transferToken)

	resp, err := c.transferClient.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		return nil, &APIError{
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(data)),
		}
	}

	return resp.Body, nil
}

func (c *Client) ReportReplicaFiles(ctx context.Context, replicaID uint, files []ReplicaFileReport) error {
	accessToken, err := c.ensureAccessToken(ctx)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/node/replica/%d/files", replicaID)
	reqBody := struct {
		Files []ReplicaFileReport `json:"files"`
	}{
		Files: files,
	}

	if err := c.doAuthenticatedJSON(ctx, http.MethodPost, path, reqBody, accessToken, nil); err != nil {
		if apiErr, ok := err.(*APIError); ok && apiErr.StatusCode == http.StatusUnauthorized {
			accessToken, err = c.refreshOrAuthenticate(ctx)
			if err != nil {
				return err
			}
			if err := c.doAuthenticatedJSON(ctx, http.MethodPost, path, reqBody, accessToken, nil); err != nil {
				return err
			}
			return nil
		}
		return err
	}

	return nil
}

func (c *Client) UpdateCommand(ctx context.Context, commandID uint, status string, lastError *string) (*Command, error) {
	accessToken, err := c.ensureAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	path := fmt.Sprintf("/node/commands/%d", commandID)
	reqBody := map[string]any{
		"status": status,
	}
	if lastError != nil {
		reqBody["error"] = *lastError
	}

	var command Command
	if err := c.doAuthenticatedJSON(ctx, http.MethodPatch, path, reqBody, accessToken, &command); err != nil {
		if apiErr, ok := err.(*APIError); ok && apiErr.StatusCode == http.StatusUnauthorized {
			accessToken, err = c.refreshOrAuthenticate(ctx)
			if err != nil {
				return nil, err
			}
			if err := c.doAuthenticatedJSON(ctx, http.MethodPatch, path, reqBody, accessToken, &command); err != nil {
				return nil, err
			}
			return &command, nil
		}
		return nil, err
	}

	return &command, nil
}

func (c *Client) ensureAccessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	accessToken := c.accessToken
	accessExpiry := c.accessTokenExpiresAt
	refreshToken := c.refreshToken
	refreshExpiry := c.refreshTokenExpiresAt
	c.mu.Unlock()

	now := c.now()
	if accessToken != "" && now.Before(accessExpiry) {
		return accessToken, nil
	}
	if refreshToken != "" && now.Before(refreshExpiry) {
		return c.refreshOrAuthenticate(ctx)
	}
	return c.refreshOrAuthenticate(ctx)
}

func (c *Client) refreshOrAuthenticate(ctx context.Context) (string, error) {
	pair, err := c.Refresh(ctx)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized {
			pair, err = c.Authenticate(ctx)
			if err != nil {
				return "", err
			}
			return pair.AccessToken, nil
		}
		return "", err
	}
	return pair.AccessToken, nil
}

func (c *Client) storeTokenPair(pair NodeTokenPair) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.accessToken = pair.AccessToken
	c.refreshToken = pair.RefreshToken
	c.accessTokenExpiresAt = pair.AccessTokenExpiresAt
	c.refreshTokenExpiresAt = pair.RefreshTokenExpiresAt
	c.transferPublicKey = pair.TransferTokenPublicKey
}

func (c *Client) doAuthenticatedJSON(ctx context.Context, method, path string, requestBody any, accessToken string, responseBody any) error {
	return c.doJSON(ctx, method, path, requestBody, accessToken, responseBody)
}

func (c *Client) doJSON(ctx context.Context, method, path string, requestBody any, accessToken string, responseBody any) error {
	var body io.Reader
	if requestBody != nil {
		data, err := json.Marshal(requestBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.coordinatorURL+path, body)
	if err != nil {
		return err
	}

	req.Header.Set("X-API-Version", apiVersion)
	if requestBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(data)),
		}
	}

	if responseBody == nil || len(data) == 0 {
		return nil
	}

	return json.Unmarshal(data, responseBody)
}
