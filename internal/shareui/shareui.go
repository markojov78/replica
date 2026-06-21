package shareui

import (
	"embed"
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
	"replica/internal/storage"
)

//go:embed templates/*.html static/*
var assets embed.FS

type Handler struct {
	runtime *storage.Runtime
	pages   *template.Template
}

type authContext struct {
	UserID uint
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
	Error          string
	Message        string
}

type fileView struct {
	apiclient.ReplicaInventoryFile
	Name         string
	Type         string
	DownloadPath string
	ThumbnailURL string
	CanPreview   bool
}

func Register(mux *http.ServeMux, runtime *storage.Runtime) error {
	pages, err := template.New("shareui").Funcs(template.FuncMap{
		"formatBytes":      formatBytes,
		"formatTime":       formatTime,
		"hasPermission":    storage.PermissionAllowed,
		"joinPermissions":  strings.Join,
		"pageCount":        pageCount,
		"pageStart":        pageStart,
		"pageEnd":          pageEnd,
		"add":              func(a, b int) int { return a + b },
		"sub":              func(a, b int) int { return a - b },
		"pathEscape":       url.PathEscape,
		"thumbnailSizeURL": thumbnailSizeURL,
	}).ParseFS(assets, "templates/*.html")
	if err != nil {
		return err
	}
	handler := &Handler{runtime: runtime, pages: pages}

	mux.Handle("GET /share/static/", http.StripPrefix("/share/static/", http.FileServer(http.FS(mustSub(assets, "static")))))
	mux.HandleFunc("GET /share", handler.loginPage)
	mux.HandleFunc("GET /share/shares", handler.protected(handler.shareListPage))
	mux.HandleFunc("GET /share/shares/{id}", handler.protected(handler.shareFilesPage))
	mux.HandleFunc("POST /share/shares/{id}/files", handler.protected(handler.uploadShareFile))
	mux.HandleFunc("POST /share/shares/{id}/files/{file_id}/replace", handler.protected(handler.replaceShareFile))
	mux.HandleFunc("POST /share/shares/{id}/files/{file_id}/delete", handler.protected(handler.deleteShareFile))
	mux.HandleFunc("GET /w/{link_hash}", handler.publicSharePage)
	mux.HandleFunc("POST /w/{link_hash}/files", handler.uploadPublicFile)
	mux.HandleFunc("POST /w/{link_hash}/files/{file_id}/replace", handler.replacePublicFile)
	mux.HandleFunc("POST /w/{link_hash}/files/{file_id}/delete", handler.deletePublicFile)
	return nil
}

func mustSub(embedded embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(embedded, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

func (h *Handler) loginPage(w http.ResponseWriter, _ *http.Request) {
	h.render(w, http.StatusOK, "login", pageData{Title: "Sign in", LoginPath: "/share"})
}

func (h *Handler) protected(next func(http.ResponseWriter, *http.Request, authContext)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := h.runtime.AuthenticateShareUserAuthorization(r.Context(), r.Header.Get("Authorization"))
		if err != nil {
			if isHTMX(r) {
				w.Header().Set("HX-Redirect", "/share")
			}
			h.render(w, h.runtime.ShareAuthErrorStatus(err), "login", pageData{Title: "Sign in", Error: apiMessage(err)})
			return
		}
		next(w, r, authContext{UserID: user.UserID})
	}
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
	page, count := parsePagination(r)
	result, err := h.runtime.ListUserShareFiles(auth.UserID, shareID, page, count)
	if err != nil {
		h.renderShareError(w, err)
		return
	}
	h.renderFilePage(w, r, false, result, "")
}

func (h *Handler) publicSharePage(w http.ResponseWriter, r *http.Request) {
	page, count := parsePagination(r)
	linkHash := strings.TrimSpace(r.PathValue("link_hash"))
	result, err := h.runtime.ListPublicShareFiles(linkHash, page, count)
	if err != nil {
		h.renderShareError(w, err)
		return
	}
	h.renderFilePage(w, r, true, result, "")
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

func (h *Handler) afterAuthenticatedMutation(w http.ResponseWriter, r *http.Request, auth authContext, shareID uint, err error, message string) {
	if err != nil {
		h.renderAuthenticatedFilePageWithMessage(w, r, auth, shareID, apiMessage(err), "")
		return
	}
	h.renderAuthenticatedFilePageWithMessage(w, r, auth, shareID, "", message)
}

func (h *Handler) afterPublicMutation(w http.ResponseWriter, r *http.Request, linkHash string, err error, message string) {
	if err != nil {
		h.renderPublicFilePageWithMessage(w, r, linkHash, apiMessage(err), "")
		return
	}
	h.renderPublicFilePageWithMessage(w, r, linkHash, "", message)
}

func (h *Handler) renderAuthenticatedFilePageWithMessage(w http.ResponseWriter, r *http.Request, auth authContext, shareID uint, errMessage, message string) {
	result, err := h.runtime.ListUserShareFiles(auth.UserID, shareID, 1, parseCount(r))
	if err != nil {
		h.renderShareError(w, err)
		return
	}
	h.renderFilePageWithMessages(w, r, false, result, errMessage, message)
}

func (h *Handler) renderPublicFilePageWithMessage(w http.ResponseWriter, r *http.Request, linkHash string, errMessage, message string) {
	result, err := h.runtime.ListPublicShareFiles(linkHash, 1, parseCount(r))
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
	basePath := fmt.Sprintf("/share/shares/%d", result.Share.ID)
	apiBase := fmt.Sprintf("/api/share/shares/%d", result.Share.ID)
	title := result.Share.Name
	if public {
		linkHash := strings.TrimSpace(r.PathValue("link_hash"))
		if linkHash == "" && result.Share.LinkHash != nil {
			linkHash = *result.Share.LinkHash
		}
		basePath = "/w/" + url.PathEscape(linkHash)
		apiBase = "/s/" + url.PathEscape(linkHash)
	}
	data := pageData{
		Title:          title,
		Authenticated:  !public,
		Public:         public,
		Share:          result.Share,
		Files:          fileViews(result.Items, apiBase, size, !public, cfg),
		Permissions:    result.EffectivePermissions,
		Page:           result.Page,
		Count:          result.Count,
		Total:          result.Total,
		BasePath:       basePath,
		APIBasePath:    apiBase,
		ThumbnailSizes: cfg.ThumbnailSizes,
		ThumbnailSize:  size,
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
	return parsePositiveQuery(r, "count", 20)
}

func parsePositiveQuery(r *http.Request, name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get(name)))
	if err != nil || value < 1 {
		return fallback
	}
	if name == "count" && value > 100 {
		return 100
	}
	return value
}

func selectedThumbnailSize(r *http.Request, cfg config.SharingConfig) int {
	size := parsePositiveQuery(r, "thumb", cfg.ThumbnailDefaultSize)
	for _, allowed := range cfg.ThumbnailSizes {
		if allowed == size {
			return size
		}
	}
	return cfg.ThumbnailDefaultSize
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

func fileViews(files []apiclient.ReplicaInventoryFile, apiBasePath string, thumbSize int, authenticated bool, cfg config.SharingConfig) []fileView {
	result := make([]fileView, 0, len(files))
	for _, file := range files {
		ext := strings.TrimPrefix(strings.ToLower(path.Ext(file.RelativeURI)), ".")
		view := fileView{
			ReplicaInventoryFile: file,
			Name:                 path.Base(file.RelativeURI),
			Type:                 fileType(ext),
			DownloadPath:         fmt.Sprintf("%s/files/%d/content", apiBasePath, file.FileID),
			ThumbnailURL:         fmt.Sprintf("%s/files/%d/thumbnail?size=%d", apiBasePath, file.FileID, thumbSize),
			CanPreview:           cfg.VideoPlaybackEnabled && isPlayableVideo(ext) && file.Size <= int64(cfg.VideoInlineMaxSizeMB)*1024*1024,
		}
		if authenticated {
			view.ThumbnailURL = ""
		}
		result = append(result, view)
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

func thumbnailSizeURL(base string, page, count, thumb int) string {
	return fmt.Sprintf("%s?page=%d&count=%d&thumb=%d", base, page, count, thumb)
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
