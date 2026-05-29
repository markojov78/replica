# API

This document is split into:
- public user-facing endpoints under `/api/`
- internal endpoints under `/internal/`

API versioning is implemented through the `X-API-Version` request header. Supported values are `1` and `v1`.

Protected endpoints expect:

```http
Authorization: Bearer <access_token>
X-API-Version: 1
```

## Public API

Base path for the endpoints in this section is `/api`.

### / endpoint
#### GET /
Returns service metadata for the authenticated user.  
  
Example response:  

```json
{
  "service": "DropOutBox",
  "version": "dev",
  "commit": "abc1234",
  "build_date": "2026-05-19T12:00:00Z",
  "node_id": "node-1",
  "coordinator": true,
  "storage": true
}
```

Possible errors:  
- `401` missing authenticated user  

### /auth endpoint

Authentication is token-based.

Current token model:
- `access_token` is a signed JWT
- `refresh_token` is an opaque random token
- protected endpoints use `Authorization: Bearer <access_token>`
- access tokens are validated by JWT signature and expiration
- refresh tokens are stored server-side only as a hash
- refresh tokens are rotated on successful refresh
- logout revokes the current refresh-token session

Expiration behavior:
- `access_token_expires_at` is the JWT expiration time returned to the client
- once the access token expires, protected endpoints return `401`
- `refresh_token_expires_at` is the server-side refresh-session expiration
- once the refresh token expires, `/auth/refresh` returns `401`

#### POST /auth/login
Authenticates a user and returns a new token pair.

Behavior:
- validates `username` and `password`
- returns a signed JWT access token
- returns a new opaque refresh token
- creates a refresh-token session on the server

Request body:
- `username` required
- `password` required

Example request:

```json
{
  "username": "jsmith",
  "password": "secret"
}
```

Example response:

```json
{
  "user_id": 1,
  "access_token": "access-token-value",
  "refresh_token": "refresh-token-value",
  "access_token_expires_at": "2026-04-07T12:30:00Z",
  "refresh_token_expires_at": "2026-04-07T20:30:00Z"
}
```

Possible errors:
- `400` invalid JSON payload
- `401` invalid username or password
- `403` inactive user

#### POST /auth/refresh
Exchanges a refresh token for a new token pair.
The refresh token is rotated on success, so the old refresh token becomes invalid.

Behavior:
- hashes the provided refresh token and looks up the matching server-side session
- rejects invalid, expired, or already revoked refresh tokens
- revokes the old refresh-token session
- returns a new JWT access token
- returns a new opaque refresh token

Request body:
- `refresh_token` required

Example request:

```json
{
  "refresh_token": "refresh-token-value"
}
```

Example response:

```json
{
  "user_id": 1,
  "access_token": "new-access-token-value",
  "refresh_token": "new-refresh-token-value",
  "access_token_expires_at": "2026-04-07T13:00:00Z",
  "refresh_token_expires_at": "2026-04-07T21:00:00Z"
}
```

Possible errors:
- `400` invalid JSON payload
- `401` invalid token
- `401` expired token

#### POST /auth/logout
Revokes the current authenticated session identified by the bearer access token.
Because access tokens are stateless JWTs, logout revokes the corresponding refresh-token session in storage rather than deleting the access token itself.

Request contract:
- no request body
- still uses `Authorization: Bearer <access_token>`

Behavior:
- parses and validates the bearer JWT
- identifies the current refresh-token session from the JWT
- revokes that session in server storage

Example request:

```http
POST /api/auth/logout
Authorization: Bearer access-token-value
X-API-Version: 1
```

Successful response:
- `204 No Content`

Possible errors:
- `401` invalid token

#### GET /auth/me
Returns the currently authenticated user with expanded roles and permissions.

Example request:

```http
GET /api/auth/me
Authorization: Bearer access-token-value
X-API-Version: 1
```

Example response:

```json
{
  "id": 1,
  "username": "jsmith",
  "status": "active",
  "roles": [
    {
      "id": 1,
      "name": "admin",
      "description": "Administrator",
      "status": "active",
      "permissions": [
        {
          "id": 1,
          "resource": "users",
          "actions": "read"
        }
      ]
    }
  ]
}
```

Possible errors:
- `401` missing authenticated user

### /users endpoint

All `/users` endpoints require the matching `users` permission for the requested action.

#### GET /users
Returns a paginated list of users.

Query parameters:
- `page` optional, default `1`
- `count` optional, default `20`, maximum `100`

Example response:

```json
{
  "items": [
    {
      "id": 1,
      "name": "jsmith",
      "password": "$2a$10$example",
      "status": "active",
      "roles": [
        {
          "id": 1,
          "name": "admin",
          "description": "Administrator",
          "status": "active",
          "permissions": [
            {
              "id": 1,
              "resource": "users",
              "actions": "read"
            }
          ]
        }
      ]
    }
  ],
  "page": 1,
  "count": 20,
  "total": 1
}
```

#### GET /users/{id}
Returns a single user.

#### POST /users
Creates a user.

Request body:
- `name` required
- `password` required
- `role_ids` optional

Example request:

```json
{
  "name": "jsmith",
  "password": "secret",
  "role_ids": [1, 2]
}
```

#### PATCH /users/{id}
Updates a user.

Request body fields are optional:
- `name`
- `password`
- `status`
- `role_ids`
- `add_role_ids`
- `remove_role_ids`

If `role_ids` is provided, it replaces the user's roles.

Example request:

```json
{
  "status": "deleted",
  "add_role_ids": [3]
}
```

#### DELETE /users/{id}
Soft-deletes a user by setting its status to `deleted`.

Possible errors:
- `401` missing authenticated user
- `403` missing required permission
- `404` user not found
- `400` invalid user status
- `400` invalid roles
- `409` user already exists

### /roles endpoint

All `/roles` endpoints currently use the `users` permission resource for authorization.
Role permissions may include the `nodes` resource with `read`, `create`, `update`, and `delete` actions.

#### GET /roles
Returns a paginated list of roles.

Query parameters:
- `page` optional, default `1`
- `count` optional, default `20`, maximum `100`

Example response:

```json
{
  "items": [
    {
      "id": 1,
      "name": "admin",
      "description": "Administrator",
      "status": "active",
      "permissions": [
        {
          "id": 1,
          "resource": "users",
          "actions": "read"
        },
        {
          "id": 2,
          "resource": "inventories",
          "actions": "update"
        }
      ]
    }
  ],
  "page": 1,
  "count": 20,
  "total": 1
}
```

#### GET /roles/{id}
Returns a single role.

#### POST /roles
Creates a role.

Request body:
- `name` required
- `description` optional
- `permissions` required array of `{resource, action}`

Example request:

```json
{
  "name": "inventory-manager",
  "description": "Can manage inventories",
  "permissions": [
    {
      "resource": "inventories",
      "action": "read"
    },
    {
      "resource": "inventories",
      "action": "update"
    }
  ]
}
```

#### PATCH /roles/{id}
Updates a role.

Request body fields are optional:
- `name`
- `description`
- `status`
- `permissions`

If `permissions` is provided, it replaces the current permissions.

#### DELETE /roles/{id}
Soft-deletes a role by setting its status to `deleted`.

Possible errors:
- `401` missing authenticated user
- `403` missing required permission
- `404` role not found
- `400` invalid role status
- `400` invalid permissions
- `409` role already exists

### /nodes endpoint

All `/nodes` endpoints require the matching `nodes` permission for the requested action.

Node management is user-facing and intended for administrative workflows such as creating and maintaining nodes from the admin panel.

Behavior:
- node secrets are accepted only on create or update
- node secrets are stored hashed and are never returned by the API
- `DELETE /nodes/{id}` is a soft delete that sets node status to `revoked`

#### GET /nodes
Returns a paginated list of nodes.

Query parameters:
- `page` optional, default `1`
- `count` optional, default `20`, maximum `100`

Example response:

```json
{
  "items": [
    {
      "id": "node-a",
      "status": "offline",
      "address": "http://node-a:8081",
      "last_seen": "2026-05-20T12:00:00Z",
      "last_callback_success": "2026-05-20T11:58:00Z",
      "last_callback_failure": null
    }
  ],
  "page": 1,
  "count": 20,
  "total": 1
}
```

#### GET /nodes/{id}
Returns a single node.

#### POST /nodes
Creates a node.

Request body:
- `id` required
- `secret` required
- `address` optional
- `status` optional, defaults to `offline`

Example request:

```json
{
  "id": "node-a",
  "secret": "plaintext-node-secret",
  "address": "http://node-a:8081",
  "status": "offline"
}
```

Example response:

```json
{
  "id": "node-a",
  "status": "offline",
  "address": "http://node-a:8081",
  "last_seen": null,
  "last_callback_success": null,
  "last_callback_failure": null
}
```

#### PATCH /nodes/{id}
Updates a node.

Request body fields are optional:
- `secret`
- `address`
- `status`

Example request:

```json
{
  "status": "disabled",
  "address": "http://node-a-new:8081"
}
```

#### DELETE /nodes/{id}
Revokes a node by setting its status to `revoked`.

Possible errors:
- `401` missing authenticated user
- `403` missing required permission
- `404` node not found
- `400` invalid node status
- `409` node already exists

### /inventories endpoint

All `/inventories` and inventory file endpoints require the matching `inventories` permission for the requested action.

#### GET /inventories
Returns a paginated list of inventories.

Query parameters:
- `page` optional, default `1`
- `count` optional, default `20`, maximum `100`

Example response:

```json
{
  "items": [
    {
      "id": 1,
      "name": "Vacation March 2026",
      "status": "online",
      "type": "folder",
      "replicas": [
        {
          "id": 1,
          "inventory_id": 1,
          "node_id": "node-1",
          "uri": "/home/username/images/Vacation March 2026",
          "status": "active",
          "type": "filesystem"
        }
      ]
    }
  ],
  "page": 1,
  "count": 20,
  "total": 1
}
```

#### GET /inventories/{id}
Returns a single inventory with its replicas.

#### POST /inventories
Creates an inventory together with its default replica.

Request body:
- `name` optional
- `type` optional, defaults to `folder`
- `node_id` required
- `uri` required

Behavior:
- if `name` is omitted, it is derived from the last path segment of `uri`
- the default replica type is currently hardcoded to `filesystem`
- the creating user is inserted into `inventory_users`
- the creating user receives `read`, `create`, `update`, and `delete` inventory permissions in `inventory_permissions`

Example request:

```json
{
  "node_id": "node-1",
  "uri": "/home/username/images/Vacation March 2026"
}
```

#### PATCH /inventories/{id}
Updates an inventory.

Request body fields are optional:
- `name`
- `status`

#### DELETE /inventories/{id}
Soft-deletes an inventory by setting its status to `deleted`.

Possible errors:
- `401` missing authenticated user
- `403` missing required permission
- `404` inventory not found
- `400` invalid inventory status
- `400` invalid inventory type
- `400` invalid inventory uri

### /inventories/{id}/files endpoint

This endpoint is read-only. Files are expected to be changed through replicas or shares.

#### GET /inventories/{id}/files
Returns a paginated list of files belonging to the inventory.

Query parameters:
- `page` optional, default `1`
- `count` optional, default `20`, maximum `100`

Example response:

```json
{
  "items": [
    {
      "id": 10,
      "inventory_id": 1,
      "relative_uri": "album/img002.jpg",
      "status": "active",
      "size": 256,
      "hash": "d5bddda567cc62b99e5695704a399c6a",
      "version": 1,
      "created": "2026-05-19T12:00:00Z",
      "modified": "2026-05-19T12:00:00Z"
    }
  ],
  "page": 1,
  "count": 20,
  "total": 1
}
```

#### GET /inventories/{id}/files/{file_id}
Returns a single file belonging to the inventory.

Possible errors:
- `401` missing authenticated user
- `403` missing required permission
- `404` inventory not found
- `404` inventory file not found

### /replicas endpoint

Replica management is exposed as a top-level endpoint. Authorization uses `inventories` permissions:
- `read` for reads
- `update` for create, update, and delete

#### GET /replicas
Returns a paginated list of replicas filtered by optional query parameters.

Query parameters:
- `page` optional, default `1`
- `count` optional, default `20`, maximum `100`
- `inventory_id`
- `node_id`
- `uri_prefix`

Example response:

```json
{
  "items": [
    {
      "id": 1,
      "inventory_id": 1,
      "node_id": "node-1",
      "uri": "/home/username/images/Vacation March 2026",
      "status": "active",
      "type": "filesystem",
      "upstream_replica_id": null
    }
  ],
  "page": 1,
  "count": 20,
  "total": 1
}
```

#### GET /replicas/{id}
Returns a single replica.

#### POST /replicas
Creates a replica.

Request body:
- `inventory_id` required
- `node_id` required
- `uri` required
- `type` required
- `upstream_replica_id` optional, nullable; when set, the new replica is downstream/read-only from replication perspective and must reference a replica in the same inventory

Example request:

```json
{
  "inventory_id": 1,
  "node_id": "node-2",
  "uri": "/mnt/backup/photos",
  "type": "filesystem",
  "upstream_replica_id": 1
}
```

#### PATCH /replicas/{id}
Updates a replica.

Request body fields are optional:
- `type`
- `status`
- `upstream_replica_id`

Replica topology:
- `upstream_replica_id: null` means the replica is a base replica and may be treated as an authoritative source for local changes.
- `upstream_replica_id` set to another replica id means the replica is downstream/read-only from replication perspective.
- upstream replicas must belong to the same inventory and a replica cannot reference itself as upstream.

#### DELETE /replicas/{id}
Soft-deletes a replica by setting its status to `deleted`.

Possible errors:
- `401` missing authenticated user
- `403` missing required permission
- `404` replica not found
- `400` invalid replica status
- `400` invalid replica type
- `400` invalid replica uri
- `400` invalid replica upstream

### /replicas/{id}/files endpoint

This endpoint is read-only. Files are expected to be changed through replica synchronization flows.

#### GET /replicas/{id}/files
Returns a paginated list of files belonging to the replica.

Query parameters:
- `page` optional, default `1`
- `count` optional, default `20`, maximum `100`
- `status` optional, filter by replica file status: `changed`, `pending`, `synchronized`, `conflict`, `error`
- `version` optional, filter by exact replica file version

Example response:

```json
{
  "items": [
    {
      "id": 10,
      "replica_id": 1,
      "version": 2,
      "status": "synchronized"
    }
  ],
  "page": 1,
  "count": 20,
  "total": 1
}
```

#### GET /replicas/{id}/files/{file_id}
Returns a single file belonging to the replica.

Possible errors:
- `401` missing authenticated user
- `403` missing required permission
- `400` invalid replica file status
- `404` replica not found
- `404` replica file not found

## Internal API

Base path for the endpoints in this section is `/internal/`.

### /auth endpoint

Node authentication model:
- node `access_token` is a signed JWT with `token_type = "node"`
- node `refresh_token` is an opaque random token
- node refresh tokens are stored server-side only as a hash
- node refresh tokens are rotated on successful refresh
- `transfer_token_public_key` is the coordinator transfer-token verification public key
- `node_id` and `secret` are used only to obtain a node token pair
- the node secret is stored hashed in the coordinator database and kept as plaintext only in node configuration
- transfer token private key is never returned to storage nodes
- node authentication does not update node address
- node authentication does not update node online/offline status
- node runtime reporting will be handled later by separate internal endpoints

Node auth config:
- `app.coordinator_url`
  - coordinator base URL used by storage nodes when authenticating with the coordinator
- `auth.node_secret`
  - plaintext node secret configured on the node side and verified against the hashed secret stored in the coordinator database

#### POST /auth/login
Authenticates a node and returns a new node token pair.

Behavior:
- validates `node_id` and `secret`
- verifies the provided node secret against the hashed secret stored in the coordinator database
- returns a signed JWT node access token with `token_type = "node"`
- returns a new opaque node refresh token
- returns `transfer_token_public_key`, which storage nodes keep in volatile memory and use to verify coordinator-issued file transfer tokens
- creates or replaces the node refresh-token session on the server
- does not update node address
- does not update node online/offline status

Request body:
- `node_id` required
- `secret` required

Example request:

```json
{
  "node_id": "node-a",
  "secret": "plaintext-node-secret-from-node-config"
}
```

Example response:

```json
{
  "node_id": "node-a",
  "access_token": "node-access-token-value",
  "refresh_token": "node-refresh-token-value",
  "access_token_expires_at": "2026-05-20T12:30:00Z",
  "refresh_token_expires_at": "2026-05-20T20:30:00Z",
  "transfer_token_public_key": "-----BEGIN PUBLIC KEY-----\n...\n-----END PUBLIC KEY-----\n"
}
```

Possible errors:
- `400` invalid JSON payload
- `401` invalid node credentials
- `403` disabled node
- `403` revoked node

#### POST /auth/refresh
Exchanges a node refresh token for a new node token pair.
The refresh token is rotated on success, so the old refresh token becomes invalid.

Behavior:
- hashes the provided refresh token and looks up the matching server-side node session
- rejects invalid, expired, revoked, disabled, or revoked-node sessions
- revokes the old node refresh-token session
- returns a new signed JWT node access token with `token_type = "node"`
- returns a new opaque node refresh token
- returns the current `transfer_token_public_key`
- does not update node address
- does not update node online/offline status

Request body:
- `refresh_token` required

Example request:

```json
{
  "refresh_token": "node-refresh-token-value"
}
```

Example response:

```json
{
  "node_id": "node-a",
  "access_token": "new-node-access-token-value",
  "refresh_token": "new-node-refresh-token-value",
  "access_token_expires_at": "2026-05-20T13:00:00Z",
  "refresh_token_expires_at": "2026-05-20T21:00:00Z",
  "transfer_token_public_key": "-----BEGIN PUBLIC KEY-----\n...\n-----END PUBLIC KEY-----\n"
}
```

Possible errors:
- `400` invalid JSON payload
- `401` invalid token
- `401` expired token
- `401` revoked token
- `403` disabled node
- `403` revoked node

#### GET /auth/me
Returns the currently authenticated node.

Behavior:
- validates the bearer node JWT
- resolves the current node from the coordinator database
- returns node identity and current status only
- does not update node address
- does not update node online/offline status

Example request:

```http
GET /internal/auth/me
Authorization: Bearer node-access-token-value
X-API-Version: 1
```

Example response:

```json
{
  "id": "node-a",
  "status": "offline"
}
```

Possible errors:
- `401` missing authenticated node
- `403` disabled node
- `403` revoked node

### /nodes endpoint

This endpoint is node-authenticated and does not require any explicit permission beyond a valid node access token.

#### POST /nodes
Reports node availability to the coordinator.

Behavior:
- validates the bearer node JWT
- resolves the current node ID from the auth token
- updates `nodes.address` from the request body
- updates `nodes.last_seen` to the current coordinator time
- returns pending durable coordinator commands for that node
- does not delete or mutate commands as part of delivery
- acts as fallback command delivery when the websocket command channel is unavailable

Request body:
- `address` required

Example request:

```json
{
  "address": "https://node-address:8081"
}
```

Example response:

```json
{
  "node_id": "node-a",
  "address": "https://node-address:8081",
  "last_seen": "2026-05-21T12:00:00Z",
  "commands": [
    {
      "id": 7,
      "node_id": "node-a",
      "type": "refresh_state",
      "status": "pending",
      "payload": {
        "placeholder": true
      },
      "created_at": "2026-05-21T11:59:00Z",
      "updated_at": "2026-05-21T11:59:00Z"
    }
  ]
}
```

Possible errors:
- `400` invalid JSON payload
- `401` missing authenticated node
- `403` disabled node
- `403` revoked node

#### GET /nodes/ws
Establishes the node websocket command channel.

Behavior:
- validates the bearer node JWT from the websocket handshake request
- upgrades the connection to websocket
- keeps the connection open for coordinator-to-node command delivery
- sends messages in the `NodeCommand` format
- does not replace heartbeat reporting; `POST /internal/nodes` is still required for node availability updates

Handshake headers:
- `Authorization: Bearer <node-access-token>`
- `X-API-Version: 1`

Example request:

```http
GET /internal/nodes/ws
Authorization: Bearer node-access-token-value
X-API-Version: 1
Upgrade: websocket
Connection: Upgrade
```

Example message:

```json
{
  "id": 7,
  "node_id": "node-a",
  "type": "refresh_state",
  "status": "pending",
  "payload": {
    "placeholder": true
  },
  "created_at": "2026-05-21T11:59:00Z",
  "updated_at": "2026-05-21T11:59:00Z"
}
```

Possible errors:
- `401` missing authenticated node
- `403` disabled node
- `403` revoked node
- `426` upgrade required

### /commands endpoint

This endpoint is node-authenticated and does not require any explicit permission beyond a valid node access token.

#### PATCH /commands/{command_id}
Updates the status of a durable coordinator command for the authenticated node.

Behavior:
- validates the bearer node JWT
- resolves the current node from the auth token
- loads the command from coordinator storage
- rejects attempts to update commands owned by another node
- validates the requested command status
- updates command status and `updated_at`
- stores `last_error` when `error` is provided

Example request:

```json
{
  "status": "failed",
  "error": "refresh failed"
}
```

Request body:
- `status` required, one of `pending`, `completed`, `failed`, `canceled`
- `error` optional, stored as `last_error`

Example response:

```json
{
  "id": 7,
  "node_id": "node-a",
  "type": "refresh_state",
  "status": "failed",
  "payload": {
    "placeholder": true
  },
  "created_at": "2026-05-21T11:59:00Z",
  "updated_at": "2026-05-21T12:05:00Z",
  "last_error": "refresh failed"
}
```

Possible errors:
- `401` missing authenticated node
- `403` disabled node
- `403` revoked node
- `403` node command belongs to another node
- `400` invalid node command status
- `404` node command not found

### /replicas endpoint

This endpoint is node-authenticated and returns replicas assigned to the authenticated node only.

#### GET /replicas
Returns the current replica assignments for the authenticated node.

Behavior:
- validates the bearer node JWT
- resolves the current node from the auth token
- returns only replicas whose `node_id` matches the authenticated node
- does not support user-style filtering or pagination

Example response:

```json
[
  {
    "id": 1,
    "inventory_id": 1,
    "node_id": "node-a",
    "uri": "/data/photos",
    "status": "active",
    "type": "filesystem"
  }
]
```

Possible errors:
- `401` missing authenticated node
- `403` disabled node
- `403` revoked node

### /replicas/{id}/files endpoint

This endpoint is node-authenticated and does not require any explicit permission beyond a valid node access token.

#### GET /replica/{id}/files
Returns the complete file state for a replica owned by the authenticated node.

Behavior:
- validates the bearer node JWT
- resolves the current node from the auth token
- verifies the replica belongs to the authenticated node
- returns an unpaginated `files` list intended for storage-node volatile state hydration
- joins `replica_files` with `inventory_files`
- includes file metadata from `inventory_files`
- includes replica-local `status` and `version` from `replica_files`

The response deliberately uses separate `inventory_*` and `replica_*` fields because inventory file version/status and replica file version/status can differ.

Example response:

```json
{
  "files": [
    {
      "file_id": 10,
      "replica_id": 7,
      "inventory_id": 3,
      "relative_uri": "album/img.jpg",
      "size": 200,
      "hash": "inventory-hash",
      "inventory_status": "active",
      "inventory_version": 5,
      "replica_status": "pending",
      "replica_version": 4,
      "created": "2026-05-21T11:00:00Z",
      "modified": "2026-05-21T12:00:00Z"
    }
  ]
}
```

Possible errors:
- `401` missing authenticated node
- `403` disabled node
- `403` revoked node
- `403` replica does not belong to authenticated node
- `404` replica not found

#### POST /replica/{id}/files
Reports one or more file states detected on a specific replica.

Behavior:
- validates the bearer node JWT
- resolves the current node from the auth token
- verifies the replica belongs to the authenticated node
- if `file_id` is provided, treats the entry as an update for an existing `inventory_files` row
- if `file_id` is omitted, creates a new `inventory_files` row unless a deleted row with the same `relative_uri` can be restored
- restores a deleted `inventory_files` row to `active` when the reported `file_id` and `relative_uri` match it
- restores a deleted `inventory_files` row to `active` when a new-file report omits `file_id` but matches its `relative_uri`
- updates `inventory_files` metadata and increments file version for existing/restored files
- creates new `inventory_files` rows at version `1`
- inserts a journal row for each changed file using the previous version and `updated` action, `restored` action for deleted-to-active files, or version `0` and `created` action for newly created files
- updates the reporting replica row in `replica_files` to the new version and `synchronized`
- marks the same file on other existing replicas as `pending`
- rejects a provided `file_id` that belongs to a different inventory
- rejects a provided `file_id` when its current `relative_uri` differs from the reported `relative_uri`
- rejects a new-file report when another active file in the same inventory already has the same `relative_uri`
- rejects reports from downstream replicas whose `upstream_replica_id` is not null, because they are not authoritative sources for local changes

Request body:
- `files` required
- each file entry contains:
  - `file_id` optional; omitted means a new file unless a deleted file with the same `relative_uri` is restored
  - `relative_uri`
  - `file_size`
  - `file_hash`
  - `created_time`
  - `modified_time`

Example request:

```json
{
  "files": [
    {
      "file_id": 10,
      "relative_uri": "photos/image025.jpg",
      "file_size": 200,
      "file_hash": "new-hash",
      "created_time": "2026-05-21T12:00:00Z",
      "modified_time": "2026-05-21T12:00:00Z"
    }
  ]
}
```

Successful response:
- `204 No Content`

Possible errors:
- `400` invalid JSON payload
- `401` missing authenticated node
- `403` disabled node
- `403` revoked node
- `403` replica does not belong to authenticated node
- `400` invalid replica file update
- `404` replica not found
- `404` inventory file not found

### /replicas/{replica_id}/files/{file_id}/content endpoint

This endpoint is served by storage nodes when `app.storage = true`. It is not a coordinator-relayed download endpoint.

#### GET /replicas/{replica_id}/files/{file_id}/content?version=123
Streams local replica file content from a source storage node to a target storage node.

Behavior:
- requires `Authorization: Bearer <transfer-token>`
- verifies the transfer token with the `transfer_token_public_key` stored in storage-node volatile runtime state
- verifies JWT signature and time claims (`iat`, `nbf`, `exp`)
- verifies token `purpose = "replica_file_transfer"`
- verifies token audience matches the source node id
- verifies token `source_replica_id`, `file_id`, and `version` match the request path/query
- verifies this node owns the requested source replica using local volatile replica state
- verifies local replica file state matches the requested file/version when that state is available
- resolves `relative_uri` under the replica URI and rejects path traversal
- streams the file without loading the full file into memory

The `version` query parameter is an authorization and integrity check. Historical versions are not stored or served; the endpoint only serves the current local file if local state matches the requested version.

Transfer tokens are coordinator-issued, short-lived, and scoped to a source replica, target replica, file id, file version, and relative URI. Transfer token generation/signing and passing transfer tokens through replication commands are intentionally not implemented yet.

Expected transfer token claims:

```json
{
  "purpose": "replica_file_transfer",
  "source_replica_id": 1,
  "target_replica_id": 2,
  "file_id": 10,
  "version": 123,
  "relative_uri": "album/img001.jpg",
  "iss": "coordinator",
  "aud": "source-node-id",
  "sub": "target-node-id",
  "iat": 1234567890,
  "nbf": 1234567890,
  "exp": 1234568790
}
```

Example request:

```http
GET /internal/replicas/1/files/10/content?version=123
Authorization: Bearer transfer-token-value
```

Successful response:
- `200 OK`
- body is raw file content
- `Content-Length` is set when local file size is available

Possible errors:
- `401` missing, invalid, expired, or unverifiable transfer token
- `403` token is valid but not authorized for the requested source replica/file/version
- `404` local replica or file does not exist
- `409` local replica file state does not match the requested version

Not implemented in this change:
- coordinator transfer-token generation/signing
- passing transfer tokens through `reconcile_replica` commands
- actual `reconcile_replica` file copy logic
