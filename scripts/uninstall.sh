#!/usr/bin/env sh
set -eu

PREFIX=${PREFIX:-"$HOME/.local"}
DESTINATION="$PREFIX/bin/llm-status"
STATE_DIR=${LLM_STATUS_STATE_DIR:-"${XDG_CACHE_HOME:-$HOME/.cache}/llm-status"}

if [ "$#" -gt 1 ] || { [ "$#" -eq 1 ] && [ "$1" != "--purge" ]; }; then
  echo "usage: $0 [--purge]" >&2
  exit 2
fi

if [ "$#" -eq 1 ] && [ "$1" = "--purge" ]; then
  NORMALIZED_STATE_DIR=${STATE_DIR%/}
  case "$NORMALIZED_STATE_DIR" in
    ""|"/"|"."|".."|"$HOME")
      echo "refusing unsafe state directory: $STATE_DIR" >&2
      exit 1
      ;;
  esac
  if [ "$(basename -- "$NORMALIZED_STATE_DIR")" != "llm-status" ]; then
    echo "refusing to purge a directory not named llm-status: $STATE_DIR" >&2
    exit 1
  fi
fi

rm -f "$DESTINATION"
echo "removed $DESTINATION"

if [ "$#" -eq 1 ] && [ "$1" = "--purge" ]; then
  rm -rf -- "$NORMALIZED_STATE_DIR"
  echo "removed state $NORMALIZED_STATE_DIR"
else
  echo "preserved state $STATE_DIR"
fi
