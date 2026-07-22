package share

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"replica/internal/apiclient"
	"replica/internal/config"
	"replica/internal/service"
	"replica/internal/storage"
	"replica/internal/ui/uiauth"
)

//go:embed templates/*.html static/*
var assets embed.FS

type Handler struct {
	runtime   *storage.Runtime
	auth      *service.AuthService
	pages     *template.Template
	cookies   uiauth.Cookies
	refreshes *uiauth.RefreshGroup
}

type authContext struct {
	UserID   uint
	Username string
	Status   string
}

type pageData struct {
	Title          string
	Subtitle       string
	Authenticated  bool
	Public         bool
	LoginPath      string
	Share          apiclient.Share
	Shares         []storage.ShareWithPermissions
	Files          []fileView
	Permissions    []string
	Page           int
	Count          int
	Total          int64
	BasePath       string
	APIBasePath    string
	ThumbnailSizes []int
	ThumbnailSize  int
	ViewMode       string
	BrowseMode     string
	Sort           string
	Order          string
	TreePath       string
	ParentFolder   *treeFolderView
	Folders        []treeFolderView
	TreePanel      *treePanelView
	HasEntries     bool
	ShowPagination bool
	Error          string
	Message        string
}

type fileView struct {
	apiclient.ReplicaInventoryFile
	Name         string
	Type         string
	ContentPath  string
	DownloadPath string
	ThumbnailURL string
	CanPreview   bool
	PreviewKind  string
}

const (
	shareUICSRFCookie = "replica_share_csrf"
	browseModeFlat    = "flat"
	browseModeTree    = "tree"
	// Tree mode intentionally uses one existing file-list request and refuses larger shares.
	treeBrowseFileLimit = 1000
)

func Register(mux *http.ServeMux, runtime *storage.Runtime, authServices ...*service.AuthService) error {
	pages, err := template.New("shareui").Funcs(templateFuncs()).ParseFS(assets, "templates/*.html")
	if err != nil {
		return err
	}
	var auth *service.AuthService
	if len(authServices) > 0 {
		auth = authServices[0]
	}
	handler := &Handler{runtime: runtime, auth: auth, pages: pages, cookies: uiauth.SharedCookies(shareUICSRFCookie, "/share"), refreshes: uiauth.SharedUserRefreshes()}
	gate := handler.sharingGate
	gateFunc := func(next http.HandlerFunc) http.HandlerFunc {
		return gate(next).ServeHTTP
	}

	mux.Handle("GET /share/static/", gate(http.StripPrefix("/share/static/", http.FileServer(http.FS(mustSub(assets, "static"))))))
	mux.HandleFunc("GET /share", gateFunc(handler.loginPage))
	mux.HandleFunc("POST /share/auth/login", gateFunc(handler.login))
	mux.HandleFunc("GET /share/auth/me", gateFunc(handler.protected(handler.me)))
	mux.HandleFunc("POST /share/logout", gateFunc(handler.logout))
	mux.HandleFunc("GET /share/api/auth/me", gateFunc(handler.shareAPIMe))
	mux.HandleFunc("GET /share/api/shares", gateFunc(handler.shareAPI))
	mux.HandleFunc("GET /share/api/shares/{id}", gateFunc(handler.shareAPI))
	mux.HandleFunc("GET /share/api/shares/{id}/files", gateFunc(handler.shareAPI))
	mux.HandleFunc("POST /share/api/shares/{id}/files", gateFunc(handler.shareAPI))
	mux.HandleFunc("DELETE /share/api/shares/{id}/files/{file_id}", gateFunc(handler.shareAPI))
	mux.HandleFunc("GET /share/api/shares/{id}/files/{file_id}/content", gateFunc(handler.shareAPI))
	mux.HandleFunc("HEAD /share/api/shares/{id}/files/{file_id}/content", gateFunc(handler.shareAPI))
	mux.HandleFunc("GET /share/api/shares/{id}/files/{file_id}/thumbnail", gateFunc(handler.shareAPI))
	mux.HandleFunc("HEAD /share/api/shares/{id}/files/{file_id}/thumbnail", gateFunc(handler.shareAPI))
	mux.HandleFunc("PUT /share/api/shares/{id}/files/{file_id}/content", gateFunc(handler.shareAPI))
	mux.HandleFunc("GET /share/shares", gateFunc(handler.protected(handler.shareListPage)))
	mux.HandleFunc("GET /share/shares/{id}", gateFunc(handler.protected(handler.shareFilesPage)))
	mux.HandleFunc("POST /share/shares/{id}/files", gateFunc(handler.protected(handler.uploadShareFile)))
	mux.HandleFunc("GET /share/shares/{id}/files/{file_id}/content", gateFunc(handler.protected(handler.shareFileContent)))
	mux.HandleFunc("POST /share/shares/{id}/files/{file_id}/replace", gateFunc(handler.protected(handler.replaceShareFile)))
	mux.HandleFunc("POST /share/shares/{id}/files/{file_id}/delete", gateFunc(handler.protected(handler.deleteShareFile)))
	mux.HandleFunc("GET /w/{link_hash}", gateFunc(handler.publicSharePage))
	mux.HandleFunc("GET /w/{link_hash}/files/{file_id}/content", gateFunc(handler.publicShareFileContent))
	mux.HandleFunc("POST /w/{link_hash}/files", gateFunc(handler.uploadPublicFile))
	mux.HandleFunc("POST /w/{link_hash}/files/{file_id}/replace", gateFunc(handler.replacePublicFile))
	mux.HandleFunc("POST /w/{link_hash}/files/{file_id}/delete", gateFunc(handler.deletePublicFile))
	return nil
}

func (h *Handler) sharingGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.runtime != nil && !h.runtime.SharingEnabled() {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"formatBytes":     formatBytes,
		"formatTime":      formatTime,
		"hasPermission":   storage.PermissionAllowed,
		"joinPermissions": strings.Join,
		"pageCount":       pageCount,
		"pageStart":       pageStart,
		"pageEnd":         pageEnd,
		"add":             func(a, b int) int { return a + b },
		"sub":             func(a, b int) int { return a - b },
		"pathEscape":      url.PathEscape,
		"pageURL":         pageURL,
		"viewURL":         viewURL,
		"browseURL":       browseURL,
		"orderURL":        orderURL,
		"thumbStyle":      thumbStyle,
		"dict":            templateDict,
	}
}

func templateDict(values ...any) map[string]any {
	result := make(map[string]any, len(values)/2)
	for i := 0; i+1 < len(values); i += 2 {
		key, ok := values[i].(string)
		if !ok {
			continue
		}
		result[key] = values[i+1]
	}
	return result
}

func mustSub(embedded embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(embedded, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

func (h *Handler) loginPage(w http.ResponseWriter, r *http.Request) {
	if _, err := h.cookies.EnsureCSRF(w, r); err != nil {
		http.Error(w, "failed to initialize session", http.StatusInternalServerError)
		return
	}
	h.render(w, http.StatusOK, "login", pageData{Title: "Sign in", LoginPath: "/share"})
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if err := uiauth.ValidateCSRF(r, h.cookies); err != nil {
		writeShareUIError(w, http.StatusForbidden, err.Error())
		return
	}
	username, password, ok := loginCredentials(w, r)
	if !ok {
		return
	}
	pair, status, err := h.loginShareUser(r, username, password)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	h.setAuthCookies(w, r, pair)
	if _, err := h.cookies.RotateCSRF(w, r); err != nil {
		writeShareUIError(w, http.StatusInternalServerError, "failed to initialize session")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) loginShareUser(r *http.Request, username string, password string) (storage.ShareTokenPair, int, error) {
	if h.auth != nil {
		pair, err := h.auth.Login(username, password)
		if err != nil {
			return storage.ShareTokenPair{}, localAuthStatus(err), err
		}
		return shareTokenPairFromService(pair), http.StatusOK, nil
	}
	return h.runtime.LoginShareUser(r.Context(), username, password)
}

func (h *Handler) refreshShareUser(r *http.Request, refreshToken string) (storage.ShareTokenPair, int, error) {
	if h.auth != nil {
		pair, err := h.auth.Refresh(refreshToken)
		if err != nil {
			return storage.ShareTokenPair{}, localAuthStatus(err), err
		}
		return shareTokenPairFromService(pair), http.StatusOK, nil
	}
	return h.runtime.RefreshShareUser(r.Context(), refreshToken)
}

func shareTokenPairFromService(pair *service.TokenPair) storage.ShareTokenPair {
	return storage.ShareTokenPair{
		UserID:                pair.UserID,
		AccessToken:           pair.AccessToken,
		RefreshToken:          pair.RefreshToken,
		AccessTokenExpiresAt:  pair.AccessTokenExpiresAt,
		RefreshTokenExpiresAt: pair.RefreshTokenExpiresAt,
	}
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if err := uiauth.ValidateCSRF(r, h.cookies); err != nil {
		h.cookies.Clear(w, r)
		writeShareUIError(w, http.StatusForbidden, err.Error())
		return
	}
	if _, accessToken, err := h.authenticateCookieToken(w, r); err == nil {
		if h.auth != nil {
			_ = h.auth.Logout(accessToken)
		} else {
			_ = h.runtime.LogoutShareUser(r.Context(), accessToken)
		}
	}
	h.cookies.Clear(w, r)
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", "/share")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, "/share", http.StatusSeeOther)
}

func (h *Handler) authenticateCookie(w http.ResponseWriter, r *http.Request) (*apiclient.ValidatedUserToken, error) {
	user, _, err := h.authenticateCookieToken(w, r)
	return user, err
}

func (h *Handler) authenticateCookieToken(w http.ResponseWriter, r *http.Request) (*apiclient.ValidatedUserToken, string, error) {
	if accessToken := h.cookies.Access(r); accessToken != "" {
		user, err := h.validateAccessToken(r, accessToken)
		if err == nil {
			return user, accessToken, nil
		}
	}

	refreshToken := h.cookies.Refresh(r)
	if refreshToken == "" {
		return nil, "", errors.New("missing authenticated user")
	}
	refreshed, err := h.refreshes.Do(refreshToken, func() (uiauth.TokenPair, error) {
		pair, _, err := h.refreshShareUser(r, refreshToken)
		if err != nil {
			return uiauth.TokenPair{}, err
		}
		return shareUITokenPair(pair), nil
	})
	if err != nil {
		h.clearAuthCookies(w, r)
		return nil, "", err
	}
	h.cookies.SetAuth(w, r, refreshed)
	user, err := h.validateAccessToken(r, refreshed.AccessToken)
	return user, refreshed.AccessToken, err
}

func (h *Handler) validateAccessToken(r *http.Request, accessToken string) (*apiclient.ValidatedUserToken, error) {
	if h.auth != nil {
		user, err := h.auth.ValidateUserAccessToken(accessToken)
		if err != nil {
			return nil, err
		}
		return &apiclient.ValidatedUserToken{
			UserID:               user.UserID,
			Username:             user.Username,
			Status:               user.Status,
			AccessTokenExpiresAt: user.AccessExpires,
		}, nil
	}
	return h.runtime.AuthenticateShareUserAuthorization(r.Context(), "Bearer "+accessToken)
}

func shareUITokenPair(pair storage.ShareTokenPair) uiauth.TokenPair {
	return uiauth.TokenPair{AccessToken: pair.AccessToken, RefreshToken: pair.RefreshToken, AccessExpiresAt: pair.AccessTokenExpiresAt, RefreshExpiresAt: pair.RefreshTokenExpiresAt}
}

func (h *Handler) setAuthCookies(w http.ResponseWriter, r *http.Request, pair storage.ShareTokenPair) {
	h.cookies.SetAuth(w, r, shareUITokenPair(pair))
}

func (h *Handler) clearAuthCookies(w http.ResponseWriter, r *http.Request) {
	h.cookies.ClearAuth(w, r)
}

func (h *Handler) protected(next func(http.ResponseWriter, *http.Request, authContext)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := h.authenticateCookie(w, r)
		if err != nil {
			if isHTMX(r) {
				w.Header().Set("HX-Redirect", "/share")
			}
			h.render(w, h.shareAuthStatus(err), "login", pageData{Title: "Sign in", Error: apiMessage(err)})
			return
		}
		if err := uiauth.ValidateCSRF(r, h.cookies); err != nil {
			writeShareUIError(w, http.StatusForbidden, err.Error())
			return
		}
		_, _ = h.cookies.EnsureCSRF(w, r)
		next(w, r, authContext{UserID: user.UserID, Username: user.Username, Status: user.Status})
	}
}

func writeShareUIError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func (h *Handler) shareAuthStatus(err error) int {
	if strings.Contains(err.Error(), "missing authenticated user") || strings.Contains(err.Error(), "invalid token") || strings.Contains(err.Error(), "expired token") || strings.Contains(err.Error(), "revoked token") {
		return http.StatusUnauthorized
	}
	if h.auth != nil {
		return localAuthStatus(err)
	}
	return h.runtime.ShareAuthErrorStatus(err)
}

func (h *Handler) me(w http.ResponseWriter, _ *http.Request, auth authContext) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"user_id": auth.UserID, "username": auth.Username, "status": auth.Status})
}

func (h *Handler) shareAPIMe(w http.ResponseWriter, r *http.Request) {
	user, err := h.authenticateCookie(w, r)
	if err != nil {
		h.clearAuthCookies(w, r)
		writeShareUIError(w, h.shareAuthStatus(err), apiMessage(err))
		return
	}
	_, _ = h.cookies.EnsureCSRF(w, r)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"user_id": user.UserID, "username": user.Username, "status": user.Status})
}

func (h *Handler) shareAPI(w http.ResponseWriter, r *http.Request) {
	user, accessToken, err := h.authenticateCookieToken(w, r)
	if err != nil {
		h.clearAuthCookies(w, r)
		writeShareUIError(w, h.shareAuthStatus(err), apiMessage(err))
		return
	}
	_ = user
	if err := uiauth.ValidateCSRF(r, h.cookies); err != nil {
		writeShareUIError(w, http.StatusForbidden, err.Error())
		return
	}
	_, _ = h.cookies.EnsureCSRF(w, r)
	forward := r.Clone(r.Context())
	forward.URL.Path = strings.Replace(r.URL.Path, "/share/api/", "/api/share/", 1)
	forward.RequestURI = forward.URL.RequestURI()
	forward.Header = r.Header.Clone()
	forward.Header.Set("Authorization", "Bearer "+accessToken)
	forward.Header.Set("X-API-Version", "1")
	forward.Header.Del("Cookie")
	h.runtime.ServeAuthenticatedShares(w, forward)
}

func (h *Handler) shareListPage(w http.ResponseWriter, r *http.Request, auth authContext) {
	page, count := parsePagination(r)
	list, err := h.runtime.ListUserShares(auth.UserID, page, count, storage.ShareListFilter{})
	if err != nil {
		h.renderShareError(w, err)
		return
	}
	h.render(w, http.StatusOK, "share_list", pageData{
		Title: "My shares", Authenticated: true, Shares: list.Items, Page: list.Page, Count: list.Count, Total: list.Total, BasePath: "/share/shares",
	})
}

func (h *Handler) shareFilesPage(w http.ResponseWriter, r *http.Request, auth authContext) {
	shareID, ok := pathUint(w, r, "id")
	if !ok {
		return
	}
	browseMode := selectedBrowseMode(r)
	page, count := parsePagination(r)
	if browseMode == browseModeTree {
		page, count = 1, treeBrowseFileLimit
	}
	result, err := h.runtime.ListUserShareFiles(auth.UserID, shareID, page, count, selectedShareFileFilter(r))
	if err != nil {
		h.renderShareError(w, err)
		return
	}
	h.renderFilePage(w, r, false, result, "")
}

func (h *Handler) publicSharePage(w http.ResponseWriter, r *http.Request) {
	browseMode := selectedBrowseMode(r)
	page, count := parsePagination(r)
	if browseMode == browseModeTree {
		page, count = 1, treeBrowseFileLimit
	}
	linkHash := strings.TrimSpace(r.PathValue("link_hash"))
	result, err := h.runtime.ListPublicShareFiles(linkHash, page, count, selectedShareFileFilter(r))
	if err != nil {
		h.renderShareError(w, err)
		return
	}
	h.renderFilePage(w, r, true, result, "")
}

func (h *Handler) shareFileContent(w http.ResponseWriter, r *http.Request, auth authContext) {
	shareID, ok := pathUint(w, r, "id")
	if !ok {
		return
	}
	fileID, ok := pathUint(w, r, "file_id")
	if !ok {
		return
	}
	h.runtime.ServeUserShareFileContent(w, r, auth.UserID, shareID, fileID)
}

func (h *Handler) publicShareFileContent(w http.ResponseWriter, r *http.Request) {
	linkHash := strings.TrimSpace(r.PathValue("link_hash"))
	fileID, ok := pathUint(w, r, "file_id")
	if !ok {
		return
	}
	h.runtime.ServePublicShareFileContent(w, r, linkHash, fileID)
}

func (h *Handler) uploadShareFile(w http.ResponseWriter, r *http.Request, auth authContext) {
	shareID, ok := pathUint(w, r, "id")
	if !ok {
		return
	}
	relativeURI, file, size, ok := uploadedFile(w, r)
	if !ok {
		return
	}
	defer file.Close()
	err := h.runtime.CreateUserShareFile(r.Context(), auth.UserID, shareID, relativeURI, file, size)
	h.afterAuthenticatedMutation(w, r, auth, shareID, err, "File uploaded.")
}

func (h *Handler) replaceShareFile(w http.ResponseWriter, r *http.Request, auth authContext) {
	shareID, ok := pathUint(w, r, "id")
	if !ok {
		return
	}
	fileID, ok := pathUint(w, r, "file_id")
	if !ok {
		return
	}
	ifMatch, ok := formIfMatch(w, r)
	if !ok {
		return
	}
	_, file, size, ok := uploadedFile(w, r)
	if !ok {
		return
	}
	defer file.Close()
	err := h.runtime.ReplaceUserShareFileContent(r.Context(), auth.UserID, shareID, fileID, ifMatch, file, size)
	h.afterAuthenticatedMutation(w, r, auth, shareID, err, "File replacement accepted.")
}

func (h *Handler) deleteShareFile(w http.ResponseWriter, r *http.Request, auth authContext) {
	shareID, ok := pathUint(w, r, "id")
	if !ok {
		return
	}
	fileID, ok := pathUint(w, r, "file_id")
	if !ok {
		return
	}
	ifMatch, ok := formIfMatch(w, r)
	if !ok {
		return
	}
	err := h.runtime.DeleteUserShareFile(r.Context(), auth.UserID, shareID, fileID, ifMatch)
	h.afterAuthenticatedMutation(w, r, auth, shareID, err, "File deleted.")
}

func (h *Handler) uploadPublicFile(w http.ResponseWriter, r *http.Request) {
	linkHash := strings.TrimSpace(r.PathValue("link_hash"))
	relativeURI, file, size, ok := uploadedFile(w, r)
	if !ok {
		return
	}
	defer file.Close()
	err := h.runtime.CreatePublicShareFile(r.Context(), linkHash, relativeURI, file, size)
	h.afterPublicMutation(w, r, linkHash, err, "File uploaded.")
}

func (h *Handler) replacePublicFile(w http.ResponseWriter, r *http.Request) {
	linkHash := strings.TrimSpace(r.PathValue("link_hash"))
	fileID, ok := pathUint(w, r, "file_id")
	if !ok {
		return
	}
	ifMatch, ok := formIfMatch(w, r)
	if !ok {
		return
	}
	_, file, size, ok := uploadedFile(w, r)
	if !ok {
		return
	}
	defer file.Close()
	err := h.runtime.ReplacePublicShareFileContent(r.Context(), linkHash, fileID, ifMatch, file, size)
	h.afterPublicMutation(w, r, linkHash, err, "File replacement accepted.")
}

func (h *Handler) deletePublicFile(w http.ResponseWriter, r *http.Request) {
	linkHash := strings.TrimSpace(r.PathValue("link_hash"))
	fileID, ok := pathUint(w, r, "file_id")
	if !ok {
		return
	}
	ifMatch, ok := formIfMatch(w, r)
	if !ok {
		return
	}
	err := h.runtime.DeletePublicShareFile(r.Context(), linkHash, fileID, ifMatch)
	h.afterPublicMutation(w, r, linkHash, err, "File deleted.")
}

func (h *Handler) afterAuthenticatedMutation(w http.ResponseWriter, r *http.Request, _ authContext, shareID uint, err error, _ string) {
	if err != nil {
		h.redirectAfterMutation(w, r, authenticatedShareViewURL(shareID, r))
		return
	}
	h.redirectAfterMutation(w, r, authenticatedShareViewURL(shareID, r))
}

func (h *Handler) afterPublicMutation(w http.ResponseWriter, r *http.Request, linkHash string, err error, _ string) {
	if err != nil {
		h.redirectAfterMutation(w, r, publicShareViewURL(linkHash, r))
		return
	}
	h.redirectAfterMutation(w, r, publicShareViewURL(linkHash, r))
}

func (h *Handler) redirectAfterMutation(w http.ResponseWriter, r *http.Request, target string) {
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func authenticatedShareViewURL(shareID uint, r *http.Request) string {
	return shareViewURL(fmt.Sprintf("/share/shares/%d", shareID), r)
}

func publicShareViewURL(linkHash string, r *http.Request) string {
	return shareViewURL("/w/"+url.PathEscape(strings.TrimSpace(linkHash)), r)
}

func shareViewURL(basePath string, r *http.Request) string {
	query := url.Values{}
	query.Set("page", strconv.Itoa(parsePositiveRequestValue(r, "page", 1)))
	query.Set("count", strconv.Itoa(parsePositiveRequestValue(r, "count", 20)))
	query.Set("browse", selectedBrowseMode(r))
	query.Set("sort", selectedSort(r))
	query.Set("order", selectedOrder(r))
	if thumb := strings.TrimSpace(requestValue(r, "thumb")); thumb != "" {
		query.Set("thumb", thumb)
	}
	if view := selectedViewMode(r); view != "" {
		query.Set("view", view)
	}
	if treePath := selectedTreePath(r); treePath != "" {
		query.Set("path", treePath)
	}
	return basePath + "?" + query.Encode()
}

func (h *Handler) renderAuthenticatedFilePageWithMessage(w http.ResponseWriter, r *http.Request, auth authContext, shareID uint, errMessage, message string) {
	result, err := h.runtime.ListUserShareFiles(auth.UserID, shareID, 1, parseCount(r), selectedShareFileFilter(r))
	if err != nil {
		h.renderShareError(w, err)
		return
	}
	h.renderFilePageWithMessages(w, r, false, result, errMessage, message)
}

func (h *Handler) renderPublicFilePageWithMessage(w http.ResponseWriter, r *http.Request, linkHash string, errMessage, message string) {
	result, err := h.runtime.ListPublicShareFiles(linkHash, 1, parseCount(r), selectedShareFileFilter(r))
	if err != nil {
		h.renderShareError(w, err)
		return
	}
	h.renderFilePageWithMessages(w, r, true, result, errMessage, message)
}

func (h *Handler) renderFilePage(w http.ResponseWriter, r *http.Request, public bool, result storage.ShareFileListResult, message string) {
	h.renderFilePageWithMessages(w, r, public, result, "", message)
}

func (h *Handler) renderFilePageWithMessages(w http.ResponseWriter, r *http.Request, public bool, result storage.ShareFileListResult, errMessage, message string) {
	cfg := h.runtime.SharingConfig()
	size := selectedThumbnailSize(r, cfg)
	viewMode := selectedViewMode(r)
	browseMode := selectedBrowseMode(r)
	sortBy := selectedSort(r)
	order := selectedOrder(r)
	treePath := selectedTreePath(r)
	basePath := fmt.Sprintf("/share/shares/%d", result.Share.ID)
	apiBase := fmt.Sprintf("/share/api/shares/%d", result.Share.ID)
	contentBase := basePath
	title := result.Share.Name
	if public {
		linkHash := strings.TrimSpace(r.PathValue("link_hash"))
		if linkHash == "" && result.Share.LinkHash != nil {
			linkHash = *result.Share.LinkHash
		}
		basePath = "/w/" + url.PathEscape(linkHash)
		apiBase = "/s/" + url.PathEscape(linkHash)
		contentBase = basePath
	}
	files := result.Items
	var folders []treeFolderView
	var parentFolder *treeFolderView
	var treePanel *treePanelView
	showPagination := browseMode != browseModeTree
	if browseMode == browseModeTree {
		if result.Total > treeBrowseFileLimit {
			files = nil
			errMessage = fmt.Sprintf("Tree browsing supports up to %d files. Use flat browsing for larger shares.", treeBrowseFileLimit)
		} else {
			model := buildTreeModel(result.Items)
			node := model.folder(treePath)
			if node == nil {
				node = model.Root
				treePath = ""
			}
			panel := treePanelFromModel(model, basePath, viewMode, size, treePath, sortBy, order)
			treePanel = &panel
			files = node.Files
			folders = treeFolderViews(node.folderEntries(), basePath, viewMode, size, sortBy, order)
			if treePath != "" {
				parentPath := parentTreePath(treePath)
				parentFolder = &treeFolderView{
					Name:     "Parent folder",
					Path:     parentPath,
					URL:      browseURL(basePath, browseModeTree, viewMode, parentPath, size, sortBy, order),
					IsParent: true,
				}
			}
		}
	}
	data := pageData{
		Title:          title,
		Authenticated:  !public,
		Public:         public,
		Share:          result.Share,
		Files:          fileViews(files, apiBase, contentBase, size, !public, cfg),
		Permissions:    result.EffectivePermissions,
		Page:           result.Page,
		Count:          result.Count,
		Total:          result.Total,
		BasePath:       basePath,
		APIBasePath:    apiBase,
		ThumbnailSizes: cfg.ThumbnailSizes,
		ThumbnailSize:  size,
		ViewMode:       viewMode,
		BrowseMode:     browseMode,
		Sort:           sortBy,
		Order:          order,
		TreePath:       treePath,
		ParentFolder:   parentFolder,
		Folders:        folders,
		TreePanel:      treePanel,
		HasEntries:     parentFolder != nil || len(folders) > 0 || len(files) > 0,
		ShowPagination: showPagination,
		Error:          errMessage,
		Message:        message,
	}
	h.render(w, http.StatusOK, "share_files", data)
}

func (h *Handler) renderShareError(w http.ResponseWriter, err error) {
	h.render(w, h.runtime.ShareOperationErrorStatus(err), "error", pageData{Title: "Share unavailable", Error: apiMessage(err)})
}

func (h *Handler) render(w http.ResponseWriter, status int, name string, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := h.pages.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "render share page", http.StatusInternalServerError)
	}
}

func parsePagination(r *http.Request) (int, int) {
	return parsePositiveQuery(r, "page", 1), parseCount(r)
}

func parseCount(r *http.Request) int {
	return parsePositiveRequestValue(r, "count", 20)
}

func parsePositiveQuery(r *http.Request, name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get(name)))
	if err != nil || value < 1 {
		return fallback
	}
	return value
}

func parsePositiveRequestValue(r *http.Request, name string, fallback int) int {
	raw := requestValue(r, name)
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return fallback
	}
	return value
}

func requestValue(r *http.Request, name string) string {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		raw = strings.TrimSpace(r.FormValue(name))
	}
	return raw
}

func selectedThumbnailSize(r *http.Request, cfg config.SharingConfig) int {
	size := parsePositiveRequestValue(r, "thumb", cfg.ThumbnailDefaultSize)
	for _, allowed := range cfg.ThumbnailSizes {
		if allowed == size {
			return size
		}
	}
	return cfg.ThumbnailDefaultSize
}

func selectedViewMode(r *http.Request) string {
	viewMode := strings.TrimSpace(r.URL.Query().Get("view"))
	if viewMode == "" {
		viewMode = strings.TrimSpace(r.FormValue("view"))
	}
	switch viewMode {
	case "grid":
		return "grid"
	default:
		return "list"
	}
}

func selectedBrowseMode(r *http.Request) string {
	browseMode := strings.TrimSpace(r.URL.Query().Get("browse"))
	if browseMode == "" {
		browseMode = strings.TrimSpace(r.FormValue("browse"))
	}
	switch browseMode {
	case browseModeTree:
		return browseModeTree
	default:
		return browseModeFlat
	}
}

func selectedShareFileFilter(r *http.Request) storage.ShareFileListFilter {
	return storage.ShareFileListFilter{Sort: selectedSort(r), Order: selectedOrder(r)}
}

func selectedSort(r *http.Request) string {
	switch strings.TrimSpace(requestValue(r, "sort")) {
	case "id", "name", "size", "created", "modified":
		return strings.TrimSpace(requestValue(r, "sort"))
	default:
		return "name"
	}
}

func selectedOrder(r *http.Request) string {
	if strings.TrimSpace(requestValue(r, "order")) == "desc" {
		return "desc"
	}
	return "asc"
}

func selectedTreePath(r *http.Request) string {
	raw := strings.TrimSpace(r.URL.Query().Get("path"))
	if raw == "" {
		raw = strings.TrimSpace(r.FormValue("path"))
	}
	return cleanTreePath(raw)
}

func uploadedFile(w http.ResponseWriter, r *http.Request) (string, multipart.File, int64, bool) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "invalid multipart request", http.StatusBadRequest)
		return "", nil, 0, false
	}
	relativeURI := strings.TrimSpace(r.FormValue("relative_uri"))
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "invalid multipart request", http.StatusBadRequest)
		return "", nil, 0, false
	}
	size := int64(-1)
	if header != nil {
		size = header.Size
	}
	return relativeURI, file, size, true
}

func formIfMatch(w http.ResponseWriter, r *http.Request) (string, bool) {
	if err := r.ParseMultipartForm(32 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		http.Error(w, "invalid form request", http.StatusBadRequest)
		return "", false
	}
	value := strings.TrimSpace(r.FormValue("version"))
	if value == "" {
		return "", true
	}
	if strings.HasPrefix(value, `"`) {
		return value, true
	}
	return strconv.Quote(value), true
}

func loginCredentials(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	contentType := strings.ToLower(r.Header.Get("Content-Type"))
	if strings.Contains(contentType, "application/json") {
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON payload", http.StatusBadRequest)
			return "", "", false
		}
		return strings.TrimSpace(body.Username), body.Password, true
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form request", http.StatusBadRequest)
		return "", "", false
	}
	return strings.TrimSpace(r.FormValue("username")), r.FormValue("password"), true
}

func localAuthStatus(err error) int {
	switch {
	case errors.Is(err, service.ErrInvalidCredentials), errors.Is(err, service.ErrInvalidToken), errors.Is(err, service.ErrExpiredToken), errors.Is(err, service.ErrRevokedToken):
		return http.StatusUnauthorized
	case errors.Is(err, service.ErrInactiveUser):
		return http.StatusForbidden
	default:
		return http.StatusInternalServerError
	}
}

func pathUint(w http.ResponseWriter, r *http.Request, name string) (uint, bool) {
	value, err := strconv.ParseUint(r.PathValue(name), 10, 64)
	if err != nil || value == 0 {
		http.Error(w, "invalid "+name, http.StatusBadRequest)
		return 0, false
	}
	return uint(value), true
}

func isHTMX(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("HX-Request"), "true")
}

func apiMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func fileViews(files []apiclient.ReplicaInventoryFile, apiBasePath string, contentBasePath string, thumbSize int, authenticated bool, cfg config.SharingConfig) []fileView {
	result := make([]fileView, 0, len(files))
	for _, file := range files {
		ext := strings.TrimPrefix(strings.ToLower(path.Ext(file.RelativeURI)), ".")
		contentPath := fmt.Sprintf("%s/files/%d/content", contentBasePath, file.FileID)
		view := fileView{
			ReplicaInventoryFile: file,
			Name:                 path.Base(file.RelativeURI),
			Type:                 fileType(ext),
			ContentPath:          contentPath,
			DownloadPath:         contentPath,
			ThumbnailURL:         fmt.Sprintf("%s/files/%d/thumbnail?size=%d", apiBasePath, file.FileID, thumbSize),
			CanPreview:           cfg.VideoPlaybackEnabled && isPlayableVideo(ext) && file.Size <= int64(cfg.VideoInlineMaxSizeMB)*1024*1024,
			PreviewKind:          previewKind(ext, cfg, file.Size),
		}
		if authenticated {
			view.ThumbnailURL = ""
		}
		result = append(result, view)
	}
	return result
}

func previewKind(ext string, cfg config.SharingConfig, size int64) string {
	switch ext {
	case "jpg", "jpeg", "png", "gif", "webp":
		return "image"
	case "mp4", "webm":
		if cfg.VideoPlaybackEnabled && size <= int64(cfg.VideoInlineMaxSizeMB)*1024*1024 {
			return "video"
		}
	case "mp3", "wav", "ogg", "m4a":
		return "audio"
	case "pdf":
		return "pdf"
	}
	return "fallback"
}

func treeFolderViews(entries []treeFolderEntry, basePath string, viewMode string, thumbSize int, sortBy string, order string) []treeFolderView {
	result := make([]treeFolderView, 0, len(entries))
	for _, entry := range entries {
		result = append(result, treeFolderView{
			Name: entry.Name,
			Path: entry.Path,
			URL:  browseURL(basePath, browseModeTree, viewMode, entry.Path, thumbSize, sortBy, order),
		})
	}
	return result
}

func fileType(ext string) string {
	switch ext {
	case "jpg", "jpeg", "png", "gif", "webp":
		return "Image (" + strings.ToUpper(ext) + ")"
	case "mp4", "webm", "mov":
		return "Video (" + strings.ToUpper(ext) + ")"
	case "pdf":
		return "Document (PDF)"
	case "txt", "md", "log":
		return "Text"
	case "":
		return "File"
	default:
		return strings.ToUpper(ext)
	}
}

func isPlayableVideo(ext string) bool {
	return ext == "mp4" || ext == "webm"
}

func pageURL(base string, page, count, thumb int, viewMode string, browseMode string, treePath string, sortBy string, order string) string {
	query := url.Values{}
	query.Set("page", strconv.Itoa(page))
	query.Set("count", strconv.Itoa(count))
	query.Set("thumb", strconv.Itoa(thumb))
	query.Set("view", selectedViewValue(viewMode))
	query.Set("browse", selectedBrowseValue(browseMode))
	query.Set("sort", selectedSortValue(sortBy))
	query.Set("order", selectedOrderValue(order))
	if cleanPath := cleanTreePath(treePath); cleanPath != "" {
		query.Set("path", cleanPath)
	}
	return base + "?" + query.Encode()
}

func viewURL(base string, page, count, thumb int, viewMode string, browseMode string, treePath string, sortBy string, order string) string {
	return pageURL(base, page, count, thumb, viewMode, browseMode, treePath, sortBy, order)
}

func browseURL(base string, browseMode string, viewMode string, treePath string, thumb int, sortBy string, order string) string {
	query := url.Values{}
	query.Set("browse", selectedBrowseValue(browseMode))
	query.Set("view", selectedViewValue(viewMode))
	query.Set("sort", selectedSortValue(sortBy))
	query.Set("order", selectedOrderValue(order))
	if thumb > 0 {
		query.Set("thumb", strconv.Itoa(thumb))
	}
	if cleanPath := cleanTreePath(treePath); cleanPath != "" {
		query.Set("path", cleanPath)
	}
	return base + "?" + query.Encode()
}

func orderURL(base string, page, count, thumb int, viewMode string, browseMode string, treePath string, sortBy string, order string) string {
	return pageURL(base, page, count, thumb, viewMode, browseMode, treePath, sortBy, order)
}

func selectedSortValue(sortBy string) string {
	switch sortBy {
	case "id", "size", "created", "modified":
		return sortBy
	default:
		return "name"
	}
}

func selectedOrderValue(order string) string {
	if order == "desc" {
		return "desc"
	}
	return "asc"
}

func selectedViewValue(viewMode string) string {
	if viewMode == "grid" {
		return "grid"
	}
	return "list"
}

func selectedBrowseValue(browseMode string) string {
	if browseMode == browseModeTree {
		return browseModeTree
	}
	return browseModeFlat
}

func thumbStyle(size int) template.CSS {
	if size < 64 {
		size = 64
	}
	if size > 512 {
		size = 512
	}
	return template.CSS(fmt.Sprintf("--thumb-size:%dpx", size))
}

func pageCount(total int64, count int) int {
	if count <= 0 {
		return 1
	}
	pages := int((total + int64(count) - 1) / int64(count))
	if pages < 1 {
		return 1
	}
	return pages
}

func pageStart(total int64, page, count int) int64 {
	if total == 0 {
		return 0
	}
	start := int64((page-1)*count + 1)
	if start > total {
		return total
	}
	return start
}

func pageEnd(total int64, page, count int) int64 {
	end := int64(page * count)
	if end > total {
		return total
	}
	return end
}

func formatBytes(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	value := float64(size) / 1024
	for _, unit := range units {
		if value < 1024 {
			return fmt.Sprintf("%.1f %s", value, unit)
		}
		value /= 1024
	}
	return fmt.Sprintf("%.1f PB", value)
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.Local().Format("Jan 2, 2006 15:04")
}
