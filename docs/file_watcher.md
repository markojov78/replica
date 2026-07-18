# File Scanner and Watcher

This document describes the reusable scanner and watcher infrastructure in `internal/storage`.

The goal of this layer is to discover replica file state and surface change hints in a storage-backend-specific way without coupling that logic to coordinator communication, database updates, or replication orchestration.

Storage writers use the centralized `TemporaryWritePrefix` constant for temporary files. Scanners and watchers exclude
paths whose basename starts with that prefix so incomplete writes never become replica changes.

## Interface

The shared abstractions are defined in `internal/storage/scan.go`.

### FileState

`FileState` is the normalized representation of one file discovered under a replica root.

It contains:

- `RelativeURI`: slash-separated path relative to the replica root
- `Size`: file size in bytes
- `Hash`: BLAKE3 content hash
- `Created`: best available creation time
- `Modified`: last modification time

The intent is to provide the fields needed later for inventory and replica file reconciliation while keeping the storage layer independent of persistence.

### FileChange

`FileChange` is the normalized representation of a watcher event.

It contains:

- `RelativeURI`: slash-separated path relative to the replica root
- `ChangeType`: `created`, `modified`, `deleted`, `renamed`, `unknown`, or `rescan_required`
- `PreviousRelativeURI`: optional old path for rename-style implementations
- `State`: optional `FileState` when the current file can be inspected safely

Watcher events are hints, not authoritative state transitions. If an implementation cannot interpret an event safely, it should emit `rescan_required` instead of guessing.

### Scanner

```go
type Scanner interface {
    Scan(ctx context.Context, rootURI string, oldStates map[string]FileState, targetRelativeURI ...string) ([]FileState, error)
}
```

A scanner performs a snapshot-style inventory of a replica root.
When one or more `targetRelativeURI` values are provided, the scan is restricted to those known files relative to
`rootURI`.
`oldStates` is an optional map keyed by relative URI. When a file is present in `oldStates` and the scanner can
confirm that file metadata has not changed, the scanner may reuse the known BLAKE3 hash instead of reading file
content again. A nil map or a missing entry preserves the old behavior: the scanner reads the file content and
calculates BLAKE3.

Implementation requirements:

- return file entries only, not directories
- normalize `RelativeURI` to slash-separated paths
- honor context cancellation
- populate `Size`, `Hash`, `Created`, and `Modified`
- return deterministic ordering, sorted by `RelativeURI`

Scanners are appropriate for:

- initial inventory collection
- periodic full rescans
- backends that do not support real-time notifications

### Watcher

```go
type Watcher interface {
    Watch(ctx context.Context, rootURI string, targetRelativeURIs []string) (<-chan FileChange, <-chan error, error)
}
```

A watcher produces a stream of change hints for a replica root.
`rootURI` identifies a directory or backend prefix. When `targetRelativeURIs` is nil or empty, every file under that
root is watched. When the list contains relative URIs, only those files are watched and unrelated paths are ignored.

Implementation requirements:

- start background work only after successful setup
- stop and close channels when the context is canceled
- use the error channel for operational failures
- emit `rescan_required` when event interpretation is unsafe
- avoid direct coordinator, DB, or replication updates

Watchers are appropriate for:

- low-latency local change detection
- reducing the need for constant full rescans
- backends where native events or polling can provide incremental hints

### How to implement a new backend

To add a new storage backend:

1. Implement `Scanner` for authoritative snapshot discovery.
2. Implement `Watcher` if the backend supports practical incremental change detection.
3. Reuse `FileState` and `FileChange` so later coordinator-facing code can stay backend-agnostic.
4. Normalize all paths as relative slash-separated URIs, regardless of platform-specific native paths.
5. Prefer conservative behavior. If a backend cannot determine the exact change, emit `rescan_required`.

## Filesystem

The filesystem implementation lives in:

- `internal/storage/filesystem_scanner.go`
- `internal/storage/filesystem_watcher.go`
- `internal/storage/filesystem_helpers.go`

### Filesystem scanner

`FilesystemScanner` accepts either a local directory root or a single local file.

Behavior:

- treats `rootURI` as a local filesystem path
- when `rootURI` is a directory, walks recursively through subdirectories
- when `rootURI` is a file, returns exactly one file entry for that file
- ignores directories as scan results
- ignores symlinks unless the filesystem replica has `follow_symlinks` enabled
- when `follow_symlinks` is enabled, includes file symlinks using the target file's metadata and content while keeping
  the symlink path as `RelativeURI`; directory symlinks remain ignored
- skips broken file symlinks when `follow_symlinks` is enabled instead of failing the scan
- ignores temporary write paths whose basename starts with `TemporaryWritePrefix` defined in `internal/storage/temporary_files.go`
- computes a BLAKE3 content hash for each file unless unchanged old metadata allows reusing a known hash
- normalizes relative paths using slash separators
- returns results sorted by `RelativeURI`

For single-file roots, the returned `RelativeURI` is the file basename, for example `photo.jpg`.

Hashing:

- local files use BLAKE3
- when `oldStates` has the same `Size` and `Modified` for a file, the scanner reuses the old `Hash` without opening
  and reading the file
- when `oldStates` is nil, missing that file, has an empty hash, or has different metadata, the scanner reads the file
  and calculates BLAKE3

Timestamps:

- `Modified` comes from the file modification time
- `Created` uses a helper abstraction for birth time when available
- if birth time is not available, `Created` falls back to `Modified`

This fallback is isolated behind helper functions so platform-specific improvements can be added later without changing the scanner interface.

### Filesystem watcher

`FilesystemWatcher` uses `github.com/fsnotify/fsnotify`. Its `rootURI` must identify a directory.

Behavior:

- creates recursive watches for all existing directories under the root
- when target relative URIs are provided, filters events to those files
- when target relative URIs are nil or empty, reports files throughout the directory tree
- when a new directory is created, adds watches for that directory tree
- converts `fsnotify` events into normalized `FileChange` values
- stats the file when possible and includes `State` for existing files
- follows file symlinks for metadata and content when the filesystem replica has `follow_symlinks` enabled
- removes directory watches when remove events occur
- ignores events for temporary write paths whose basename starts with`TemporaryWritePrefix` defined in `internal/storage/temporary_files.go`


Event mapping:

- `create` on a file becomes `created` with `State`
- `write`, `chmod`, and most existing-path updates become `modified` with `State`
- `remove` or `rename` when the path no longer exists becomes `deleted`
- untrusted or ambiguous situations become `rescan_required`

Important limitations:

- filesystem notifications are not treated as authoritative state
- rename correlation is not currently reconstructed into full old-path/new-path pairs
- watcher errors are surfaced on the error channel and also trigger a `rescan_required` hint

This conservative design is intentional. The watcher helps detect likely changes quickly, while the scanner remains the authoritative way to rebuild complete file state.

## S3

The S3 implementation lives in:

- `internal/storage/s3_scanner.go`
- `internal/storage/s3_watcher.go`

For the S3 scanner and watcher to work, the caller must provide an already-configured AWS SDK v2 S3 client.

The storage package does not authenticate to AWS by itself. `S3Scanner` accepts an injected `*s3.Client`, so authentication and AWS configuration are handled by the code that constructs that client.

In practice, this means the later integration layer will need to:

- load AWS SDK configuration
- construct an authenticated S3 client
- pass that client into `NewS3Scanner`
- pass the scanner into `NewS3Watcher` when polling is needed

Typical AWS SDK v2 authentication sources include:

- environment variables such as `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, and optional session token
- shared AWS config or credentials files
- profile-based configuration such as `AWS_PROFILE`
- IAM role credentials in AWS runtime environments such as EC2, ECS, or EKS
- an explicitly configured credentials provider

The scanner and watcher assume that the supplied client can list objects, read object metadata, and download object
content when a BLAKE3 hash must be calculated.

### S3 scanner

`S3Scanner` scans an S3 prefix rather than a local directory.

It accepts URIs in the form:

- `s3://bucket`
- `s3://bucket/path/to/prefix`

Behavior:

- parses the bucket and optional prefix from the S3 URI
- lists objects with `ListObjectsV2`
- handles pagination through continuation tokens
- converts object keys under the prefix into relative slash-separated URIs
- skips empty or out-of-prefix keys
- skips temporary write keys whose basename starts with `TemporaryWritePrefix` defined in `internal/storage/temporary_files.go`
- returns results sorted by `RelativeURI`

Timestamps:

- `Modified` comes from `LastModified`
- `Created` currently matches `LastModified`

Hashing and fingerprints:

- S3 file content hashes use BLAKE3, matching filesystem replicas
- ETag, object checksum, size, and `LastModified` are metadata-change hints only and are never returned as `Hash`
- when `oldStates` has the same `Size` and `Modified` for an object, the scanner reuses the old `Hash` without
  calling `HeadObject` or downloading object content
- the scanner caches BLAKE3 hashes in memory with the corresponding object metadata fingerprint
- the first scan after process startup downloads objects to establish their BLAKE3 hashes when coordinator metadata is
  unavailable
- unchanged metadata reuses the cached BLAKE3 hash without downloading the object
- new objects and objects with changed metadata are downloaded and hashed with BLAKE3

### S3 watcher

S3 does not provide a filesystem-style recursive event stream comparable to `fsnotify`, so the current watcher is implemented as polling.

`S3Watcher` works by:

1. running an initial scan
2. rescanning at a fixed interval
3. comparing the previous and current snapshots in memory
4. emitting `created`, `modified`, and `deleted` events

Metadata polling detects possible changes using:

- `RelativeURI`
- `Size`
- `Modified`
- ETag and available object checksum

When metadata indicates a possible change, the scanner calculates BLAKE3 before the change is compared with authoritative file state.

Behavior:

- context cancellation stops polling and closes the channels
- scan failures are reported on the error channel
- state is kept only in memory
- no persistence or coordinator integration happens at this layer

This polling watcher is not real-time, but it provides a clean backend-specific `Watcher` implementation while preserving the same interface used by the filesystem watcher.
