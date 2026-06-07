# DropOutBbox service

A distributed, self-hosted file sharing and file replication service.  
While the initial idea for the service is to facilitate storage, backup and sharing of my own photo collection,
it is not limited to specific data type: storage and replication functionality should be agnostic to data type and
frontend is intended to be extensible to present different file types (images, audio, video, documents ...)
Skeleton `Go + Huma + GORM` project for a distributed, self-hosted file sharing and replication service.

## [Detailed application description](docs/application.md)
## [API specification](docs/api.md)

## Build
To build the service, use the build script:
```bash
./build.sh
```
Or for a different platform:
```bash
./build.sh pi
```
Build script will create `bin/dropoutbox` and `bin/dropoutbox-seed` files.  

## Configure
```bash
cp config_sample.yaml config.yaml
```
## Seed

```bash
go run ./cmd/seed
```
## Run
```bash
go run ./cmd/api
```
