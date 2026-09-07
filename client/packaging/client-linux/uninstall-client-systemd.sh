#!/bin/sh
set -eu
if [ "${1:-}" != --yes ]; then
  echo 'Stops and removes the independent system service; keeps config and backups. Confirm with --yes.' >&2
  exit 1
fi
if [ "$(id -u)" -ne 0 ]; then exec sudo sh "$0" "$@"; fi
unit=/etc/systemd/system/1cat-tunnel-client.service
test ! -L "$unit" || { echo 'Refusing a linked service file' >&2; exit 1; }
fragment="$(systemctl show 1cat-tunnel-client.service -p FragmentPath --value)"
test "$fragment" = "$unit" || { echo 'Custom service location; inspect manually before removing.' >&2; exit 1; }
systemctl disable --now 1cat-tunnel-client.service
rm -f -- "$unit"
systemctl daemon-reload
echo 'Service stopped and removed. Config, historical binaries and rollback backups retained.'
