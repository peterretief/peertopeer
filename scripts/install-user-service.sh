#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SERVICE_NAME="peertopeer-dstore.service"
SERVICE_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
SERVICE_FILE="$SERVICE_DIR/$SERVICE_NAME"

mkdir -p "$ROOT_DIR/bin" "$ROOT_DIR/outfiles" "$ROOT_DIR/.dstore-shards" "$SERVICE_DIR"
go build -o "$ROOT_DIR/bin/dstore" ./cmd/dstore

cat > "$SERVICE_FILE" <<UNIT
[Unit]
Description=PeerToPeer DStore shard server and outfiles watcher
Documentation=https://github.com/peterretief/peertopeer
After=network-online.target tailscaled.service
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=$ROOT_DIR
ExecStart=$ROOT_DIR/bin/dstore agent -origin $ROOT_DIR/outfiles -shards $ROOT_DIR/.dstore-shards -addr :8080
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
UNIT

systemctl --user daemon-reload

echo "Installed $SERVICE_FILE"
echo "Start:   systemctl --user start peertopeer-dstore"
echo "Status:  systemctl --user status peertopeer-dstore"
echo "Logs:    journalctl --user -u peertopeer-dstore -f"
echo "Stop:    systemctl --user stop peertopeer-dstore"
echo "Enable:  systemctl --user enable peertopeer-dstore"
