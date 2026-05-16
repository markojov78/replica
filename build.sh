#!/usr/bin/env bash
set -euo pipefail

VERSION=$(git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")
BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)
GIT_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
LDFLAGS="-X dropoutbox/internal/buildinfo.Version=$VERSION -X dropoutbox/internal/buildinfo.BuildDate=$BUILD_DATE -X dropoutbox/internal/buildinfo.Commit=$GIT_COMMIT"

mkdir -p bin

go build -ldflags "$LDFLAGS" -o bin/dropoutbox ./cmd/api
go build -ldflags "$LDFLAGS" -o bin/dropoutbox-seed ./cmd/seed
