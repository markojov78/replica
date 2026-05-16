# Dropoutbox

Skeleton `Go + Huma + GORM` project for a distributed, self-hosted file sharing and replication service.

Current scaffold includes:

* single API binary configurable as `coordinator + storage` or `storage-only`
* seed binary for database bootstrap
* GORM models for the schema from `docs/description.md` and `docs/database.jpg`
* enum/status constants with `Valid()` helpers for documented database states
* Huma-based auth and user management endpoints under `/api`

## Run

```bash
cp config_sample.yaml config.yaml
go run ./cmd/api
```

## Seed

```bash
go run ./cmd/seed
```
