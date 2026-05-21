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
Returns replicas filtered by optional query parameters:
- `inventory_id`
- `node_id`
- `uri_prefix`

Example response:

```json
[
  {
    "id": 1,
    "inventory_id": 1,
    "node_id": "node-1",
    "uri": "/home/username/images/Vacation March 2026",
    "status": "active",
    "type": "filesystem"
  }
]
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

Example request:

```json
{
  "inventory_id": 1,
  "node_id": "node-2",
  "uri": "/mnt/backup/photos",
  "type": "filesystem"
}
```

#### PATCH /replicas/{id}
Updates a replica.

Request body fields are optional:
- `type`
- `status`

#### DELETE /replicas/{id}
Soft-deletes a replica by setting its status to `deleted`.

Possible errors:
- `401` missing authenticated user
- `403` missing required permission
- `404` replica not found
- `400` invalid replica status
- `400` invalid replica type
- `400` invalid replica uri

### /replicas/{id}/files endpoint

This endpoint is read-only. Files are expected to be changed through replica synchronization flows.

#### GET /replicas/{id}/files
Returns a paginated list of files belonging to the replica.

Query parameters:
- `page` optional, default `1`
- `count` optional, default `20`, maximum `100`

Example response:

```json
{
  "items": [
    {
      "id": 1,
      "file_id": 10,
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
- `node_id` and `secret` are used only to obtain a node token pair
- the node secret is stored hashed in the coordinator database and kept as plaintext only in node configuration
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
  "refresh_token_expires_at": "2026-05-20T20:30:00Z"
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
  "refresh_token_expires_at": "2026-05-20T21:00:00Z"
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
- returns a placeholder task list for future coordinator-to-node work distribution

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
  "tasks": []
}
```

Possible errors:
- `400` invalid JSON payload
- `401` missing authenticated node
- `403` disabled node
- `403` revoked node
