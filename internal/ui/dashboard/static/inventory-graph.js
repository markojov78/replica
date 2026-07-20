(() => {
  "use strict";

  const validReplicaTypes = new Set(["filesystem", "storage", "removable"]);
  const graphInstances = new WeakMap();

  function positiveID(value) {
    const id = Number(value);
    return Number.isSafeInteger(id) && id > 0 ? id : undefined;
  }

  function cleanText(value, fallback = "—") {
    if (typeof value !== "string") return fallback;
    const text = value.replace(/[\u0000-\u001f\u007f]/g, " ").trim();
    return text || fallback;
  }

  function displayReplicaType(type, storageProfile) {
    if (!validReplicaTypes.has(type)) return "Unknown";
    const displayType = type.charAt(0).toUpperCase() + type.slice(1);
    if (type !== "storage") return displayType;
    const profile = cleanText(storageProfile, "");
    return profile ? `${displayType} (${profile})` : displayType;
  }

  function replicaLabel(replica) {
    const id = positiveID(replica.id);
    return [
      `Replica #${id}`,
      `Type: ${displayReplicaType(replica.type, replica.storage_profile)}`,
      `Node: ${cleanText(replica.node_id, "unknown")}`,
      `Status: ${cleanText(replica.status, "unknown")}`,
    ].join("\n");
  }

  function shareLabel(share, replica) {
    const permissions = Array.isArray(share.anonymous_permissions)
      ? share.anonymous_permissions.filter((value) => typeof value === "string" && value.trim()).map((value) => value.trim())
      : [];
    const lines = [
      `Share #${positiveID(share.id)}`,
      `Name: ${cleanText(share.name, "unnamed")}`,
      `Node: ${replica ? cleanText(replica.node_id, "unknown") : "unknown"}`,
      `Status: ${cleanText(share.status, "unknown")}`,
    ];
    if (permissions.length) lines.push(`Anonymous ${permissions.join(", ")}`);
    return lines.join("\n");
  }

  function anonymousState(share, now = Date.now()) {
    const permissions = Array.isArray(share.anonymous_permissions)
      ? share.anonymous_permissions.filter((value) => typeof value === "string" && value.trim()).map((value) => value.trim())
      : [];
    const hasLink = typeof share.link_hash === "string" && share.link_hash.trim() !== "";
    const expiration = typeof share.share_expiration === "string" ? Date.parse(share.share_expiration) : NaN;
    if (Number.isFinite(expiration) && expiration < now) {
      return {label: "Expired", kind: "expired", permissions};
    }
    if (permissions.length && !hasLink) {
      return {label: "Anonymous permissions configured, no public link", kind: "warning", permissions};
    }
    if (hasLink && !permissions.length) {
      return {label: "Public link inactive", kind: "warning", permissions};
    }
    if (hasLink && permissions.length) {
      return {label: `Anonymous: ${permissions.join(", ")}`, kind: "public", permissions};
    }
    return {label: "Private", kind: "private", permissions};
  }

  function anonymousDetailsValue(permissions) {
    if (!Array.isArray(permissions)) return "";
    const values = [];
    if (permissions.includes("read")) values.push("read");
    if (permissions.includes("update")) values.push("update");
    return values.join(", ");
  }

  function replicaElement(replica, warning) {
    const id = positiveID(replica.id);
    if (!id) return undefined;
    const type = validReplicaTypes.has(replica.type) ? replica.type : "unknown";
    const status = cleanText(replica.status, "unknown");
    const sync = cleanText(replica.sync_status, "");
    return {
      group: "nodes",
      data: {
        id: `replica:${id}`,
        entityID: id,
        entity: "replica",
        kind: type,
        label: replicaLabel(replica),
        displayType: displayReplicaType(replica.type, replica.storage_profile),
        nodeID: cleanText(replica.node_id, "unknown"),
        status,
        syncStatus: sync || "—",
        uri: cleanText(replica.uri),
        href: `/dashboard/inventories/${positiveID(replica.inventory_id)}/replicas/${id}/edit`,
        warning: warning ? "true" : "false",
      },
      classes: `${type} status-${status} sync-${sync || "unknown"}${warning ? " warning" : ""}`,
    };
  }

  function shareElement(share, replica, now) {
    const id = positiveID(share.id);
    if (!id) return undefined;
    const access = anonymousState(share, now);
    const status = cleanText(share.status, "unknown");
    const unresolved = !replica;
    const nodeID = replica ? cleanText(replica.node_id, "unknown") : "unknown";
    return {
      group: "nodes",
      data: {
        id: `share:${id}`,
        entityID: id,
        entity: "share",
        kind: "share",
        label: shareLabel(share, replica),
        name: cleanText(share.name, "unnamed"),
        nodeID,
        status,
        syncStatus: "—",
        uri: "—",
        href: `/dashboard/shares/${id}/edit`,
        access: access.label,
        anonymousDetails: anonymousDetailsValue(access.permissions),
        warning: unresolved || access.kind === "warning" ? "true" : "false",
      },
      classes: `share status-${status} access-${access.kind}${unresolved ? " unresolved warning" : access.kind === "warning" ? " warning" : ""}`,
    };
  }

  function edge(id, source, target, label, classes) {
    return {group: "edges", data: {id, source, target, label}, classes};
  }

  function buildElements(inventory, shares, now = Date.now()) {
    const elements = [];
    const warnings = [];
    const ids = new Set();
    const allReplicas = Array.isArray(inventory?.replicas) ? inventory.replicas : [];
    const activeReplicas = allReplicas.filter((item) => item && item.status !== "deleted" && positiveID(item.id));
    const replicaByID = new Map(activeReplicas.map((item) => [positiveID(item.id), item]));

    for (const replica of activeReplicas) {
      const upstreamID = replica.upstream_replica_id == null ? undefined : positiveID(replica.upstream_replica_id);
      const warning = replica.upstream_replica_id != null && (!upstreamID || !replicaByID.has(upstreamID))
        ? "Upstream replica unavailable" : "";
      if (warning) warnings.push(`Replica #${replica.id}: ${warning.toLowerCase()}.`);
      addUnique(elements, ids, replicaElement(replica, warning), warnings);
    }

    const bases = activeReplicas.filter((item) => item.upstream_replica_id == null).sort((a, b) => a.id - b.id);
    for (let left = 0; left < bases.length; left += 1) {
      for (let right = left + 1; right < bases.length; right += 1) {
        const a = positiveID(bases[left].id);
        const b = positiveID(bases[right].id);
        addUnique(elements, ids, edge(`base:${a}:${b}`, `replica:${a}`, `replica:${b}`, "base sync", "base-sync"), warnings);
      }
    }
    for (const replica of activeReplicas) {
      const id = positiveID(replica.id);
      const upstreamID = positiveID(replica.upstream_replica_id);
      if (upstreamID && replicaByID.has(upstreamID)) {
        addUnique(elements, ids, edge(`downstream:${upstreamID}:${id}`, `replica:${upstreamID}`, `replica:${id}`, "downstream", "downstream"), warnings);
      }
    }

    for (const share of Array.isArray(shares) ? shares : []) {
      if (!share || share.status === "deleted") continue;
      const replicaID = positiveID(share.replica_id);
      const replica = replicaByID.get(replicaID);
      const node = shareElement(share, replica, now);
      addUnique(elements, ids, node, warnings);
      if (replica && node) {
        addUnique(elements, ids, edge(`share:${replicaID}:${share.id}`, `replica:${replicaID}`, node.data.id, "serves", "serves"), warnings);
      } else if (node) {
        warnings.push(`Share #${share.id}: backing replica unavailable.`);
      }
    }
    return {elements, warnings, replicaCount: activeReplicas.length, shareCount: elements.filter((item) => item.data.entity === "share").length};
  }

  function addUnique(elements, ids, element, warnings) {
    if (!element) return;
    if (ids.has(element.data.id)) {
      warnings.push(`Duplicate graph ID ignored: ${element.data.id}.`);
      return;
    }
    ids.add(element.data.id);
    elements.push(element);
  }

  async function responseJSON(response, entity) {
    if (response?.ok) return response.json();
    if (!response) throw new Error("Authentication expired. Sign in again.");
    if (response.status === 404) throw new Error(`${entity} not found.`);
    if (response.status === 403) throw new Error(`Permission denied while loading ${entity.toLowerCase()}.`);
    const problem = await response.json().catch(() => ({}));
    throw new Error(problem.detail || problem.title || `Unable to load ${entity.toLowerCase()} (${response.status}).`);
  }

  async function loadAllShares(inventoryID, request, signal) {
    const shares = [];
    const count = 100;
    for (let page = 1; ; page += 1) {
      const response = await request(`/api/admin/shares?inventory_id=${inventoryID}&page=${page}&count=${count}`, {signal});
      const result = await responseJSON(response, "Shares");
      const items = Array.isArray(result.items) ? result.items : [];
      shares.push(...items);
      if (items.length === 0 || shares.length >= Number(result.total || 0)) return shares;
    }
  }

  function graphStyles() {
    return [
      {selector: "node", style: {shape: "round-rectangle", width: 280, height: 138, padding: 12, "background-color": "#fff", "border-width": 1.5, "border-color": "#d9e0ea", label: "data(label)", "font-family": "Inter, system-ui, sans-serif", "font-size": 20, "font-weight": 500, "line-height": 1.4, color: "#172033", "text-wrap": "wrap", "text-max-width": 250, "text-valign": "center", "text-halign": "center", "text-justification": "center", "overlay-opacity": 0}},
      {selector: "node:selected", style: {"border-width": 3, "border-color": "#2563eb"}},
      {selector: "node.share", style: {height: 154, "border-color": "#027a48"}},
      {selector: "node.share:selected", style: {"border-width": 3, "border-color": "#027a48"}},
      {selector: "node.warning", style: {"border-color": "#b7791f", "border-style": "dashed"}},
      {selector: "node.status-error, node.sync-error, node.sync-conflict, node.access-expired", style: {"border-color": "#b42318"}},
      {selector: "node.access-public", style: {"border-color": "#027a48"}},
      {selector: "edge", style: {width: 2, "line-color": "#718096", "curve-style": "taxi", "taxi-direction": "rightward", "target-arrow-shape": "triangle", "target-arrow-color": "#718096", label: "data(label)", "font-size": 10, color: "#475467", "text-background-color": "#f5f7fb", "text-background-opacity": 1, "text-background-padding": 3, "text-rotation": "autorotate", "arrow-scale": 0.85}},
      {selector: "edge.base-sync", style: {"source-arrow-shape": "triangle", "source-arrow-color": "#4f46e5", "target-arrow-color": "#4f46e5", "line-color": "#4f46e5", "curve-style": "bezier"}},
      {selector: "edge.serves", style: {width: 1.4, "line-color": "#98a2b3", "target-arrow-color": "#98a2b3"}},
    ];
  }

  function showDetails(root, data) {
    const details = root.querySelector("[data-graph-details]");
    const rows = data.entity === "replica"
      ? [["Entity", "Replica"], ["ID", data.entityID], ["Node", data.nodeID], ["Status", data.status], ["Sync status", data.syncStatus], ["URI", data.uri]]
      : [["Entity", "Share"], ["ID", data.entityID], ["Node", data.nodeID], ["Status", data.status]];
    if (data.entity === "share" && data.anonymousDetails) rows.push(["Anonymous", data.anonymousDetails]);
    details.replaceChildren();
    const heading = document.createElement("h2");
    heading.textContent = `${rows[0][1]} #${data.entityID}`;
    const list = document.createElement("dl");
    for (const [term, value] of rows.slice(1)) {
      const dt = document.createElement("dt"); dt.textContent = term;
      const dd = document.createElement("dd"); dd.textContent = String(value);
      list.append(dt, dd);
    }
    const link = document.createElement("a");
    link.className = "btn primary"; link.href = data.href; link.textContent = `Open ${data.entity}`;
    details.append(heading, list, link);
  }

  function zoomAtCenter(cy, factor) {
    const extent = cy.extent();
    cy.zoom({level: Math.min(cy.maxZoom(), Math.max(cy.minZoom(), cy.zoom() * factor)), renderedPosition: {x: (extent.x1 + extent.x2) / 2, y: (extent.y1 + extent.y2) / 2}});
  }

  async function initialize(root) {
    if (graphInstances.has(root)) return;
    const inventoryID = positiveID(root.dataset.inventoryId);
    const message = root.querySelector("[data-graph-message]");
    const canvas = root.querySelector("[data-graph-canvas]");
    const controller = new AbortController();
    graphInstances.set(root, {controller});
    try {
      if (!inventoryID || !window.ReplicaAdmin?.requestWithRefresh) throw new Error("The graph page could not be initialized.");
      const request = window.ReplicaAdmin.requestWithRefresh;
      const [inventoryResponse, shares] = await Promise.all([
        request(`/api/admin/inventories/${inventoryID}`, {signal: controller.signal}),
        loadAllShares(inventoryID, request, controller.signal),
      ]);
      const inventory = await responseJSON(inventoryResponse, "Inventory");
      if (controller.signal.aborted) return;
      document.querySelector(".topbar h1").textContent = cleanText(inventory.name, `Inventory #${inventoryID}`);
      document.title = `${cleanText(inventory.name, `Inventory #${inventoryID}`)} · Replica Admin`;
      const topology = buildElements(inventory, shares);
      if (!topology.replicaCount && !topology.shareCount) {
        message.textContent = "This inventory has no active replicas or shares.";
        message.classList.add("empty");
        return;
      }
      if (topology.warnings.length) message.textContent = topology.warnings.join(" ");
      else message.hidden = true;
      cytoscape.use(cytoscapeElk);
      const cy = cytoscape({container: canvas, elements: topology.elements, style: graphStyles(), minZoom: 0.25, maxZoom: 2.5, wheelSensitivity: 0.18, boxSelectionEnabled: false});
      graphInstances.set(root, {controller, cy});
      cy.on("tap", "node", (event) => showDetails(root, event.target.data()));
      cy.on("dbltap", "node", (event) => window.ReplicaAdmin.showPage(event.target.data("href")));
      root.querySelector("[data-graph-zoom-in]").addEventListener("click", () => zoomAtCenter(cy, 1.2));
      root.querySelector("[data-graph-zoom-out]").addEventListener("click", () => zoomAtCenter(cy, 1 / 1.2));
      root.querySelector("[data-graph-fit]").addEventListener("click", () => cy.fit(cy.elements(), 42));
      cy.layout({name: "elk", nodeDimensionsIncludeLabels: true, fit: true, padding: 42, elk: {algorithm: "layered", "elk.direction": "RIGHT", "elk.edgeRouting": "ORTHOGONAL", "elk.spacing.nodeNode": "45", "elk.layered.spacing.nodeNodeBetweenLayers": "80", "elk.layered.nodePlacement.strategy": "NETWORK_SIMPLEX"}}).run();
    } catch (error) {
      if (error.name === "AbortError") return;
      message.hidden = false;
      message.classList.add("error");
      message.textContent = error instanceof Error ? error.message : "Unable to load the graph.";
    }
  }

  function dispose() {
    for (const root of document.querySelectorAll("[data-inventory-graph]")) {
      const instance = graphInstances.get(root);
      instance?.controller.abort();
      instance?.cy?.destroy();
      graphInstances.delete(root);
    }
  }

  window.addEventListener("replica:page-dispose", dispose, {once: true});
  window.addEventListener("pagehide", dispose, {once: true});
  document.querySelectorAll("[data-inventory-graph]").forEach(initialize);
  window.ReplicaInventoryGraphTest = {anonymousDetailsValue, anonymousState, buildElements, graphStyles, loadAllShares, positiveID, replicaLabel, shareLabel, zoomAtCenter};
})();
