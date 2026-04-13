#!/bin/sh
set -eu

DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
UNIT_PATH="/etc/systemd/system/1cat-tunnel-server.service"

sudo tee "$UNIT_PATH" > /dev/null <<EOF
[Unit]
Description=1cat Tunnel Server
After=network.target

[Service]
Type=simple
WorkingDirectory=$DIR
ExecStart=/bin/sh $DIR/start-server.sh
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now 1cat-tunnel-server.service
sudo systemctl status 1cat-tunnel-server.service --no-pager
