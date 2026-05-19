# API

Base path for all documented endpoints is `/api/`. API versioning is implemented trough the request header.

## /auth endpoint
Authentication is token-based. Login returns an access token and a refresh token. Protected endpoints expect the access token in the `Authorization` header as `Bearer <access_token>`.

### POST /auth/login
Authenticates a user and returns a new token pair.

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
- `403` password expired

### POST /auth/refresh
Exchanges a refresh token for a new token pair.

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

### POST /auth/logout
Invalidates the current access token.

Request headers:
- `Authorization: Bearer <access_token>`

Example request:
```http
POST /api/v1/auth/logout
Authorization: Bearer access-token-value
```

Successful response:
- `204 No Content`

Possible errors:
- `401` invalid token

### GET /auth/me
Returns the currently authenticated user with expanded roles and permissions.

Request headers:
- `Authorization: Bearer <access_token>`

Example request:
```http
GET /api/v1/auth/me
Authorization: Bearer access-token-value
```

Example response:
```json
{
  "id": 1,
  "first_name": "John",
  "last_name": "Smith",
  "username": "jsmith",
  "status": "Active",
  "password_expires_at": "2026-04-30T12:00:00Z"
}
```

Possible errors:
- `401` missing authenticated user

## /users endpoint

Create user:
name
password (unencrypted)

Update user:
name
password (unencrypted)
status

Delete user:
set status to deleted, same as updating and changing status to deleted

Response:
complete user object with:
id, 
name, 
password (ecrypted),
status

## /roles endpoint
Create role:
Name
Description
Permissions

Update role:
Name
Description
Permissions
Status

Delete user:
set status to deleted, same as updating and changing status to deleted

Response:
Complete role object with
id
Name
Description
Permissions
Status

## /inventories endpoint
Create inventory:
When creating an inventory, user must specify uri and that uri is used to create the first replica, 
because every inventory must have at least one replica.
For now replica type will be "filesystem" unless something else is explicitly specified, 
but leave a placeholder in code to determine type from url
If inventory name is not specified, folder name (last segment of the path) is used,
for example  if the folder is "/home/username/images/Vacation March 2026", inventory name is "Vacation March 2026"


