#!/usr/bin/env bash
set -euo pipefail

notify() {
  if command -v notify-send >/dev/null 2>&1; then
    notify-send "DStore" "$1"
  fi
}

if [[ $# -lt 1 ]]; then
  notify "No .dstore file was provided"
  echo "Usage: dstore-restore-file FILE.dstore" >&2
  exit 2
fi

STUB_PATH="$1"
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DSTORE_BIN="$ROOT_DIR/bin/dstore"
OUTPUT_DIR="${DSTORE_RESTORE_DIR:-$ROOT_DIR/restored}"

if [[ ! -x "$DSTORE_BIN" ]]; then
  notify "dstore binary not found"
  echo "dstore binary not found at $DSTORE_BIN" >&2
  exit 1
fi

mkdir -p "$OUTPUT_DIR"

if OUTPUT="$($DSTORE_BIN restore -stub "$STUB_PATH" -output-dir "$OUTPUT_DIR" 2>&1)"; then
  printf '%s\n' "$OUTPUT"
  notify "$OUTPUT"
else
  STATUS=$?
  printf '%s\n' "$OUTPUT" >&2
  notify "Restore failed: $OUTPUT"
  exit "$STATUS"
fi
