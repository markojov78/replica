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

  function authHeaders(headers = new Headers()) {
    headers.set("X-API-Version", "1");
    if (token("access_token")) {
      headers.set("Authorization", `Bearer ${token("access_token")}`);
    }
    return headers;
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
    const headers = authHeaders(new Headers(options.headers));
    const response = await fetch(path, {...options, headers});
    if (response.status !== 401 || !retry) {
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
    const response = await fetch("/api/share/auth/login", {
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
    const response = await request("/api/share/auth/me");
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

  function bindAuthenticatedPage() {
    applyFileViewPreferences();
    fillCurrentUser();
    request("/api/share/auth/me")
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
        window.location.replace("/share");
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
    applyFileViewPreferences();
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

  function applyFileViewPreferences() {
    const viewRoot = document.querySelector("[data-share-file-view]");
    if (!viewRoot) {
      return;
    }
    const viewKey = prefix + "file_view_mode";
    const thumbKey = prefix + "thumbnail_size";
    const params = new URLSearchParams(window.location.search);
    const current = viewRoot.dataset.viewMode === "grid" ? "grid" : "list";
    const currentThumb = viewRoot.dataset.thumbnailSize || "";
    if (params.has("view")) {
      localStorage.setItem(viewKey, current);
    }
    if (params.has("thumb") && currentThumb) {
      localStorage.setItem(thumbKey, currentThumb);
    }

    const storedView = localStorage.getItem(viewKey);
    const storedThumb = localStorage.getItem(thumbKey);
    let changed = false;

    if (storedView !== "grid" && storedView !== "list") {
      localStorage.setItem(viewKey, current);
    } else if (!params.has("view") && storedView !== current) {
      params.set("view", storedView);
      changed = true;
    }

    if (!storedThumb || !/^\d+$/.test(storedThumb)) {
      if (currentThumb) {
        localStorage.setItem(thumbKey, currentThumb);
      }
    } else if (!params.has("thumb") && storedThumb !== currentThumb) {
      params.set("thumb", storedThumb);
      changed = true;
    }

    if (!changed) {
      return;
    }
    window.location.replace(`${window.location.pathname}?${params.toString()}`);
  }

  document.body.addEventListener("htmx:configRequest", (event) => {
    event.detail.headers["X-API-Version"] = "1";
    if (token("access_token") && event.detail.path.startsWith("/share")) {
      event.detail.headers.Authorization = `Bearer ${token("access_token")}`;
    }
  });

  if (document.body.dataset.shareAuthenticated === "true") {
    bindAuthenticatedPage();
  } else if (document.querySelector("[data-share-login-form]")) {
    bootstrapLogin();
  } else {
    bindPublicPage();
  }
})();
