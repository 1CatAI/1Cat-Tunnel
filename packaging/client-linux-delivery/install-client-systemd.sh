#!/bin/sh
set -eu

DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
UNIT_PATH="/etc/systemd/system/1cat-tunnel-client.service"

sudo tee "$UNIT_PATH" > /dev/null <<EOF
[Unit]
Description=1cat Tunnel Client
After=network.target

[Service]
Type=simple
WorkingDirectory=$DIR
ExecStart=/bin/sh $DIR/start-client.sh
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now 1cat-tunnel-client.service
sudo systemctl status 1cat-tunnel-client.service --no-pager
