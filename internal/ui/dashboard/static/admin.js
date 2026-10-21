(() => {
	for (const key of ["access_token", "refresh_token", "access_token_expires_at", "refresh_token_expires_at", "replica_admin_user_id", "replica_admin_username"]) {
		localStorage.removeItem(key);
	}
	let currentUser;

	function csrfToken() {
		const prefix = "replica_admin_csrf=";
		return document.cookie.split(";").map((value) => value.trim()).find((value) => value.startsWith(prefix))?.slice(prefix.length) || "";
	}

  function preferenceKey(name) {
    const userID = currentUser?.id;
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
      bindShareFilePicker(form, replicaSelect);
    }
  }

  function formatBytes(size) {
    if (size < 1024) {
      return `${size} B`;
    }
    let div = 1024;
    let exp = 0;
    for (let value = Math.floor(size / 1024); value >= 1024 && exp < 4; value = Math.floor(value / 1024)) {
      div *= 1024;
      exp++;
    }
    return `${(size / div).toFixed(1)} ${"KMGTPE"[exp]}iB`;
  }

  // bindShareFilePicker keeps the selected share files in a set across pages and filters and writes them to the
  // hidden file_ids input. Files are listed through the dashboard API proxy for the selected replica's inventory.
  function bindShareFilePicker(form, replicaSelect) {
    const panel = form.querySelector("[data-share-files]");
    if (!panel) {
      return;
    }
    const scopeInputs = [...panel.querySelectorAll("[data-file-scope]")];
    const idsInput = panel.querySelector("[data-file-ids]");
    const picker = panel.querySelector("[data-file-picker]");
    const filterInput = panel.querySelector("[data-file-filter]");
    const countSelect = panel.querySelector("[data-file-count]");
    const sortSelect = panel.querySelector("[data-file-sort]");
    const orderButton = panel.querySelector("[data-file-order]");
    const rows = panel.querySelector("[data-file-rows]");
    const message = panel.querySelector("[data-file-message]");
    const summary = panel.querySelector("[data-file-summary]");
    const countLabel = panel.querySelector("[data-file-selected-count]");
    const firstButton = panel.querySelector("[data-file-first]");
    const prevButton = panel.querySelector("[data-file-prev]");
    const nextButton = panel.querySelector("[data-file-next]");
    const lastButton = panel.querySelector("[data-file-last]");
    const deletedBox = panel.querySelector("[data-file-deleted]");
    const deletedRows = panel.querySelector("[data-file-deleted-rows]");
    const selected = new Set(idsInput.value.split(",").filter(Boolean));
    let inventoryID = panel.dataset.inventoryId || replicaSelect?.selectedOptions[0]?.dataset.inventory || "";
    let page = 1;
    let totalPages = 1;
    let order = "asc";
    let filterTimer;
    let loadSequence = 0;

    const writeSelection = () => {
      idsInput.value = [...selected].join(",");
      countLabel.textContent = `${selected.size} file${selected.size === 1 ? "" : "s"} selected`;
    };
    const showMessage = (text) => {
      message.textContent = text;
      message.hidden = text === "";
    };
    const fileCheckbox = (file) => {
      const checkbox = document.createElement("input");
      checkbox.type = "checkbox";
      checkbox.checked = selected.has(String(file.id));
      checkbox.setAttribute("aria-label", `Select ${file.relative_uri}`);
      checkbox.addEventListener("change", () => {
        if (checkbox.checked) {
          selected.add(String(file.id));
        } else {
          selected.delete(String(file.id));
        }
        writeSelection();
      });
      return checkbox;
    };
    const fileRow = (file) => {
      const row = document.createElement("tr");
      const select = document.createElement("td");
      select.append(fileCheckbox(file));
      const uri = document.createElement("td");
      uri.className = "mono";
      uri.textContent = file.relative_uri;
      const size = document.createElement("td");
      size.textContent = formatBytes(file.size);
      row.append(select, uri, size);
      return row;
    };

    const load = async () => {
      const sequence = ++loadSequence;
      picker.hidden = scopeInputs.find((input) => input.checked)?.value !== "selected";
      if (picker.hidden) {
        return;
      }
      rows.replaceChildren();
      if (!inventoryID) {
        summary.textContent = "";
        firstButton.disabled = true;
        prevButton.disabled = true;
        nextButton.disabled = true;
        lastButton.disabled = true;
        showMessage("Select a replica first.");
        return;
      }
      const query = new URLSearchParams({status: "active", sort: sortSelect.value, order, count: countSelect.value, page: String(page)});
      if (filterInput.value.trim() !== "") {
        query.set("path", filterInput.value.trim());
      }
      const response = await requestWithRefresh(`/dashboard/api/inventories/${encodeURIComponent(inventoryID)}/files?${query}`);
      if (!response || sequence !== loadSequence) {
        return;
      }
      if (!response.ok) {
        showMessage("Inventory files can't be listed.");
        return;
      }
      const body = await response.json();
      totalPages = Math.max(1, Math.ceil(body.total / body.count));
      rows.replaceChildren(...body.items.map(fileRow));
      showMessage(body.items.length === 0 ? "No active files found." : "");
      summary.textContent = `${body.items.length} of ${body.total} files, page ${body.page} of ${totalPages}`;
      firstButton.disabled = page <= 1;
      prevButton.disabled = page <= 1;
      nextButton.disabled = page >= totalPages;
      lastButton.disabled = page >= totalPages;
    };

    // Deleted files stay in the share until unchecked; they never appear in the active file list.
    const loadDeleted = async () => {
      const shareID = panel.dataset.shareId;
      if (!shareID || selected.size === 0) {
        return;
      }
      const response = await requestWithRefresh(`/dashboard/api/shares/${encodeURIComponent(shareID)}/files?status=deleted&count=100&sort=name`);
      if (!response?.ok) {
        return;
      }
      const body = await response.json();
      const files = body.items.filter((file) => selected.has(String(file.id)));
      deletedRows.replaceChildren(...files.map((file) => {
        const label = document.createElement("label");
        label.className = "filter-toggle";
        label.append(fileCheckbox(file), ` ${file.relative_uri}`);
        return label;
      }));
      deletedBox.hidden = files.length === 0;
    };

    for (const input of scopeInputs) {
      input.addEventListener("change", load);
    }
    filterInput.addEventListener("input", () => {
      clearTimeout(filterTimer);
      filterTimer = setTimeout(() => {
        page = 1;
        load();
      }, 300);
    });
    countSelect.addEventListener("change", () => {
      page = 1;
      load();
    });
    sortSelect.addEventListener("change", () => {
      page = 1;
      load();
    });
    orderButton.addEventListener("click", () => {
      order = order === "asc" ? "desc" : "asc";
      orderButton.textContent = order === "asc" ? "Ascending ↑" : "Descending ↓";
      page = 1;
      load();
    });
    firstButton.addEventListener("click", () => {
      page = 1;
      load();
    });
    lastButton.addEventListener("click", () => {
      page = totalPages;
      load();
    });
    prevButton.addEventListener("click", () => {
      page = Math.max(1, page - 1);
      load();
    });
    nextButton.addEventListener("click", () => {
      page++;
      load();
    });
    if (!panel.dataset.inventoryId && replicaSelect) {
      // The node select resets the replica select without a change event, so both trigger a recheck.
      const syncInventory = () => {
        const next = replicaSelect.selectedOptions[0]?.dataset.inventory || "";
        if (next === inventoryID) {
          return;
        }
        inventoryID = next;
        selected.clear();
        page = 1;
        writeSelection();
        load();
      };
      replicaSelect.addEventListener("change", syncInventory);
      form.querySelector("[data-share-node-select]")?.addEventListener("change", syncInventory);
    }
    writeSelection();
    load();
    loadDeleted();
  }

  function bindReplicaForms() {
    for (const form of document.querySelectorAll("[data-replica-form]")) {
      const typeSelect = form.querySelector("[data-replica-type]");
      const profileField = form.querySelector("[data-storage-profile-field]");
      const profileSelect = profileField?.querySelector("[name=storage_profile]");
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
          const uri = form.querySelector("[name=uri]")?.value || form.querySelector("[name=folder_uri]")?.value || form.querySelector("[name=file_uris]")?.value.trim().split(/\s+/)[0] || form.dataset.replicaUri || "";
          followSymlinks.disabled = !(typeSelect?.value === "filesystem" || (typeSelect?.value === "storage" && uri.startsWith("sftp://")));
        }
      };

      for (const field of form.querySelectorAll("[name=uri], [name=folder_uri], [name=file_uris]")) {
        field.addEventListener("input", syncFollowSymlinks);
      }
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
		if (options.method && !["GET", "HEAD"].includes(options.method.toUpperCase())) {
			headers.set("X-CSRF-Token", csrfToken());
    }
    return fetch(path, {...options, headers});
  }

  async function requestWithRefresh(path, options = {}) {
		const response = await authRequest(path, options);
		if (response.status === 401) {
			if (window.location.pathname !== "/dashboard/login") {
				window.location.replace("/dashboard/login");
			}
      return undefined;
    }
    return response;
  }

	async function logout() {
		try {
			await authRequest("/dashboard/logout", {method: "POST"});
		} catch {
			// Navigation still completes if the service becomes unavailable.
		}
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
    window.dispatchEvent(new CustomEvent("replica:page-dispose"));
    document.open();
    document.write(html);
    document.close();
  }

	async function bindAdminPage() {
		const me = await requestWithRefresh("/dashboard/api/auth/me");
		if (!me?.ok) {
			return;
		}
		currentUser = await me.json();
    for (const element of document.querySelectorAll("[data-current-username]")) {
			element.textContent = currentUser.name || currentUser.username || "";
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
		const response = await fetch("/dashboard/auth/login", {
      method: "POST",
			headers: {"Content-Type": "application/json", "X-API-Version": "1", "X-CSRF-Token": csrfToken()},
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
    window.location.replace("/dashboard");
  }

  async function bootstrapLogin() {
    const form = document.querySelector("[data-login-form]");
    form.addEventListener("submit", (event) => {
      event.preventDefault();
      login(form);
    });
		const response = await authRequest("/dashboard/api/auth/me");
    if (response?.ok) {
      const destination = window.location.pathname === "/dashboard/login" ? "/dashboard" : window.location.href;
      await showPage(destination, undefined, false);
    }
  }

  window.ReplicaAdmin = {requestWithRefresh, showPage};

  if (document.body.dataset.adminAuthenticated === "true") {
    bindAdminPage();
  } else {
    bootstrapLogin();
  }
})();
