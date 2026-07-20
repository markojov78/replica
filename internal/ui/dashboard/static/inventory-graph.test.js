/* Run with: node --test internal/ui/dashboard/static/inventory-graph.test.js */
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");

const source = fs.readFileSync(require.resolve("./inventory-graph.js"), "utf8");
const window = {addEventListener() {}};
const document = {querySelectorAll() { return []; }};
vm.runInNewContext(source, {window, document, console, Date, Number, Set, Map, Error, Array, String, CustomEvent: class {}});
const graph = window.ReplicaInventoryGraphTest;
const plain = (value) => JSON.parse(JSON.stringify(value));

function inventory(replicas) {
  return {id: 9, replicas: replicas.map((item) => ({inventory_id: 9, status: "active", sync_status: "synchronized", node_id: `node-${item.id}`, uri: `/data/${item.id}`, ...item}))};
}

test("replica nodes include stable IDs and kinds without icon data", () => {
  const result = graph.buildElements(inventory([
    {id: 1, type: "filesystem", upstream_replica_id: null},
    {id: 2, type: "storage", upstream_replica_id: 1},
    {id: 3, type: "removable", upstream_replica_id: 1},
  ]), []);
  const nodes = result.elements.filter((item) => item.group === "nodes");
  assert.deepEqual(plain(nodes.map((item) => item.data.id)), ["replica:1", "replica:2", "replica:3"]);
  assert.deepEqual(plain(nodes.map((item) => item.data.kind)), ["filesystem", "storage", "removable"]);
  assert.ok(nodes.every((item) => !("iconUrl" in item.data) && !("icon" in item.data)));
});

test("node styles contain no images and center labels", () => {
  const styles = graph.graphStyles();
  const base = styles.find((entry) => entry.selector === "node").style;
  assert.ok(!Object.keys(base).some((key) => key.startsWith("background-image") || key.startsWith("background-fit") || key.startsWith("background-position") || key.startsWith("background-width") || key.startsWith("background-height")));
  assert.equal(base["text-halign"], "center");
  assert.equal(base["text-valign"], "center");
  assert.equal(base["text-justification"], "center");
  assert.ok(!("text-margin-x" in base));

  const replicaSelected = styles.find((entry) => entry.selector === "node:selected").style;
  const share = styles.find((entry) => entry.selector === "node.share").style;
  const shareSelected = styles.find((entry) => entry.selector === "node.share:selected").style;
  assert.equal(replicaSelected["border-color"], "#2563eb");
  assert.equal(share["border-color"], "#027a48");
  assert.equal(shareSelected["border-color"], "#027a48");
});

test("replica labels use the exact semantic four-line structure", () => {
  assert.equal(graph.replicaLabel({id: 4, type: "storage", node_id: "hetz", status: "active"}), "Replica #4\nType: Storage\nNode: hetz\nStatus: active");
  assert.equal(graph.replicaLabel({id: 1, type: "filesystem", node_id: "laptop", status: "active"}), "Replica #1\nType: Filesystem\nNode: laptop\nStatus: active");
  assert.equal(graph.replicaLabel({id: 17, type: "removable", node_id: "laptop", status: "active"}), "Replica #17\nType: Removable\nNode: laptop\nStatus: active");
  assert.equal(graph.replicaLabel({id: 18, type: "other"}), "Replica #18\nType: Unknown\nNode: unknown\nStatus: unknown");
});

test("share labels use fallbacks and optional anonymous permission line", () => {
  const backing = {node_id: "hetz"};
  assert.equal(graph.shareLabel({id: 1, name: "Fluff", status: "active", anonymous_permissions: ["read"]}, backing), "Share #1\nName: Fluff\nNode: hetz\nStatus: active\nAnonymous read");
  assert.equal(graph.shareLabel({id: 2, name: "Files", status: "active", anonymous_permissions: ["read", "create", "update", "delete"]}, backing), "Share #2\nName: Files\nNode: hetz\nStatus: active\nAnonymous read, create, update, delete");
  assert.equal(graph.shareLabel({id: 3, status: "active", anonymous_permissions: []}), "Share #3\nName: unnamed\nNode: unknown\nStatus: active");
  assert.equal(graph.shareLabel({id: 4, name: "Private", status: "active"}, backing).split("\n").length, 4);
});

test("share details collapse anonymous permissions into one optional value", () => {
  assert.equal(graph.anonymousDetailsValue(["read"]), "read");
  assert.equal(graph.anonymousDetailsValue(["update"]), "write");
  assert.equal(graph.anonymousDetailsValue(["read", "update"]), "read, write");
  assert.equal(graph.anonymousDetailsValue([]), "");
  assert.equal(graph.anonymousDetailsValue(undefined), "");
});

test("base pairs occur once and downstream edges point upstream to downstream", () => {
  const result = graph.buildElements(inventory([
    {id: 3, type: "filesystem", upstream_replica_id: null},
    {id: 1, type: "filesystem", upstream_replica_id: null},
    {id: 2, type: "filesystem", upstream_replica_id: null},
    {id: 4, type: "filesystem", upstream_replica_id: 2},
  ]), []);
  const bases = result.elements.filter((item) => item.classes === "base-sync");
  assert.deepEqual(plain(bases.map((item) => item.data.id)), ["base:1:2", "base:1:3", "base:2:3"]);
  assert.equal(bases.length, 3);
  assert.deepEqual(plain(result.elements.find((item) => item.data.id === "downstream:2:4").data), {id: "downstream:2:4", source: "replica:2", target: "replica:4", label: "downstream"});
});

test("share direction and anonymous states do not infer general read or write access", () => {
  const replica = {id: 1, type: "filesystem", upstream_replica_id: null};
  const shares = [
    {id: 5, replica_id: 1, status: "active", name: "Private", link_hash: "", anonymous_permissions: [], user_permissions: [{permissions: ["update"]}]},
    {id: 6, replica_id: 1, status: "active", name: "Public", link_hash: "hash", anonymous_permissions: ["read", "create"]},
    {id: 7, replica_id: 1, status: "active", link_hash: "hash", anonymous_permissions: []},
    {id: 8, replica_id: 1, status: "active", link_hash: "", anonymous_permissions: ["read"]},
    {id: 9, replica_id: 1, status: "active", link_hash: "hash", anonymous_permissions: ["read"], share_expiration: "2020-01-01T00:00:00Z"},
  ];
  const result = graph.buildElements(inventory([replica]), shares, Date.parse("2026-01-01T00:00:00Z"));
  const served = result.elements.filter((item) => item.classes === "serves");
  assert.ok(served.every((item) => item.data.source === "replica:1" && item.data.target.startsWith("share:")));
  const labels = result.elements.filter((item) => item.data.entity === "share").map((item) => item.data.access);
  assert.deepEqual(plain(labels), ["Private", "Anonymous: read, create", "Public link inactive", "Anonymous permissions configured, no public link", "Expired"]);
  assert.ok(!JSON.stringify(result.elements).includes("writable"));
  assert.ok(!JSON.stringify(result.elements).includes("read-only"));
});

test("missing relationships create warning nodes without dangling edges", () => {
  const result = graph.buildElements(inventory([
    {id: 2, type: "filesystem", upstream_replica_id: 99},
  ]), [{id: 7, replica_id: 88, status: "active", anonymous_permissions: []}]);
  assert.ok(result.elements.find((item) => item.data.id === "replica:2").classes.includes("warning"));
  assert.ok(result.elements.find((item) => item.data.id === "share:7").classes.includes("unresolved"));
  assert.equal(result.elements.filter((item) => item.group === "edges").length, 0);
});

test("deleted items, duplicate IDs, and empty topologies are handled", () => {
  const result = graph.buildElements(inventory([
    {id: 1, type: "filesystem", upstream_replica_id: null},
    {id: 1, type: "storage", upstream_replica_id: null},
    {id: 2, type: "storage", status: "deleted", upstream_replica_id: null},
  ]), [{id: 3, replica_id: 1, status: "deleted"}]);
  assert.equal(new Set(result.elements.map((item) => item.data.id)).size, result.elements.length);
  assert.ok(result.warnings.some((item) => item.includes("Duplicate graph ID")));
  const empty = graph.buildElements(inventory([]), []);
  assert.equal(empty.elements.length, 0);
  assert.equal(empty.replicaCount, 0);
});

test("share pagination retrieves every page and API failure is surfaced", async () => {
  const pages = [];
  const request = async (url) => {
    pages.push(url);
    const page = Number(new URL(url, "http://test").searchParams.get("page"));
    return {ok: true, json: async () => ({items: page === 1 ? [{id: 1}, {id: 2}] : [{id: 3}], total: 3})};
  };
  assert.equal((await graph.loadAllShares(9, request)).length, 3);
  assert.equal(pages.length, 2);
  await assert.rejects(() => graph.loadAllShares(9, async () => ({ok: false, status: 500, json: async () => ({detail: "API unavailable"})})), /API unavailable/);
});

test("viewport controls invoke zoom and can support fit", () => {
  let invocation;
  const cy = {zoom(value) { if (value) invocation = value; return 1; }, minZoom: () => 0.25, maxZoom: () => 2.5, extent: () => ({x1: 0, x2: 200, y1: 0, y2: 100})};
  graph.zoomAtCenter(cy, 1.2);
  assert.equal(invocation.level, 1.2);
  assert.deepEqual(plain(invocation.renderedPosition), {x: 100, y: 50});
  const fit = {called: false, fit() { this.called = true; }};
  fit.fit();
  assert.equal(fit.called, true);
});

test("normal replica-only and share-only topology notices are absent", () => {
  assert.ok(!source.includes("This inventory has replicas but no active shares."));
  assert.ok(!source.includes("This inventory has no active replicas. Shares are shown as unresolved."));
});
