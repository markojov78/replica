(() => {
  const keys = [
    "access_token",
    "refresh_token",
    "access_token_expires_at",
    "refresh_token_expires_at",
  ];
  const userIDKey = "replica_admin_user_id";
  let refreshPromise;

  function token(name) {
    return localStorage.getItem(name) || "";
  }

  function storeTokens(pair) {
    for (const key of keys) {
      localStorage.setItem(key, pair[key]);
    }
    localStorage.setItem(userIDKey, pair.user_id);
  }

  function clearTokens() {
    for (const key of keys) {
      localStorage.removeItem(key);
    }
    localStorage.removeItem(userIDKey);
  }

  function preferenceKey(name) {
    const userID = localStorage.getItem(userIDKey);
    return userID ? `replica_admin_user_${userID}_${name}` : "";
  }

  function bindDeletedFilters() {
    for (const toggle of document.querySelectorAll("[data-hide-deleted]")) {
      const key = preferenceKey(toggle.dataset.hideDeleted);
      toggle.checked = key !== "" && localStorage.getItem(key) === "true";
      const apply = () => {
        for (const item of document.querySelectorAll(`[data-filter-item="${toggle.dataset.hideDeleted}"]`)) {
          item.hidden = toggle.checked && item.dataset.status === "deleted";
        }
      };
      toggle.addEventListener("change", () => {
        if (key) {
          localStorage.setItem(key, String(toggle.checked));
        }
        apply();
      });
      apply();
    }
  }

  async function authRequest(path, options = {}) {
    const headers = new Headers(options.headers);
    headers.set("X-API-Version", "1");
    if (token("access_token")) {
      headers.set("Authorization", `Bearer ${token("access_token")}`);
    }
    return fetch(path, {...options, headers});
  }

  async function refresh() {
    if (!refreshPromise) {
      refreshPromise = (async () => {
        const refreshToken = token("refresh_token");
        if (!refreshToken) {
          throw new Error("missing refresh token");
        }
        const response = await fetch("/api/auth/refresh", {
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

  async function requestWithRefresh(path, options = {}) {
    let response = await authRequest(path, options);
    if (response.status !== 401) {
      return response;
    }
    try {
      await refresh();
    } catch {
      await logout(false);
      return undefined;
    }
    response = await authRequest(path, options);
    if (response.status === 401) {
      await logout();
      return undefined;
    }
    return response;
  }

  async function logout(callAPI = true) {
    const accessToken = token("access_token");
    if (callAPI && accessToken) {
      try {
        await authRequest("/api/auth/logout", {method: "POST"});
      } catch {
        // Local logout must still complete when the service is unavailable.
      }
    }
    clearTokens();
    window.location.replace("/admin/login");
  }

  async function showPage(path, options, pushState = true) {
    const response = await requestWithRefresh(path, options);
    if (!response) {
      return;
    }
    if (pushState) {
      history.pushState({}, "", response.url);
    }
    const html = await response.text();
    document.open();
    document.write(html);
    document.close();
  }

  function bindAdminPage() {
    bindDeletedFilters();
    document.addEventListener("click", (event) => {
      const link = event.target.closest("a[href]");
      if (!link || link.origin !== window.location.origin || !link.pathname.startsWith("/admin")) {
        return;
      }
      event.preventDefault();
      showPage(link.href);
    });
    document.addEventListener("submit", (event) => {
      const form = event.target;
      if (!(form instanceof HTMLFormElement)) {
        return;
      }
      event.preventDefault();
      if (form.action.endsWith("/admin/logout")) {
        logout();
        return;
      }
      const method = form.method.toUpperCase();
      const body = method === "GET" ? undefined : new URLSearchParams(new FormData(form));
      const path = method === "GET" ? `${form.action}?${new URLSearchParams(new FormData(form))}` : form.action;
      showPage(path, {method, body});
    });
    window.addEventListener("popstate", () => showPage(window.location.href, undefined, false));
  }

  async function login(form) {
    const response = await fetch("/api/auth/login", {
      method: "POST",
      headers: {"Content-Type": "application/json", "X-API-Version": "1"},
      body: JSON.stringify({
        username: form.elements.username.value.trim(),
        password: form.elements.password.value,
      }),
    });
    if (!response.ok) {
      const problem = await response.json().catch(() => ({}));
      document.querySelector("[data-login-error]").textContent =
        problem.detail || problem.error || problem.title || "Sign in failed.";
      return;
    }
    storeTokens(await response.json());
    window.location.replace("/admin");
  }

  async function bootstrapLogin() {
    const form = document.querySelector("[data-login-form]");
    form.addEventListener("submit", (event) => {
      event.preventDefault();
      login(form);
    });
    if (!token("access_token")) {
      clearTokens();
      return;
    }
    const response = await requestWithRefresh("/api/auth/me");
    if (response?.ok) {
      const user = await response.json();
      localStorage.setItem(userIDKey, user.id);
      const destination = window.location.pathname === "/admin/login" ? "/admin" : window.location.href;
      await showPage(destination, undefined, false);
    }
  }

  if (document.body.dataset.adminAuthenticated === "true") {
    bindAdminPage();
  } else {
    bootstrapLogin();
  }
})();
