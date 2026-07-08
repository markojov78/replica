# Configuration

Replica loads configuration from an optional config file and environment variables. Supported config file names are 
`config.json`, `config.yaml`, `config.yml`, and `config.toml`. Set `CONFIG_FILE` to load a specific config file path.

Environment variables override config file values. Empty environment variables are ignored.

Duration values use Go duration syntax such as `15s`, `10m`, or `8h`.

Storage profiles configure credentials and connection settings for cloud storage providers. 
Profiles live under `storage.profiles` and are keyed by profile name. 
Profile names are case-insensitive: names from files and environment variables are normalized to lowercase, 
so `aws` and `AWS` refer to the same profile.

There is no limit on the number of profiles. Each profile supports only the fields documented below, and every field is optional. 
Storage profiles have no built-in defaults. Environment variables for profiles use 
`STORAGE_PROFILES_<PROFILE_NAME>_<FIELD_NAME>`, for example `STORAGE_PROFILES_AWS_ACCESS_KEY_ID` or 
`STORAGE_PROFILES_BACKBLAZE_ENDPOINT`.

## Config file path

Config value: n/a  
Environment variable: `CONFIG_FILE`  
Type: string  
Mandatory: no
Default value: unset

Optional environment-only path to a config file. When unset, Replica searches for `config.json`, `config.yaml`,
`config.yml`, then `config.toml` in the current working directory.

## Node identifier

Config value: `app.node_id`  
Environment variable: `APP_NODE_ID`  
Type: string  
Mandatory: yes
Default value: n/a

Node identifier used by this Replica process. Must not be empty.

## Coordinator mode flag

Config value: `app.coordinator`  
Environment variable: `APP_COORDINATOR`  
Type: bool  
Mandatory: no
Default value: false

Enables coordinator mode for this process. At least one of `app.coordinator` or `app.storage` must be true.

## Storage mode flag

Config value: `app.storage`  
Environment variable: `APP_STORAGE`  
Type: bool  
Mandatory: no
Default value: false

Enables storage-node mode for this process. At least one of `app.coordinator` or `app.storage` must be true.

## Coordinator URL

Config value: `app.coordinator_url`  
Environment variable: `APP_COORDINATOR_URL`  
Type: string  
Mandatory: no
Default value: empty string

Coordinator base URL used by storage nodes. Required when `app.storage` is true and `app.coordinator` is false.

## Node address

Config value: `app.node_address`  
Environment variable: `APP_NODE_ADDRESS`  
Type: string  
Mandatory: no
Default value: empty string

Address this storage node reports to the coordinator for node-to-node communication. Required when `app.storage`
is true and `app.coordinator` is false.

## Heartbeat interval

Config value: `app.heartbeat_interval`  
Environment variable: `APP_HEARTBEAT_INTERVAL`  
Type: duration string  
Mandatory: no
Default value: `10m`

Interval between storage-node heartbeat reports. Must be greater than zero when storage mode is enabled.

## API request timeout

Config value: `app.api_request_timeout`  
Environment variable: `APP_API_REQUEST_TIMEOUT`  
Type: duration string  
Mandatory: no
Default value: `15s`

Timeout for API requests made by storage nodes. Must be greater than zero when storage mode is enabled.

## File transfer timeout

Config value: `app.file_transfer_timeout`  
Environment variable: `APP_FILE_TRANSFER_TIMEOUT`  
Type: duration string  
Mandatory: no
Default value: `30m`

Timeout for file transfer operations. Must be greater than zero when storage mode is enabled.

## Thumbnail sizes

Config value: `sharing.thumbnail_sizes`  
Environment variable: `SHARING_THUMBNAIL_SIZES`  
Type: integer array  
Mandatory: no
Default value: `[256, 512, 1024]`

Allowed thumbnail sizes. Config files use an integer array. Environment values may be a comma-separated list such
as `256,512,1024` or a JSON array such as `[256,512,1024]`. Values must be greater than zero.

## Default thumbnail size

Config value: `sharing.thumbnail_default_size`  
Environment variable: `SHARING_THUMBNAIL_DEFAULT_SIZE`  
Type: int  
Mandatory: no
Default value: `256`

Default thumbnail size used when a request does not specify a thumbnail size.

## Video thumbnail generation flag

Config value: `sharing.thumbnails_generate_for_video`  
Environment variable: `SHARING_THUMBNAILS_GENERATE_FOR_VIDEO`  
Type: bool  
Mandatory: no
Default value: `true`

Enables thumbnail generation for video files.

## FFmpeg path

Config value: `sharing.ffmpeg_path`  
Environment variable: `SHARING_FFMPEG_PATH`  
Type: string  
Mandatory: no
Default value: `ffmpeg`

Path or executable name used to invoke FFmpeg.

## Inline video size limit

Config value: `sharing.video_inline_max_size_mb`  
Environment variable: `SHARING_VIDEO_INLINE_MAX_SIZE_MB`  
Type: int  
Mandatory: no
Default value: `25`

Maximum video size, in MB, eligible for inline video handling.

## Video playback flag

Config value: `sharing.video_playback_enabled`  
Environment variable: `SHARING_VIDEO_PLAYBACK_ENABLED`  
Type: bool  
Mandatory: no
Default value: `true`

Enables shared video playback support.

## Thumbnail storage directory

Config value: `sharing.thumbnail_storage`  
Environment variable: `SHARING_THUMBNAIL_STORAGE`  
Type: string  
Mandatory: no
Default value: `/tmp/replica_thumbnails`

Directory used to store generated thumbnails.

## Thumbnail storage limit

Config value: `sharing.thumbnail_storage_limit_mb`  
Environment variable: `SHARING_THUMBNAIL_STORAGE_LIMIT_MB`  
Type: int  
Mandatory: no
Default value: `250`

Thumbnail storage size limit, in MB.

## JWT secret

Config value: `auth.jwt_secret`  
Environment variable: `AUTH_JWT_SECRET`  
Type: string  
Mandatory: no
Default value: empty string

Secret used for JWT signing in user-facing admin API and sharing API. 
Required when `app.coordinator` is true.  

## Node secret

Config value: `auth.node_secret`  
Environment variable: `AUTH_NODE_SECRET`  
Type: string  
Mandatory: no
Default value: empty string

Plaintext node secret used by storage nodes when authenticating to the coordinator. 
Required when `app.storage` is true.  

## Access token duration

Config value: `auth.access_token_duration`  
Environment variable: `AUTH_ACCESS_TOKEN_DURATION`  
Type: duration string  
Mandatory: no
Default value: `30m`

Lifetime for access tokens. Must be greater than zero when coordinator validation is active.

## Refresh token duration

Config value: `auth.refresh_token_duration`  
Environment variable: `AUTH_REFRESH_TOKEN_DURATION`  
Type: duration string  
Mandatory: no
Default value: `8h`

Lifetime for refresh tokens. Must be greater than zero when coordinator validation is active.

## Share API token cache duration

Config value: `auth.share_api_token_cache_duration`  
Environment variable: `AUTH_SHARE_API_TOKEN_CACHE_DURATION`  
Type: duration string  
Mandatory: no
Default value: `5m`

Duration for caching share API token validation results.

## HTTP listen address

Config value: `http.address`  
Environment variable: `HTTP_ADDR`  
Type: string  
Mandatory: no
Default value: `:8080`

HTTP listen address. Required unless running storage-only mode without coordinator mode.

## Database driver

Config value: `database.driver`  
Environment variable: `DB_DRIVER`  
Type: string  
Mandatory: no
Default value: `sqlite`

Database driver. Supported values are `sqlite` and `postgres`. Required unless running storage-only mode without
coordinator mode.

## Database DSN

Config value: `database.dsn`  
Environment variable: `DB_DSN`  
Type: string  
Mandatory: no
Default value:  
`replica.db` for `sqlite`  
`host=localhost user=postgres password=postgres dbname=replica port=5432 sslmode=disable` for `postgres`

Database connection string. Required unless running storage-only mode without coordinator mode.

## Automatic database migration flag

Config value: `database.auto_migrate`  
Environment variable: `DB_AUTO_MIGRATE`  
Type: bool  
Mandatory: no
Default value: `true`

Enables automatic database migrations at startup.

## Seed admin name

Config value: `seed.admin_name`  
Environment variable: `SEED_ADMIN_NAME`  
Type: string  
Mandatory: no
Default value: `admin`

Initial admin username used by seed logic.

## Seed admin password

Config value: `seed.admin_password`  
Environment variable: `SEED_ADMIN_PASSWORD`  
Type: string  
Mandatory: no
Default value: `change-me`

Initial admin password used by seed logic.

## Storage profile access key ID

Config value: `storage.profiles.<profile_name>.access_key_id`  
Environment variable: `STORAGE_PROFILES_<PROFILE_NAME>_ACCESS_KEY_ID`  
Type: string  
Mandatory: no
Default value: n/a

Access key ID for the named storage profile.

## Storage profile secret access key

Config value: `storage.profiles.<profile_name>.secret_access_key`  
Environment variable: `STORAGE_PROFILES_<PROFILE_NAME>_SECRET_ACCESS_KEY`  
Type: string  
Mandatory: no
Default value: n/a

Secret access key for the named storage profile.

## Storage profile region

Config value: `storage.profiles.<profile_name>.region`  
Environment variable: `STORAGE_PROFILES_<PROFILE_NAME>_REGION`  
Type: string  
Mandatory: no
Default value: n/a

Region for the named storage profile.

## Storage profile endpoint

Config value: `storage.profiles.<profile_name>.endpoint`  
Environment variable: `STORAGE_PROFILES_<PROFILE_NAME>_ENDPOINT`  
Type: string  
Mandatory: no
Default value: n/a

Endpoint for the named storage profile.

