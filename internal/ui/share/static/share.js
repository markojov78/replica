(() => {
  const keys = [
    "access_token",
    "refresh_token",
    "access_token_expires_at",
    "refresh_token_expires_at",
  ];
  const prefix = "replica_share_";
  let refreshPromise;

  function token(name) {
    return localStorage.getItem(prefix + name) || "";
  }

  function storeTokens(pair) {
    for (const key of keys) {
      if (pair[key]) {
        localStorage.setItem(prefix + key, pair[key]);
      }
    }
    if (pair.user_id) {
      localStorage.setItem(prefix + "user_id", pair.user_id);
    }
    if (pair.username) {
      localStorage.setItem(prefix + "username", pair.username);
    }
  }

  function clearTokens() {
    for (const key of keys) {
      localStorage.removeItem(prefix + key);
    }
    localStorage.removeItem(prefix + "user_id");
    localStorage.removeItem(prefix + "username");
  }

  async function refresh() {
    if (!refreshPromise) {
      refreshPromise = (async () => {
        const refreshToken = token("refresh_token");
        if (!refreshToken) {
          throw new Error("missing refresh token");
        }
        const response = await fetch("/api/share/auth/refresh", {
          method: "POST",
          headers: {"Content-Type": "application/json", "X-API-Version": "1"},
          body: JSON.stringify({refresh_token: refreshToken}),
        });
        if (!response.ok) {
          throw new Error("refresh failed");
        }
        storeTokens(await response.json());
      })().finally(() => {
        refreshPromise = undefined;
      });
    }
    return refreshPromise;
  }

  async function request(path, options = {}, retry = true) {
    const url = new URL(path, window.location.origin);
    const headers = new Headers(options.headers);
    headers.set("X-API-Version", "1");
    if (!url.pathname.startsWith("/share") && token("access_token")) {
      headers.set("Authorization", `Bearer ${token("access_token")}`);
    }
    const response = await fetch(path, {...options, headers});
    if (url.pathname.startsWith("/share") || response.status !== 401 || !retry) {
      return response;
    }
    try {
      await refresh();
    } catch {
      clearTokens();
      window.location.replace("/share");
      return undefined;
    }
    return request(path, options, false);
  }

  async function showPage(path, options, pushState = true) {
    const response = await request(path, options);
    if (!response) {
      return;
    }
    if (response.status === 401) {
      clearTokens();
      window.location.replace("/share");
      return;
    }
    const html = await response.text();
    if (pushState) {
      history.pushState({}, "", response.url);
    }
    document.open();
    document.write(html);
    document.close();
  }

  async function login(form) {
    const response = await fetch(form.action, {
      method: "POST",
      headers: {"Content-Type": "application/json", "X-API-Version": "1"},
      body: JSON.stringify({
        username: form.elements.username.value.trim(),
        password: form.elements.password.value,
      }),
    });
    if (!response.ok) {
      const problem = await response.json().catch(() => ({}));
      const error = document.querySelector("[data-share-login-error]");
      if (error) {
        error.textContent = problem.detail || problem.error || problem.title || "Sign in failed.";
      }
      return;
    }
    storeTokens(await response.json());
    localStorage.setItem(prefix + "username", form.elements.username.value.trim());
    window.location.replace("/share/shares");
  }

  async function bootstrapLogin() {
    const form = document.querySelector("[data-share-login-form]");
    form?.addEventListener("submit", (event) => {
      event.preventDefault();
      login(form);
    });
    if (!token("access_token")) {
      clearTokens();
      return;
    }
    const response = await request("/share/auth/me");
    if (response?.ok) {
      storeCurrentUser(await response.json());
      const current = window.location.pathname === "/share" ? "/share/shares" : window.location.href;
      await showPage(current, undefined, false);
    }
  }

  function storeCurrentUser(user) {
    if (user.user_id) {
      localStorage.setItem(prefix + "user_id", user.user_id);
    }
    if (user.username) {
      localStorage.setItem(prefix + "username", user.username);
    }
  }

  function fillCurrentUser() {
    const username = localStorage.getItem(prefix + "username") || "";
    const userID = localStorage.getItem(prefix + "user_id") || "";
    for (const element of document.querySelectorAll("[data-share-current-username]")) {
      element.textContent = username || (userID ? `User #${userID}` : "");
    }
  }

  function applyTheme(theme) {
    const selected = theme === "dark" ? "dark" : "light";
    document.documentElement.dataset.theme = selected;
    for (const button of document.querySelectorAll("[data-share-theme-toggle]")) {
      const next = selected === "dark" ? "light" : "dark";
      const label = `Switch to ${next} theme`;
      button.dataset.theme = next;
      button.setAttribute("aria-label", label);
      button.setAttribute("title", label);
    }
  }

  function bindTheme() {
    const themeKey = prefix + "theme";
    const viewRoot = document.querySelector("[data-share-file-view]");
    const storedTheme = localStorage.getItem(themeKey);
    if (storedTheme !== "light" && storedTheme !== "dark") {
      if (storedTheme) {
        localStorage.removeItem(themeKey);
      }
      applyTheme(viewRoot?.dataset.defaultTheme);
    } else {
      applyTheme(storedTheme);
    }
    document.body.addEventListener("click", (event) => {
      const button = event.target.closest("[data-share-theme-toggle]");
      if (!button) {
        return;
      }
      const theme = button.dataset.theme === "dark" ? "dark" : "light";
      localStorage.setItem(themeKey, theme);
      applyTheme(theme);
    });
  }

  function bindUploadForm() {
    document.body.addEventListener("change", (event) => {
      const input = event.target;
      if (!(input instanceof HTMLInputElement) || !input.matches('.upload-form input[type="file"][name="file"]')) {
        return;
      }
      const form = input.closest(".upload-form");
      if (form instanceof HTMLFormElement) {
        uploadSelectedFiles(form);
      }
    });
  }

  async function uploadSelectedFiles(form) {
    const input = form.querySelector('input[type="file"][name="file"]');
    const files = input instanceof HTMLInputElement ? [...(input.files || [])] : [];
    if (!files.length) {
      return;
    }

    const error = document.querySelector("[data-upload-error]");
    if (error) {
      error.hidden = true;
      error.textContent = "";
    }
    input.disabled = true;

    const listURL = new URL(form.dataset.uploadAction || form.action, window.location.origin);
    listURL.searchParams.set("page", "1");
    listURL.searchParams.set("count", "1");
    const initialTotal = await currentShareTotal(listURL);
    if (initialTotal === null) {
      input.disabled = false;
      input.value = "";
      if (error) {
        error.textContent = "Unable to determine the current file count.";
        error.hidden = false;
      }
      return;
    }

    const prefix = (form.dataset.uploadPrefix || "").replace(/^\/+|\/+$/g, "");
    for (const file of files) {
      const body = new FormData();
      body.set("relative_uri", prefix ? `${prefix}/${file.name}` : file.name);
      body.set("file", file, file.name);
      let response;
      try {
        response = await request(form.dataset.uploadAction || form.action, {method: "POST", body});
      } catch {
        input.disabled = false;
        input.value = "";
        if (error) {
          error.textContent = `Upload failed for ${file.name}.`;
          error.hidden = false;
        }
        return;
      }
      if (!response || !response.ok) {
        input.disabled = false;
        input.value = "";
        if (error && response) {
          const problem = await response.json().catch(() => ({}));
          const message = problem.detail || problem.error || problem.title || "Upload failed.";
          error.textContent = `${file.name}: ${message}`;
          error.hidden = false;
        }
        return;
      }
    }
    const expectedTotal = initialTotal + files.length;
    for (const delaySeconds of [0, 1, 2, 4, 8, 16, 32]) {
      if (delaySeconds) {
        await new Promise((resolve) => setTimeout(resolve, delaySeconds * 1000));
      }
      const total = await currentShareTotal(listURL);
      if (total !== null && total >= expectedTotal) {
        window.location.reload();
        return;
      }
    }
    input.disabled = false;
    input.value = "";
    if (error) {
      error.textContent = "Uploads were accepted, but the files did not appear in the share within the expected time.";
      error.hidden = false;
    }
  }

  async function currentShareTotal(listURL) {
    try {
      const response = await request(listURL.toString());
      if (!response?.ok) {
        return null;
      }
      const total = Number((await response.json()).total);
      return Number.isFinite(total) ? total : null;
    } catch {
      return null;
    }
  }

  function bindAuthenticatedPage() {
    if (applyFileViewPreferences()) {
      return;
    }
    bindActionsMenus();
    bindFolderTreePanel();
    fillCurrentUser();
    request("/share/auth/me")
      .then(async (response) => {
        if (!response?.ok) {
          return;
        }
        storeCurrentUser(await response.json());
        fillCurrentUser();
      })
      .catch(() => {});
    document.body.addEventListener("click", (event) => {
      const download = event.target.closest("[data-auth-download]");
      if (download) {
        event.preventDefault();
        authenticatedDownload(download.dataset.authDownload, download.dataset.filename || "download");
        return;
      }
      const link = event.target.closest("a[href]");
      if (!link || link.origin !== window.location.origin || !link.pathname.startsWith("/share")) {
        return;
      }
      if (link.pathname.endsWith("/content")) {
        return;
      }
      event.preventDefault();
      showPage(link.href);
    });
    document.body.addEventListener("submit", (event) => {
      const form = event.target;
      if (!(form instanceof HTMLFormElement)) {
        return;
      }
      event.preventDefault();
      if (form.matches("[data-share-logout]")) {
        clearTokens();
        fetch(form.action, {method: "POST", headers: {"X-API-Version": "1"}}).finally(() => {
          window.location.replace("/share");
        });
        return;
      }
      if (!form.action.startsWith(window.location.origin + "/share")) {
        return;
      }
      const method = form.method.toUpperCase();
      const body = method === "GET" ? undefined : new FormData(form);
      const path = method === "GET" ? `${form.action}?${new URLSearchParams(new FormData(form))}` : form.action;
      showPage(path, {method, body});
    });
    window.addEventListener("popstate", () => {
      if (window.replicaPreviewHistoryHandled) {
        window.replicaPreviewHistoryHandled = false;
        return;
      }
      showPage(window.location.href, undefined, false);
    });
    loadAuthenticatedThumbnails();
  }

  function bindPublicPage() {
    if (applyFileViewPreferences()) {
      return;
    }
    bindActionsMenus();
    bindFolderTreePanel();
    loadPublicThumbnails();
  }

  function bindActionsMenus() {
    document.body.addEventListener("click", (event) => {
      const menuButton = event.target.closest("[data-actions-menu-button]");
      if (menuButton) {
        event.preventDefault();
        const menu = menuButton.closest("[data-actions-menu]");
        const popover = menu?.querySelector("[data-actions-menu-popover]");
        if (!menu || !popover) {
          return;
        }
        const opening = popover.hidden;
        closeActionsMenus(menu);
        popover.hidden = !opening;
        menuButton.setAttribute("aria-expanded", opening ? "true" : "false");
        return;
      }
      if (!event.target.closest("[data-actions-menu]")) {
        closeActionsMenus();
      }
    });
    document.body.addEventListener("keydown", (event) => {
      if (event.key === "Escape") {
        closeActionsMenus();
      }
    });
  }

  function closeActionsMenus(except) {
    for (const menu of document.querySelectorAll("[data-actions-menu]")) {
      if (except && menu === except) {
        continue;
      }
      const popover = menu.querySelector("[data-actions-menu-popover]");
      const button = menu.querySelector("[data-actions-menu-button]");
      if (popover) {
        popover.hidden = true;
      }
      if (button) {
        button.setAttribute("aria-expanded", "false");
      }
    }
  }

  async function authenticatedDownload(path, filename) {
    const response = await request(path);
    if (!response?.ok) {
      return;
    }
    const blob = await response.blob();
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = filename;
    document.body.appendChild(link);
    link.click();
    link.remove();
    URL.revokeObjectURL(url);
  }

  async function loadAuthenticatedThumbnails() {
    for (const target of document.querySelectorAll("[data-auth-src]")) {
      const response = await request(target.dataset.authSrc);
      if (!response?.ok) {
        continue;
      }
      const blobURL = URL.createObjectURL(await response.blob());
      const image = document.createElement("img");
      image.className = target.classList.contains("grid-auth-thumb") ? "grid-thumb" : "thumb";
      image.alt = "";
      image.src = blobURL;
      target.replaceWith(image);
    }
  }

  function loadPublicThumbnails() {
    for (const image of document.querySelectorAll("img[data-public-src]")) {
      image.src = image.dataset.publicSrc;
      image.removeAttribute("data-public-src");
    }
  }

  function applyFileViewPreferences() {
    const viewRoot = document.querySelector("[data-share-file-view]");
    if (!viewRoot) {
      return false;
    }
    const viewKey = prefix + "file_view_mode";
    const browseKey = prefix + "file_browse_mode";
    const thumbKey = prefix + "thumbnail_size";
    const pageSizeKey = prefix + "page_size";
    const params = new URLSearchParams(window.location.search);
    const current = viewRoot.dataset.viewMode === "grid" ? "grid" : "list";
    const currentBrowse = viewRoot.dataset.browseMode === "tree" ? "tree" : "flat";
    const currentThumb = viewRoot.dataset.thumbnailSize || "";
    const currentPageSize = viewRoot.dataset.pageSize || "20";
    const defaultView = viewRoot.dataset.defaultView || "";
    const defaultThumb = viewRoot.dataset.defaultThumbnailSize || "";
    const defaultPageSize = viewRoot.dataset.defaultPageSize || "";
    const allowedThumbs = new Set([...document.querySelectorAll('select[name="thumb"] option')].map((option) => option.value));

    let storedView = localStorage.getItem(viewKey);
    let storedBrowse = localStorage.getItem(browseKey);
    let storedThumb = localStorage.getItem(thumbKey);
    let storedPageSize = localStorage.getItem(pageSizeKey);
    let changed = false;

    if (storedView !== "grid" && storedView !== "list") {
      if (storedView) {
        localStorage.removeItem(viewKey);
      }
      storedView = "";
    }
    if (!params.has("view")) {
      const preferredView = storedView || (defaultView === "grid" || defaultView === "list" ? defaultView : "");
      if (preferredView && preferredView !== current) {
        params.set("view", preferredView);
        changed = true;
      }
    }

    if (storedBrowse !== "tree" && storedBrowse !== "flat") {
      if (storedBrowse) {
        localStorage.removeItem(browseKey);
      }
      storedBrowse = "";
    } else if (!params.has("browse") && storedBrowse !== currentBrowse) {
      params.set("browse", storedBrowse);
      changed = true;
    }

    if (!storedThumb || !/^\d+$/.test(storedThumb) || !allowedThumbs.has(storedThumb)) {
      if (storedThumb) {
        localStorage.removeItem(thumbKey);
      }
      storedThumb = "";
    }
    if (!params.has("thumb")) {
      const preferredThumb = storedThumb || (allowedThumbs.has(defaultThumb) ? defaultThumb : "");
      if (preferredThumb && preferredThumb !== currentThumb) {
        params.set("thumb", preferredThumb);
        changed = true;
      }
    }

    if (!storedPageSize || !/^\d+$/.test(storedPageSize) || Number(storedPageSize) < 1) {
      if (storedPageSize) {
        localStorage.removeItem(pageSizeKey);
      }
      storedPageSize = "";
    }
    if (!params.has("count")) {
      const validDefaultPageSize = /^\d+$/.test(defaultPageSize) && Number(defaultPageSize) > 0 ? defaultPageSize : "";
      const preferredPageSize = storedPageSize || validDefaultPageSize;
      if (preferredPageSize && preferredPageSize !== currentPageSize) {
        params.set("count", preferredPageSize);
        changed = true;
      }
    }

    if (!changed) {
      return false;
    }
    window.location.replace(`${window.location.pathname}?${params.toString()}`);
    return true;
  }

  function bindFilePreferenceControls() {
    document.body.addEventListener("click", (event) => {
      const view = event.target.closest("[data-share-view-toggle][data-view-mode]");
      if (view) {
        localStorage.setItem(prefix + "file_view_mode", view.dataset.viewMode);
        return;
      }
      const browse = event.target.closest("[data-share-browse-toggle][data-browse-mode]");
      if (browse) {
        localStorage.setItem(prefix + "file_browse_mode", browse.dataset.browseMode);
      }
    });
    document.body.addEventListener("change", (event) => {
      const select = event.target;
      if (!(select instanceof HTMLSelectElement)) {
        return;
      }
      if (select.name === "thumb") {
        localStorage.setItem(prefix + "thumbnail_size", select.value);
      } else if (select.name === "count") {
        localStorage.setItem(prefix + "page_size", select.value);
      }
    });
  }

  function bindFolderTreePanel() {
    const layout = document.querySelector("[data-folder-tree-layout]");
    if (!layout) {
      return;
    }
    const root = layout.closest("[data-share-file-view]") || layout;
    const visibilityKey = prefix + "folder_tree_visible";
    const collapsedKey = prefix + "folder_tree_collapsed";

    const storedVisible = localStorage.getItem(visibilityKey);
    if (storedVisible === "false") {
      layout.classList.add("tree-panel-hidden");
      root.classList.add("tree-panel-hidden");
    } else if (storedVisible === "true") {
      layout.classList.add("tree-panel-open");
      root.classList.add("tree-panel-open");
    }

    let collapsed = new Set(JSON.parse(localStorage.getItem(collapsedKey) || "[]"));
    for (const node of layout.querySelectorAll("[data-tree-node]")) {
      if (collapsed.has(node.dataset.treePath || "")) {
        node.classList.add("collapsed");
      }
    }

    document.body.addEventListener("click", (event) => {
      const panelToggle = event.target.closest("[data-folder-panel-toggle]");
      if (panelToggle) {
        event.preventDefault();
        const hidden = layout.classList.toggle("tree-panel-hidden");
        layout.classList.toggle("tree-panel-open", !hidden);
        root.classList.toggle("tree-panel-hidden", hidden);
        root.classList.toggle("tree-panel-open", !hidden);
        localStorage.setItem(visibilityKey, hidden ? "false" : "true");
        return;
      }

      const treeToggle = event.target.closest("[data-tree-toggle]");
      if (!treeToggle) {
        return;
      }
      event.preventDefault();
      const node = treeToggle.closest("[data-tree-node]");
      if (!node) {
        return;
      }
      const nodePath = node.dataset.treePath || "";
      node.classList.toggle("collapsed");
      collapsed = new Set(JSON.parse(localStorage.getItem(collapsedKey) || "[]"));
      if (node.classList.contains("collapsed")) {
        collapsed.add(nodePath);
      } else {
        collapsed.delete(nodePath);
      }
      localStorage.setItem(collapsedKey, JSON.stringify([...collapsed]));
    });
  }

  function bindPreviewViewer() {
    let current;
    let opener;
    let scrollY = 0;
    let contextURL;
    let resultMode = "tree";
    let pageSize = 20;
    let total = 0;
    let loading = false;
    let retryOffset = 0;
    const pages = new Map();
    const knownIDs = new Set();

    const dialog = () => document.querySelector("[data-preview-dialog]");
    const items = () => [...document.querySelectorAll("[data-preview-item]")];

    function itemData(item, page) {
      return {
        fileID: item.dataset.fileId,
        fileName: item.dataset.fileName,
        fileType: item.dataset.fileType,
        fileSize: item.dataset.fileSize,
        contentURL: item.dataset.contentUrl,
        previewKind: item.dataset.previewKind,
        page,
        element: item.isConnected ? item : undefined,
      };
    }

    function initializeSequence() {
      const root = document.querySelector("[data-share-file-view]");
      if (!root) {
        return;
      }
      resultMode = root.dataset.browseMode === "flat" ? "flat" : "tree";
      pageSize = Number(root.dataset.pageSize) || 20;
      total = Number(root.dataset.resultTotal) || 0;
      const page = Number(root.dataset.resultPage) || 1;
      contextURL = new URL(window.location.href);
      contextURL.searchParams.delete("preview");
      contextURL.searchParams.delete("preview_page");
      const pageItems = items().map((item) => itemData(item, page));
      pages.clear();
      knownIDs.clear();
      pages.set(page, pageItems);
      for (const item of pageItems) {
        knownIDs.add(item.fileID);
      }
    }

    function inlinePage(page) {
      return (pages.get(page) || []).filter((item) => item.previewKind !== "fallback");
    }

    function totalPages() {
      return Math.max(1, Math.ceil(total / pageSize));
    }

    function previewURL(fileID, page) {
      const url = new URL(window.location.href);
      if (fileID) {
        url.searchParams.set("preview", fileID);
        if (page) {
          url.searchParams.set("preview_page", page);
        }
      } else {
        url.searchParams.delete("preview");
        url.searchParams.delete("preview_page");
      }
      return `${url.pathname}${url.search}${url.hash}`;
    }

    function setStatus(message, retry = false) {
      const status = dialog()?.querySelector("[data-preview-status]");
      if (!status) {
        return;
      }
      status.replaceChildren();
      if (!message) {
        return;
      }
      status.append(document.createTextNode(message));
      if (retry) {
        const button = document.createElement("button");
        button.type = "button";
        button.className = "btn";
        button.dataset.previewRetry = "";
        button.textContent = "Retry";
        status.append(button);
      }
    }

    function updateControls() {
      const modal = dialog();
      if (!modal || !current) {
        return;
      }
      const pageItems = inlinePage(current.page);
      const index = pageItems.findIndex((item) => item.fileID === current.fileID);
      const previousAvailable = index > 0 || (resultMode === "flat" && current.page > 1);
      const nextAvailable = index >= 0 && (index < pageItems.length - 1 || (resultMode === "flat" && current.page < totalPages()));
      modal.querySelector("[data-preview-previous]").disabled = loading || !previousAvailable;
      modal.querySelector("[data-preview-next]").disabled = loading || !nextAvailable;
    }

    function render(item) {
      const modal = dialog();
      if (!modal || !item) {
        close(false);
        return;
      }
      current = item;
      const content = modal.querySelector("[data-preview-content]");
      const kind = item.previewKind;
      const url = item.contentURL;
      content.replaceChildren();
      let media;
      if (kind === "image") {
        media = document.createElement("img");
        media.alt = item.fileName || "File preview";
        media.src = url;
      } else if (kind === "video") {
        media = document.createElement("video");
        media.controls = true;
        media.src = url;
      } else if (kind === "audio") {
        media = document.createElement("audio");
        media.controls = true;
        media.src = url;
      } else if (kind === "pdf") {
        media = document.createElement("iframe");
        media.title = `PDF preview: ${item.fileName || "file"}`;
        media.src = url;
      } else {
        media = document.createElement("div");
        media.className = "preview-fallback";
        const name = document.createElement("strong");
        name.textContent = item.fileName || "File";
        const details = document.createElement("p");
        details.textContent = [item.fileType, item.fileSize].filter(Boolean).join(" · ");
        const original = document.createElement("a");
        original.className = "btn primary";
        original.href = url;
        original.textContent = "Download / open original";
        media.append(name, details, original);
      }
      content.append(media);
      modal.querySelector("[data-preview-filename]").textContent = item.fileName || "File preview";
      setStatus("");
      updateControls();
    }

    function open(item, addHistory = true) {
      const modal = dialog();
      if (!modal) {
        return;
      }
      if (!contextURL) {
        initializeSequence();
      }
      const data = item instanceof Element ? itemData(item, Number(document.querySelector("[data-share-file-view]")?.dataset.resultPage) || 1) : item;
      if (modal.hidden) {
        opener = data.element;
        scrollY = window.scrollY;
        modal.hidden = false;
        document.body.classList.add("preview-open");
      }
      render(data);
      if (addHistory) {
        history.pushState({...history.state, replicaPreview: true}, "", previewURL(data.fileID, data.page));
      }
      modal.querySelector("[data-preview-close]").focus();
    }

    function close(useHistory = true) {
      const modal = dialog();
      if (!modal || modal.hidden) {
        return;
      }
      if (useHistory && history.state?.replicaPreview) {
        history.back();
        return;
      }
      if (new URL(window.location.href).searchParams.has("preview")) {
        history.replaceState(history.state, "", previewURL(""));
      }
      modal.hidden = true;
      modal.querySelector("[data-preview-content]")?.replaceChildren();
      document.body.classList.remove("preview-open");
      current = undefined;
      window.scrollTo(0, scrollY);
      if (opener?.isConnected) {
        opener.focus();
      }
      opener = undefined;
      pages.clear();
      knownIDs.clear();
      contextURL = undefined;
    }

    async function loadPage(page) {
      if (pages.has(page)) {
        return pages.get(page);
      }
      const url = new URL(contextURL);
      url.searchParams.set("page", page);
      const response = await request(url.toString());
      if (!response?.ok) {
        throw new Error("page request failed");
      }
      const parsed = new DOMParser().parseFromString(await response.text(), "text/html");
      const root = parsed.querySelector("[data-share-file-view]");
      if (!root || root.dataset.browseMode !== resultMode) {
        throw new Error("result context changed");
      }
      total = Number(root.dataset.resultTotal) || 0;
      const resultPage = Number(root.dataset.resultPage) || page;
      const result = [];
      for (const element of parsed.querySelectorAll("[data-preview-item]")) {
        const data = itemData(element, resultPage);
        if (knownIDs.has(data.fileID)) {
          continue;
        }
        knownIDs.add(data.fileID);
        result.push(data);
      }
      pages.set(resultPage, result);
      return result;
    }

    async function move(offset) {
      if (!current || loading || current.previewKind === "fallback") {
        return;
      }
      const currentItems = inlinePage(current.page);
      const index = currentItems.findIndex((item) => item.fileID === current.fileID);
      let next = currentItems[index + offset];
      let page = current.page;
      loading = true;
      retryOffset = offset;
      setStatus(next ? "" : "Loading files…");
      updateControls();
      try {
        while (!next && resultMode === "flat") {
          page += offset;
          if (page < 1 || page > totalPages()) {
            break;
          }
          await loadPage(page);
          const candidates = inlinePage(page);
          next = offset > 0 ? candidates[0] : candidates[candidates.length - 1];
        }
        if (next) {
          render(next);
          history.replaceState({...history.state, replicaPreview: true}, "", previewURL(next.fileID, next.page));
        }
        setStatus("");
      } catch {
        setStatus("Unable to load more files.", true);
      } finally {
        loading = false;
        updateControls();
      }
    }

    document.addEventListener("click", (event) => {
      const item = event.target.closest("[data-preview-item]");
      if (item) {
        event.preventDefault();
        open(item);
        return;
      }
      if (event.target.closest("[data-preview-close]")) {
        close();
      } else if (event.target.closest("[data-preview-previous]")) {
        move(-1);
      } else if (event.target.closest("[data-preview-next]")) {
        move(1);
      } else if (event.target.closest("[data-preview-retry]")) {
        move(retryOffset);
      } else if (event.target.matches("[data-preview-dialog]")) {
        close();
      }
    });

    document.addEventListener("keydown", (event) => {
      const modal = dialog();
      if (!modal || modal.hidden) {
        return;
      }
      if (event.key === "Escape") {
        event.preventDefault();
        close();
      } else if (event.key === "ArrowLeft") {
        event.preventDefault();
        move(-1);
      } else if (event.key === "ArrowRight") {
        event.preventDefault();
        move(1);
      } else if (event.key === "Tab") {
        const focusable = [...modal.querySelectorAll('button:not([disabled]),a[href],video[controls],audio[controls],iframe')];
        if (!focusable.length) {
          event.preventDefault();
          return;
        }
        const first = focusable[0];
        const last = focusable[focusable.length - 1];
        if (event.shiftKey && document.activeElement === first) {
          event.preventDefault();
          last.focus();
        } else if (!event.shiftKey && document.activeElement === last) {
          event.preventDefault();
          first.focus();
        }
      }
    });

    async function restoreURLPreview(fromPopState = false) {
      const fileID = new URL(window.location.href).searchParams.get("preview");
      if (fromPopState && (current || fileID)) {
        window.replicaPreviewHistoryHandled = true;
      }
      let item = fileID && ([...pages.values()].flat().find((candidate) => candidate.fileID === fileID) || items().find((candidate) => candidate.dataset.fileId === fileID));
      const previewPage = Number(new URL(window.location.href).searchParams.get("preview_page"));
      if (!item && fileID && resultMode === "flat" && previewPage > 0 && previewPage <= totalPages()) {
        try {
          await loadPage(previewPage);
          item = (pages.get(previewPage) || []).find((candidate) => candidate.fileID === fileID);
        } catch {
          setStatus("Unable to load the linked file.", true);
        }
      }
      if (item) {
        open(item, false);
      } else {
        close(false);
      }
    }

    window.addEventListener("popstate", () => {
      restoreURLPreview(true);
    });

    document.addEventListener("htmx:beforeSwap", () => close(false));
    initializeSequence();
    restoreURLPreview();
  }

  document.body.addEventListener("htmx:configRequest", (event) => {
    event.detail.headers["X-API-Version"] = "1";
    if (token("access_token") && !event.detail.path.startsWith("/share")) {
      event.detail.headers.Authorization = `Bearer ${token("access_token")}`;
    }
  });

  document.addEventListener("htmx:afterSwap", () => {
    loadPublicThumbnails();
  });

  bindTheme();
  bindFilePreferenceControls();
  bindPreviewViewer();

  if (document.body.dataset.shareAuthenticated === "true") {
    bindUploadForm();
    bindAuthenticatedPage();
  } else if (document.querySelector("[data-share-login-form]")) {
    bootstrapLogin();
  } else {
    bindUploadForm();
    bindPublicPage();
  }
})();
