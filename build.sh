#!/usr/bin/env bash
set -euo pipefail

OUTPUT_DIR="bin"

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo "unknown")}"
BUILD_DATE="${BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
GIT_COMMIT="${GIT_COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")}"

LDFLAGS="-X replica/internal/buildinfo.Version=${VERSION} \
-X replica/internal/buildinfo.BuildDate=${BUILD_DATE} \
-X replica/internal/buildinfo.Commit=${GIT_COMMIT}"

build_target() {
    local goos="$1"
    local goarch="$2"
    local output_name="$3"
    local goarm="${4:-}"
    local extension=""

    if [[ "$goos" == "windows" ]]; then
        extension=".exe"
    fi

    local target_dir="${OUTPUT_DIR}/${output_name}"
    mkdir -p "$target_dir"

    echo "Building ${output_name}..."

    if [[ -n "$goarm" ]]; then
        CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" GOARM="$goarm" \
            go build \
            -trimpath \
            -ldflags "$LDFLAGS" \
            -o "${target_dir}/replica${extension}" \
            ./cmd/api

        CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" GOARM="$goarm" \
            go build \
            -trimpath \
            -ldflags "$LDFLAGS" \
            -o "${target_dir}/replica-seed${extension}" \
            ./cmd/seed
    else
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
    fi
}

detect_current_target() {
    local os
    local arch

    os="$(uname -s)"
    arch="$(uname -m)"

    if [[ "$os" != "Linux" ]]; then
        echo "Unsupported host operating system: ${os}" >&2
        echo "Automatic detection currently supports Linux only." >&2
        exit 1
    fi

    case "$arch" in
        x86_64)
            echo "linux-amd64"
            ;;
        aarch64)
            echo "linux-arm64"
            ;;
        armv7l)
            echo "linux-armv7"
            ;;
        *)
            echo "Unsupported Linux architecture: ${arch}" >&2
            exit 1
            ;;
    esac
}

TARGET="${1:-$(detect_current_target)}"

case "$TARGET" in
    linux-amd64)
        build_target linux amd64 linux-amd64
        ;;

    linux-arm64)
        build_target linux arm64 linux-arm64
        ;;

    linux-armv7)
        build_target linux arm linux-armv7 7
        ;;

    windows-amd64)
        build_target windows amd64 windows-amd64
        ;;

    all)
        build_target linux amd64 linux-amd64
        build_target linux arm64 linux-arm64
        build_target linux arm linux-armv7 7
        build_target windows amd64 windows-amd64
        ;;

    *)
        echo "Usage: $0 [linux-amd64|linux-arm64|linux-armv7|windows-amd64|all]" >&2
        exit 1
        ;;
esac
