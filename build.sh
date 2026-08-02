#!/bin/bash
# Builds the diskusage binary using a throwaway golang container (no local Go
# toolchain needed) and drops it straight into the compose directory Docker
# runs from, so a redeploy is just `docker restart diskusage` — no image
# rebuild, no `docker compose up --build`.
set -euo pipefail

SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="${DISKUSAGE_DEPLOY_DIR:-$HOME/homelab/compose/diskusage}"
GO_IMAGE="golang:1.22-alpine"

mkdir -p "$DEPLOY_DIR"

echo "==> Building diskusage ($SRC_DIR -> $DEPLOY_DIR/diskusage)"
docker run --rm \
  -v "$SRC_DIR":/src -w /src \
  -v "$DEPLOY_DIR":/out \
  -e CGO_ENABLED=0 -e GOOS=linux -e GOARCH=amd64 \
  "$GO_IMAGE" \
  go build -o /out/diskusage .

echo "==> Built. Redeploy with:"
echo "    docker restart diskusage"
