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
    for (const button of document.querySelectorAll("[data-share-theme-toggle] [data-theme]")) {
      const active = button.dataset.theme === selected;
      button.classList.toggle("active", active);
      button.setAttribute("aria-pressed", active ? "true" : "false");
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
      const button = event.target.closest("[data-share-theme-toggle] [data-theme]");
      if (!button) {
        return;
      }
      const theme = button.dataset.theme === "dark" ? "dark" : "light";
      localStorage.setItem(themeKey, theme);
      applyTheme(theme);
    });
  }

  function bindUploadFilenamePrefill() {
    document.body.addEventListener("change", (event) => {
      const input = event.target;
      if (!(input instanceof HTMLInputElement) || input.type !== "file" || input.name !== "file") {
        return;
      }
      const form = input.closest(".upload-form");
      if (!form) {
        return;
      }
      const relativeURI = form.querySelector("input[name=relative_uri]");
      if (!(relativeURI instanceof HTMLInputElement)) {
        return;
      }
      const fileName = input.files?.[0]?.name || input.value.split(/[/\\]/).pop() || "";
      if (fileName) {
        relativeURI.value = fileName;
      }
    });
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
    window.addEventListener("popstate", () => showPage(window.location.href, undefined, false));
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
    const allowedThumbs = new Set([...viewRoot.querySelectorAll('select[name="thumb"] option')].map((option) => option.value));

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
      const view = event.target.closest("[data-share-view-toggle] [data-view-mode]");
      if (view) {
        localStorage.setItem(prefix + "file_view_mode", view.dataset.viewMode);
        return;
      }
      const browse = event.target.closest("[data-share-browse-toggle] [data-browse-mode]");
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

  document.body.addEventListener("htmx:configRequest", (event) => {
    event.detail.headers["X-API-Version"] = "1";
    if (token("access_token") && !event.detail.path.startsWith("/share")) {
      event.detail.headers.Authorization = `Bearer ${token("access_token")}`;
    }
  });

  bindTheme();
  bindFilePreferenceControls();

  if (document.body.dataset.shareAuthenticated === "true") {
    bindUploadFilenamePrefill();
    bindAuthenticatedPage();
  } else if (document.querySelector("[data-share-login-form]")) {
    bootstrapLogin();
  } else {
    bindUploadFilenamePrefill();
    bindPublicPage();
  }
})();
