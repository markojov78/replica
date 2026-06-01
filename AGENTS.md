# AGENTS.md

## Project

This repository contains DropOutBox, a distributed self-hosted file replication and file sharing service.

## Authoritative Documentation

The following documents are the source of truth:

* `docs/application.md`
* `docs/api.md`
* `docs/file_watcher.md`

Do not duplicate or redefine behavior already documented there.

If implementation and documentation disagree, assume the documentation represents the intended behavior and highlight the discrepancy.

---

## Documentation Lookup Rules

Before changing inventory logic:

* Read `Inventory` in `docs/application.md`

Before changing replica logic:

* Read `Replica` in `docs/application.md`
* Read `Creating a new replica`
* Read `File replication between replicas`

Before changing sharing functionality:

* Read `Share` in `docs/application.md`

Before changing permissions or authorization:

* Read `Users, ownership and permissions` in `docs/application.md`
* Read relevant authorization sections in `docs/api.md`

Before changing coordinator/node communication:

* Read `Communication between nodes` in `docs/application.md`

Before changing heartbeat behavior:

* Read `Heartbeat` in `docs/application.md`
* Read `/internal/nodes` in `docs/api.md`

Before changing websocket behavior:

* Read `WebSocket orchestration channel` in `docs/application.md`
* Read `/internal/nodes/ws` in `docs/api.md`

Before changing replication commands:

* Read `/internal/commands` in `docs/api.md`

Before changing file synchronization:

* Read:

  * `Replica change reporting`
  * `Data transfer between nodes`
  * `Creating a new replica`
  * `File replication between replicas`

Before changing database models:

* Read `Database` in `docs/application.md`

Before changing public REST endpoints:

* Read the relevant section in `docs/api.md`

Before changing internal REST endpoints:

* Read the relevant section in `docs/api.md`

Before changing scanners or watchers:

* Read `docs/file_watcher.md`

---

## Non-Negotiable Architecture Rules

The following rules must not be violated unless explicitly requested:

1. Coordinator database is the authoritative source of truth.
2. Storage services never persist authoritative state.
3. Node communication is coordinator-centric.
4. Storage nodes initiate coordinator communication.
5. Replication decisions are made by the coordinator.
6. File transfer occurs between storage services.
7. Data integrity has priority over availability.
8. Storage nodes rebuild runtime state from the coordinator after startup.
9. Coordinator + Storage mode must behave the same as a standalone storage node whenever possible.
10. Storage services do not independently decide global synchronization state.

---

## API Rules

* Public endpoints live under `/api`.
* Internal coordinator/node endpoints live under `/internal`.
* Preserve API compatibility unless explicitly instructed otherwise.
* Do not introduce new endpoints when existing endpoints can be extended.
* Update API documentation whenever request or response contracts change.
* Keep public and internal APIs clearly separated.

---

## Database Rules

* Preserve documented table meanings and relationships.
* Do not rename tables, fields, statuses, or documented concepts without approval.
* Prefer additive schema changes over breaking schema changes.
* Keep GORM models aligned with documented schema.
* Update database documentation when schema changes.

---

## Replication Rules

* Base replicas have `upstream_replica_id = null`.
* Downstream replicas have `upstream_replica_id != null`.
* Downstream replicas are not authoritative sources for inventory changes.
* Inventory state is authoritative.
* Replica state reflects synchronization progress relative to inventory state.
* Changes flow through coordinator validation before becoming authoritative inventory state.

---

## Development Rules

* Do not make code changes unless explicitly requested.
* If asked a design question, answer the question without modifying code.
* Prefer small localized changes over broad refactors.
* Do not introduce new frameworks without approval.
* Do not redesign existing architecture without approval.
* Preserve existing naming and project structure whenever possible.
* When changing behavior, update relevant documentation.

---

## Go Rules

* Run `gofmt` on modified Go files.
* Do not run `go mod tidy` unless dependencies changed.
* Do not modify `go.mod` or `go.sum` unless required by the task.
* Follow existing project patterns.
* Prefer consistency with existing code over introducing new abstractions.

---

## Response Style

* Be concise.
* Explain intended changes before making major modifications.
* Prefer minimal diffs.
* Avoid unrelated cleanup.
* Do not change files unrelated to the requested task.
