(() => {
  const keys = [
    "access_token",
    "refresh_token",
    "access_token_expires_at",
    "refresh_token_expires_at",
  ];
  const userIDKey = "replica_admin_user_id";
  const usernameKey = "replica_admin_username";
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
    localStorage.removeItem(usernameKey);
  }

  function preferenceKey(name) {
    const userID = localStorage.getItem(userIDKey);
    return userID ? `replica_admin_user_${userID}_${name}` : "";
  }

  function applyListFilters(scope) {
    const deleteToggle = document.querySelector(`[data-hide-deleted="${scope}"]`);
    const filters = [...document.querySelectorAll(`[data-list-filter="${scope}"]`)];
    for (const item of document.querySelectorAll(`[data-filter-item="${scope}"]`)) {
      let hidden = deleteToggle?.checked && item.dataset.status === "deleted";
      for (const filter of filters) {
        if (hidden) {
          break;
        }
        const value = filter.value;
        const field = filter.dataset.filterField;
        if (value && field && item.dataset[field] !== value) {
          hidden = true;
        }
      }
      item.hidden = hidden;
    }
  }

  function bindDeletedFilters() {
    for (const toggle of document.querySelectorAll("[data-hide-deleted]")) {
      const key = preferenceKey(toggle.dataset.hideDeleted);
      toggle.checked = key !== "" && localStorage.getItem(key) === "true";
      toggle.addEventListener("change", () => {
        if (key) {
          localStorage.setItem(key, String(toggle.checked));
        }
        applyListFilters(toggle.dataset.hideDeleted);
      });
      applyListFilters(toggle.dataset.hideDeleted);
    }
  }

  function bindChoiceFilters() {
    for (const filter of document.querySelectorAll("[data-list-filter]")) {
      const key = preferenceKey(`${filter.dataset.listFilter}_${filter.dataset.filterField}`);
      const initialValue = filter.dataset.initialValue;
      if (initialValue && [...filter.options].some((option) => option.value === initialValue)) {
        filter.value = initialValue;
      } else if (key) {
        const value = localStorage.getItem(key) || "";
        if ([...filter.options].some((option) => option.value === value)) {
          filter.value = value;
        }
      }
      filter.addEventListener("change", () => {
        if (key) {
          localStorage.setItem(key, filter.value);
        }
        applyListFilters(filter.dataset.listFilter);
      });
      applyListFilters(filter.dataset.listFilter);
    }
  }

  function bindAutoSubmitControls() {
    for (const control of document.querySelectorAll("[data-auto-submit]")) {
      control.addEventListener("change", () => control.form?.requestSubmit());
    }
  }

  function bindShareForms() {
    for (const form of document.querySelectorAll("[data-share-form]")) {
      const expirationToggle = form.querySelector("[data-expiration-toggle]");
      const expirationInput = form.querySelector("[data-expiration-input]");
      const anonymousWarning = form.querySelector("[data-anonymous-warning]");
      const anonymousInputs = [...form.querySelectorAll("[data-anonymous-permission]")];
      const nodeSelect = form.querySelector("[data-share-node-select]");
      const replicaSelect = form.querySelector("[data-share-replica-select]");

      const syncExpiration = () => {
        if (expirationInput) {
          expirationInput.disabled = expirationToggle ? !expirationToggle.checked : false;
        }
      };
      const syncAnonymous = () => {
        if (anonymousWarning) {
          anonymousWarning.hidden = !anonymousInputs.some((input) => input.checked);
        }
      };
      const syncReplicas = () => {
        if (!nodeSelect || !replicaSelect) {
          return;
        }
        const nodeID = nodeSelect.value;
        replicaSelect.disabled = nodeID === "";
        for (const option of replicaSelect.options) {
          if (option.value === "") {
            option.hidden = false;
            option.textContent = nodeID === "" ? "Select node first" : "Select replica";
            continue;
          }
          option.hidden = option.dataset.node !== nodeID;
        }
        if (replicaSelect.selectedOptions.length > 0 && replicaSelect.selectedOptions[0].hidden) {
          replicaSelect.value = "";
        }
      };

      expirationToggle?.addEventListener("change", syncExpiration);
      for (const input of anonymousInputs) {
        input.addEventListener("change", syncAnonymous);
      }
      if (nodeSelect && replicaSelect) {
        const selectedReplica = replicaSelect.selectedOptions[0];
        if (selectedReplica?.dataset.node) {
          nodeSelect.value = selectedReplica.dataset.node;
        }
        nodeSelect.addEventListener("change", syncReplicas);
      }
      syncExpiration();
      syncAnonymous();
      syncReplicas();
    }
  }

  function bindReplicaForms() {
    for (const form of document.querySelectorAll("[data-replica-form]")) {
      const typeSelect = form.querySelector("[data-replica-type]");
      const profileField = form.querySelector("[data-storage-profile-field]");
      const profileSelect = profileField?.querySelector("select");
      const followSymlinks = form.querySelector("[data-follow-symlinks]");
      const syncProfile = () => {
        const enabled = typeSelect?.value === "storage";
        if (profileSelect) {
          profileSelect.disabled = !enabled;
          if (!enabled) {
            profileSelect.value = "";
          }
        }
      };
      const syncFollowSymlinks = () => {
        if (followSymlinks) {
          followSymlinks.disabled = typeSelect?.value !== "filesystem";
        }
      };

      typeSelect?.addEventListener("change", syncProfile);
      typeSelect?.addEventListener("change", syncFollowSymlinks);
      syncProfile();
      syncFollowSymlinks();
    }
  }

  function yamlString(value) {
    return JSON.stringify(value || "");
  }

  function nodeConfigYAML(values) {
    return [
      "app:",
      `  node_id: ${yamlString(values.nodeID)}`,
      "  storage: true",
      `  coordinator_url: ${yamlString(values.coordinatorURL)}`,
      `  node_address: ${yamlString(values.nodeAddress)}`,
      `  heartbeat_interval: ${yamlString(values.heartbeatInterval)}`,
      "",
      "auth:",
      `  node_secret: ${yamlString(values.nodeSecret)}`,
      "",
      "http:",
      `  address: ${yamlString(values.httpAddress)}`,
      "",
    ].join("\n");
  }

  function bindNodeConfigPreview() {
    const form = document.querySelector("[data-node-form]");
    const preview = form?.querySelector("[data-node-config-preview]");
    const output = form?.querySelector("[data-node-config-output]");
    if (!form || !preview || !output) {
      return;
    }

    const nodeID = form.querySelector("[data-node-config-id]");
    const nodeAddress = form.querySelector("[data-node-config-address]");
    const nodeSecret = form.querySelector("[data-node-config-secret]");
    const sync = () => {
      output.value = nodeConfigYAML({
        nodeID: nodeID?.value,
        nodeAddress: nodeAddress?.value,
        nodeSecret: nodeSecret?.value,
        coordinatorURL: preview.dataset.coordinatorUrl,
        heartbeatInterval: preview.dataset.heartbeatInterval,
        httpAddress: preview.dataset.httpAddress,
      });
    };

    for (const input of [nodeID, nodeAddress, nodeSecret]) {
      input?.addEventListener("input", sync);
    }
    preview.querySelector("[data-copy-node-config]")?.addEventListener("click", async () => {
      output.focus();
      output.select();
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(output.value);
      } else {
        document.execCommand("copy");
      }
    });
    sync();
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
        const response = await fetch("/api/admin/auth/refresh", {
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
        await authRequest("/api/admin/auth/logout", {method: "POST"});
      } catch {
        // Local logout must still complete when the service is unavailable.
      }
    }
    clearTokens();
    window.location.replace("/dashboard/login");
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
    for (const element of document.querySelectorAll("[data-current-username]")) {
      element.textContent = localStorage.getItem(usernameKey) || "";
    }
    bindDeletedFilters();
    bindChoiceFilters();
    bindAutoSubmitControls();
    bindShareForms();
    bindReplicaForms();
    bindNodeConfigPreview();
    document.addEventListener("click", (event) => {
      const link = event.target.closest("a[href]");
      if (!link || link.origin !== window.location.origin || !link.pathname.startsWith("/dashboard")) {
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
      if (form.action.endsWith("/dashboard/logout")) {
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
    const response = await fetch("/api/admin/auth/login", {
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
    localStorage.setItem(usernameKey, form.elements.username.value.trim());
    window.location.replace("/dashboard");
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
    const response = await requestWithRefresh("/api/admin/auth/me");
    if (response?.ok) {
      const user = await response.json();
      localStorage.setItem(userIDKey, user.id);
      localStorage.setItem(usernameKey, user.username);
      const destination = window.location.pathname === "/dashboard/login" ? "/dashboard" : window.location.href;
      await showPage(destination, undefined, false);
    }
  }

  if (document.body.dataset.adminAuthenticated === "true") {
    bindAdminPage();
  } else {
    bootstrapLogin();
  }
})();
