package dashboard

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"replica/internal/config"
)

var errUnauthorized = errors.New("unauthorized")

//go:embed templates/*.html static/*
var assets embed.FS

type Handler struct {
	api               http.Handler
	pages             *template.Template
	storageProfiles   []string
	storageProfileSet map[string]struct{}
	nodeConfig        nodeConfigTemplate
}

type authContext struct {
	AccessToken string
}

type node struct {
	ID             string   `json:"id"`
	Status         string   `json:"status"`
	Address        string   `json:"address"`
	SharingEnabled bool     `json:"sharing_enabled"`
	Interval       *float64 `json:"interval"`
	LastSeen       *string  `json:"last_seen"`
}

type nodeConfigTemplate struct {
	CoordinatorURL    string
	HeartbeatInterval string
	HTTPAddress       string
}

type nodeList struct {
	Items []node `json:"items"`
	Total int64  `json:"total"`
}

type replica struct {
	ID                uint   `json:"id"`
	InventoryID       uint   `json:"inventory_id"`
	InventoryType     string `json:"inventory_type"`
	NodeID            string `json:"node_id"`
	URI               string `json:"uri"`
	Status            string `json:"status"`
	SyncStatus        string `json:"sync_status"`
	Type              string `json:"type"`
	UpstreamReplicaID *uint  `json:"upstream_replica_id"`
	StorageProfile    string `json:"storage_profile"`
	FollowSymlinks    bool   `json:"follow_symlinks"`
}

type replicaList struct {
	Items []replica `json:"items"`
	Page  int       `json:"page"`
	Count int       `json:"count"`
	Total int64     `json:"total"`
}

type share struct {
	ID                   uint                  `json:"id"`
	InventoryID          uint                  `json:"inventory_id"`
	ReplicaID            uint                  `json:"replica_id"`
	Name                 string                `json:"name"`
	Status               string                `json:"status"`
	LinkHash             *string               `json:"link_hash"`
	ShareExpiration      *string               `json:"share_expiration"`
	Properties           shareProperties       `json:"properties"`
	UserPermissions      []shareUserPermission `json:"user_permissions"`
	AnonymousPermissions []string              `json:"anonymous_permissions"`
}

type shareProperties struct {
	View          *string `json:"view"`
	PageSize      *int    `json:"page_size"`
	ThumbnailSize *int    `json:"thumbnail_size"`
	Theme         *string `json:"theme"`
}

type shareUserPermission struct {
	UserID      uint     `json:"user_id"`
	Permissions []string `json:"permissions"`
}

type shareList struct {
	Items []share `json:"items"`
	Page  int     `json:"page"`
	Count int     `json:"count"`
	Total int64   `json:"total"`
}

type shareView struct {
	ID            uint
	InventoryID   uint
	InventoryName string
	ReplicaID     uint
	NodeID        string
	Name          string
	Status        string
	HasAnonymous  bool
	LinkHash      string
}

type inventory struct {
	ID              uint                  `json:"id"`
	Name            string                `json:"name"`
	Status          string                `json:"status"`
	Type            string                `json:"type"`
	Replicas        []replica             `json:"replicas"`
	UserPermissions []shareUserPermission `json:"user_permissions"`
	ShareCount      int
}

type inventoryList struct {
	Items []inventory `json:"items"`
	Total int64       `json:"total"`
}

type role struct {
	ID          uint         `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Status      string       `json:"status"`
	Permissions []permission `json:"permissions"`
}

type permission struct {
	Resource string `json:"resource"`
	Action   string `json:"actions"`
}

type permissionInput struct {
	Resource string `json:"resource"`
	Action   string `json:"action"`
}

type rolePermissionResource struct {
	Name    string
	Actions []string
}

type roleList struct {
	Items []role `json:"items"`
	Total int64  `json:"total"`
}

type user struct {
	ID     uint   `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Roles  []role `json:"roles"`
}

type userList struct {
	Items []user `json:"items"`
	Total int64  `json:"total"`
}

type configItem struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

type configList struct {
	Items []configItem `json:"items"`
}

type configUpdateItem struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
}

type configView struct {
	Key         string
	Label       string
	Description string
	Kind        string
	Value       string
	BoolValue   bool
}

type inventoryFile struct {
	ID          uint      `json:"id"`
	InventoryID uint      `json:"inventory_id"`
	RelativeURI string    `json:"relative_uri"`
	Status      string    `json:"status"`
	Size        int64     `json:"size"`
	Hash        string    `json:"hash"`
	Version     uint      `json:"version"`
	Created     time.Time `json:"created"`
	Modified    time.Time `json:"modified"`
}

type inventoryFileList struct {
	Items []inventoryFile `json:"items"`
	Page  int             `json:"page"`
	Count int             `json:"count"`
	Total int64           `json:"total"`
}

type filePage struct {
	Items      []inventoryFile
	Page       int
	Count      int
	Displayed  int
	Total      int64
	TotalPages int
	PrevPage   int
	NextPage   int
	HasPrev    bool
	HasNext    bool
}

type pageData struct {
	Title               string
	Subtitle            string
	Active              string
	Error               string
	Nodes               []node
	Node                node
	Inventories         []inventory
	Inventory           inventory
	Shares              []shareView
	Share               share
	Files               filePage
	Replica             replica
	Users               []user
	User                user
	Roles               []role
	Role                role
	PermissionResources []rolePermissionResource
	Settings            []configView
	ThumbnailSizes      []int
	StorageProfiles     []string
	NodeConfig          nodeConfigTemplate
	IsEdit              bool
	FolderURI           string
	FileURIs            string
}

func Register(mux *http.ServeMux, api http.Handler, cfg config.Config) error {
	pages, err := template.New("admin").Funcs(template.FuncMap{
		"statusClass":            statusClass,
		"syncStatusClass":        syncStatusClass,
		"formatTime":             formatTime,
		"pathEscape":             url.PathEscape,
		"isUpstream":             isUpstream,
		"formatBytes":            formatBytes,
		"formatDate":             formatDate,
		"hasRole":                hasRole,
		"hasPermission":          hasPermission,
		"hasShareUserPermission": hasShareUserPermission,
		"hasStringPermission":    hasStringPermission,
		"expirationValue":        expirationValue,
		"anonymousEnabled":       anonymousEnabled,
		"selectedString":         selectedString,
		"selectedInt":            selectedInt,
		"optionalIntValue":       optionalIntValue,
		"shareNodeIDs":           shareNodeIDs,
		"shareFilterNodes":       shareFilterNodes,
		"shareFilterInventories": shareFilterInventories,
	}).ParseFS(assets, "templates/*.html")
	if err != nil {
		return err
	}

	storageProfiles := storageProfileNames(cfg.Storage)
	handler := &Handler{
		api:               api,
		pages:             pages,
		storageProfiles:   storageProfiles,
		storageProfileSet: storageProfileSet(storageProfiles),
		nodeConfig: nodeConfigTemplate{
			CoordinatorURL:    cfg.App.CoordinatorURL,
			HeartbeatInterval: formatConfigDuration(cfg.App.HeartbeatInterval),
			HTTPAddress:       cfg.HTTP.Address,
		},
	}

	mux.Handle("GET /dashboard/static/", http.StripPrefix("/dashboard/static/", http.FileServer(http.FS(mustSub(assets, "static")))))
	mux.HandleFunc("GET /dashboard/login", handler.loginPage)
	mux.HandleFunc("GET /dashboard", handler.protected(handler.dashboard))
	mux.HandleFunc("GET /dashboard/nodes", handler.protected(handler.nodesPage))
	mux.HandleFunc("GET /dashboard/nodes/new", handler.protected(handler.newNodePage))
	mux.HandleFunc("POST /dashboard/nodes", handler.protected(handler.createNode))
	mux.HandleFunc("GET /dashboard/nodes/{id}/edit", handler.protected(handler.editNodePage))
	mux.HandleFunc("POST /dashboard/nodes/{id}", handler.protected(handler.updateNode))
	mux.HandleFunc("POST /dashboard/nodes/{id}/revoke", handler.protected(handler.revokeNode))
	mux.HandleFunc("GET /dashboard/inventories", handler.protected(handler.inventoriesPage))
	mux.HandleFunc("GET /dashboard/inventories/new", handler.protected(handler.newInventoryPage))
	mux.HandleFunc("POST /dashboard/inventories", handler.protected(handler.createInventory))
	mux.HandleFunc("GET /dashboard/inventories/{id}", handler.protected(handler.inventoryPage))
	mux.HandleFunc("GET /dashboard/inventories/{id}/edit", handler.protected(handler.editInventoryPage))
	mux.HandleFunc("POST /dashboard/inventories/{id}", handler.protected(handler.updateInventory))
	mux.HandleFunc("POST /dashboard/inventories/{id}/delete", handler.protected(handler.deleteInventory))
	mux.HandleFunc("GET /dashboard/inventories/{id}/replicas/new", handler.protected(handler.newReplicaPage))
	mux.HandleFunc("POST /dashboard/inventories/{id}/replicas", handler.protected(handler.createReplica))
	mux.HandleFunc("GET /dashboard/inventories/{id}/replicas/{replica_id}/edit", handler.protected(handler.editReplicaPage))
	mux.HandleFunc("POST /dashboard/inventories/{id}/replicas/{replica_id}", handler.protected(handler.updateReplica))
	mux.HandleFunc("POST /dashboard/inventories/{id}/replicas/{replica_id}/delete", handler.protected(handler.deleteReplica))
	mux.HandleFunc("GET /dashboard/shares", handler.protected(handler.sharesPage))
	mux.HandleFunc("GET /dashboard/shares/new", handler.protected(handler.newSharePage))
	mux.HandleFunc("POST /dashboard/shares", handler.protected(handler.createShare))
	mux.HandleFunc("GET /dashboard/shares/{id}/edit", handler.protected(handler.editSharePage))
	mux.HandleFunc("POST /dashboard/shares/{id}", handler.protected(handler.updateShare))
	mux.HandleFunc("POST /dashboard/shares/{id}/delete", handler.protected(handler.deleteShare))
	mux.HandleFunc("GET /dashboard/users", handler.protected(handler.usersPage))
	mux.HandleFunc("GET /dashboard/users/new", handler.protected(handler.newUserPage))
	mux.HandleFunc("POST /dashboard/users", handler.protected(handler.createUser))
	mux.HandleFunc("GET /dashboard/users/{id}/edit", handler.protected(handler.editUserPage))
	mux.HandleFunc("POST /dashboard/users/{id}", handler.protected(handler.updateUser))
	mux.HandleFunc("GET /dashboard/roles", handler.protected(handler.rolesPage))
	mux.HandleFunc("GET /dashboard/roles/new", handler.protected(handler.newRolePage))
	mux.HandleFunc("POST /dashboard/roles", handler.protected(handler.createRole))
	mux.HandleFunc("GET /dashboard/roles/{id}/edit", handler.protected(handler.editRolePage))
	mux.HandleFunc("POST /dashboard/roles/{id}", handler.protected(handler.updateRole))
	mux.HandleFunc("GET /dashboard/settings", handler.protected(handler.settingsPage))
	mux.HandleFunc("POST /dashboard/settings", handler.protected(handler.updateSettings))
	mux.HandleFunc("POST /dashboard/settings/reset", handler.protected(handler.resetSettings))
	mux.HandleFunc("POST /dashboard/settings/{key}/reset", handler.protected(handler.resetSetting))
	return nil
}

func mustSub(embedded embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(embedded, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

func formatConfigDuration(value time.Duration) string {
	if value%time.Second == 0 {
		return fmt.Sprintf("%ds", int64(value/time.Second))
	}
	return value.String()
}

func (h *Handler) loginPage(w http.ResponseWriter, r *http.Request) {
	h.render(w, "login", pageData{Title: "Sign in"})
}

func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request, _ authContext) {
	http.Redirect(w, r, "/dashboard/inventories", http.StatusSeeOther)
}

func (h *Handler) nodesPage(w http.ResponseWriter, r *http.Request, sess authContext) {
	var list nodeList
	if !h.load(w, r, sess, "/api/admin/nodes?count=100", &list) {
		return
	}
	h.render(w, "nodes", pageData{
		Title: "Nodes", Subtitle: "Create, disable, revoke, and inspect storage service nodes.",
		Active: "nodes", Nodes: list.Items,
	})
}

func (h *Handler) newNodePage(w http.ResponseWriter, _ *http.Request, _ authContext) {
	h.render(w, "node_form", pageData{
		Title: "Add node", Subtitle: "Register a storage service node.", Active: "nodes", NodeConfig: h.nodeConfig,
	})
}

func (h *Handler) createNode(w http.ResponseWriter, r *http.Request, sess authContext) {
	if err := r.ParseForm(); err != nil {
		h.nodeFormError(w, false, node{}, "Invalid form submission.")
		return
	}
	input := map[string]any{
		"id":              strings.TrimSpace(r.FormValue("id")),
		"secret":          r.FormValue("secret"),
		"address":         strings.TrimSpace(r.FormValue("address")),
		"status":          r.FormValue("status"),
		"sharing_enabled": r.FormValue("sharing_enabled") == "on",
	}
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodPost, "/api/admin/nodes", input, nil); err != nil {
		if errors.Is(err, errUnauthorized) {
			h.renderError(w, r, sess, err)
			return
		}
		h.nodeFormError(w, false, node{ID: r.FormValue("id"), Address: r.FormValue("address"), Status: r.FormValue("status"), SharingEnabled: r.FormValue("sharing_enabled") == "on"}, apiMessage(err))
		return
	}
	http.Redirect(w, r, "/dashboard/nodes", http.StatusSeeOther)
}

func (h *Handler) editNodePage(w http.ResponseWriter, r *http.Request, sess authContext) {
	var item node
	if !h.load(w, r, sess, "/api/admin/nodes/"+url.PathEscape(r.PathValue("id")), &item) {
		return
	}
	h.render(w, "node_form", pageData{
		Title: "Edit node", Subtitle: "Update node administration settings.", Active: "nodes", Node: item, IsEdit: true,
	})
}

func (h *Handler) updateNode(w http.ResponseWriter, r *http.Request, sess authContext) {
	if err := r.ParseForm(); err != nil {
		h.nodeFormError(w, true, node{ID: r.PathValue("id")}, "Invalid form submission.")
		return
	}
	input := map[string]any{
		"address":         strings.TrimSpace(r.FormValue("address")),
		"sharing_enabled": r.FormValue("sharing_enabled") == "on",
	}
	if status := r.FormValue("status"); status != "" {
		input["status"] = status
	}
	if secret := r.FormValue("secret"); secret != "" {
		input["secret"] = secret
	}
	id := r.PathValue("id")
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodPatch, "/api/admin/nodes/"+url.PathEscape(id), input, nil); err != nil {
		if errors.Is(err, errUnauthorized) {
			h.renderError(w, r, sess, err)
			return
		}
		h.nodeFormError(w, true, node{ID: id, Address: r.FormValue("address"), Status: r.FormValue("status"), SharingEnabled: r.FormValue("sharing_enabled") == "on"}, apiMessage(err))
		return
	}
	http.Redirect(w, r, "/dashboard/nodes", http.StatusSeeOther)
}

func (h *Handler) revokeNode(w http.ResponseWriter, r *http.Request, sess authContext) {
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodDelete, "/api/admin/nodes/"+url.PathEscape(r.PathValue("id")), nil, nil); err != nil {
		h.renderError(w, r, sess, err)
		return
	}
	http.Redirect(w, r, "/dashboard/nodes", http.StatusSeeOther)
}

func (h *Handler) inventoriesPage(w http.ResponseWriter, r *http.Request, sess authContext) {
	var list inventoryList
	if !h.load(w, r, sess, "/api/admin/inventories?count=100", &list) {
		return
	}
	shares, ok := h.loadAllShares(w, r, sess)
	if !ok {
		return
	}
	inventories := withInventoryShareCounts(list.Items, shares)
	inventories = withActiveInventoryReplicas(inventories)
	h.render(w, "inventories", pageData{
		Title: "Inventories", Subtitle: "Logical datasets with replicas managed in inventory context.",
		Active: "inventories", Inventories: inventories,
	})
}

func (h *Handler) newInventoryPage(w http.ResponseWriter, r *http.Request, sess authContext) {
	nodes, ok := h.loadNodes(w, r, sess)
	if !ok {
		return
	}
	users, ok := h.loadUsers(w, r, sess)
	if !ok {
		return
	}
	users = activeUsers(users)
	h.render(w, "inventory_form", pageData{
		Title: "New inventory", Subtitle: "Create an inventory and its first replica.", Active: "inventories", Nodes: nodes, Users: users,
		Replica: replica{Type: "filesystem"}, StorageProfiles: h.storageProfiles,
	})
}

func (h *Handler) createInventory(w http.ResponseWriter, r *http.Request, sess authContext) {
	if err := r.ParseForm(); err != nil {
		h.inventoryFormError(w, r, sess, false, inventory{}, "Invalid form submission.")
		return
	}
	users, ok := h.loadUsers(w, r, sess)
	if !ok {
		return
	}
	users = activeUsers(users)
	userPermissions := userPermissionsFromForm(r.Form, users)
	replicaType := r.FormValue("replica_type")
	storageProfile := storageProfileForReplicaType(replicaType, r.FormValue("storage_profile"))
	if !h.validStorageProfile(storageProfile) {
		h.inventoryFormError(w, r, sess, false, inventory{Name: r.FormValue("name"), UserPermissions: userPermissions}, "Storage profile must match a configured profile.")
		return
	}
	input := map[string]any{
		"name":             strings.TrimSpace(r.FormValue("name")),
		"node_id":          r.FormValue("node_id"),
		"replica_type":     replicaType,
		"storage_profile":  storageProfile,
		"follow_symlinks":  r.FormValue("follow_symlinks") == "on",
		"user_permissions": userPermissions,
	}
	if folderURI := strings.TrimSpace(r.FormValue("folder_uri")); folderURI != "" {
		input["folder_uri"] = folderURI
	}
	if fileURIs := fileURILines(r.FormValue("file_uris")); fileURIs != nil {
		input["file_uris"] = fileURIs
	}
	var created inventory
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodPost, "/api/admin/inventories", input, &created); err != nil {
		if errors.Is(err, errUnauthorized) {
			h.renderError(w, r, sess, err)
			return
		}
		h.inventoryFormError(w, r, sess, false, inventory{Name: r.FormValue("name"), UserPermissions: userPermissions}, apiMessage(err))
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/dashboard/inventories/%d", created.ID), http.StatusSeeOther)
}

func (h *Handler) inventoryPage(w http.ResponseWriter, r *http.Request, sess authContext) {
	h.renderInventoryPage(w, r, sess, "")
}

func (h *Handler) renderInventoryPage(w http.ResponseWriter, r *http.Request, sess authContext, message string) {
	var item inventory
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodGet, "/api/admin/inventories/"+r.PathValue("id"), nil, &item); err != nil {
		h.renderInventoryPageLoadError(w, r, sess, err, message)
		return
	}
	if err := h.loadInventoryReplicaSyncStatuses(r.Context(), &sess, &item); err != nil {
		h.renderInventoryPageLoadError(w, r, sess, err, message)
		return
	}
	page := positiveInt(r.URL.Query().Get("page"), 1)
	count := filePageSize(r.URL.Query().Get("count"))
	var files inventoryFileList
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodGet, fmt.Sprintf("/api/admin/inventories/%s/files?page=%d&count=%d", r.PathValue("id"), page, count), nil, &files); err != nil {
		h.renderInventoryPageLoadError(w, r, sess, err, message)
		return
	}
	totalPages := pageCount(files.Total, files.Count)
	if files.Total > 0 && files.Page > totalPages {
		if err := h.apiAuthJSON(r.Context(), &sess, http.MethodGet, fmt.Sprintf("/api/admin/inventories/%s/files?page=%d&count=%d", r.PathValue("id"), totalPages, count), nil, &files); err != nil {
			h.renderInventoryPageLoadError(w, r, sess, err, message)
			return
		}
	}
	h.render(w, "inventory", pageData{
		Title: item.Name, Subtitle: fmt.Sprintf("Inventory #%d · %s · %s", item.ID, item.Type, item.Status),
		Active: "inventories", Error: message, Inventory: item, Files: newFilePage(files),
	})
}

func (h *Handler) loadInventoryReplicaSyncStatuses(ctx context.Context, sess *authContext, item *inventory) error {
	if len(item.Replicas) == 0 {
		return nil
	}

	syncStatuses := make(map[uint]string, len(item.Replicas))
	for page := 1; ; page++ {
		var list replicaList
		path := fmt.Sprintf("/api/admin/replicas?inventory_id=%d&page=%d&count=100", item.ID, page)
		if err := h.apiAuthJSON(ctx, sess, http.MethodGet, path, nil, &list); err != nil {
			return err
		}
		for _, replica := range list.Items {
			syncStatuses[replica.ID] = replica.SyncStatus
		}
		if int64(page*list.Count) >= list.Total || len(list.Items) == 0 {
			break
		}
	}

	for i := range item.Replicas {
		item.Replicas[i].SyncStatus = syncStatuses[item.Replicas[i].ID]
	}
	return nil
}

func (h *Handler) renderInventoryPageLoadError(w http.ResponseWriter, r *http.Request, sess authContext, loadErr error, message string) {
	if message == "" || errors.Is(loadErr, errUnauthorized) {
		h.renderError(w, r, sess, loadErr)
		return
	}
	h.render(w, "error", pageData{
		Title: "Request failed", Subtitle: "The inventory could not be loaded after the delete request.", Error: message,
	})
}

func (h *Handler) editInventoryPage(w http.ResponseWriter, r *http.Request, sess authContext) {
	var item inventory
	if !h.load(w, r, sess, "/api/admin/inventories/"+r.PathValue("id"), &item) {
		return
	}
	users, ok := h.loadUsers(w, r, sess)
	if !ok {
		return
	}
	users = activeUsers(users)
	h.render(w, "inventory_form", pageData{
		Title: "Edit inventory", Subtitle: fmt.Sprintf("Update inventory #%d.", item.ID),
		Active: "inventories", Inventory: item, Users: users, IsEdit: true,
	})
}

func (h *Handler) updateInventory(w http.ResponseWriter, r *http.Request, sess authContext) {
	if err := r.ParseForm(); err != nil {
		h.inventoryFormError(w, r, sess, true, inventory{}, "Invalid form submission.")
		return
	}
	users, ok := h.loadUsers(w, r, sess)
	if !ok {
		return
	}
	users = activeUsers(users)
	id, _ := strconv.ParseUint(r.PathValue("id"), 10, 64)
	var current inventory
	if !h.load(w, r, sess, "/api/admin/inventories/"+r.PathValue("id"), &current) {
		return
	}
	userPermissions := mergeHiddenUserPermissions(current.UserPermissions, userPermissionsFromForm(r.Form, users), users)
	item := inventory{ID: uint(id), Name: r.FormValue("name"), Status: r.FormValue("status"), UserPermissions: userPermissions}
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodPatch, "/api/admin/inventories/"+r.PathValue("id"), map[string]any{
		"name": item.Name, "status": item.Status, "user_permissions": item.UserPermissions,
	}, nil); err != nil {
		if errors.Is(err, errUnauthorized) {
			h.renderError(w, r, sess, err)
			return
		}
		h.inventoryFormError(w, r, sess, true, item, apiMessage(err))
		return
	}
	http.Redirect(w, r, "/dashboard/inventories/"+r.PathValue("id"), http.StatusSeeOther)
}

func (h *Handler) deleteInventory(w http.ResponseWriter, r *http.Request, sess authContext) {
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodDelete, "/api/admin/inventories/"+r.PathValue("id"), nil, nil); err != nil {
		if errors.Is(err, errUnauthorized) {
			h.renderError(w, r, sess, err)
			return
		}
		h.renderInventoryPage(w, r, sess, apiMessage(err))
		return
	}
	http.Redirect(w, r, "/dashboard/inventories", http.StatusSeeOther)
}

func (h *Handler) newReplicaPage(w http.ResponseWriter, r *http.Request, sess authContext) {
	inv, nodes, ok := h.loadReplicaFormData(w, r, sess)
	if !ok {
		return
	}
	h.render(w, "replica_form", pageData{
		Title: "Add replica", Subtitle: "Add a physical location to " + inv.Name + ".",
		Active: "inventories", Inventory: inv, Nodes: nodes, StorageProfiles: h.storageProfiles,
	})
}

func (h *Handler) createReplica(w http.ResponseWriter, r *http.Request, sess authContext) {
	if err := r.ParseForm(); err != nil {
		h.replicaFormError(w, r, sess, false, replica{}, "Invalid form submission.")
		return
	}
	inventoryID, _ := strconv.ParseUint(r.PathValue("id"), 10, 64)
	replicaType := r.FormValue("type")
	storageProfile := storageProfileForReplicaType(replicaType, r.FormValue("storage_profile"))
	input := map[string]any{
		"inventory_id":    uint(inventoryID),
		"node_id":         r.FormValue("node_id"),
		"uri":             strings.TrimSpace(r.FormValue("uri")),
		"type":            replicaType,
		"storage_profile": storageProfile,
		"follow_symlinks": r.FormValue("follow_symlinks") == "on",
	}
	if !h.validStorageProfile(storageProfile) {
		h.replicaFormError(w, r, sess, false, replica{
			InventoryID: uint(inventoryID), NodeID: r.FormValue("node_id"), URI: r.FormValue("uri"),
			Type: replicaType, UpstreamReplicaID: optionalUint(r.FormValue("upstream_replica_id")),
			StorageProfile: storageProfile,
			FollowSymlinks: r.FormValue("follow_symlinks") == "on",
		}, "Storage profile must match a configured profile.")
		return
	}
	if upstream := optionalUint(r.FormValue("upstream_replica_id")); upstream != nil {
		input["upstream_replica_id"] = *upstream
	}
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodPost, "/api/admin/replicas", input, nil); err != nil {
		if errors.Is(err, errUnauthorized) {
			h.renderError(w, r, sess, err)
			return
		}
		h.replicaFormError(w, r, sess, false, replica{
			InventoryID: uint(inventoryID), NodeID: r.FormValue("node_id"), URI: r.FormValue("uri"),
			Type: replicaType, UpstreamReplicaID: optionalUint(r.FormValue("upstream_replica_id")),
			StorageProfile: storageProfile,
			FollowSymlinks: r.FormValue("follow_symlinks") == "on",
		}, apiMessage(err))
		return
	}
	http.Redirect(w, r, "/dashboard/inventories/"+r.PathValue("id"), http.StatusSeeOther)
}

func (h *Handler) editReplicaPage(w http.ResponseWriter, r *http.Request, sess authContext) {
	inv, nodes, ok := h.loadReplicaFormData(w, r, sess)
	if !ok {
		return
	}
	var item replica
	if !h.load(w, r, sess, "/api/admin/replicas/"+r.PathValue("replica_id"), &item) {
		return
	}
	if item.InventoryID != inv.ID {
		http.NotFound(w, r)
		return
	}
	h.render(w, "replica_form", pageData{
		Title: "Edit replica", Subtitle: fmt.Sprintf("Update replica #%d in %s.", item.ID, inv.Name),
		Active: "inventories", Inventory: inv, Nodes: nodes, Replica: item, StorageProfiles: h.storageProfiles, IsEdit: true,
	})
}

func (h *Handler) updateReplica(w http.ResponseWriter, r *http.Request, sess authContext) {
	if err := r.ParseForm(); err != nil {
		h.replicaFormError(w, r, sess, true, replica{}, "Invalid form submission.")
		return
	}
	replicaID, _ := strconv.ParseUint(r.PathValue("replica_id"), 10, 64)
	replicaType := r.FormValue("type")
	storageProfile := storageProfileForReplicaType(replicaType, r.FormValue("storage_profile"))
	item := replica{
		ID: uint(replicaID), Type: replicaType, Status: r.FormValue("status"),
		UpstreamReplicaID: optionalUint(r.FormValue("upstream_replica_id")),
		StorageProfile:    storageProfile,
		FollowSymlinks:    r.FormValue("follow_symlinks") == "on",
	}
	if !h.validStorageProfile(item.StorageProfile) {
		h.replicaFormError(w, r, sess, true, item, "Storage profile must match a configured profile.")
		return
	}
	input := map[string]any{
		"type":                item.Type,
		"status":              item.Status,
		"upstream_replica_id": item.UpstreamReplicaID,
		"storage_profile":     item.StorageProfile,
		"follow_symlinks":     item.FollowSymlinks,
	}
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodPatch, "/api/admin/replicas/"+r.PathValue("replica_id"), input, nil); err != nil {
		if errors.Is(err, errUnauthorized) {
			h.renderError(w, r, sess, err)
			return
		}
		h.replicaFormError(w, r, sess, true, item, apiMessage(err))
		return
	}
	http.Redirect(w, r, "/dashboard/inventories/"+r.PathValue("id"), http.StatusSeeOther)
}

func (h *Handler) deleteReplica(w http.ResponseWriter, r *http.Request, sess authContext) {
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodDelete, "/api/admin/replicas/"+r.PathValue("replica_id"), nil, nil); err != nil {
		h.renderError(w, r, sess, err)
		return
	}
	http.Redirect(w, r, "/dashboard/inventories/"+r.PathValue("id"), http.StatusSeeOther)
}

func (h *Handler) sharesPage(w http.ResponseWriter, r *http.Request, sess authContext) {
	var list shareList
	if !h.load(w, r, sess, "/api/admin/shares?count=100", &list) {
		return
	}
	inventories, ok := h.loadInventories(w, r, sess)
	if !ok {
		return
	}
	h.render(w, "shares", pageData{
		Title: "Shares", Subtitle: "Expose selected replicas through share records.",
		Active: "shares", Shares: shareViews(list.Items, inventories),
	})
}

func (h *Handler) newSharePage(w http.ResponseWriter, r *http.Request, sess authContext) {
	inventories, ok := h.loadInventories(w, r, sess)
	if !ok {
		return
	}
	nodes, ok := h.loadNodes(w, r, sess)
	if !ok {
		return
	}
	inventories = shareSelectableInventories(inventories, nodes)
	users, ok := h.loadUsers(w, r, sess)
	if !ok {
		return
	}
	users = activeUsers(users)
	thumbnailSizes, ok := h.loadShareThumbnailSizes(w, r, sess)
	if !ok {
		return
	}
	h.render(w, "share_form", pageData{
		Title: "New share", Subtitle: "Create a share for an existing replica.",
		Active: "shares", Inventories: inventories, Users: users, ThumbnailSizes: thumbnailSizes,
	})
}

func (h *Handler) createShare(w http.ResponseWriter, r *http.Request, sess authContext) {
	if err := r.ParseForm(); err != nil {
		h.shareFormError(w, r, sess, false, share{}, "Invalid form submission.")
		return
	}
	users, ok := h.loadUsers(w, r, sess)
	if !ok {
		return
	}
	users = activeUsers(users)
	replicaID, _ := strconv.ParseUint(r.FormValue("replica_id"), 10, 64)
	item := share{
		ReplicaID:            uint(replicaID),
		Name:                 r.FormValue("name"),
		UserPermissions:      shareUserPermissionsFromForm(r.Form, users),
		AnonymousPermissions: sharePermissionsFromForm(r.Form["anonymous_permissions"], []string{"read", "update"}),
	}
	properties, err := sharePropertiesFromForm(r.Form)
	if err != nil {
		h.shareFormError(w, r, sess, false, item, err.Error())
		return
	}
	item.Properties = properties
	input := map[string]any{
		"replica_id":            item.ReplicaID,
		"user_permissions":      item.UserPermissions,
		"anonymous_permissions": item.AnonymousPermissions,
		"generate_hash":         len(item.AnonymousPermissions) > 0,
		"properties":            properties,
	}
	if name := strings.TrimSpace(item.Name); name != "" {
		input["name"] = name
	}
	if r.FormValue("enable_expiration") != "" {
		expirationInput := strings.TrimSpace(r.FormValue("share_expiration"))
		item.ShareExpiration = &expirationInput
		expiration, err := normalizeShareExpiration(expirationInput)
		if err != nil {
			h.shareFormError(w, r, sess, false, item, "Expiration is required when expiration is enabled.")
			return
		}
		input["share_expiration"] = expiration
	}
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodPost, "/api/admin/shares", input, nil); err != nil {
		if errors.Is(err, errUnauthorized) {
			h.renderError(w, r, sess, err)
			return
		}
		h.shareFormError(w, r, sess, false, item, apiMessage(err))
		return
	}
	http.Redirect(w, r, "/dashboard/shares", http.StatusSeeOther)
}

func (h *Handler) editSharePage(w http.ResponseWriter, r *http.Request, sess authContext) {
	var item share
	if !h.load(w, r, sess, "/api/admin/shares/"+r.PathValue("id"), &item) {
		return
	}
	users, ok := h.loadUsers(w, r, sess)
	if !ok {
		return
	}
	users = activeUsers(users)
	thumbnailSizes, ok := h.loadShareThumbnailSizes(w, r, sess)
	if !ok {
		return
	}
	h.render(w, "share_form", pageData{
		Title: "Edit share", Subtitle: fmt.Sprintf("Update share #%d.", item.ID),
		Active: "shares", Share: item, Users: users, ThumbnailSizes: thumbnailSizes, IsEdit: true,
	})
}

func (h *Handler) updateShare(w http.ResponseWriter, r *http.Request, sess authContext) {
	if err := r.ParseForm(); err != nil {
		id, _ := strconv.ParseUint(r.PathValue("id"), 10, 64)
		h.shareFormError(w, r, sess, true, share{ID: uint(id)}, "Invalid form submission.")
		return
	}
	users, ok := h.loadUsers(w, r, sess)
	if !ok {
		return
	}
	users = activeUsers(users)
	id, _ := strconv.ParseUint(r.PathValue("id"), 10, 64)
	var current share
	if !h.load(w, r, sess, "/api/admin/shares/"+r.PathValue("id"), &current) {
		return
	}
	item := share{
		ID:                   uint(id),
		InventoryID:          current.InventoryID,
		ReplicaID:            current.ReplicaID,
		Name:                 r.FormValue("name"),
		Status:               r.FormValue("status"),
		LinkHash:             current.LinkHash,
		UserPermissions:      mergeHiddenUserPermissions(current.UserPermissions, shareUserPermissionsFromForm(r.Form, users), users),
		AnonymousPermissions: sharePermissionsFromForm(r.Form["anonymous_permissions"], []string{"read", "update"}),
	}
	properties, err := sharePropertiesFromForm(r.Form)
	if err != nil {
		h.shareFormError(w, r, sess, true, item, err.Error())
		return
	}
	item.Properties = properties
	input := map[string]any{
		"name":                  strings.TrimSpace(item.Name),
		"status":                item.Status,
		"user_permissions":      item.UserPermissions,
		"anonymous_permissions": item.AnonymousPermissions,
		"properties":            properties,
	}
	if len(item.AnonymousPermissions) > 0 {
		if current.LinkHash == nil || *current.LinkHash == "" {
			input["generate_hash"] = true
		}
	} else {
		input["generate_hash"] = false
	}
	if r.FormValue("enable_expiration") != "" {
		expirationInput := strings.TrimSpace(r.FormValue("share_expiration"))
		item.ShareExpiration = &expirationInput
		expiration, err := normalizeShareExpiration(expirationInput)
		if err != nil {
			h.shareFormError(w, r, sess, true, item, "Expiration is required when expiration is enabled.")
			return
		}
		input["share_expiration"] = expiration
	} else {
		input["share_expiration"] = nil
	}
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodPatch, "/api/admin/shares/"+r.PathValue("id"), input, nil); err != nil {
		if errors.Is(err, errUnauthorized) {
			h.renderError(w, r, sess, err)
			return
		}
		h.shareFormError(w, r, sess, true, item, apiMessage(err))
		return
	}
	http.Redirect(w, r, "/dashboard/shares", http.StatusSeeOther)
}

func (h *Handler) deleteShare(w http.ResponseWriter, r *http.Request, sess authContext) {
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodDelete, "/api/admin/shares/"+r.PathValue("id"), nil, nil); err != nil {
		if errors.Is(err, errUnauthorized) {
			h.renderError(w, r, sess, err)
			return
		}
		h.renderSharesPageError(w, r, sess, apiMessage(err))
		return
	}
	http.Redirect(w, r, "/dashboard/shares", http.StatusSeeOther)
}

func (h *Handler) usersPage(w http.ResponseWriter, r *http.Request, sess authContext) {
	var list userList
	if !h.load(w, r, sess, "/api/admin/users?count=100", &list) {
		return
	}
	h.render(w, "users", pageData{
		Title: "Users", Subtitle: "Manage user accounts, status, and assigned roles.",
		Active: "users", Users: list.Items,
	})
}

func (h *Handler) newUserPage(w http.ResponseWriter, r *http.Request, sess authContext) {
	roles, ok := h.loadRoles(w, r, sess)
	if !ok {
		return
	}
	h.render(w, "user_form", pageData{
		Title: "New user", Subtitle: "Create a user account and assign roles.",
		Active: "users", Roles: roles,
	})
}

func (h *Handler) createUser(w http.ResponseWriter, r *http.Request, sess authContext) {
	if err := r.ParseForm(); err != nil {
		h.userFormError(w, r, sess, false, user{}, "Invalid form submission.")
		return
	}
	input := map[string]any{
		"name":     strings.TrimSpace(r.FormValue("name")),
		"password": r.FormValue("password"),
		"role_ids": formUintValues(r.Form["role_ids"]),
	}
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodPost, "/api/admin/users", input, nil); err != nil {
		if errors.Is(err, errUnauthorized) {
			h.renderError(w, r, sess, err)
			return
		}
		h.userFormError(w, r, sess, false, user{Name: r.FormValue("name")}, apiMessage(err))
		return
	}
	http.Redirect(w, r, "/dashboard/users", http.StatusSeeOther)
}

func (h *Handler) editUserPage(w http.ResponseWriter, r *http.Request, sess authContext) {
	var item user
	if !h.load(w, r, sess, "/api/admin/users/"+r.PathValue("id"), &item) {
		return
	}
	roles, ok := h.loadRoles(w, r, sess)
	if !ok {
		return
	}
	h.render(w, "user_form", pageData{
		Title: "Edit user", Subtitle: fmt.Sprintf("Update user #%d.", item.ID),
		Active: "users", User: item, Roles: roles, IsEdit: true,
	})
}

func (h *Handler) updateUser(w http.ResponseWriter, r *http.Request, sess authContext) {
	if err := r.ParseForm(); err != nil {
		id, _ := strconv.ParseUint(r.PathValue("id"), 10, 64)
		h.userFormError(w, r, sess, true, user{ID: uint(id)}, "Invalid form submission.")
		return
	}
	id, _ := strconv.ParseUint(r.PathValue("id"), 10, 64)
	item := user{ID: uint(id), Name: r.FormValue("name"), Status: r.FormValue("status")}
	input := map[string]any{
		"name":     strings.TrimSpace(item.Name),
		"status":   item.Status,
		"role_ids": formUintValues(r.Form["role_ids"]),
	}
	if password := r.FormValue("password"); password != "" {
		input["password"] = password
	}
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodPatch, "/api/admin/users/"+r.PathValue("id"), input, nil); err != nil {
		if errors.Is(err, errUnauthorized) {
			h.renderError(w, r, sess, err)
			return
		}
		h.userFormError(w, r, sess, true, item, apiMessage(err))
		return
	}
	http.Redirect(w, r, "/dashboard/users", http.StatusSeeOther)
}

func (h *Handler) rolesPage(w http.ResponseWriter, r *http.Request, sess authContext) {
	var list roleList
	if !h.load(w, r, sess, "/api/admin/roles?count=100", &list) {
		return
	}
	h.render(w, "roles", pageData{
		Title: "Roles", Subtitle: "Manage role permissions and status.",
		Active: "roles", Roles: list.Items,
	})
}

func (h *Handler) newRolePage(w http.ResponseWriter, _ *http.Request, _ authContext) {
	h.render(w, "role_form", pageData{
		Title: "New role", Subtitle: "Create a role and assign permissions.", Active: "roles",
		PermissionResources: rolePermissionResources(),
	})
}

func (h *Handler) createRole(w http.ResponseWriter, r *http.Request, sess authContext) {
	if err := r.ParseForm(); err != nil {
		h.roleFormError(w, false, role{}, nil, "Invalid form submission.")
		return
	}
	item := role{Name: r.FormValue("name"), Description: r.FormValue("description")}
	permissions := formPermissions(r.Form["permissions"])
	input := map[string]any{
		"name":        strings.TrimSpace(item.Name),
		"description": strings.TrimSpace(item.Description),
		"permissions": permissions,
	}
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodPost, "/api/admin/roles", input, nil); err != nil {
		if errors.Is(err, errUnauthorized) {
			h.renderError(w, r, sess, err)
			return
		}
		h.roleFormError(w, false, item, permissions, apiMessage(err))
		return
	}
	http.Redirect(w, r, "/dashboard/roles", http.StatusSeeOther)
}

func (h *Handler) editRolePage(w http.ResponseWriter, r *http.Request, sess authContext) {
	var item role
	if !h.load(w, r, sess, "/api/admin/roles/"+r.PathValue("id"), &item) {
		return
	}
	h.render(w, "role_form", pageData{
		Title: "Edit role", Subtitle: fmt.Sprintf("Update role #%d.", item.ID),
		Active: "roles", Role: item, PermissionResources: rolePermissionResources(), IsEdit: true,
	})
}

func (h *Handler) updateRole(w http.ResponseWriter, r *http.Request, sess authContext) {
	if err := r.ParseForm(); err != nil {
		id, _ := strconv.ParseUint(r.PathValue("id"), 10, 64)
		h.roleFormError(w, true, role{ID: uint(id)}, nil, "Invalid form submission.")
		return
	}
	id, _ := strconv.ParseUint(r.PathValue("id"), 10, 64)
	item := role{
		ID: uint(id), Name: r.FormValue("name"), Description: r.FormValue("description"), Status: r.FormValue("status"),
	}
	permissions := formPermissions(r.Form["permissions"])
	input := map[string]any{
		"name":        strings.TrimSpace(item.Name),
		"description": strings.TrimSpace(item.Description),
		"status":      item.Status,
		"permissions": permissions,
	}
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodPatch, "/api/admin/roles/"+r.PathValue("id"), input, nil); err != nil {
		if errors.Is(err, errUnauthorized) {
			h.renderError(w, r, sess, err)
			return
		}
		h.roleFormError(w, true, item, permissions, apiMessage(err))
		return
	}
	http.Redirect(w, r, "/dashboard/roles", http.StatusSeeOther)
}

func (h *Handler) settingsPage(w http.ResponseWriter, r *http.Request, sess authContext) {
	h.renderSettingsPage(w, r, sess, "")
}

func (h *Handler) renderSettingsPage(w http.ResponseWriter, r *http.Request, sess authContext, message string) {
	var list configList
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodGet, "/api/admin/config", nil, &list); err != nil {
		h.renderError(w, r, sess, err)
		return
	}
	h.render(w, "settings", pageData{
		Title: "Settings", Subtitle: "Update coordinator-managed sharing configuration.",
		Active: "settings", Settings: configViews(list.Items), Error: message,
	})
}

func (h *Handler) updateSettings(w http.ResponseWriter, r *http.Request, sess authContext) {
	if err := r.ParseForm(); err != nil {
		h.settingsFormError(w, configViewsFromForm(r.Form), "Invalid form submission.")
		return
	}
	updates, err := configUpdatesFromForm(r.Form)
	if err != nil {
		h.settingsFormError(w, configViewsFromForm(r.Form), err.Error())
		return
	}
	input := map[string]any{"items": updates}
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodPatch, "/api/admin/config", input, nil); err != nil {
		if errors.Is(err, errUnauthorized) {
			h.renderError(w, r, sess, err)
			return
		}
		h.settingsFormError(w, configViewsFromForm(r.Form), apiMessage(err))
		return
	}
	http.Redirect(w, r, "/dashboard/settings", http.StatusSeeOther)
}

func (h *Handler) resetSettings(w http.ResponseWriter, r *http.Request, sess authContext) {
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodDelete, "/api/admin/config", nil, nil); err != nil {
		if errors.Is(err, errUnauthorized) {
			h.renderError(w, r, sess, err)
			return
		}
		h.renderSettingsPage(w, r, sess, apiMessage(err))
		return
	}
	http.Redirect(w, r, "/dashboard/settings", http.StatusSeeOther)
}

func (h *Handler) resetSetting(w http.ResponseWriter, r *http.Request, sess authContext) {
	key := r.PathValue("key")
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodDelete, "/api/admin/config/"+url.PathEscape(key), nil, nil); err != nil {
		if errors.Is(err, errUnauthorized) {
			h.renderError(w, r, sess, err)
			return
		}
		h.renderSettingsPage(w, r, sess, apiMessage(err))
		return
	}
	http.Redirect(w, r, "/dashboard/settings", http.StatusSeeOther)
}

func (h *Handler) protected(next func(http.ResponseWriter, *http.Request, authContext)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		accessToken, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok {
			h.render(w, "login", pageData{Title: "Sign in"})
			return
		}
		next(w, r, authContext{AccessToken: accessToken})
	}
}

func (h *Handler) load(w http.ResponseWriter, r *http.Request, sess authContext, path string, output any) bool {
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodGet, path, nil, output); err != nil {
		h.renderError(w, r, sess, err)
		return false
	}
	return true
}

func (h *Handler) loadNodes(w http.ResponseWriter, r *http.Request, sess authContext) ([]node, bool) {
	var list nodeList
	if !h.load(w, r, sess, "/api/admin/nodes?count=100", &list) {
		return nil, false
	}
	return list.Items, true
}

func (h *Handler) loadInventories(w http.ResponseWriter, r *http.Request, sess authContext) ([]inventory, bool) {
	var list inventoryList
	if !h.load(w, r, sess, "/api/admin/inventories?count=100", &list) {
		return nil, false
	}
	return list.Items, true
}

func (h *Handler) loadReplicaFormData(w http.ResponseWriter, r *http.Request, sess authContext) (inventory, []node, bool) {
	var inv inventory
	if !h.load(w, r, sess, "/api/admin/inventories/"+r.PathValue("id"), &inv) {
		return inventory{}, nil, false
	}
	nodes, ok := h.loadNodes(w, r, sess)
	return inv, nodes, ok
}

func (h *Handler) loadRoles(w http.ResponseWriter, r *http.Request, sess authContext) ([]role, bool) {
	var list roleList
	if !h.load(w, r, sess, "/api/admin/roles?count=100", &list) {
		return nil, false
	}
	return list.Items, true
}

func (h *Handler) loadUsers(w http.ResponseWriter, r *http.Request, sess authContext) ([]user, bool) {
	var list userList
	if !h.load(w, r, sess, "/api/admin/users?count=100", &list) {
		return nil, false
	}
	return list.Items, true
}

func activeUsers(users []user) []user {
	result := make([]user, 0, len(users))
	for _, item := range users {
		if item.Status == "active" {
			result = append(result, item)
		}
	}
	return result
}

func (h *Handler) loadAllShares(w http.ResponseWriter, r *http.Request, sess authContext) ([]share, bool) {
	const count = 100
	var result []share
	for page := 1; ; page++ {
		var list shareList
		if !h.load(w, r, sess, fmt.Sprintf("/api/admin/shares?page=%d&count=%d", page, count), &list) {
			return nil, false
		}
		result = append(result, list.Items...)
		if int64(len(result)) >= list.Total || len(list.Items) == 0 {
			return result, true
		}
	}
}

func (h *Handler) renderError(w http.ResponseWriter, r *http.Request, _ authContext, err error) {
	if errors.Is(err, errUnauthorized) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	h.renderStatus(w, http.StatusBadGateway, "error", pageData{
		Title: "Request failed", Subtitle: "The public API request could not be completed.", Error: apiMessage(err),
	})
}

func (h *Handler) nodeFormError(w http.ResponseWriter, edit bool, item node, message string) {
	title := "Add node"
	if edit {
		title = "Edit node"
	}
	h.render(w, "node_form", pageData{Title: title, Active: "nodes", Node: item, IsEdit: edit, Error: message, NodeConfig: h.nodeConfig})
}

func (h *Handler) inventoryFormError(w http.ResponseWriter, r *http.Request, sess authContext, edit bool, item inventory, message string) {
	nodes := []node{}
	if !edit {
		var list nodeList
		err := h.apiAuthJSON(r.Context(), &sess, http.MethodGet, "/api/admin/nodes?count=100", nil, &list)
		if errors.Is(err, errUnauthorized) {
			h.renderError(w, r, sess, err)
			return
		}
		if err == nil {
			nodes = list.Items
		}
	}
	users, ok := h.loadUsers(w, r, sess)
	if !ok {
		return
	}
	users = activeUsers(users)
	title := "New inventory"
	if edit {
		title = "Edit inventory"
	}
	h.render(w, "inventory_form", pageData{
		Title: title, Active: "inventories", Inventory: item, Nodes: nodes, Users: users, IsEdit: edit, Error: message,
		FolderURI: r.FormValue("folder_uri"), FileURIs: r.FormValue("file_uris"), StorageProfiles: h.storageProfiles,
		Replica: replica{Type: r.FormValue("replica_type"), StorageProfile: storageProfileForReplicaType(r.FormValue("replica_type"), r.FormValue("storage_profile")), FollowSymlinks: r.FormValue("follow_symlinks") == "on"},
	})
}

func fileURILines(value string) []string {
	normalized := strings.ReplaceAll(value, "\r\n", "\n")
	if strings.TrimSpace(normalized) == "" {
		return nil
	}
	lines := strings.Split(normalized, "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	return lines
}

func (h *Handler) replicaFormError(w http.ResponseWriter, r *http.Request, sess authContext, edit bool, item replica, message string) {
	inv, nodes, ok := h.loadReplicaFormData(w, r, sess)
	if !ok {
		return
	}
	title := "Add replica"
	if edit {
		title = "Edit replica"
	}
	h.render(w, "replica_form", pageData{
		Title: title, Active: "inventories", Inventory: inv, Nodes: nodes, Replica: item, StorageProfiles: h.storageProfiles, IsEdit: edit, Error: message,
	})
}

func storageProfileNames(cfg config.StorageConfig) []string {
	profiles := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		name = strings.TrimSpace(name)
		if name != "" {
			profiles = append(profiles, name)
		}
	}
	sort.Strings(profiles)
	return profiles
}

func storageProfileSet(profiles []string) map[string]struct{} {
	set := make(map[string]struct{}, len(profiles))
	for _, profile := range profiles {
		set[profile] = struct{}{}
	}
	return set
}

func (h *Handler) validStorageProfile(profile string) bool {
	profile = normalizeStorageProfileFormValue(profile)
	if profile == "" {
		return true
	}
	_, ok := h.storageProfileSet[profile]
	return ok
}

func normalizeStorageProfileFormValue(profile string) string {
	return strings.ToLower(strings.TrimSpace(profile))
}

func storageProfileForReplicaType(replicaType string, profile string) string {
	if strings.TrimSpace(replicaType) != "storage" {
		return ""
	}
	return normalizeStorageProfileFormValue(profile)
}

func (h *Handler) shareFormError(w http.ResponseWriter, r *http.Request, sess authContext, edit bool, item share, message string) {
	inventories := []inventory{}
	if !edit {
		var ok bool
		inventories, ok = h.loadInventories(w, r, sess)
		if !ok {
			return
		}
		nodes, ok := h.loadNodes(w, r, sess)
		if !ok {
			return
		}
		inventories = shareSelectableInventories(inventories, nodes)
	}
	users, ok := h.loadUsers(w, r, sess)
	if !ok {
		return
	}
	users = activeUsers(users)
	thumbnailSizes, ok := h.loadShareThumbnailSizes(w, r, sess)
	if !ok {
		return
	}
	title := "New share"
	if edit {
		title = "Edit share"
	}
	h.render(w, "share_form", pageData{
		Title: title, Active: "shares", Share: item, Inventories: inventories, Users: users, ThumbnailSizes: thumbnailSizes, IsEdit: edit, Error: message,
	})
}

func (h *Handler) loadShareThumbnailSizes(w http.ResponseWriter, r *http.Request, sess authContext) ([]int, bool) {
	var list configList
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodGet, "/api/admin/config", nil, &list); err != nil {
		h.renderError(w, r, sess, err)
		return nil, false
	}
	for _, item := range list.Items {
		if item.Key != "sharing.thumbnail_sizes" {
			continue
		}
		var sizes []int
		if err := json.Unmarshal(item.Value, &sizes); err != nil {
			h.renderError(w, r, sess, err)
			return nil, false
		}
		return sizes, true
	}
	h.renderError(w, r, sess, errors.New("sharing thumbnail sizes are unavailable"))
	return nil, false
}

func sharePropertiesFromForm(values url.Values) (shareProperties, error) {
	var properties shareProperties
	view := strings.TrimSpace(values.Get("property_view"))
	if view != "" {
		if view != "grid" && view != "list" {
			return properties, errors.New("View must be grid, list, or unset.")
		}
		properties.View = &view
	}
	pageSize, err := optionalPositiveFormInt(values.Get("property_page_size"))
	if err != nil {
		return properties, errors.New("Page size must be a positive integer or unset.")
	}
	properties.PageSize = pageSize
	thumbnailSize, err := optionalPositiveFormInt(values.Get("property_thumbnail_size"))
	if err != nil {
		return properties, errors.New("Thumbnail size must be a configured size or unset.")
	}
	properties.ThumbnailSize = thumbnailSize
	theme := strings.TrimSpace(values.Get("property_theme"))
	if theme != "" {
		if theme != "light" && theme != "dark" {
			return properties, errors.New("Theme must be light, dark, or unset.")
		}
		properties.Theme = &theme
	}
	return properties, nil
}

func optionalPositiveFormInt(value string) (*int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return nil, errors.New("invalid positive integer")
	}
	return &parsed, nil
}

func selectedString(value *string, expected string) bool {
	return value != nil && *value == expected
}

func selectedInt(value *int, expected int) bool {
	return value != nil && *value == expected
}

func optionalIntValue(value *int) string {
	if value == nil {
		return ""
	}
	return strconv.Itoa(*value)
}

func (h *Handler) renderSharesPageError(w http.ResponseWriter, r *http.Request, sess authContext, message string) {
	var list shareList
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodGet, "/api/admin/shares?count=100", nil, &list); err != nil {
		h.renderError(w, r, sess, err)
		return
	}
	inventories, ok := h.loadInventories(w, r, sess)
	if !ok {
		return
	}
	h.render(w, "shares", pageData{
		Title: "Shares", Subtitle: "Expose selected replicas through share records.",
		Active: "shares", Shares: shareViews(list.Items, inventories), Error: message,
	})
}

func (h *Handler) userFormError(w http.ResponseWriter, r *http.Request, sess authContext, edit bool, item user, message string) {
	roles, ok := h.loadRoles(w, r, sess)
	if !ok {
		return
	}
	item.Roles = selectedRoles(roles, formUintValues(r.Form["role_ids"]))
	title := "New user"
	if edit {
		title = "Edit user"
	}
	h.render(w, "user_form", pageData{
		Title: title, Active: "users", User: item, Roles: roles, IsEdit: edit, Error: message,
	})
}

func (h *Handler) roleFormError(w http.ResponseWriter, edit bool, item role, permissions []permissionInput, message string) {
	item.Permissions = make([]permission, 0, len(permissions))
	for _, selected := range permissions {
		item.Permissions = append(item.Permissions, permission{Resource: selected.Resource, Action: selected.Action})
	}
	title := "New role"
	if edit {
		title = "Edit role"
	}
	h.render(w, "role_form", pageData{
		Title: title, Active: "roles", Role: item, PermissionResources: rolePermissionResources(), IsEdit: edit, Error: message,
	})
}

func (h *Handler) settingsFormError(w http.ResponseWriter, settings []configView, message string) {
	h.render(w, "settings", pageData{
		Title: "Settings", Subtitle: "Update coordinator-managed sharing configuration.",
		Active: "settings", Settings: settings, Error: message,
	})
}

func (h *Handler) apiJSON(ctx context.Context, accessToken, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}

	req := httptest.NewRequestWithContext(ctx, method, path, body)
	req.Header.Set("X-API-Version", "1")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
	recorder := httptest.NewRecorder()
	h.api.ServeHTTP(recorder, req)

	if recorder.Code == http.StatusUnauthorized {
		return errUnauthorized
	}
	if recorder.Code < 200 || recorder.Code >= 300 {
		return apiResponseError(recorder.Code, recorder.Body.Bytes())
	}
	if output == nil || recorder.Code == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(recorder.Body).Decode(output)
}

func (h *Handler) apiAuthJSON(ctx context.Context, sess *authContext, method, path string, input, output any) error {
	return h.apiJSON(ctx, sess.AccessToken, method, path, input, output)
}

func (h *Handler) render(w http.ResponseWriter, name string, data pageData) {
	h.renderStatus(w, http.StatusOK, name, data)
}

func (h *Handler) renderStatus(w http.ResponseWriter, status int, name string, data pageData) {
	var output bytes.Buffer
	if err := h.pages.ExecuteTemplate(&output, name, data); err != nil {
		http.Error(w, "render admin page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = output.WriteTo(w)
}

func bearerToken(header string) (string, bool) {
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return "", false
	}
	return strings.TrimSpace(token), true
}

type responseError struct {
	Status  int
	Message string
}

func (e *responseError) Error() string {
	return e.Message
}

func apiResponseError(status int, body []byte) error {
	var problem struct {
		Title  string `json:"title"`
		Detail string `json:"detail"`
		Error  string `json:"error"`
	}
	_ = json.Unmarshal(body, &problem)
	message := problem.Detail
	if message == "" {
		message = problem.Error
	}
	if message == "" {
		message = problem.Title
	}
	if message == "" {
		message = http.StatusText(status)
	}
	return &responseError{Status: status, Message: message}
}

func apiMessage(err error) string {
	if errors.Is(err, errUnauthorized) {
		return "Your session has expired. Sign in again."
	}
	return err.Error()
}

func optionalUint(value string) *uint {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed == 0 {
		return nil
	}
	result := uint(parsed)
	return &result
}

func formUintValues(values []string) []uint {
	result := make([]uint, 0, len(values))
	for _, value := range values {
		parsed, err := strconv.ParseUint(value, 10, 64)
		if err == nil && parsed > 0 {
			result = append(result, uint(parsed))
		}
	}
	return result
}

func selectedRoles(roles []role, ids []uint) []role {
	selected := make(map[uint]struct{}, len(ids))
	for _, id := range ids {
		selected[id] = struct{}{}
	}
	result := make([]role, 0, len(ids))
	for _, item := range roles {
		if _, ok := selected[item.ID]; ok {
			result = append(result, item)
		}
	}
	return result
}

func hasRole(id uint, roles []role) bool {
	for _, item := range roles {
		if item.ID == id {
			return true
		}
	}
	return false
}

func formPermissions(values []string) []permissionInput {
	result := make([]permissionInput, 0, len(values))
	for _, value := range values {
		resource, action, ok := strings.Cut(value, ":")
		if ok && resource != "" && action != "" {
			result = append(result, permissionInput{Resource: resource, Action: action})
		}
	}
	return result
}

func hasPermission(resource, action string, permissions []permission) bool {
	for _, item := range permissions {
		if item.Resource == resource && item.Action == action {
			return true
		}
	}
	return false
}

func shareUserPermissionsFromForm(values url.Values, users []user) []shareUserPermission {
	return userPermissionsFromForm(values, users)
}

func userPermissionsFromForm(values url.Values, users []user) []shareUserPermission {
	result := make([]shareUserPermission, 0, len(users))
	for _, item := range users {
		permissions := sharePermissionsFromForm(values["user_permissions_"+strconv.FormatUint(uint64(item.ID), 10)], []string{"read", "update", "delete"})
		if len(permissions) == 0 {
			continue
		}
		result = append(result, shareUserPermission{
			UserID:      item.ID,
			Permissions: permissions,
		})
	}
	return result
}

func mergeHiddenUserPermissions(existing, submitted []shareUserPermission, visibleUsers []user) []shareUserPermission {
	visible := make(map[uint]struct{}, len(visibleUsers))
	for _, item := range visibleUsers {
		visible[item.ID] = struct{}{}
	}

	result := make([]shareUserPermission, 0, len(existing)+len(submitted))
	for _, item := range existing {
		if _, ok := visible[item.UserID]; ok {
			continue
		}
		result = append(result, item)
	}
	result = append(result, submitted...)
	return result
}

func sharePermissionsFromForm(values []string, allowedActions []string) []string {
	allowed := make(map[string]struct{}, len(allowedActions))
	for _, action := range allowedActions {
		allowed[action] = struct{}{}
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if _, ok := allowed[value]; !ok {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func hasShareUserPermission(userID uint, action string, permissions []shareUserPermission) bool {
	for _, item := range permissions {
		if item.UserID != userID {
			continue
		}
		return hasStringPermission(action, item.Permissions)
	}
	return false
}

func hasStringPermission(action string, permissions []string) bool {
	for _, item := range permissions {
		if item == action {
			return true
		}
	}
	return false
}

func anonymousEnabled(share share) bool {
	return len(share.AnonymousPermissions) > 0
}

func normalizeShareExpiration(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("empty share expiration")
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.UTC().Format(time.RFC3339), nil
	}

	for _, layout := range []string{
		"2006-01-02",
		"2006 01 02",
		"2006-01-02T15:04",
		"2006-01-02T15:04:05",
		"2006-01-02T15:04:05.000",
	} {
		parsed, err := time.ParseInLocation(layout, value, time.UTC)
		if err == nil {
			return parsed.UTC().Format(time.RFC3339), nil
		}
	}
	return "", errors.New("invalid share expiration")
}

func expirationValue(share share) string {
	if share.ShareExpiration == nil {
		return ""
	}
	value := strings.TrimSpace(*share.ShareExpiration)
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.Format("2006-01-02")
	}
	for _, layout := range []string{"2006-01-02", "2006 01 02"} {
		if parsed, err := time.ParseInLocation(layout, value, time.UTC); err == nil {
			return parsed.Format("2006-01-02")
		}
	}
	return value
}

func rolePermissionResources() []rolePermissionResource {
	return []rolePermissionResource{
		{Name: "users", Actions: []string{"read", "update", "create", "delete"}},
		{Name: "shares", Actions: []string{"read", "update", "create", "delete"}},
		{Name: "inventories", Actions: []string{"read", "update", "create", "delete"}},
		{Name: "nodes", Actions: []string{"read", "update", "create", "delete"}},
		{Name: "settings", Actions: []string{"read", "update"}},
	}
}

func configViews(items []configItem) []configView {
	byKey := make(map[string]json.RawMessage, len(items))
	for _, item := range items {
		byKey[item.Key] = item.Value
	}
	result := make([]configView, 0, len(configDefinitions()))
	for _, def := range configDefinitions() {
		view := def
		value := byKey[def.Key]
		switch def.Kind {
		case "bool":
			var parsed bool
			_ = json.Unmarshal(value, &parsed)
			view.BoolValue = parsed
			view.Value = strconv.FormatBool(parsed)
		case "int":
			var parsed int
			_ = json.Unmarshal(value, &parsed)
			view.Value = strconv.Itoa(parsed)
		case "int_list":
			var parsed []int
			_ = json.Unmarshal(value, &parsed)
			parts := make([]string, 0, len(parsed))
			for _, item := range parsed {
				parts = append(parts, strconv.Itoa(item))
			}
			view.Value = strings.Join(parts, ", ")
		}
		result = append(result, view)
	}
	return result
}

func configViewsFromForm(values url.Values) []configView {
	result := make([]configView, 0, len(configDefinitions()))
	for _, def := range configDefinitions() {
		view := def
		switch def.Kind {
		case "bool":
			view.BoolValue = values.Get(def.Key) == "true"
			view.Value = strconv.FormatBool(view.BoolValue)
		default:
			view.Value = strings.TrimSpace(values.Get(def.Key))
		}
		result = append(result, view)
	}
	return result
}

func configUpdatesFromForm(values url.Values) ([]configUpdateItem, error) {
	result := make([]configUpdateItem, 0, len(configDefinitions()))
	for _, def := range configDefinitions() {
		switch def.Kind {
		case "bool":
			result = append(result, configUpdateItem{Key: def.Key, Value: values.Get(def.Key) == "true"})
		case "int":
			value, err := strconv.Atoi(strings.TrimSpace(values.Get(def.Key)))
			if err != nil || value <= 0 {
				return nil, fmt.Errorf("%s must be a positive integer", def.Label)
			}
			result = append(result, configUpdateItem{Key: def.Key, Value: value})
		case "int_list":
			parsed, err := parseConfigIntList(values.Get(def.Key))
			if err != nil {
				return nil, fmt.Errorf("%s must be a non-empty list of unique positive integers", def.Label)
			}
			result = append(result, configUpdateItem{Key: def.Key, Value: parsed})
		}
	}
	return result, nil
}

func parseConfigIntList(value string) ([]int, error) {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t'
	})
	if len(fields) == 0 {
		return nil, errors.New("empty integer list")
	}
	result := make([]int, 0, len(fields))
	seen := make(map[int]struct{}, len(fields))
	for _, field := range fields {
		parsed, err := strconv.Atoi(strings.TrimSpace(field))
		if err != nil || parsed <= 0 {
			return nil, errors.New("invalid integer")
		}
		if _, ok := seen[parsed]; ok {
			return nil, errors.New("duplicate integer")
		}
		seen[parsed] = struct{}{}
		result = append(result, parsed)
	}
	return result, nil
}

func configDefinitions() []configView {
	return []configView{
		{
			Key:         "sharing.thumbnail_sizes",
			Label:       "Thumbnail sizes",
			Description: "Allowed thumbnail widths in pixels.",
			Kind:        "int_list",
		},
		{
			Key:         "sharing.thumbnail_default_size",
			Label:       "Default thumbnail size",
			Description: "Default thumbnail width; it must be one of the configured thumbnail sizes.",
			Kind:        "int",
		},
		{
			Key:         "sharing.thumbnails_generate_for_video",
			Label:       "Generate video thumbnails",
			Description: "Generate thumbnails for video shares.",
			Kind:        "bool",
		},
		{
			Key:         "sharing.video_inline_max_size_mb",
			Label:       "Inline video limit",
			Description: "Maximum inline video size in megabytes.",
			Kind:        "int",
		},
		{
			Key:         "sharing.video_playback_enabled",
			Label:       "Video playback",
			Description: "Enable video playback in sharing views.",
			Kind:        "bool",
		},
	}
}

func shareViews(shares []share, inventories []inventory) []shareView {
	replicas := make(map[uint]struct {
		inventoryName string
		nodeID        string
	})
	for _, inv := range inventories {
		for _, rep := range inv.Replicas {
			replicas[rep.ID] = struct {
				inventoryName string
				nodeID        string
			}{inventoryName: inv.Name, nodeID: rep.NodeID}
		}
	}

	result := make([]shareView, 0, len(shares))
	for _, item := range shares {
		view := shareView{
			ID:           item.ID,
			InventoryID:  item.InventoryID,
			ReplicaID:    item.ReplicaID,
			Name:         item.Name,
			Status:       item.Status,
			HasAnonymous: len(item.AnonymousPermissions) > 0,
		}
		if item.LinkHash != nil {
			view.LinkHash = *item.LinkHash
		}
		if rep, ok := replicas[item.ReplicaID]; ok {
			view.InventoryName = rep.inventoryName
			view.NodeID = rep.nodeID
		}
		result = append(result, view)
	}
	return result
}

func shareFilterNodes(shares []shareView) []string {
	seen := make(map[string]struct{})
	for _, item := range shares {
		nodeID := strings.TrimSpace(item.NodeID)
		if nodeID == "" {
			continue
		}
		seen[nodeID] = struct{}{}
	}

	result := make([]string, 0, len(seen))
	for nodeID := range seen {
		result = append(result, nodeID)
	}
	sort.Strings(result)
	return result
}

type shareInventoryFilterOption struct {
	ID   uint
	Name string
}

func shareFilterInventories(shares []shareView) []shareInventoryFilterOption {
	seen := make(map[uint]string)
	for _, item := range shares {
		if item.InventoryID == 0 {
			continue
		}
		if _, ok := seen[item.InventoryID]; !ok {
			seen[item.InventoryID] = item.InventoryName
		}
	}

	result := make([]shareInventoryFilterOption, 0, len(seen))
	for inventoryID, name := range seen {
		result = append(result, shareInventoryFilterOption{ID: inventoryID, Name: name})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].ID < result[j].ID
	})
	return result
}

func withInventoryShareCounts(inventories []inventory, shares []share) []inventory {
	replicaInventoryIDs := make(map[uint]uint)
	for _, inv := range inventories {
		for _, rep := range inv.Replicas {
			replicaInventoryIDs[rep.ID] = inv.ID
		}
	}

	shareCounts := make(map[uint]int)
	for _, item := range shares {
		inventoryID, ok := replicaInventoryIDs[item.ReplicaID]
		if !ok {
			continue
		}
		shareCounts[inventoryID]++
	}

	result := make([]inventory, len(inventories))
	copy(result, inventories)
	for i := range result {
		result[i].ShareCount = shareCounts[result[i].ID]
	}
	return result
}

func withActiveInventoryReplicas(inventories []inventory) []inventory {
	result := make([]inventory, len(inventories))
	for i, inv := range inventories {
		result[i] = inv
		result[i].Replicas = nil
		for _, rep := range inv.Replicas {
			if rep.Status == "active" {
				result[i].Replicas = append(result[i].Replicas, rep)
			}
		}
	}
	return result
}

func shareSelectableInventories(inventories []inventory, nodes []node) []inventory {
	selectableNodes := make(map[string]struct{}, len(nodes))
	for _, item := range nodes {
		if item.Status == "disabled" {
			continue
		}
		selectableNodes[item.ID] = struct{}{}
	}

	result := make([]inventory, 0, len(inventories))
	for _, inv := range inventories {
		filtered := inv
		filtered.Replicas = nil
		for _, rep := range inv.Replicas {
			if rep.Status != "active" {
				continue
			}
			if _, ok := selectableNodes[rep.NodeID]; !ok {
				continue
			}
			filtered.Replicas = append(filtered.Replicas, rep)
		}
		if len(filtered.Replicas) == 0 {
			continue
		}
		result = append(result, filtered)
	}
	return result
}

func shareNodeIDs(inventories []inventory) []string {
	seen := make(map[string]struct{})
	for _, inv := range inventories {
		for _, rep := range inv.Replicas {
			nodeID := strings.TrimSpace(rep.NodeID)
			if nodeID == "" {
				continue
			}
			seen[nodeID] = struct{}{}
		}
	}

	result := make([]string, 0, len(seen))
	for nodeID := range seen {
		result = append(result, nodeID)
	}
	sort.Strings(result)
	return result
}

func positiveInt(value string, fallback int) int {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return fallback
	}
	return parsed
}

func filePageSize(value string) int {
	size := positiveInt(value, 20)
	switch size {
	case 10, 20, 50, 100:
		return size
	default:
		return 20
	}
}

func pageCount(total int64, count int) int {
	if total == 0 || count < 1 {
		return 1
	}
	return int((total + int64(count) - 1) / int64(count))
}

func newFilePage(list inventoryFileList) filePage {
	totalPages := pageCount(list.Total, list.Count)
	return filePage{
		Items:      list.Items,
		Page:       list.Page,
		Count:      list.Count,
		Displayed:  len(list.Items),
		Total:      list.Total,
		TotalPages: totalPages,
		PrevPage:   list.Page - 1,
		NextPage:   list.Page + 1,
		HasPrev:    list.Page > 1,
		HasNext:    list.Page < totalPages,
	}
}

func isUpstream(id uint, upstream *uint) bool {
	return upstream != nil && id == *upstream
}

func formatBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for value := size / unit; value >= unit && exp < 4; value /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(size)/float64(div), "KMGTPE"[exp])
}

func formatDate(value time.Time) string {
	if value.IsZero() {
		return "—"
	}
	return value.Local().Format("2006-01-02 15:04:05")
}

func statusClass(status string) string {
	switch status {
	case "online", "active", "synchronized":
		return "ok"
	case "unreachable", "offline", "pending":
		return "warn"
	case "disabled", "revoked", "deleted", "error", "conflict":
		return "danger"
	default:
		return "neutral"
	}
}

func syncStatusClass(status string) string {
	switch status {
	case "synchronized":
		return "ok"
	case "changed", "pending":
		return "warn"
	case "error", "conflict":
		return "danger"
	default:
		return "neutral"
	}
}

func formatTime(value *string) string {
	if value == nil || *value == "" {
		return "never"
	}
	parsed, err := time.Parse(time.RFC3339, *value)
	if err != nil {
		return *value
	}
	return parsed.Local().Format("2006-01-02 15:04:05")
}
