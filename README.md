# Replica

A distributed, self-hosted file sharing and file replication service made with `Go + Huma + GORM`.

While the initial idea for the service is to facilitate storage, backup and sharing of my own photo collection,
it is not limited to specific data type:  
storage and replication functionality should be agnostic to data type and
frontend is intended to be extensible to present different file types (images, audio, video, documents ...)

## [Detailed description](docs/application.md)
## [API specification](docs/api.md)

## Build
Requirements:  
go 1.25.0 or newer.

To build the service, use the build script:
```bash
./build.sh
```
Or for a different platform:
```bash
./build.sh pi
```
Build script will create `bin/replica` and `bin/replica-seed` files.  

## Configure
```bash
cp config_sample.yaml config.yaml
```
[Detailed configuration options](docs/config.md)

## Seed
```bash
go run ./cmd/seed
```
## Run
```bash
go run ./cmd/api
```
