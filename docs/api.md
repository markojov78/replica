# API

This document is split into:
- Coordinator Admin API under `/api/admin/`
- Coordinator Node Control API under `/node/`
- Storage Transfer API under `/transfer/`
- Storage Sharing API under `/api/share/` and anonymous public links under `/s/`

API versioning is implemented through the `X-API-Version` request header. Supported values are `1` and `v1`.

Protected endpoints expect:

```http
Authorization: Bearer <access_token>
X-API-Version: 1
```

## Coordinator Admin API
This API is exposed ont he coordinator and used to manage nodes, inventories, replicas, shares, users and roles.  

Base path for the endpoints in this section is `/api/admin`.

### / endpoint
#### GET /
Returns service metadata for the authenticated user.  
  
Example response:  

```json
{
  "service": "Replica",
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
- `503` coordinator unavailable when called on a storage-only node

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
POST /api/admin/auth/logout
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
GET /api/admin/auth/me
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
Role permissions may include the `users`, `shares`, `inventories`, `nodes`, and `settings` resources. The `settings`
resource supports `read` and `update`; the other resources support `read`, `create`, `update`, and `delete`.

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

### /config endpoint
All `/config` endpoints require the matching `settings` permission for the requested action.
The config endpoint manages user-changeable configuration overrides persisted in the coordinator database.
This endpoint does not expose or modify bootstrap configuration required for application startup, such as database connection settings, node secrets, JWT secrets, coordinator URLs, node IDs, or HTTP listen addresses.
Only user-changeable configuration keys are accepted, other keys are rejected.

Effective configuration is resolved in this order:

1. built-in defaults
2. local config file and environment values
3. database-persisted user overrides

Database-persisted overrides affect coordinator runtime configuration and are propagated to storage nodes through the coordinator node-control API.

#### GET /config
Returns all known user-changeable configuration values.

Example response:
```json
{
  "items": [
    {
      "key": "sharing.thumbnails.sizes",
      "value": [128, 256, 512]
    },
    {
      "key": "sharing.thumbnails.default_size",
      "value": 256
    },
    {
      "key": "sharing.thumbnails.generate_video_thumbnails",
      "value": true
    },
    {
      "key": "sharing.video.inline_max_size",
      "value": "25mb"
    },
    {
      "key": "sharing.video.playback_enabled",
      "value": true
    }
  ]
}
```

Possible errors:
- `401` missing authenticated user
- `403` missing required permission

#### PATCH /config
Creates or updates one or more database-persisted configuration overrides.
```json
{
  "items": [
    {
      "key": "sharing.thumbnails.default_size",
      "value": 512
    },
    {
      "key": "sharing.video.inline_max_size",
      "value": "50mb"
    }
  ]
}
```
Behavior:
- request must contain at least one item
- every key must be a known user-changeable configuration key
- every value is validated according to the key-specific type and validation rules
- all provided updates are applied atomically
- omitted keys remain unchanged
- updated values are persisted in the coordinator database
- coordinator runtime configuration is refreshed after persistence
- a durable `refresh_config` command is created for all storage nodes

Example response:
```json
{
  "items": [
    {
      "key": "sharing.thumbnails.sizes",
      "value": [128, 256, 512],
      "source": "database"
    },
    {
      "key": "sharing.thumbnails.default_size",
      "value": 512,
      "source": "database"
    },
    {
      "key": "sharing.thumbnails.generate_video_thumbnails",
      "value": true,
      "source": "database"
    },
    {
      "key": "sharing.video.inline_max_size",
      "value": "50mb",
      "source": "database"
    },
    {
      "key": "sharing.video.playback_enabled",
      "value": true,
      "source": "default"
    }
  ]
}
```
Validation rules:
- `sharing.thumbnails.sizes` must be a non-empty list of unique positive integers
- `sharing.thumbnails.default_size` must be a positive integer and must exist in `sharing.thumbnails.sizes`
- `sharing.thumbnails.generate_video_thumbnails` must be boolean
- `sharing.video.inline_max_size` must be a valid size string
- `sharing.video.playback_enabled` must be boolean

Possible errors:
- `400` invalid JSON payload
- `400` empty config update
- `400` unknown configuration key
- `400` invalid configuration value
- `401` missing authenticated user
- `403` missing required permission

#### DELETE /config
Deletes all database-persisted configuration overrides.  
After deletion, effective configuration falls back to local config file, environment values, and built-in defaults.

Behavior:
- deletes only database-persisted user-changeable configuration overrides
- does not modify local config files
- does not modify environment variables
- does not modify bootstrap configuration
- coordinator runtime configuration is refreshed after deletion
- a durable `refresh_config` command is created for all storage nodes

Successful response:
`204 No Content`

Possible errors:
- `401` missing authenticated user
- `403` missing required permission

#### DELETE /config/{key}
Deletes one database-persisted configuration override.

After deletion, the selected key falls back to local config file, environment value, or built-in default.

Example request:
```http request
DELETE /api/admin/config/sharing.thumbnails.default_size
Authorization: Bearer access-token-value
X-API-Version: 1
```

Behavior:
- key must be a known user-changeable configuration key
- deletes only the database-persisted override for the selected key
- operation is idempotent
- local config files and environment variables are not modified
- coordinator runtime configuration is refreshed after deletion
- a durable `refresh_config` command is created for all storage nodes

Successful response:
`204 No Content`

Possible errors:
- `400` unknown configuration key
- `401` missing authenticated user
- `403` missing required permission

### /nodes endpoint
All `/nodes` endpoints require the matching `nodes` permission for the requested action.

Node management is user-facing and intended for administrative workflows such as creating and maintaining nodes from the admin panel.

Behavior:
- node secrets are accepted only on create or update
- node secrets are stored hashed and are never returned by the API
- administrators may set node status to `disabled` or `revoked`
- administrators may re-enable a `disabled` or `revoked` node by setting its status to `offline`
- `online`, `unreachable` and `offline` transitions for other nodes are managed automatically
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
      "interval": 600,
      "last_seen": "2026-05-20T12:00:00Z"
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
  - allowed values are `offline`, `disabled` and `revoked`

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
  "interval": null,
  "last_seen": null
}
```

#### PATCH /nodes/{id}
Updates a node.

Request body fields are optional:
- `secret`
- `address`
- `status`
  - may be set to `disabled` or `revoked`
  - may be set to `offline` only to re-enable a `disabled` or `revoked` node

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
- `status` optional, filter by inventory status: `active`, `deleted`

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
      ],
      "user_permissions": [
        {
          "user_id": 15,
          "permissions": ["read", "update", "delete"]
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
Creating the default replica creates durable `scan_replica` and `refresh_state` commands for its storage node.

Request body:
- `name` required
- `node_id` required
- `user_permissions` optional per-user permissions for the inventory
- exactly one of `folder_uri` or `file_uris` is required
- `folder_uri` is a non-empty URI for a folder inventory
- `file_uris` is a non-empty list of unique absolute filesystem paths, local `file://` URIs, or `s3://` object URIs

Behavior:
- `folder_uri` creates a `folder` inventory, stores the URI unchanged as the default replica prefix, and discovers files during the initial scan
- `file_uris` creates a `file` inventory representing a file set
- filesystem paths and local `file://` URIs are normalized to unified `file://` URIs; they may be mixed in one file set
- S3 file URIs in a file set must use the same bucket
- filesystem and S3 file URIs cannot be mixed
- the deepest common directory/prefix becomes the default replica URI
- one active synchronized version-`0` placeholder is created for every requested file
- the initial scan is restricted to the placeholder relative URIs; missing files are reported as deleted
- S3-derived default replicas use replica type `storage`; filesystem-derived default replicas use `filesystem`

Example requests:

```json
{
  "name": "Vacation March 2026",
  "node_id": "node-1",
  "folder_uri": "/home/username/images/Vacation March 2026",
  "user_permissions": [
    {
      "user_id": 15,
      "permissions": ["read", "update", "delete"]
    }
  ]
}
```

```json
{
  "name": "Album highlights",
  "node_id": "node-1",
  "file_uris": [
    "/home/username/images/album/file1.jpg",
    "file:///home/username/images/album/subfolder/file2.jpg"
  ]
}
```
#### PATCH /inventories/{id}
Updates an inventory.

Changing the inventory status to `deleted` is rejected with `409 inventory has active replicas` while any replica is
active.

Request body fields are optional:
- `name`
- `status`
- `user_permissions`

Behavior:   
user_permissions omitted: leave existing user permissions unchanged  
user_permissions provided: replace all explicit user permissions for this inventory/share  
user_permissions: []: remove all per-user permissions  

#### DELETE /inventories/{id}
Soft-deletes an inventory by setting its status to `deleted`.

Deletion is rejected while the inventory has any active replicas. Replicas are not automatically deleted.

Possible errors:
- `401` missing authenticated user
- `403` missing required permission
- `404` inventory not found
- `409` inventory has active replicas
- `400` invalid inventory status
- `400` invalid inventory type
- `400` invalid permissions
- `400` invalid inventory uri

### /inventories/{id}/files endpoint

This endpoint is read-only. Files are expected to be changed through replicas or shares.

#### GET /inventories/{id}/files
Returns a paginated list of files belonging to the inventory.

Query parameters:
- `page` optional, default `1`
- `count` optional, default `20`, maximum `100`
- `status` optional, filter by inventory file status: `active`, `deleted`

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
- `400` invalid inventory file status
- `404` inventory not found
- `404` inventory file not found

### /replicas endpoint

Replica management is exposed as a top-level endpoint. Authorization uses `inventories` permissions:
- `read` for reads
- `update` for create, update, and delete

Replica topology:
- `upstream_replica_id: null` means the replica is a base replica and may be treated as an authoritative source for local changes.
- `upstream_replica_id` set to another replica id means the replica is downstream/read-only from replication perspective.
- upstream replicas must be active, belong to the same inventory and a replica cannot reference itself as upstream.

#### GET /replicas
Returns a paginated list of replicas filtered by optional query parameters.

Query parameters:
- `page` optional, default `1`
- `count` optional, default `20`, maximum `100`
- `inventory_id`
- `node_id`
- `uri_prefix`
- `status` optional, filter by replica status: `active`, `deleted`

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

Creating a replica for a deleted inventory is rejected with `409 inventory is deleted`.
Creating a replica also creates a durable `refresh_state` command for the responsible storage node, in addition to
the replica's initial scan or reconciliation command.

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

When the update changes replica state, the coordinator creates a durable `refresh_state` command for the responsible
storage node.

Changing a non-deleted replica status to `deleted` is rejected with `409 replica has active shares` while any share
linked to the replica has a status other than `deleted`.

Changing a deleted replica to a non-deleted status is rejected with `409 inventory is deleted` when its inventory is
deleted. Updates that leave the replica deleted remain allowed.

Changing a deleted replica to `active` is rejected with `409` when another active replica already has the same
`node_id` and exact `uri`. The error message identifies the conflicting replica, node and URI.

Request body fields are optional:
- `type`
- `status`
- `upstream_replica_id`

#### DELETE /replicas/{id}
Soft-deletes a replica by setting its status to `deleted`.
Deletion is rejected with `409 replica has active shares` while any share linked to the replica has a status other
than `deleted`.
Deleting a replica creates a durable `refresh_state` command for the responsible storage node so it can stop runtime
work, including its replica watcher.

Possible errors:
- `401` missing authenticated user
- `403` missing required permission
- `404` replica not found
- `400` invalid replica status
- `400` invalid replica type
- `400` invalid replica uri
- `400` invalid replica upstream
- `409` inventory is deleted
- `409` replica has active shares
- `409` active replica location conflict

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

### /shares endpoint

All `/shares` endpoints require the matching `shares` permission for the requested action.

#### GET /shares
Returns a paginated list of shares.

Query parameters:
- `page` optional, default `1`
- `count` optional, default `20`, maximum `100`
- `status` optional, filter by share status: `active`, `deleted`
- `replica_id` optional
- `name` optional

Example response:
```json
{
  "items": [
    {
      "id": 1,
      "inventory_id": 1,
      "replica_id": 3,
      "name": "Vacation March 2026",
      "status": "active",
      "link_hash": "ImyZbX8zv0UrsCB7Rthq9R7nQMMKRyhT",
      "share_expiration": "2026-03-17T10:30:00Z",
      "user_permissions": [
        {
          "user_id": 15,
          "permissions": ["read", "create", "update", "delete"]
        },
        {
          "user_id": 12,
          "permissions": ["read"]
        }
      ],
      "anonymous_permissions": ["read"]
    }
  ],
  "page": 1,
  "count": 20,
  "total": 1
}
```

#### GET /shares/{id}
Returns a single share.

Example response:
```json
{
  "id": 1,
  "inventory_id": 1,
  "replica_id": 3,
  "name": "Vacation March 2026",
  "status": "active",
  "link_hash": "ImyZbX8zv0UrsCB7Rthq9R7nQMMKRyhT",
  "share_expiration": "2026-03-17T10:30:00Z",
  "user_permissions": [
    {
      "user_id": 15,
      "permissions": ["read", "create", "update", "delete"]
    }
  ],
  "anonymous_permissions": ["read"]
}
```

#### POST /shares
Creates a share.

Request body:
- `replica_id` required
- `name` optional, defaults to inventory name
- `status` optional, defaults to `active`
  - allowed values are `active`, `deleted`
- `share_expiration` optional RFC3339 timestamp, defaults to `null`
- `generate_hash` optional boolean to generate new `link_hash`
- `user_permissions` optional, per-user permissions for the share
- `anonymous_permissions` optional permissions for anonymous users

Example request:
```json
{
  "replica_id": 3,
  "name": "Vacation March 2026",
  "share_expiration": "2026-03-17T10:30:00Z",
  "generate_hash": true,
  "user_permissions": [
    {
      "user_id": 15,
      "permissions": ["read", "create", "update", "delete"]
    },
    {
      "user_id": 12,
      "permissions": ["read"]
    }
  ],
  "anonymous_permissions": ["read"]
}
```

Behavior:
anonymous_permissions omitted or `[]`: do not set anonymous permissions
anonymous_permissions provided with permissions: create share with those anonymous permissions

Example response:
```json
{
  "id": 1,
  "inventory_id": 1,
  "replica_id": 3,
  "name": "Vacation March 2026",
  "status": "active",
  "link_hash": "ImyZbX8zv0UrsCB7Rthq9R7nQMMKRyhT",
  "share_expiration": "2026-03-17T10:30:00Z",
  "user_permissions": [
    {
      "user_id": 15,
      "permissions": ["read", "create", "update", "delete"]
    },
    {
      "user_id": 12,
      "permissions": ["read"]
    }
  ],
  "anonymous_permissions": ["read"]
}
```

#### PATCH /shares/{id}
Updates a share.

Request body fields are optional:
* `name`
* `status`
* `share_expiration`
* `generate_hash`
* `user_permissions`
* `anonymous_permissions`

Behavior:   
share_expiration omitted: leave existing share expiration unchanged  
share_expiration provided as RFC3339 timestamp: replace existing share expiration  
share_expiration provided as `null` or empty string: remove existing share expiration  
generate_hash omitted: leave existing `link_hash` value unchanged  
generate_hash: true: generate a new`link_hash` value  
generate_hash: false: remove existing `link_hash` value    
user_permissions omitted: leave existing user permissions unchanged  
user_permissions provided: replace all explicit user permissions for this inventory/share  
user_permissions: []: remove all per-user permissions  
anonymous_permissions omitted: leave existing anonymous permissions unchanged
anonymous_permissions provided: replace anonymous permissions with provided permissions
anonymous_permissions: []: remove all anonymous permissions

Example request:
```json
{
  "name": "Vacation March 2026 - shared",
  "status": "active",
  "share_expiration": null,
  "generate_hash": true,
  "anonymous_permissions": []
}
```

Example response:
```json
{
  "id": 1,
  "inventory_id": 1,
  "replica_id": 3,
  "name": "Vacation March 2026 - shared",
  "status": "active",
  "link_hash": "ST7E4WQq-bNF9V26YmjpdJpZPzfdikEI",
  "share_expiration": null,
  "user_permissions": [
    {
      "user_id": 15,
      "permissions": ["read", "create", "update", "delete"]
    }
  ],
  "anonymous_permissions": []
}
```

#### DELETE /shares/{id}
Soft-deletes a share by setting its status to `deleted`.

Successful response:
* `204 No Content`

Possible errors:
* `401` missing authenticated user
* `403` missing required permission
* `404` share not found
* `400` invalid share status
* `400` invalid share name
* `400` invalid share expiration
* `400` invalid permissions
* `404` replica not found
* `409` replica is deleted
* `409` share already exists

## Storage Sharing API
This API is exposed on the storage node and used to access private aand public shares.  

Authenticated endpoints are exposed by storage nodes under `/api/share`.
Anonymous browser-friendly public endpoints are exposed under `/s`.

Storage nodes expose sharing authentication endpoints so a sharing UI can authenticate through the storage node without
knowing whether it is connected to a storage-only deployment or coordinator + storage deployment. In storage-only
mode, login and refresh are proxied to the coordinator admin auth endpoints. In coordinator + storage mode, the local
coordinator auth service is used directly.

Storage nodes do not validate passwords locally, store user tokens server-side, mint user tokens, or receive
`AUTH_JWT_SECRET`.

Storage nodes do not persist share, user or permission state. They hydrate assigned shares from the coordinator `/node/shares` endpoint during startup and whenever a `refresh_state` command is processed. The coordinator database remains the only source of truth.

Storage nodes do not receive `AUTH_JWT_SECRET` and do not validate normal user JWTs locally. For authenticated share access, a previously unseen bearer user access token is validated once through coordinator `POST /node/auth/validate-user-token`, then cached in volatile memory by token hash until the earlier of:
- the JWT expiration returned by the coordinator
- `auth.share_api_token_cache_duration`, default `5m`

When the cache entry expires, the storage node revalidates the token through the coordinator. If the coordinator is unavailable and the token is not already positively cached, the storage node returns `503`.

### /auth endpoint

#### POST /auth/login
Authenticates a user for storage sharing.

Storage-only behavior:
- proxies the request to coordinator `POST /api/admin/auth/login`
- returns the coordinator response unchanged

Coordinator + storage behavior:
- uses the local coordinator auth service directly

Request and response bodies match coordinator `POST /api/admin/auth/login`.

#### POST /auth/refresh
Refreshes a user token pair for storage sharing.

Storage-only behavior:
- proxies the request to coordinator `POST /api/admin/auth/refresh`
- returns the coordinator response unchanged

Coordinator + storage behavior:
- uses the local coordinator auth service directly

Request and response bodies match coordinator `POST /api/admin/auth/refresh`.

#### GET /auth/me
Returns the authenticated sharing user.

Authorization:
- `Authorization: Bearer <normal-user-access-token>` required

Storage-only behavior:
- validates the user access token through coordinator `POST /node/auth/validate-user-token`
- uses the in-memory positive validation cache when available

Coordinator + storage behavior:
- validates the user access token through the local coordinator auth service

Example response:
```json
{
  "user_id": 15,
  "status": "active"
}
```

Possible errors:
- `401` missing, invalid or expired user access token
- `403` inactive user
- `503` coordinator unavailable for uncached token validation in storage-only mode

### /shares endpoint
#### GET /shares
Returns a paginated list of active shares on the current storage node where the authenticated user has read permission.

Authorization:
- `Authorization: Bearer <normal-user-access-token>` required

Query parameters:
* `page` optional, default `1`
* `count` optional, default `20`, maximum `100`
* `status` optional, filter by share status: `active`, `deleted`
* `replica_id` optional
* `name` optional

Availability rules:
- share status must be `active`
- share expiration must be unset or in the future
- replica status must be `active`
- replica must belong to the authenticated storage node
- authenticated user must have share permission `read`

The response shape intentionally matches coordinator `GET /api/admin/shares`. Storage nodes still only return shares currently available from their local runtime state, so filters such as `status=deleted` return an empty page.

Example response:
```json
{
  "items": [
    {
      "id": 1,
      "inventory_id": 1,
      "replica_id": 3,
      "name": "Vacation March 2026",
      "status": "active",
      "link_hash": "ImyZbX8zv0UrsCB7Rthq9R7nQMMKRyhT",
      "share_expiration": "2026-03-17T10:30:00Z",
      "user_permissions": [
        {
          "user_id": 15,
          "permissions": ["read"]
        }
      ],
      "anonymous_permissions": ["read"]
    }
  ],
  "page": 1,
  "count": 20,
  "total": 1
}
```

#### GET /shares/{id}
Returns one readable share from the current storage node.

Errors:
- `401` missing, invalid or expired user access token
- `403` authenticated user does not have share permission `read`
- `404` share does not exist, is inactive, is expired, or is not available on this storage node
- `503` coordinator unavailable for uncached token validation

### /shares/{id}/files endpoint
#### GET /shares/{id}/files
Returns files available for read through the share.

Query parameters:
* `page` optional, default `1`
* `count` optional, default `20`, maximum `100`

Only files matching all of the following are returned:
- `inventory_files.status = active`
- local `replica_files` row exists for the share replica
- `replica_files.status = synchronized`

The response shape intentionally matches coordinator list endpoints.

Example response:
```json
{
  "items": [
    {
      "file_id": 10,
      "replica_id": 3,
      "inventory_id": 1,
      "relative_uri": "album/photo.jpg",
      "size": 12345,
      "hash": "blake3-hash",
      "inventory_status": "active",
      "inventory_version": 4,
      "replica_status": "synchronized",
      "replica_version": 4,
      "created": "2026-03-17T10:30:00Z",
      "modified": "2026-03-17T10:30:00Z"
    }
  ],
  "page": 1,
  "count": 20,
  "total": 1
}
```
#### POST /shares/{id}/files
Content-Type: multipart/form-data  

Request fields:
`relative_uri` required
`file` required

Example:
```shell
curl -X POST \
  "https://servername.com/api/share/shares/5/files" \
  -H "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI..." \
  -F "relative_uri=image.jpg" \
  -F "file=@/path/to/image.jpg"
```

Behavior:  
- Requires create permission.
- Allowed only for folder inventories.
- relative_uri must be relative, normalized, non-empty, and must not escape replica root.
- File must not already exist as active inventory file.
- Storage node writes file to local replica.  

Response:  
`202` Accepted for processing  

Errors:  
- `400` invalid relative_uri / invalid multipart request
- `401` missing, invalid or expired user access token
- `403` missing required share permission
- `409` create not allowed for inventory of type file  / active file already exists under the same relative_uri
- `503` coordinator unavailable for uncached token validation  
- `500` local storage write/delete failed  

#### DELETE /shares/{id}/files/{file_id}

Example:
```shell
curl -X DELETE \
  "https://servername.com/api/share/shares/5/files/123" \
  -H "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI..." \
  -H 'If-Match: "1"'
```

Behavior:  
- Requires delete permission.
- Allowed for folder and file inventories.
- file_id must belong to this share inventory.
- File must be active.
- Local replica file should be synchronized before delete.
- Storage node deletes local file.
- Storage node reports change to coordinator through existing replica watcher mechanism.    

Response:  
`204` No Content

For conflict safety, require client to pass expected version:
```http request
If-Match: "4"
```

Errors:  
- `401` missing, invalid or expired user access token
- `403` missing required share permission
- `404` share or file not found / unavailable on this storage node
- `409` version conflict / file not synchronized
- `428` missing If-Match
- `400` malformed If-Match
- `503` coordinator unavailable for uncached token validation
- `500` local storage write/delete failed

### /shares/{id}/files/{file_id}/content endpoint
#### GET /shares/{id}/files/{file_id}/content
Streams file content from the local replica storage.

The request identifies files by numeric `file_id`; raw filesystem paths are not accepted.
If a known active inventory file is not synchronized locally, direct content access returns `409`.

#### PUT /shares/{id}/files/{file_id}/content
Content-Type: application/octet-stream  

Example:
```shell
curl -X PUT \
  "https://servername.com/api/share/shares/6/files/208/content" \
  -H "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..." \
  -H 'If-Match: "4"' \
  -H "Content-Type: application/octet-stream" \
  --data-binary "@/path/to/file.txt"
```

Behavior:
- Requires update permission.
- Allowed for folder and file inventories.
- file_id must belong to this share inventory.
- File must be active.
- Local replica file should be synchronized before overwrite.
- Storage node replaces local content atomically where possible.

For conflict safety, require client to pass expected version:
```http request
If-Match: "4"
```

Response:  
`202` Accepted for processing  

Errors:  
- `401` missing, invalid or expired user access token
- `403` missing required share permission
- `404` share or file not found / unavailable / inactive / expired on this storage node
- `409` version conflict / file not synchronized
- `428` missing If-Match
- `400` malformed If-Match
- `503` coordinator unavailable for uncached token validation
- `500` local storage write/delete failed

### /shares/{id}/files/{file_id}/thumbnail endpoint
#### GET /shares/{id}/files/{file_id}/thumbnail
Streams thumbnail content from the server.

Query parameters:
- `size` optional
  - if omitted, the configured default thumbnail size is used
  - if provided, the value must match one of the configured allowed thumbnail sizes

Example:
`GET /shares/17/files/125/thumbnail?size=256`

Behavior:
- Thumbnail response format is `image/jpeg` for generated thumbnail or `image/svg+xml` for generic thumbnail.
- Thumbnail size must be one of the configured allowed thumbnail sizes.
- If `size` is omitted, the configured default thumbnail size is used.

Response:
```
200 OK
Content-Type: image/jpeg
Cache-Control: public, max-age=31536000, immutable
ETag: "file-125-v4-s256"
```

Errors:
- `400` invalid thumbnail size requested
- `401` missing, invalid or expired user access token
- `403` authenticated user does not have share permission read
- `404` share or file not found / unavailable / inactive / expired on this storage node
- `409` file not synchronized
- `415` thumbnail generation unsupported for this file type
- `503` coordinator unavailable for uncached token validation
- `500` thumbnail generation or local cache error

### Anonymous access

These endpoints are exposed by storage nodes under `/s`.
They require no `Authorization` header.

The public identifier is the share `link_hash`. It must be non-guessable and must not be the numeric share ID or share name. In v1, `public_id` is represented by `link_hash`; there is no separate `public_id` response field. Public access is enabled when `link_hash` is present and anonymous permissions include `read`; there is no separate `public_enabled` response field.

#### /s/{link_hash} endpoint
##### GET /s/{link_hash}
Returns the public share when:
- `link_hash` matches the share
- share status is `active`
- share expiration is unset or in the future
- replica status is `active`
- anonymous permissions include `read`

Errors:
- `404` unknown `link_hash`, missing `link_hash`, inactive share, expired share, inactive replica, or share not available on this storage node
- `403` matching public share exists but anonymous read is not allowed

#### /s/{link_hash}/files endpoint
##### GET /s/{link_hash}/files
Returns the same synchronized active file list as authenticated share file listing.

Query parameters:
* `page` optional, default `1`
* `count` optional, default `20`, maximum `100`

The response shape intentionally matches coordinator list endpoints.

##### POST /s/{link_hash}/files
Content-Type: multipart/form-data

Request fields:
`relative_uri` required
`file` required

Example:
```shell
curl -X POST \
  "https://servername.com/s/JDFpfRV6Sis2rNuwYvaLa07F-CJE4rqbEGMwbY4RBb8/files" \
  -F "relative_uri=image.jpg" \
  -F "file=@/path/to/image.jpg"
```

Behavior:
- Requires anonymous create permission.
- Allowed only for folder inventories.
- relative_uri must be relative, normalized, non-empty, and must not escape replica root.
- File must not already exist as active inventory file.
- Storage node writes file to local replica.

Response:  
`202` Accepted for processing  

Errors:  
- `400` invalid relative_uri / invalid multipart request
- `403` missing required share permission
- `404` share or link_hash not found / unavailable / inactive / expired on this storage node
- `409` create not allowed for inventory of type file / active file already exists under the same relative_uri
- `500` local storage write/delete failed

##### DELETE /s/{link_hash}/files/{file_id}

Example:
```shell
curl -X DELETE \
  "https://servername.com/s/JDFpfRV6Sis2rNuwYvaLa07F-CJE4rqbEGMwbY4RBb8/files/207" \
  -H 'If-Match: "1"'
```

Behavior:
- Requires anonymous delete permission.
- Allowed for folder and file inventories.
- file_id must belong to this share inventory.
- File must be active.
- Local replica file should be synchronized before delete.
- Storage node deletes local file.
- Storage node reports change to coordinator through existing replica watcher mechanism.  

For conflict safety, require client to pass expected version:
```http request
If-Match: "4"
```

Response:  
`204` No Content

Errors:
- `403` missing required share permission
- `404` share or file not found / unavailable on this storage node
- `409` version conflict / file not synchronized
- `428` missing If-Match
- `400` malformed If-Match
- `500` local storage write/delete failed

#### /s/{link_hash}/files/{file_id}/content endpoint
##### GET /s/{link_hash}/files/{file_id}/content
Streams file content from the local replica storage.

##### PUT /s/{link_hash}/files/{file_id}/content
Content-Type: application/octet-stream

Example:
```shell
curl -X PUT \
  "https://servername.com/s/JDFpfRV6Sis2rNuwYvaLa07F-CJE4rqbEGMwbY4RBb8/files/207/content" \
  -H 'If-Match: "2"' \
  -H "Content-Type: application/octet-stream" \
  --data-binary "@/path/to/file.txt"
```

Behavior:
- Requires anonymous update permission.
- Allowed for folder and file inventories.
- file_id must belong to this share inventory.
- File must be active.
- Local replica file should be synchronized before overwrite.
- Storage node replaces local content atomically where possible.

For conflict safety, require client to pass expected version:  
```http request
If-Match: "4"
```

Response:  
`202` Accepted for processing  

Errors:  
- `403` missing required share permission
- `404` share or file not found / unavailable on this storage node
- `409` version conflict / file not synchronized
- `428` missing If-Match
- `400` malformed If-Match
- `500` local storage write/delete failed

#### /s/{link_hash}/files/{file_id}/thumbnail endpoint
##### GET /s/{link_hash}/files/{file_id}/thumbnail
Streams thumbnail content from the server.

Query parameters:
- `size` optional
  - if omitted, the configured default thumbnail size is used
  - if provided, the value must match one of the configured allowed thumbnail sizes

Example:
`GET /shares/17/files/125/thumbnail?size=256`

Behavior:
- Thumbnail response format is `image/jpeg` for generated thumbnail or `image/svg+xml` for generic thumbnail.
- Thumbnail size must be one of the configured allowed thumbnail sizes.
- If `size` is omitted, the configured default thumbnail size is used.

Response:
```
200 OK
Content-Type: image/jpeg
Cache-Control: public, max-age=31536000, immutable
ETag: "file-125-v4-s256"
```

Errors:
- `400` invalid thumbnail size requested
- `404` share or file not found / unavailable / inactive / expired on this storage node
- `409` file not synchronized
- `415` thumbnail generation unsupported for this file type
- `503` coordinator unavailable for uncached token validation
- `500` thumbnail generation or local cache error

## Coordinator Node Control API
This API is exposed on the coordinator and used by the storage nodes to get data from the coordinator.

Base path for the endpoints in this section is `/node/`.

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
- `app.api_request_timeout`
  - timeout for coordinator control-plane API requests; defaults to `15s`
- `app.file_transfer_timeout`
  - timeout for replica file-content transfers; defaults to `30m`
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

#### POST /auth/validate-user-token
Coordinator-only internal endpoint used by storage nodes to introspect a normal user access token for the storage-node sharing API.

Authorization:
- requires `Authorization: Bearer <node-access-token>`
- any valid node access token identifies the caller as a storage node for internal API purposes

Behavior:
- validates the caller's node access token
- parses and validates the provided normal user access token using coordinator auth logic
- verifies JWT signature and expiration
- rejects node tokens
- rejects invalid or expired tokens with `401`
- rejects deleted or inactive users with `403`
- does not require refresh-token/session lookup in v1 beyond existing access-token validation
- does not expose password, roles or global permissions

Request body:
```json
{
  "access_token": "normal-user-access-token"
}
```

Example response:
```json
{
  "user_id": 15,
  "status": "active",
  "access_token_expires_at": "2026-04-07T12:30:00Z"
}
```

Possible errors:
- `401` missing authenticated node
- `401` invalid, expired or non-user access token
- `403` disabled or revoked node
- `403` deleted or inactive user

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
GET /node/auth/me
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
- updates `nodes.interval` from the heartbeat interval in seconds
- updates `nodes.last_seen` to the current coordinator time
- updates node status according to current WebSocket connectivity and heartbeat freshness
- ensures each active replica assigned to the node with pending `replica_files` has a pending `reconcile_replica`
  command for that destination replica
- returns pending durable coordinator commands for that node
- does not delete or mutate commands as part of delivery
- acts as fallback command delivery when the websocket command channel is unavailable

Request body:
- `address` required
- `interval` required, greater than zero, heartbeat interval in seconds

Example request:

```json
{
  "address": "https://node-address:8081",
  "interval": 600
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
      "payload": {},
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
- marks the node `online` while at least one WebSocket connection is active
- reconciles node status from heartbeat freshness when the last WebSocket connection closes
- sends messages in the `NodeCommand` format
- does not replace heartbeat reporting; `POST /node/nodes` is still required for node availability updates

Handshake headers:
- `Authorization: Bearer <node-access-token>`
- `X-API-Version: 1`

Example request:

```http
GET /node/nodes/ws
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
  "payload": {},
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
  "payload": {},
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
- may include deleted replicas so storage nodes can stop runtime work for them
- does not support user-style filtering or pagination

Example response:
```json
[
  {
    "id": 1,
    "inventory_id": 1,
    "inventory_type": "folder",
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

### /shares endpoint

This endpoint is node-authenticated and returns shares assigned to replicas on the authenticated node only.

#### GET /shares
Returns the current share assignments for the authenticated node.

Behavior:
- validates the bearer node JWT
- resolves the current node from the auth token
- returns only shares whose replica `node_id` matches the authenticated node
- includes per-user and anonymous share permissions
- does not support user-style filtering or pagination

Example request:

```http
GET /node/shares
Authorization: Bearer node-access-token-value
X-API-Version: 1
```

Example response:

```json
[
  {
    "id": 1,
    "inventory_id": 1,
    "replica_id": 3,
    "name": "Vacation March 2026",
    "status": "active",
    "link_hash": "ImyZbX8zv0UrsCB7Rthq9R7nQMMKRyhT",
    "share_expiration": "2026-03-17T10:30:00Z",
    "user_permissions": [
      {
        "user_id": 15,
        "permissions": ["read", "create", "update", "delete"]
      },
      {
        "user_id": 16,
        "permissions": ["read"]
      }
    ],
    "anonymous_permissions": ["read"]
  }
]
```
Behavior: 
`user_permissions` returned here are effective permissions for all the users derived from user roles and per-use permissions.  

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
- filters by `replica_files.status` when the optional `status` query parameter is provided
- joins `replica_files` with `inventory_files`
- includes file metadata from `inventory_files`
- includes replica-local `status` and `version` from `replica_files`

The response deliberately uses separate `inventory_*` and `replica_*` fields because inventory file version/status and replica file version/status can differ.

Query parameters:
- `status` optional, one of `changed`, `pending`, `synchronized`, `conflict`, `error`

Example request:

```http
GET /node/replica/7/files?status=pending
Authorization: Bearer node-access-token-value
X-API-Version: 1
```

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
- `400` invalid replica file status
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
- supports explicit `action` values: `created`, `updated`, `deleted`
- preserves backward compatibility when `action` is omitted:
  - if `file_id` is provided, treats the entry as an update for an existing `inventory_files` row
  - if `file_id` is omitted, creates a new `inventory_files` row unless a deleted row with the same `relative_uri` can be restored
- restores a deleted `inventory_files` row to `active` when the reported `file_id` and `relative_uri` match it
- restores a deleted `inventory_files` row to `active` when a new-file report omits `file_id` but matches its `relative_uri`
- updates `inventory_files` metadata and increments file version for existing/restored files
- creates new `inventory_files` rows at version `1`
- inserts a journal row for each changed file using the previous version and `updated` action, `restored` action for deleted-to-active files, or version `0` and `created` action for newly created files
- updates the reporting replica row in `replica_files` to the new version and `synchronized`
- marks the same file on other existing replicas as `pending`
- for `action = "deleted"`, sets `inventory_files.status` to `deleted`, increments `inventory_files.version`, inserts a `deleted` journal action with the previous version, marks the reporting replica file `synchronized`, marks other replicas `pending`, and creates reconciliation commands using existing source-selection logic
- if `action = "deleted"` is repeated for an already deleted inventory file, treats it as already synchronized and does not increment version or insert another journal row
- ignores content-changing reports for existing inventory files when the reporting replica is not synchronized to the current inventory version for that file
- ignores timestamp-only reports: content identity is `relative_uri` + `file_size` + `file_hash`
- requires storage backends to report `file_hash` as a BLAKE3 content hash; provider fingerprints such as S3 ETag are not file hashes
- if a reported `created` or `updated` entry has the same content identity as the authoritative active `inventory_files` row, it does not increment version, insert journal rows, mark other replicas pending, or create reconciliation commands
- if a matching no-content-change report comes from a pending replica, that replica file may be marked `synchronized` at the authoritative inventory version
- rejects a provided `file_id` that belongs to a different inventory
- rejects a provided `file_id` when its current `relative_uri` differs from the reported `relative_uri`
- for base replicas, rejects a new-file report when another active file in the same inventory already has the same `relative_uri`
- for downstream replicas whose `upstream_replica_id` is not null, does not update `inventory_files`, increment versions, or create file journal entries
- for downstream reports affecting known active inventory files, marks only the reporting `replica_files` rows `pending` and creates a `reconcile_replica` command sourced from the configured upstream
- for downstream reports affecting unknown paths or paths matching deleted inventory files, includes those paths in the `reconcile_replica` command for deletion from the downstream replica
- rejects invalid or inconsistent explicit actions

Request body:
- `files` required
- each file entry contains:
  - `action` optional; allowed values are `created`, `updated`, `deleted`
  - `file_id` optional; omitted means a new file unless a deleted file with the same `relative_uri` is restored
  - `relative_uri`
  - `file_size` required for `created` and `updated`, omitted for `deleted`
  - `file_hash` required for `created` and `updated`, omitted for `deleted`
  - `created_time` required for `created` and `updated`, omitted for `deleted`
  - `modified_time` required for `created` and `updated`, omitted for `deleted`

Validation rules:
- `action = "created"` requires omitted `file_id` and requires `relative_uri`, `file_size`, `file_hash`, `created_time`, and `modified_time`
- `action = "updated"` requires `file_id`, `relative_uri`, `file_size`, `file_hash`, `created_time`, and `modified_time`
- `action = "deleted"` requires `file_id` and `relative_uri`; `file_size`, `file_hash`, `created_time`, and `modified_time` must be omitted
- unknown `action` values return `400 invalid file action`
- timestamp-only changes must not be reported as `updated`

Example created request:

```json
{
  "files": [
    {
      "action": "created",
      "relative_uri": "photos/image026.jpg",
      "file_size": 200,
      "file_hash": "new-hash",
      "created_time": "2026-05-21T12:00:00Z",
      "modified_time": "2026-05-21T12:00:00Z"
    }
  ]
}
```

Example updated request:

```json
{
  "files": [
    {
      "action": "updated",
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

Example deleted request:

```json
{
  "files": [
    {
      "action": "deleted",
      "file_id": 10,
      "relative_uri": "photos/image025.jpg"
    }
  ]
}
```

Successful response:
- `204 No Content`

Possible errors:
- `400` invalid JSON payload
- `400` invalid file action
- `401` missing authenticated node
- `403` disabled node
- `403` revoked node
- `403` replica does not belong to authenticated node
- `400` invalid replica file update
- `404` replica not found
- `404` inventory file not found

#### PATCH /replica/{id}/files/{file_id}
Updates local status for one file on a replica owned by the authenticated storage node.

Behavior:
- validates the bearer node JWT
- resolves the current node from the auth token
- verifies the replica belongs to the authenticated node
- validates `status` against `ReplicaFileStatus` values: `changed`, `pending`, `synchronized`, `conflict`, `error`
- updates `replica_files.status` for `(replica_id, file_id)`
- when `status = "synchronized"`, requires `version` and verifies it matches `inventory_files.version`
- when `status = "synchronized"`, updates `replica_files.version` to the verified version
- if `error` is provided, logs the message on the coordinator for now
- does not update `inventory_files`
- does not create file journal entries
- does not persist the error message yet

Request body:
- `status` required
- `version` required only when `status = "synchronized"`
- `error` optional

Example request:

```json
{
  "status": "synchronized",
  "version": 5
}
```

Successful response:
- `204 No Content`

Possible errors:
- `400` invalid JSON payload
- `400` invalid replica file status
- `400` invalid replica file update
- `401` missing authenticated node
- `403` disabled node
- `403` revoked node
- `403` replica does not belong to authenticated node
- `404` replica not found
- `404` replica file not found

### /config endpoint
Returns all known user-changeable configuration values.

Example response:
```json
{
  "items": [
    {
      "key": "sharing.thumbnails.sizes",
      "value": [128, 256, 512]
    },
    {
      "key": "sharing.thumbnails.default_size",
      "value": 256
    },
    {
      "key": "sharing.thumbnails.generate_video_thumbnails",
      "value": true
    },
    {
      "key": "sharing.video.inline_max_size",
      "value": "25mb"
    },
    {
      "key": "sharing.video.playback_enabled",
      "value": true
    }
  ]
}
```

## Storage Transfer API
This API is exposed on the storage nodes and used for ndoe-to-node file transfer.

Base path for the endpoints in this section is `/transfer/`.  

Endpoints use transfer token issued by the coordinator and signed by coordinator private key.
Node can decrypt transfer token using coordinator public kay received on [node login](#post-authlogin-2).

### /replicas/{replica_id}/files/{file_id}/content endpoint
#### GET /replicas/{replica_id}/files/{file_id}/content?version=123
Streams replica file content from a source storage node to a target storage node.

Behavior:
- requires `Authorization: Bearer <transfer-token>`
- verifies the transfer token with the `transfer_token_public_key` stored in storage-node volatile runtime state
- verifies JWT signature and time claims (`iat`, `nbf`, `exp`)
- verifies token `purpose = "replica_file_transfer"`
- verifies token audience matches the source node id
- verifies token subject identifies the destination node
- verifies token `source_replica_id` matches the request path
- verifies this node owns the requested source replica using local volatile replica state
- verifies local replica file state matches the requested file/version when that state is available
- resolves `relative_uri` under the replica URI using the source replica storage backend and rejects path traversal
- streams the file without loading the full file into memory

The `version` query parameter is an authorization and integrity check. Historical versions are not stored or served; the endpoint only serves the current local file if local state matches the requested version.

Transfer tokens are coordinator-issued, short-lived, and scoped to a source replica, destination replica, source node, and destination node. A single token can authorize multiple file downloads during one `reconcile_replica` operation. Tokens are intentionally not file-specific yet; file/version validation is handled through coordinator state and `replica_files`.

Expected transfer token claims:

```json
{
  "purpose": "replica_file_transfer",
  "source_replica_id": 1,
  "destination_replica_id": 2,
  "source_node_id": "source-node-id",
  "destination_node_id": "target-node-id",
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
GET /replicas/1/files/10/content?version=123
Authorization: Bearer transfer-token-value
```

Successful response:
- `200 OK`
- body is raw file content
- `Content-Length` is set when local file size is available

Possible errors:
- `401` missing, invalid, expired, or unverifiable transfer token
- `403` token is valid but not authorized for the requested source replica
- `404` local replica or file does not exist
- `409` local replica file state does not match the requested version
