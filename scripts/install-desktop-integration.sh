#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MIME_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/mime"
APP_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/applications"
BIN_DIR="$ROOT_DIR/bin"
WRAPPER="$BIN_DIR/dstore-restore-file"
DESKTOP_FILE="$APP_DIR/peertopeer-dstore-restore.desktop"
MIME_XML="$MIME_DIR/packages/application-x-dstore.xml"

mkdir -p "$BIN_DIR" "$MIME_DIR/packages" "$APP_DIR" "$ROOT_DIR/outfiles" "$ROOT_DIR/.dstore-shards"
go build -o "$BIN_DIR/dstore" ./cmd/dstore
install -m 0755 "$ROOT_DIR/scripts/dstore-restore-file.sh" "$WRAPPER"
install -m 0644 "$ROOT_DIR/desktop/application-x-dstore.xml" "$MIME_XML"

cat > "$DESKTOP_FILE" <<UNIT
[Desktop Entry]
Type=Application
Name=DStore Restore
Comment=Restore a file from a DStore manifest
Exec=$WRAPPER %f
Terminal=false
NoDisplay=true
MimeType=application/x-dstore;
Categories=Utility;
UNIT

if command -v update-mime-database >/dev/null 2>&1; then
  update-mime-database "$MIME_DIR"
fi

if command -v update-desktop-database >/dev/null 2>&1; then
  update-desktop-database "$APP_DIR" >/dev/null 2>&1 || true
fi

if command -v xdg-mime >/dev/null 2>&1; then
  xdg-mime default peertopeer-dstore-restore.desktop application/x-dstore
fi

echo "Installed .dstore desktop integration"
echo "MIME type: application/x-dstore"
echo "Handler:   $DESKTOP_FILE"
echo "Wrapper:   $WRAPPER"
echo "Test:      xdg-open /path/to/file.dstore"
