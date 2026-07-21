#!/usr/bin/env bash
set -euo pipefail

OUTPUT_DIR="bin"

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo "unknown")}"
BUILD_DATE="${BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
GIT_COMMIT="${GIT_COMMIT:-$(git rev-parse HEAD 2>/dev/null || echo "unknown")}"

LDFLAGS="-X replica/internal/buildinfo.Version=${VERSION} \
-X replica/internal/buildinfo.BuildDate=${BUILD_DATE} \
-X replica/internal/buildinfo.Commit=${GIT_COMMIT}"

build_target() {
    local goos="$1"
    local goarch="$2"
    local extension=""
    local target_dir="${OUTPUT_DIR}/${goos}-${goarch}"

    if [[ "$goos" == "windows" ]]; then
        extension=".exe"
    fi

    mkdir -p "$target_dir"

    echo "Building ${goos}/${goarch}..."

    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
        go build \
        -trimpath \
        -ldflags "$LDFLAGS" \
        -o "${target_dir}/replica${extension}" \
        ./cmd/api

    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
        go build \
        -trimpath \
        -ldflags "$LDFLAGS" \
        -o "${target_dir}/replica-seed${extension}" \
        ./cmd/seed
}

TARGET="${1:-all}"

case "$TARGET" in
    linux-amd64)
        build_target linux amd64
        ;;

    linux-arm64)
        build_target linux arm64
        ;;

    windows-amd64)
        build_target windows amd64
        ;;

    all)
        build_target linux amd64
        build_target linux arm64
        build_target windows amd64
        ;;

    *)
        echo "Usage: $0 {linux-amd64|linux-arm64|windows-amd64|all}" >&2
        exit 1
        ;;
esac
