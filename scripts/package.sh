#!/usr/bin/env sh
set -eu

if [ "$#" -ne 1 ]; then
  echo "usage: $0 VERSION" >&2
  exit 2
fi

VERSION=$1
case "$VERSION" in
  ""|"."|".."|*[!A-Za-z0-9._+-]*)
    echo "invalid version: $VERSION" >&2
    exit 2
    ;;
esac

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
DIST_DIR=${LLM_STATUS_DIST_DIR:-"$REPO_DIR/dist"}
WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/llm-status-package.XXXXXX")
trap 'rm -rf "$WORK_DIR"' EXIT HUP INT TERM
OUTPUT_DIR="$WORK_DIR/output"

mkdir -p "$OUTPUT_DIR"

COMMIT=$(git -C "$REPO_DIR" rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS="-s -w -X github.com/dvgamerr/llm-status/internal/app.Version=$VERSION -X github.com/dvgamerr/llm-status/internal/app.Commit=$COMMIT -X github.com/dvgamerr/llm-status/internal/app.Date=$BUILD_DATE"

for ARCH in arm64 amd64; do
  NAME="llm-status_${VERSION}_linux_${ARCH}"
  PACKAGE_DIR="$WORK_DIR/$NAME"
  mkdir -p "$PACKAGE_DIR"
  (
    cd "$REPO_DIR"
    CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -trimpath -ldflags="$LDFLAGS" -o "$PACKAGE_DIR/llm-status" ./cmd/llm-status
  )
  cp "$REPO_DIR/README.md" "$PACKAGE_DIR/README.md"
  cp "$REPO_DIR/configs/claude-settings.json" "$PACKAGE_DIR/claude-settings.json"
  tar -C "$WORK_DIR" -czf "$OUTPUT_DIR/$NAME.tar.gz" "$NAME"
done

(
  cd "$OUTPUT_DIR"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum \
      "./llm-status_${VERSION}_linux_amd64.tar.gz" \
      "./llm-status_${VERSION}_linux_arm64.tar.gz" > SHA256SUMS
  else
    shasum -a 256 \
      "./llm-status_${VERSION}_linux_amd64.tar.gz" \
      "./llm-status_${VERSION}_linux_arm64.tar.gz" > SHA256SUMS
  fi
)

mkdir -p "$DIST_DIR"
for PACKAGE in "$OUTPUT_DIR"/*.tar.gz; do
  mv -f "$PACKAGE" "$DIST_DIR/"
done
mv -f "$OUTPUT_DIR/SHA256SUMS" "$DIST_DIR/SHA256SUMS"

echo "packages written to $DIST_DIR"
