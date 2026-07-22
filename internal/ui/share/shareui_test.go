package share

import (
	"bytes"
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"replica/internal/apiclient"
	"replica/internal/config"
	"replica/internal/storage"
)

func TestRegisterServesLoginAndStaticAssets(t *testing.T) {
	mux := http.NewServeMux()
	if err := Register(mux, nil); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/share", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /share status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), `data-share-login-form`) ||
		!strings.Contains(rec.Body.String(), `/share/auth/login`) {
		t.Fatalf("GET /share body = %s, want sharing login form", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/share/static/share.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET static status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), `htmx:configRequest`) ||
		!strings.Contains(rec.Body.String(), `X-CSRF-Token`) ||
		!strings.Contains(rec.Body.String(), `window.location.pathname !== "/share"`) ||
		!strings.Contains(rec.Body.String(), `fetch("/share/api/auth/me"`) ||
		!strings.Contains(rec.Body.String(), `thumbnail_size`) ||
		!strings.Contains(rec.Body.String(), `page_size`) ||
		!strings.Contains(rec.Body.String(), `defaultThumbnailSize`) ||
		!strings.Contains(rec.Body.String(), `htmx:afterSwap`) ||
		!strings.Contains(rec.Body.String(), `loadPublicThumbnails`) ||
		!strings.Contains(rec.Body.String(), `folder_tree_visible`) ||
		!strings.Contains(rec.Body.String(), `data-actions-menu-button`) ||
		!strings.Contains(rec.Body.String(), `uploadSelectedFiles`) ||
		!strings.Contains(rec.Body.String(), `currentShareTotal`) ||
		!strings.Contains(rec.Body.String(), `[0, 1, 2, 4, 8, 16, 32]`) ||
		!strings.Contains(rec.Body.String(), `window.location.reload()`) ||
		!strings.Contains(rec.Body.String(), `bindPreviewViewer`) ||
		!strings.Contains(rec.Body.String(), `replicaPreview`) ||
		!strings.Contains(rec.Body.String(), `data-preview-item`) ||
		!strings.Contains(rec.Body.String(), `async function loadPage(page)`) ||
		!strings.Contains(rec.Body.String(), `knownIDs`) ||
		!strings.Contains(rec.Body.String(), `Loading files`) ||
		!strings.Contains(rec.Body.String(), `DOMParser`) ||
		!strings.Contains(rec.Body.String(), `relative_uri`) {
		t.Fatalf("share.js body = %s, want HTMX auth header handling", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `headers.Authorization`) {
		t.Fatal("share.js must not construct an Authorization header")
	}
}

func TestShareFileTemplateRendersListAndGridModes(t *testing.T) {
	file := fileView{
		ReplicaInventoryFile: apiclient.ReplicaInventoryFile{
			FileID:           10,
			RelativeURI:      "album/photo.jpg",
			Size:             12345,
			InventoryVersion: 4,
			Modified:         time.Date(2026, 3, 17, 10, 30, 0, 0, time.UTC),
		},
		Name:         "photo.jpg",
		Type:         "Image (JPG)",
		ContentPath:  "/share/shares/4/files/10/content",
		DownloadPath: "/api/share/shares/4/files/10/content",
		PreviewKind:  "image",
	}

	list := renderShareTemplate(t, pageData{
		Title:          "Photos",
		Authenticated:  true,
		Share:          apiclient.Share{ID: 4, Name: "Photos"},
		Files:          []fileView{file},
		Permissions:    []string{"read"},
		Page:           1,
		Count:          20,
		Total:          1,
		BasePath:       "/share/shares/4",
		APIBasePath:    "/api/share/shares/4",
		ThumbnailSizes: []int{128, 256},
		ThumbnailSize:  128,
		ViewMode:       "list",
	})
	if !strings.Contains(list, "<table>") || strings.Contains(list, `class="file-grid"`) {
		t.Fatalf("list view = %s, want table and no grid", list)
	}
	if !strings.Contains(list, `href="/share/shares/4/files/10/content"`) {
		t.Fatalf("list view = %s, want authenticated content link", list)
	}

	file.ThumbnailURL = "/s/public-link/files/10/thumbnail?size=256"
	file.ContentPath = "/w/public-link/files/10/content"
	file.DownloadPath = "/w/public-link/files/10/content"
	defaultView := "grid"
	defaultPageSize := 25
	defaultThumbnailSize := 256
	defaultTheme := "dark"
	grid := renderShareTemplate(t, pageData{
		Title:  "Photos",
		Public: true,
		Share: apiclient.Share{
			ID:   4,
			Name: "Photos",
			Properties: apiclient.ShareProperties{
				View:          &defaultView,
				PageSize:      &defaultPageSize,
				ThumbnailSize: &defaultThumbnailSize,
				Theme:         &defaultTheme,
			},
		},
		Files:          []fileView{file},
		Permissions:    []string{"read", "update", "delete"},
		Page:           1,
		Count:          25,
		Total:          1,
		BasePath:       "/w/public-link",
		APIBasePath:    "/s/public-link",
		ThumbnailSizes: []int{128, 256},
		ThumbnailSize:  256,
		ViewMode:       "grid",
		ShowPagination: true,
	})
	for _, want := range []string{
		`class="file-grid"`,
		`class="gallery-item has-actions"`,
		`class="gallery-media"`,
		`class="gallery-actions"`,
		`class="gallery-caption"`,
		`data-public-src="/s/public-link/files/10/thumbnail?size=256"`,
		`data-preview-item`,
		`data-file-id="10"`,
		`data-preview-kind="image"`,
		`role="dialog" aria-modal="true"`,
		`aria-label="Preview previous file"`,
		`href="/w/public-link/files/10/content"`,
		`data-default-view="grid"`,
		`data-default-page-size="25"`,
		`data-default-thumbnail-size="256"`,
		`data-default-theme="dark"`,
		`data-share-default-theme="dark"`,
		`localStorage.getItem("replica_share_theme")`,
		`src="/share/static/share.js?v=20260719-7"`,
		`data-result-page="1"`,
		`data-result-total="1"`,
		`data-preview-status role="status" aria-live="polite"`,
		`<noscript><img class="grid-thumb" src="/s/public-link/files/10/thumbnail?size=256" alt=""></noscript>`,
		`<option value="25" selected>25</option>`,
		`Delete`,
	} {
		if !strings.Contains(grid, want) {
			t.Fatalf("grid view = %s, want %q", grid, want)
		}
	}
	if strings.Contains(grid, `class="file-card`) || strings.Contains(grid, `class="card-preview`) {
		t.Fatalf("grid view = %s, want gallery items without nested cards", grid)
	}
	if strings.Index(grid, `localStorage.getItem("replica_share_theme")`) > strings.Index(grid, `<link rel="stylesheet"`) {
		t.Fatalf("grid view = %s, want theme bootstrap before stylesheet", grid)
	}
}

func TestShareFileHeaderUsesBreadcrumbAndCompactControls(t *testing.T) {
	html := renderShareTemplate(t, pageData{
		Authenticated:  true,
		Share:          apiclient.Share{ID: 4, Name: "Bali"},
		Permissions:    []string{"read", "create"},
		Page:           2,
		Count:          50,
		BasePath:       "/share/shares/4",
		ThumbnailSizes: []int{128, 256},
		ThumbnailSize:  256,
		ViewMode:       "grid",
		BrowseMode:     "tree",
		TreePath:       "photos",
	})

	for _, want := range []string{
		`href="/share/shares"`,
		`<span>Replica Share</span>`,
		`<span class="share-name">Bali</span>`,
		`name="thumb"`,
		`data-share-view-toggle data-view-mode="list"`,
		`aria-label="Switch to list view"`,
		`data-share-browse-toggle data-browse-mode="flat"`,
		`aria-label="Switch to flat browsing"`,
		`data-share-theme-toggle`,
		`aria-label="Switch color theme"`,
		`name="page" value="2"`,
		`name="count" value="50"`,
		`name="path" value="photos"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("share header = %s, want %q", html, want)
		}
	}
	for _, unwanted := range []string{`class="back-link"`, `>My shares</a>`, `read, create`} {
		if strings.Contains(html, unwanted) {
			t.Fatalf("share header = %s, do not want %q", html, unwanted)
		}
	}
}

func TestUploadPanelRemainsPermissionGatedAndPreservesFormContract(t *testing.T) {
	data := pageData{
		Authenticated:  true,
		Share:          apiclient.Share{ID: 4, Name: "Bali"},
		Page:           2,
		Count:          50,
		BasePath:       "/share/shares/4",
		APIBasePath:    "/api/share/shares/4",
		ThumbnailSizes: []int{128, 256},
		ThumbnailSize:  256,
		ViewMode:       "grid",
		BrowseMode:     "tree",
		TreePath:       "photos",
	}

	data.Permissions = []string{"read"}
	if html := renderShareTemplate(t, data); strings.Contains(html, `class="upload-form"`) {
		t.Fatalf("read-only share = %s, do not want upload controls", html)
	}

	data.Permissions = []string{"read", "create"}
	html := renderShareTemplate(t, data)
	for _, want := range []string{
		`action="/share/shares/4/files"`,
		`class="upload-form"`,
		`class="btn upload-picker"`,
		`>Upload files<input`,
		`data-upload-action="/api/share/shares/4/files"`,
		`data-upload-prefix="photos"`,
		`data-upload-error`,
		`name="file" type="file" multiple required`,
		`name="relative_uri" value=""`,
		`name="page" value="2"`,
		`name="count" value="50"`,
		`name="thumb" value="256"`,
		`name="view" value="grid"`,
		`name="browse" value="tree"`,
		`name="path" value="photos"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("writable share = %s, want %q", html, want)
		}
	}
	if strings.Contains(html, `hx-post="/share/shares/4/files"`) {
		t.Fatalf("writable share = %s, upload must be sequenced by the UI", html)
	}

	data.Authenticated = false
	data.Public = true
	data.BasePath = "/w/public-link"
	data.APIBasePath = "/s/public-link"
	data.BrowseMode = "flat"
	data.TreePath = ""
	publicHTML := renderShareTemplate(t, data)
	if !strings.Contains(publicHTML, `data-upload-action="/s/public-link/files"`) || !strings.Contains(publicHTML, `data-upload-prefix=""`) {
		t.Fatalf("public flat share = %s, want public API upload with base prefix", publicHTML)
	}
}

func TestSharePaginationDoesNotCapPageSize(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/share/shares/4?count=250", nil)
	if got := parseCount(r); got != 250 {
		t.Fatalf("parseCount() = %d, want 250", got)
	}
}

func TestShareFileSortAndOrderSelection(t *testing.T) {
	for _, tt := range []struct {
		query     string
		wantSort  string
		wantOrder string
	}{
		{query: "", wantSort: "name", wantOrder: "asc"},
		{query: "?sort=modified&order=desc", wantSort: "modified", wantOrder: "desc"},
		{query: "?sort=size&order=asc", wantSort: "size", wantOrder: "asc"},
		{query: "?sort=unknown&order=unknown", wantSort: "name", wantOrder: "asc"},
	} {
		r := httptest.NewRequest(http.MethodGet, "/share/shares/4"+tt.query, nil)
		filter := selectedShareFileFilter(r)
		if filter.Sort != tt.wantSort || filter.Order != tt.wantOrder {
			t.Fatalf("selectedShareFileFilter(%q) = %+v, want sort=%q order=%q", tt.query, filter, tt.wantSort, tt.wantOrder)
		}
	}
}

func TestFileActionsMenuSharedByListAndGrid(t *testing.T) {
	file := fileView{
		ReplicaInventoryFile: apiclient.ReplicaInventoryFile{
			FileID:           10,
			RelativeURI:      "album/photo.jpg",
			Size:             12345,
			InventoryVersion: 4,
			Modified:         time.Date(2026, 3, 17, 10, 30, 0, 0, time.UTC),
		},
		Name:         "photo.jpg",
		Type:         "Image (JPG)",
		ContentPath:  "/share/shares/4/files/10/content",
		DownloadPath: "/api/share/shares/4/files/10/content",
	}

	base := pageData{
		Title:          "Photos",
		Authenticated:  true,
		Share:          apiclient.Share{ID: 4, Name: "Photos"},
		Files:          []fileView{file},
		Permissions:    []string{"read", "update", "delete"},
		Page:           1,
		Count:          20,
		Total:          1,
		BasePath:       "/share/shares/4",
		APIBasePath:    "/api/share/shares/4",
		ThumbnailSizes: []int{128, 256},
		ThumbnailSize:  128,
		BrowseMode:     "flat",
		ShowPagination: true,
	}
	for _, mode := range []string{"list", "grid"} {
		data := base
		data.ViewMode = mode
		html := renderShareTemplate(t, data)
		for _, want := range []string{
			`data-actions-menu`,
			`data-actions-menu-button`,
			`data-actions-menu-popover`,
			`data-auth-download="/api/share/shares/4/files/10/content"`,
			`Delete`,
		} {
			if !strings.Contains(html, want) {
				t.Fatalf("%s view = %s, want %q", mode, html, want)
			}
		}
		if strings.Count(html, `data-actions-menu-button`) != 1 {
			t.Fatalf("%s view = %s, want one compact actions menu for one file", mode, html)
		}
		if strings.Contains(html, `Replace`) {
			t.Fatalf("%s view = %s, do not want replace action", mode, html)
		}
	}
}

func TestAuthenticatedGridUsesAPIThumbnailDataSource(t *testing.T) {
	html := renderShareTemplate(t, pageData{
		Title:         "Photos",
		Authenticated: true,
		Share:         apiclient.Share{ID: 4, Name: "Photos"},
		Files: []fileView{{
			ReplicaInventoryFile: apiclient.ReplicaInventoryFile{FileID: 10, RelativeURI: "photo.jpg", Size: 100, InventoryVersion: 4},
			Name:                 "photo.jpg",
			Type:                 "Image (JPG)",
			ContentPath:          "/share/shares/4/files/10/content",
			DownloadPath:         "/api/share/shares/4/files/10/content",
		}},
		Permissions:    []string{"read"},
		Page:           1,
		Count:          20,
		Total:          1,
		BasePath:       "/share/shares/4",
		APIBasePath:    "/api/share/shares/4",
		ThumbnailSizes: []int{128, 512},
		ThumbnailSize:  512,
		ViewMode:       "grid",
	})
	if !strings.Contains(html, `data-auth-src="/api/share/shares/4/files/10/thumbnail?size=512"`) {
		t.Fatalf("authenticated grid = %s, want API thumbnail data source with selected size", html)
	}
	if strings.Contains(html, `Delete`) || strings.Contains(html, `Replace`) {
		t.Fatalf("authenticated read-only grid = %s, want no update/delete controls", html)
	}
}

func TestTreeModelGenerationFromRelativeURIs(t *testing.T) {
	model := buildTreeModel([]apiclient.ReplicaInventoryFile{
		{FileID: 1, RelativeURI: "image01.jpg"},
		{FileID: 2, RelativeURI: "sub/image03.jpg"},
		{FileID: 3, RelativeURI: "sub/videos/video01.mp4"},
	})

	root := model.folder("")
	if root == nil || len(root.Files) != 1 || root.Files[0].RelativeURI != "image01.jpg" {
		t.Fatalf("root files = %+v, want image01.jpg", root)
	}
	sub := model.folder("sub")
	if sub == nil || len(sub.Files) != 1 || sub.Files[0].RelativeURI != "sub/image03.jpg" {
		t.Fatalf("sub node = %+v, want sub/image03.jpg", sub)
	}
	videos := model.folder("sub/videos")
	if videos == nil || len(videos.Files) != 1 || videos.Files[0].RelativeURI != "sub/videos/video01.mp4" {
		t.Fatalf("videos node = %+v, want video01.mp4", videos)
	}
}

func TestTreeRootFolderRendering(t *testing.T) {
	html := renderShareTemplate(t, treeTemplateData("list", "", false))
	for _, want := range []string{
		`data-browse-mode="tree"`,
		`href="/share/shares/4?browse=tree&amp;order=asc&amp;path=sub&amp;sort=name&amp;thumb=128&amp;view=list"`,
		`Folder sub`,
		`image01.jpg`,
		`image02.jpg`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("tree root html = %s, want %q", html, want)
		}
	}
	if strings.Contains(html, `Parent folder`) || strings.Contains(html, `class="pagination"`) {
		t.Fatalf("tree root html = %s, want no parent or pagination", html)
	}
}

func TestTreeNestedFolderRenderingWithParent(t *testing.T) {
	html := renderShareTemplate(t, treeTemplateData("list", "sub", false))
	for _, want := range []string{
		`Up Parent folder`,
		`Folder videos`,
		`image03.jpg`,
		`image04.jpg`,
		`href="/share/shares/4?browse=tree&amp;order=asc&amp;sort=name&amp;thumb=128&amp;view=list"`,
		`href="/share/shares/4?browse=tree&amp;order=asc&amp;path=sub%2Fvideos&amp;sort=name&amp;thumb=128&amp;view=list"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("tree nested html = %s, want %q", html, want)
		}
	}
}

func TestTreeListAndGridRendering(t *testing.T) {
	list := renderShareTemplate(t, treeTemplateData("list", "sub/videos", false))
	if !strings.Contains(list, `<table>`) || strings.Contains(list, `class="file-grid"`) {
		t.Fatalf("tree list html = %s, want table only", list)
	}
	if !strings.Contains(list, `video01.mp4`) || !strings.Contains(list, `Up Parent folder`) {
		t.Fatalf("tree list html = %s, want video and parent folder", list)
	}

	grid := renderShareTemplate(t, treeTemplateData("grid", "sub", false))
	for _, want := range []string{
		`class="file-grid"`,
		`gallery-folder`,
		`folder-preview`,
		`image03.jpg`,
	} {
		if !strings.Contains(grid, want) {
			t.Fatalf("tree grid html = %s, want %q", grid, want)
		}
	}
}

func TestTreeFileLabelsAreRelativeToCurrentFolder(t *testing.T) {
	html := renderShareTemplate(t, treeTemplateData("list", "sub/videos", false))
	if !strings.Contains(html, `>video01.mp4</a>`) {
		t.Fatalf("tree nested html = %s, want basename file label", html)
	}
	if strings.Contains(html, `>sub/videos/video01.mp4</a>`) {
		t.Fatalf("tree nested html = %s, want no full relative path file label", html)
	}
}

func TestFlatFileLabelsShowRelativeURI(t *testing.T) {
	file := fileView{
		ReplicaInventoryFile: apiclient.ReplicaInventoryFile{FileID: 10, RelativeURI: "album/photo.jpg", Size: 100},
		Name:                 "photo.jpg",
		Type:                 "Image (JPG)",
		ContentPath:          "/share/shares/4/files/10/content",
		DownloadPath:         "/share/shares/4/files/10/content",
	}
	html := renderShareTemplate(t, pageData{
		Title:          "Photos",
		Authenticated:  true,
		Share:          apiclient.Share{ID: 4, Name: "Photos"},
		Files:          []fileView{file},
		Permissions:    []string{"read"},
		Page:           1,
		Count:          20,
		Total:          1,
		BasePath:       "/share/shares/4",
		APIBasePath:    "/api/share/shares/4",
		ThumbnailSizes: []int{128, 256},
		ThumbnailSize:  128,
		ViewMode:       "list",
		BrowseMode:     "flat",
		ShowPagination: true,
		HasEntries:     true,
	})
	if !strings.Contains(html, `>album/photo.jpg</a>`) {
		t.Fatalf("flat html = %s, want relative URI file label", html)
	}
}

func TestFlatModePaginationUnchangedAndTreePaginationHidden(t *testing.T) {
	file := fileView{
		ReplicaInventoryFile: apiclient.ReplicaInventoryFile{FileID: 10, RelativeURI: "photo.jpg", Size: 100},
		Name:                 "photo.jpg",
		Type:                 "Image (JPG)",
		ContentPath:          "/share/shares/4/files/10/content",
		DownloadPath:         "/share/shares/4/files/10/content",
	}
	flat := renderShareTemplate(t, pageData{
		Title:          "Photos",
		Authenticated:  true,
		Share:          apiclient.Share{ID: 4, Name: "Photos"},
		Files:          []fileView{file},
		Permissions:    []string{"read"},
		Page:           2,
		Count:          20,
		Total:          41,
		BasePath:       "/share/shares/4",
		APIBasePath:    "/api/share/shares/4",
		ThumbnailSizes: []int{128, 256},
		ThumbnailSize:  128,
		ViewMode:       "list",
		BrowseMode:     "flat",
		ShowPagination: true,
		HasEntries:     true,
	})
	if !strings.Contains(flat, `Page 2 of 3`) || !strings.Contains(flat, `browse=flat`) {
		t.Fatalf("flat html = %s, want existing pagination with browse=flat", flat)
	}

	tree := renderShareTemplate(t, treeTemplateData("list", "", false))
	if strings.Contains(tree, `class="pagination"`) {
		t.Fatalf("tree html = %s, want pagination hidden", tree)
	}
}

func TestAuthenticatedAndAnonymousTreeRenderingUseCorrectBasePaths(t *testing.T) {
	authHTML := renderShareTemplate(t, treeTemplateData("grid", "sub", false))
	if !strings.Contains(authHTML, `href="/share/shares/4?browse=tree`) ||
		!strings.Contains(authHTML, `href="/share/shares/4/files/`) {
		t.Fatalf("authenticated tree html = %s, want /share content and navigation links", authHTML)
	}

	publicHTML := renderShareTemplate(t, treeTemplateData("grid", "sub", true))
	if !strings.Contains(publicHTML, `href="/w/public-link?browse=tree`) ||
		!strings.Contains(publicHTML, `href="/w/public-link/files/`) {
		t.Fatalf("anonymous tree html = %s, want /w content and navigation links", publicHTML)
	}
}

func TestTreeSidePanelRenderedOnlyInTreeMode(t *testing.T) {
	tree := renderShareTemplate(t, treeTemplateData("list", "", false))
	if !strings.Contains(tree, `data-folder-tree-panel`) || !strings.Contains(tree, `aria-label="Folders"`) {
		t.Fatalf("tree html = %s, want folder side panel", tree)
	}

	flat := renderShareTemplate(t, pageData{
		Title:          "Photos",
		Authenticated:  true,
		Share:          apiclient.Share{ID: 4, Name: "Photos"},
		Permissions:    []string{"read"},
		Page:           1,
		Count:          20,
		Total:          0,
		BasePath:       "/share/shares/4",
		APIBasePath:    "/api/share/shares/4",
		ThumbnailSizes: []int{128, 256},
		ThumbnailSize:  128,
		ViewMode:       "list",
		BrowseMode:     "flat",
		ShowPagination: true,
	})
	if strings.Contains(flat, `data-folder-tree-panel`) {
		t.Fatalf("flat html = %s, want no folder side panel", flat)
	}
}

func TestTreeSidePanelHighlightsCurrentFolderAndRendersNestedFolders(t *testing.T) {
	html := renderShareTemplate(t, treeTemplateData("list", "sub", false))
	for _, want := range []string{
		`data-tree-path="sub" class="active"`,
		`data-tree-path="sub/videos"`,
		`href="/share/shares/4?browse=tree&amp;order=asc&amp;path=sub%2Fvideos&amp;sort=name&amp;thumb=128&amp;view=list"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("tree side panel html = %s, want %q", html, want)
		}
	}
	if strings.Contains(html, `data-tree-path="sub/videos" class="active"`) {
		t.Fatalf("tree side panel html = %s, want only current folder active", html)
	}
}

func TestTreeSidePanelNavigationPreservesBrowseViewAndThumbnail(t *testing.T) {
	html := renderShareTemplate(t, treeTemplateData("grid", "sub", false))
	if !strings.Contains(html, `href="/share/shares/4?browse=tree&amp;order=asc&amp;path=sub%2Fvideos&amp;sort=name&amp;thumb=128&amp;view=grid"`) {
		t.Fatalf("tree side panel html = %s, want navigation preserving browse/view/thumb", html)
	}
}

func TestTreeSidePanelExpandCollapseStateHooks(t *testing.T) {
	html := renderShareTemplate(t, treeTemplateData("list", "", false))
	if !strings.Contains(html, `data-tree-toggle`) || !strings.Contains(html, `data-tree-node`) {
		t.Fatalf("tree side panel html = %s, want expand/collapse hooks", html)
	}

	mux := http.NewServeMux()
	if err := Register(mux, nil); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/share/static/share.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET share.js status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusOK)
	}
	for _, want := range []string{`folder_tree_collapsed`, `folder_tree_visible`, `data-tree-toggle`, `data-folder-panel-toggle`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("share.js = %s, want %q", rec.Body.String(), want)
		}
	}
}

func TestTreeModelFeedsMainContentAndSidePanel(t *testing.T) {
	html := renderShareTemplate(t, treeTemplateData("grid", "", false))
	if !strings.Contains(html, `gallery-folder`) || !strings.Contains(html, `data-folder-tree-panel`) {
		t.Fatalf("tree html = %s, want main gallery folders and side panel", html)
	}
	if !strings.Contains(html, `href="/share/shares/4?browse=tree&amp;order=asc&amp;path=sub&amp;sort=name&amp;thumb=128&amp;view=grid"`) {
		t.Fatalf("tree html = %s, want shared sub folder URL in rendered tree", html)
	}
}

func TestAuthenticatedUIContentRouteRequiresCookieAuth(t *testing.T) {
	runtime := newShareUIRuntime(t)
	mux := http.NewServeMux()
	if err := Register(mux, runtime); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/share/shares/4/files/10/content", nil)
	req.SetPathValue("id", "4")
	req.SetPathValue("file_id", "10")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET authenticated content without cookie status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusUnauthorized)
	}
}

func TestShareUILoginSetsHttpOnlyAuthCookies(t *testing.T) {
	runtime := newShareUIRuntime(t)
	mux := http.NewServeMux()
	if err := Register(mux, runtime); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	page := httptest.NewRecorder()
	mux.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/share", nil))
	var csrf *http.Cookie
	for _, cookie := range page.Result().Cookies() {
		if cookie.Name == shareUICSRFCookie {
			csrf = cookie
		}
	}
	if csrf == nil {
		t.Fatal("GET /share did not set CSRF cookie")
	}

	req := httptest.NewRequest(http.MethodPost, "/share/auth/login", strings.NewReader(`{"username":"alice","password":"secret"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", csrf.Value)
	req.AddCookie(csrf)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("POST login status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusNoContent)
	}
	if strings.Contains(rec.Body.String(), "access_token") || strings.Contains(rec.Body.String(), "refresh_token") {
		t.Fatalf("POST login exposed tokens: %s", rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	var accessCookie, refreshCookie *http.Cookie
	for _, cookie := range cookies {
		switch cookie.Name {
		case shareUIAccessCookie:
			accessCookie = cookie
		case shareUIRefreshCookie:
			refreshCookie = cookie
		}
	}
	if accessCookie == nil || refreshCookie == nil {
		t.Fatalf("cookies = %+v, want access and refresh cookies", cookies)
	}
	if !accessCookie.HttpOnly || !refreshCookie.HttpOnly || accessCookie.Path != "/share" || refreshCookie.Path != "/share" {
		t.Fatalf("cookies = %+v, want HttpOnly cookies scoped to /share", cookies)
	}
}

func TestShareUIDeletePostRedirectsToCanonicalSharePageAndGetStaysMethodNotAllowed(t *testing.T) {
	runtime := newShareUIRuntime(t)
	mux := http.NewServeMux()
	if err := Register(mux, runtime); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	form := url.Values{
		"version": {"2"},
		"page":    {"3"},
		"count":   {"40"},
		"thumb":   {"256"},
		"view":    {"grid"},
	}
	req := httptest.NewRequest(http.MethodPost, "/share/shares/5/files/216/delete", strings.NewReader(form.Encode()))
	req.AddCookie(&http.Cookie{Name: shareUIAccessCookie, Value: "user-token", Path: "/share"})
	req.AddCookie(&http.Cookie{Name: shareUICSRFCookie, Value: "csrf-token", Path: "/share"})
	req.Header.Set("X-CSRF-Token", "csrf-token")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST delete status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusSeeOther)
	}
	location := rec.Header().Get("Location")
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("Location %q parse error = %v", location, err)
	}
	if parsed.Path != "/share/shares/5" {
		t.Fatalf("Location path = %q, want /share/shares/5", parsed.Path)
	}
	query := parsed.Query()
	for key, want := range map[string]string{"page": "3", "count": "40", "thumb": "256", "view": "grid"} {
		if got := query.Get(key); got != want {
			t.Fatalf("Location %s = %q in %q, want %q", key, got, location, want)
		}
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/share/shares/5/files/216/delete", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET delete status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusMethodNotAllowed)
	}
}

func renderShareTemplate(t *testing.T, data pageData) string {
	t.Helper()
	if !data.HasEntries && (len(data.Files) > 0 || len(data.Folders) > 0 || data.ParentFolder != nil) {
		data.HasEntries = true
	}
	pages, err := template.New("shareui-test").Funcs(templateFuncs()).ParseFS(assets, "templates/*.html")
	if err != nil {
		t.Fatalf("ParseFS() error = %v", err)
	}
	var out bytes.Buffer
	if err := pages.ExecuteTemplate(&out, "share_files", data); err != nil {
		t.Fatalf("ExecuteTemplate(share_files) error = %v", err)
	}
	return out.String()
}

func treeTemplateData(viewMode string, treePath string, public bool) pageData {
	files := []apiclient.ReplicaInventoryFile{
		{FileID: 1, RelativeURI: "image01.jpg", Size: 100, InventoryVersion: 1, Modified: time.Date(2026, 3, 17, 10, 30, 0, 0, time.UTC)},
		{FileID: 2, RelativeURI: "image02.jpg", Size: 100, InventoryVersion: 1, Modified: time.Date(2026, 3, 17, 10, 31, 0, 0, time.UTC)},
		{FileID: 3, RelativeURI: "sub/image03.jpg", Size: 100, InventoryVersion: 1, Modified: time.Date(2026, 3, 17, 10, 32, 0, 0, time.UTC)},
		{FileID: 4, RelativeURI: "sub/image04.jpg", Size: 100, InventoryVersion: 1, Modified: time.Date(2026, 3, 17, 10, 33, 0, 0, time.UTC)},
		{FileID: 5, RelativeURI: "sub/videos/video01.mp4", Size: 100, InventoryVersion: 1, Modified: time.Date(2026, 3, 17, 10, 34, 0, 0, time.UTC)},
	}
	model := buildTreeModel(files)
	cleanPath := cleanTreePath(treePath)
	node := model.folder(cleanPath)
	if node == nil {
		node = model.Root
		cleanPath = ""
	}
	basePath := "/share/shares/4"
	apiBasePath := "/api/share/shares/4"
	authenticated := true
	if public {
		basePath = "/w/public-link"
		apiBasePath = "/s/public-link"
		authenticated = false
	}
	folders := treeFolderViews(node.folderEntries(), basePath, viewMode, 128, "name", "asc")
	panel := treePanelFromModel(model, basePath, viewMode, 128, cleanPath, "name", "asc")
	var parent *treeFolderView
	if cleanPath != "" {
		parentPath := parentTreePath(cleanPath)
		parent = &treeFolderView{Name: "Parent folder", Path: parentPath, URL: browseURL(basePath, browseModeTree, viewMode, parentPath, 128, "name", "asc"), IsParent: true}
	}
	return pageData{
		Title:          "Photos",
		Authenticated:  authenticated,
		Public:         public,
		Share:          apiclient.Share{ID: 4, Name: "Photos"},
		Files:          fileViews(node.Files, apiBasePath, basePath, 128, authenticated, config.SharingConfig{}),
		Permissions:    []string{"read"},
		Page:           1,
		Count:          20,
		Total:          int64(len(files)),
		BasePath:       basePath,
		APIBasePath:    apiBasePath,
		ThumbnailSizes: []int{128, 256},
		ThumbnailSize:  128,
		ViewMode:       viewMode,
		BrowseMode:     "tree",
		Sort:           "name",
		Order:          "asc",
		TreePath:       cleanPath,
		ParentFolder:   parent,
		Folders:        folders,
		TreePanel:      &panel,
		HasEntries:     parent != nil || len(folders) > 0 || len(node.Files) > 0,
		ShowPagination: false,
	}
}

func newShareUIRuntime(t *testing.T) *storage.Runtime {
	t.Helper()
	coordinator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/admin/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"user_id":                  15,
				"access_token":             "user-access-token",
				"refresh_token":            "user-refresh-token",
				"access_token_expires_at":  time.Now().UTC().Add(time.Hour),
				"refresh_token_expires_at": time.Now().UTC().Add(2 * time.Hour),
			})
		case "/node/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node_id":                  "node-a",
				"access_token":             "node-token",
				"refresh_token":            "refresh-token",
				"access_token_expires_at":  time.Now().UTC().Add(time.Hour),
				"refresh_token_expires_at": time.Now().UTC().Add(2 * time.Hour),
			})
		case "/node/auth/validate-user-token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"user_id":                 15,
				"username":                "jsmith",
				"status":                  "active",
				"access_token_expires_at": time.Now().UTC().Add(time.Hour),
			})
		default:
			t.Fatalf("unexpected coordinator path %q", r.URL.Path)
		}
	}))
	t.Cleanup(coordinator.Close)

	runtime, err := storage.NewRuntime(config.Config{
		App: config.AppConfig{
			NodeID:            "node-a",
			NodeAddress:       "http://node-a",
			CoordinatorURL:    coordinator.URL,
			HeartbeatInterval: time.Minute,
		},
		Auth: config.AuthConfig{
			NodeSecret:                 "secret",
			ShareAPITokenCacheDuration: 5 * time.Minute,
		},
		Sharing: config.SharingConfig{
			Enabled:                    true,
			ThumbnailSizes:             []int{128, 256},
			ThumbnailDefaultSize:       128,
			ThumbnailsGenerateForVideo: false,
			FfmpegPath:                 "ffmpeg",
			ThumbnailStorage:           t.TempDir(),
			ThumbnailStorageLimitMB:    500,
		},
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	return runtime
}
