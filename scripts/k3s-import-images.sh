#!/usr/bin/env bash
# k3s-import-images.sh — Import podman-built images into k3s containerd.
set -euo pipefail

VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"

echo "Importing Alcove images into k3s (version: ${VERSION})..."

for img in bridge gate skiff-base; do
    LOCAL_TAG="localhost/alcove-${img}:${VERSION}"
    if ! podman image exists "${LOCAL_TAG}" 2>/dev/null; then
        echo "ERROR: Image ${LOCAL_TAG} not found. Run 'make build-images' first."
        exit 1
    fi
    echo "  ${LOCAL_TAG}..."
    podman save "${LOCAL_TAG}" | sudo k3s ctr images import -
done

echo "Images imported."
