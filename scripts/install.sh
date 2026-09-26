#!/usr/bin/env sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
PREFIX=${PREFIX:-"$HOME/.local"}
DESTINATION="$PREFIX/bin/llm-status"

if [ "$#" -gt 1 ]; then
  echo "usage: $0 [path-to-llm-status-binary]" >&2
  exit 2
fi

if [ "$#" -eq 1 ]; then
  if [ ! -f "$1" ] || [ ! -r "$1" ]; then
    echo "binary is not a readable file: $1" >&2
    exit 1
  fi
else
  if ! command -v go >/dev/null 2>&1; then
    echo "go is required when no prebuilt binary is supplied" >&2
    exit 1
  fi
fi

mkdir -p "$PREFIX/bin"
TEMP_BINARY=$(mktemp "${TMPDIR:-/tmp}/llm-status.XXXXXX")
STAGED_BINARY=$(mktemp "$PREFIX/bin/.llm-status.XXXXXX")
trap 'rm -f "$TEMP_BINARY" "$STAGED_BINARY"' EXIT HUP INT TERM

if [ "$#" -eq 1 ]; then
  cp "$1" "$TEMP_BINARY"
else
  (
    cd "$REPO_DIR"
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "$TEMP_BINARY" ./cmd/llm-status
  )
fi

install -m 0755 "$TEMP_BINARY" "$STAGED_BINARY"
mv -f "$STAGED_BINARY" "$DESTINATION"

echo "installed $DESTINATION"
echo "next: add configs/claude-settings.json to ~/.claude/settings.json, then run llm-status tui"
