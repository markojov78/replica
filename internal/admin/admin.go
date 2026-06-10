package admin

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
	"strconv"
	"strings"
	"time"
)

var errUnauthorized = errors.New("unauthorized")

//go:embed templates/*.html static/*
var assets embed.FS

type Handler struct {
	api   http.Handler
	pages *template.Template
}

type authContext struct {
	AccessToken string
}

type node struct {
	ID       string   `json:"id"`
	Status   string   `json:"status"`
	Address  string   `json:"address"`
	Interval *float64 `json:"interval"`
	LastSeen *string  `json:"last_seen"`
}

type nodeList struct {
	Items []node `json:"items"`
	Total int64  `json:"total"`
}

type replica struct {
	ID                uint   `json:"id"`
	InventoryID       uint   `json:"inventory_id"`
	NodeID            string `json:"node_id"`
	URI               string `json:"uri"`
	Status            string `json:"status"`
	Type              string `json:"type"`
	UpstreamReplicaID *uint  `json:"upstream_replica_id"`
}

type inventory struct {
	ID       uint      `json:"id"`
	Name     string    `json:"name"`
	Status   string    `json:"status"`
	Type     string    `json:"type"`
	Replicas []replica `json:"replicas"`
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
	Files               filePage
	Replica             replica
	Users               []user
	User                user
	Roles               []role
	Role                role
	PermissionResources []string
	IsEdit              bool
}

func Register(mux *http.ServeMux, api http.Handler) error {
	pages, err := template.New("admin").Funcs(template.FuncMap{
		"statusClass":   statusClass,
		"formatTime":    formatTime,
		"pathEscape":    url.PathEscape,
		"isUpstream":    isUpstream,
		"formatBytes":   formatBytes,
		"formatDate":    formatDate,
		"hasRole":       hasRole,
		"hasPermission": hasPermission,
	}).ParseFS(assets, "templates/*.html")
	if err != nil {
		return err
	}

	handler := &Handler{
		api:   api,
		pages: pages,
	}

	mux.Handle("GET /admin/static/", http.StripPrefix("/admin/static/", http.FileServer(http.FS(mustSub(assets, "static")))))
	mux.HandleFunc("GET /admin/login", handler.loginPage)
	mux.HandleFunc("GET /admin", handler.protected(handler.dashboard))
	mux.HandleFunc("GET /admin/nodes", handler.protected(handler.nodesPage))
	mux.HandleFunc("GET /admin/nodes/new", handler.protected(handler.newNodePage))
	mux.HandleFunc("POST /admin/nodes", handler.protected(handler.createNode))
	mux.HandleFunc("GET /admin/nodes/{id}/edit", handler.protected(handler.editNodePage))
	mux.HandleFunc("POST /admin/nodes/{id}", handler.protected(handler.updateNode))
	mux.HandleFunc("POST /admin/nodes/{id}/revoke", handler.protected(handler.revokeNode))
	mux.HandleFunc("GET /admin/inventories", handler.protected(handler.inventoriesPage))
	mux.HandleFunc("GET /admin/inventories/new", handler.protected(handler.newInventoryPage))
	mux.HandleFunc("POST /admin/inventories", handler.protected(handler.createInventory))
	mux.HandleFunc("GET /admin/inventories/{id}", handler.protected(handler.inventoryPage))
	mux.HandleFunc("GET /admin/inventories/{id}/edit", handler.protected(handler.editInventoryPage))
	mux.HandleFunc("POST /admin/inventories/{id}", handler.protected(handler.updateInventory))
	mux.HandleFunc("POST /admin/inventories/{id}/delete", handler.protected(handler.deleteInventory))
	mux.HandleFunc("GET /admin/inventories/{id}/replicas/new", handler.protected(handler.newReplicaPage))
	mux.HandleFunc("POST /admin/inventories/{id}/replicas", handler.protected(handler.createReplica))
	mux.HandleFunc("GET /admin/inventories/{id}/replicas/{replica_id}/edit", handler.protected(handler.editReplicaPage))
	mux.HandleFunc("POST /admin/inventories/{id}/replicas/{replica_id}", handler.protected(handler.updateReplica))
	mux.HandleFunc("POST /admin/inventories/{id}/replicas/{replica_id}/delete", handler.protected(handler.deleteReplica))
	mux.HandleFunc("GET /admin/users", handler.protected(handler.usersPage))
	mux.HandleFunc("GET /admin/users/new", handler.protected(handler.newUserPage))
	mux.HandleFunc("POST /admin/users", handler.protected(handler.createUser))
	mux.HandleFunc("GET /admin/users/{id}/edit", handler.protected(handler.editUserPage))
	mux.HandleFunc("POST /admin/users/{id}", handler.protected(handler.updateUser))
	mux.HandleFunc("GET /admin/roles", handler.protected(handler.rolesPage))
	mux.HandleFunc("GET /admin/roles/new", handler.protected(handler.newRolePage))
	mux.HandleFunc("POST /admin/roles", handler.protected(handler.createRole))
	mux.HandleFunc("GET /admin/roles/{id}/edit", handler.protected(handler.editRolePage))
	mux.HandleFunc("POST /admin/roles/{id}", handler.protected(handler.updateRole))
	return nil
}

func mustSub(embedded embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(embedded, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

func (h *Handler) loginPage(w http.ResponseWriter, r *http.Request) {
	h.render(w, "login", pageData{Title: "Sign in"})
}

func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request, _ authContext) {
	http.Redirect(w, r, "/admin/inventories", http.StatusSeeOther)
}

func (h *Handler) nodesPage(w http.ResponseWriter, r *http.Request, sess authContext) {
	var list nodeList
	if !h.load(w, r, sess, "/api/nodes?count=100", &list) {
		return
	}
	h.render(w, "nodes", pageData{
		Title: "Nodes", Subtitle: "Create, disable, revoke, and inspect storage service nodes.",
		Active: "nodes", Nodes: list.Items,
	})
}

func (h *Handler) newNodePage(w http.ResponseWriter, _ *http.Request, _ authContext) {
	h.render(w, "node_form", pageData{
		Title: "Add node", Subtitle: "Register a storage service node.", Active: "nodes",
	})
}

func (h *Handler) createNode(w http.ResponseWriter, r *http.Request, sess authContext) {
	if err := r.ParseForm(); err != nil {
		h.nodeFormError(w, false, node{}, "Invalid form submission.")
		return
	}
	input := map[string]any{
		"id":      strings.TrimSpace(r.FormValue("id")),
		"secret":  r.FormValue("secret"),
		"address": strings.TrimSpace(r.FormValue("address")),
		"status":  r.FormValue("status"),
	}
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodPost, "/api/nodes", input, nil); err != nil {
		if errors.Is(err, errUnauthorized) {
			h.renderError(w, r, sess, err)
			return
		}
		h.nodeFormError(w, false, node{ID: r.FormValue("id"), Address: r.FormValue("address"), Status: r.FormValue("status")}, apiMessage(err))
		return
	}
	http.Redirect(w, r, "/admin/nodes", http.StatusSeeOther)
}

func (h *Handler) editNodePage(w http.ResponseWriter, r *http.Request, sess authContext) {
	var item node
	if !h.load(w, r, sess, "/api/nodes/"+url.PathEscape(r.PathValue("id")), &item) {
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
		"address": strings.TrimSpace(r.FormValue("address")),
	}
	if status := r.FormValue("status"); status != "" {
		input["status"] = status
	}
	if secret := r.FormValue("secret"); secret != "" {
		input["secret"] = secret
	}
	id := r.PathValue("id")
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodPatch, "/api/nodes/"+url.PathEscape(id), input, nil); err != nil {
		if errors.Is(err, errUnauthorized) {
			h.renderError(w, r, sess, err)
			return
		}
		h.nodeFormError(w, true, node{ID: id, Address: r.FormValue("address"), Status: r.FormValue("status")}, apiMessage(err))
		return
	}
	http.Redirect(w, r, "/admin/nodes", http.StatusSeeOther)
}

func (h *Handler) revokeNode(w http.ResponseWriter, r *http.Request, sess authContext) {
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodDelete, "/api/nodes/"+url.PathEscape(r.PathValue("id")), nil, nil); err != nil {
		h.renderError(w, r, sess, err)
		return
	}
	http.Redirect(w, r, "/admin/nodes", http.StatusSeeOther)
}

func (h *Handler) inventoriesPage(w http.ResponseWriter, r *http.Request, sess authContext) {
	var list inventoryList
	if !h.load(w, r, sess, "/api/inventories?count=100", &list) {
		return
	}
	h.render(w, "inventories", pageData{
		Title: "Inventories", Subtitle: "Logical datasets with replicas managed in inventory context.",
		Active: "inventories", Inventories: list.Items,
	})
}

func (h *Handler) newInventoryPage(w http.ResponseWriter, r *http.Request, sess authContext) {
	nodes, ok := h.loadNodes(w, r, sess)
	if !ok {
		return
	}
	h.render(w, "inventory_form", pageData{
		Title: "New inventory", Subtitle: "Create an inventory and its first replica.", Active: "inventories", Nodes: nodes,
	})
}

func (h *Handler) createInventory(w http.ResponseWriter, r *http.Request, sess authContext) {
	if err := r.ParseForm(); err != nil {
		h.inventoryFormError(w, r, sess, false, inventory{}, "Invalid form submission.")
		return
	}
	input := map[string]any{
		"name":    strings.TrimSpace(r.FormValue("name")),
		"type":    r.FormValue("type"),
		"node_id": r.FormValue("node_id"),
		"uri":     strings.TrimSpace(r.FormValue("uri")),
	}
	var created inventory
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodPost, "/api/inventories", input, &created); err != nil {
		if errors.Is(err, errUnauthorized) {
			h.renderError(w, r, sess, err)
			return
		}
		h.inventoryFormError(w, r, sess, false, inventory{Name: r.FormValue("name"), Type: r.FormValue("type")}, apiMessage(err))
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/inventories/%d", created.ID), http.StatusSeeOther)
}

func (h *Handler) inventoryPage(w http.ResponseWriter, r *http.Request, sess authContext) {
	h.renderInventoryPage(w, r, sess, "")
}

func (h *Handler) renderInventoryPage(w http.ResponseWriter, r *http.Request, sess authContext, message string) {
	var item inventory
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodGet, "/api/inventories/"+r.PathValue("id"), nil, &item); err != nil {
		h.renderInventoryPageLoadError(w, r, sess, err, message)
		return
	}
	page := positiveInt(r.URL.Query().Get("page"), 1)
	count := filePageSize(r.URL.Query().Get("count"))
	var files inventoryFileList
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodGet, fmt.Sprintf("/api/inventories/%s/files?page=%d&count=%d", r.PathValue("id"), page, count), nil, &files); err != nil {
		h.renderInventoryPageLoadError(w, r, sess, err, message)
		return
	}
	totalPages := pageCount(files.Total, files.Count)
	if files.Total > 0 && files.Page > totalPages {
		if err := h.apiAuthJSON(r.Context(), &sess, http.MethodGet, fmt.Sprintf("/api/inventories/%s/files?page=%d&count=%d", r.PathValue("id"), totalPages, count), nil, &files); err != nil {
			h.renderInventoryPageLoadError(w, r, sess, err, message)
			return
		}
	}
	h.render(w, "inventory", pageData{
		Title: item.Name, Subtitle: fmt.Sprintf("Inventory #%d · %s · %s", item.ID, item.Type, item.Status),
		Active: "inventories", Error: message, Inventory: item, Files: newFilePage(files),
	})
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
	if !h.load(w, r, sess, "/api/inventories/"+r.PathValue("id"), &item) {
		return
	}
	h.render(w, "inventory_form", pageData{
		Title: "Edit inventory", Subtitle: fmt.Sprintf("Update inventory #%d.", item.ID),
		Active: "inventories", Inventory: item, IsEdit: true,
	})
}

func (h *Handler) updateInventory(w http.ResponseWriter, r *http.Request, sess authContext) {
	if err := r.ParseForm(); err != nil {
		h.inventoryFormError(w, r, sess, true, inventory{}, "Invalid form submission.")
		return
	}
	id, _ := strconv.ParseUint(r.PathValue("id"), 10, 64)
	item := inventory{ID: uint(id), Name: r.FormValue("name"), Status: r.FormValue("status")}
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodPatch, "/api/inventories/"+r.PathValue("id"), map[string]any{
		"name": item.Name, "status": item.Status,
	}, nil); err != nil {
		if errors.Is(err, errUnauthorized) {
			h.renderError(w, r, sess, err)
			return
		}
		h.inventoryFormError(w, r, sess, true, item, apiMessage(err))
		return
	}
	http.Redirect(w, r, "/admin/inventories/"+r.PathValue("id"), http.StatusSeeOther)
}

func (h *Handler) deleteInventory(w http.ResponseWriter, r *http.Request, sess authContext) {
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodDelete, "/api/inventories/"+r.PathValue("id"), nil, nil); err != nil {
		if errors.Is(err, errUnauthorized) {
			h.renderError(w, r, sess, err)
			return
		}
		h.renderInventoryPage(w, r, sess, apiMessage(err))
		return
	}
	http.Redirect(w, r, "/admin/inventories", http.StatusSeeOther)
}

func (h *Handler) newReplicaPage(w http.ResponseWriter, r *http.Request, sess authContext) {
	inv, nodes, ok := h.loadReplicaFormData(w, r, sess)
	if !ok {
		return
	}
	h.render(w, "replica_form", pageData{
		Title: "Add replica", Subtitle: "Add a physical location to " + inv.Name + ".",
		Active: "inventories", Inventory: inv, Nodes: nodes,
	})
}

func (h *Handler) createReplica(w http.ResponseWriter, r *http.Request, sess authContext) {
	if err := r.ParseForm(); err != nil {
		h.replicaFormError(w, r, sess, false, replica{}, "Invalid form submission.")
		return
	}
	inventoryID, _ := strconv.ParseUint(r.PathValue("id"), 10, 64)
	input := map[string]any{
		"inventory_id": uint(inventoryID),
		"node_id":      r.FormValue("node_id"),
		"uri":          strings.TrimSpace(r.FormValue("uri")),
		"type":         r.FormValue("type"),
	}
	if upstream := optionalUint(r.FormValue("upstream_replica_id")); upstream != nil {
		input["upstream_replica_id"] = *upstream
	}
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodPost, "/api/replicas", input, nil); err != nil {
		if errors.Is(err, errUnauthorized) {
			h.renderError(w, r, sess, err)
			return
		}
		h.replicaFormError(w, r, sess, false, replica{
			InventoryID: uint(inventoryID), NodeID: r.FormValue("node_id"), URI: r.FormValue("uri"),
			Type: r.FormValue("type"), UpstreamReplicaID: optionalUint(r.FormValue("upstream_replica_id")),
		}, apiMessage(err))
		return
	}
	http.Redirect(w, r, "/admin/inventories/"+r.PathValue("id"), http.StatusSeeOther)
}

func (h *Handler) editReplicaPage(w http.ResponseWriter, r *http.Request, sess authContext) {
	inv, nodes, ok := h.loadReplicaFormData(w, r, sess)
	if !ok {
		return
	}
	var item replica
	if !h.load(w, r, sess, "/api/replicas/"+r.PathValue("replica_id"), &item) {
		return
	}
	if item.InventoryID != inv.ID {
		http.NotFound(w, r)
		return
	}
	h.render(w, "replica_form", pageData{
		Title: "Edit replica", Subtitle: fmt.Sprintf("Update replica #%d in %s.", item.ID, inv.Name),
		Active: "inventories", Inventory: inv, Nodes: nodes, Replica: item, IsEdit: true,
	})
}

func (h *Handler) updateReplica(w http.ResponseWriter, r *http.Request, sess authContext) {
	if err := r.ParseForm(); err != nil {
		h.replicaFormError(w, r, sess, true, replica{}, "Invalid form submission.")
		return
	}
	replicaID, _ := strconv.ParseUint(r.PathValue("replica_id"), 10, 64)
	item := replica{
		ID: uint(replicaID), Type: r.FormValue("type"), Status: r.FormValue("status"),
		UpstreamReplicaID: optionalUint(r.FormValue("upstream_replica_id")),
	}
	input := map[string]any{
		"type":                item.Type,
		"status":              item.Status,
		"upstream_replica_id": item.UpstreamReplicaID,
	}
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodPatch, "/api/replicas/"+r.PathValue("replica_id"), input, nil); err != nil {
		if errors.Is(err, errUnauthorized) {
			h.renderError(w, r, sess, err)
			return
		}
		h.replicaFormError(w, r, sess, true, item, apiMessage(err))
		return
	}
	http.Redirect(w, r, "/admin/inventories/"+r.PathValue("id"), http.StatusSeeOther)
}

func (h *Handler) deleteReplica(w http.ResponseWriter, r *http.Request, sess authContext) {
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodDelete, "/api/replicas/"+r.PathValue("replica_id"), nil, nil); err != nil {
		h.renderError(w, r, sess, err)
		return
	}
	http.Redirect(w, r, "/admin/inventories/"+r.PathValue("id"), http.StatusSeeOther)
}

func (h *Handler) usersPage(w http.ResponseWriter, r *http.Request, sess authContext) {
	var list userList
	if !h.load(w, r, sess, "/api/users?count=100", &list) {
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
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodPost, "/api/users", input, nil); err != nil {
		if errors.Is(err, errUnauthorized) {
			h.renderError(w, r, sess, err)
			return
		}
		h.userFormError(w, r, sess, false, user{Name: r.FormValue("name")}, apiMessage(err))
		return
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (h *Handler) editUserPage(w http.ResponseWriter, r *http.Request, sess authContext) {
	var item user
	if !h.load(w, r, sess, "/api/users/"+r.PathValue("id"), &item) {
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
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodPatch, "/api/users/"+r.PathValue("id"), input, nil); err != nil {
		if errors.Is(err, errUnauthorized) {
			h.renderError(w, r, sess, err)
			return
		}
		h.userFormError(w, r, sess, true, item, apiMessage(err))
		return
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (h *Handler) rolesPage(w http.ResponseWriter, r *http.Request, sess authContext) {
	var list roleList
	if !h.load(w, r, sess, "/api/roles?count=100", &list) {
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
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodPost, "/api/roles", input, nil); err != nil {
		if errors.Is(err, errUnauthorized) {
			h.renderError(w, r, sess, err)
			return
		}
		h.roleFormError(w, false, item, permissions, apiMessage(err))
		return
	}
	http.Redirect(w, r, "/admin/roles", http.StatusSeeOther)
}

func (h *Handler) editRolePage(w http.ResponseWriter, r *http.Request, sess authContext) {
	var item role
	if !h.load(w, r, sess, "/api/roles/"+r.PathValue("id"), &item) {
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
	if err := h.apiAuthJSON(r.Context(), &sess, http.MethodPatch, "/api/roles/"+r.PathValue("id"), input, nil); err != nil {
		if errors.Is(err, errUnauthorized) {
			h.renderError(w, r, sess, err)
			return
		}
		h.roleFormError(w, true, item, permissions, apiMessage(err))
		return
	}
	http.Redirect(w, r, "/admin/roles", http.StatusSeeOther)
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
	if !h.load(w, r, sess, "/api/nodes?count=100", &list) {
		return nil, false
	}
	return list.Items, true
}

func (h *Handler) loadReplicaFormData(w http.ResponseWriter, r *http.Request, sess authContext) (inventory, []node, bool) {
	var inv inventory
	if !h.load(w, r, sess, "/api/inventories/"+r.PathValue("id"), &inv) {
		return inventory{}, nil, false
	}
	nodes, ok := h.loadNodes(w, r, sess)
	return inv, nodes, ok
}

func (h *Handler) loadRoles(w http.ResponseWriter, r *http.Request, sess authContext) ([]role, bool) {
	var list roleList
	if !h.load(w, r, sess, "/api/roles?count=100", &list) {
		return nil, false
	}
	return list.Items, true
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
	h.render(w, "node_form", pageData{Title: title, Active: "nodes", Node: item, IsEdit: edit, Error: message})
}

func (h *Handler) inventoryFormError(w http.ResponseWriter, r *http.Request, sess authContext, edit bool, item inventory, message string) {
	nodes := []node{}
	if !edit {
		var list nodeList
		err := h.apiAuthJSON(r.Context(), &sess, http.MethodGet, "/api/nodes?count=100", nil, &list)
		if errors.Is(err, errUnauthorized) {
			h.renderError(w, r, sess, err)
			return
		}
		if err == nil {
			nodes = list.Items
		}
	}
	title := "New inventory"
	if edit {
		title = "Edit inventory"
	}
	h.render(w, "inventory_form", pageData{Title: title, Active: "inventories", Inventory: item, Nodes: nodes, IsEdit: edit, Error: message})
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
		Title: title, Active: "inventories", Inventory: inv, Nodes: nodes, Replica: item, IsEdit: edit, Error: message,
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

func rolePermissionResources() []string {
	return []string{"users", "shares", "inventories", "nodes"}
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
