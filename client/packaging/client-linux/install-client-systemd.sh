#!/bin/sh
set -eu

DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
if [ "${1:-}" != "--yes" ]; then
  echo "This explicitly installs/starts a persistent system service. Run with --yes [absolute-config-path]." >&2
  exit 1
fi
if [ "$(id -u)" -ne 0 ]; then
  exec sudo sh "$0" "$@"
fi
shift
umask 077
case "$(uname -m)" in x86_64|amd64) ;; *) echo "This package requires Linux x86_64/amd64." >&2; exit 1 ;; esac
for required in systemctl systemd-analyze runuser getent stat install sed; do
  command -v "$required" >/dev/null 2>&1 || { echo "Required system command is missing: $required" >&2; exit 1; }
done
trusted_path() {
  p="$1"
  while :; do
    if [ -L "$p" ]; then echo "Refusing linked service path: $p" >&2; exit 1; fi
    if [ -e "$p" ]; then
      owner="$(stat -c %u -- "$p")"
      mode="$(stat -c %a -- "$p")"
      if [ "$owner" -ne 0 ] || [ "$((0$mode & 18))" -ne 0 ]; then
        echo "Refusing writable/non-root service path: $p" >&2; exit 1
      fi
    fi
    [ "$p" = / ] && break
    p="$(dirname -- "$p")"
  done
}
UNIT_NAME=1cat-tunnel-client.service
UNIT_PATH="/etc/systemd/system/$UNIT_NAME"
INSTALL_DIR=/usr/local/lib/1cat-tunnel
trusted_path "$UNIT_PATH"
trusted_path "$INSTALL_DIR"
SERVICE_USER="${SUDO_USER:-root}"
if [ -f "$UNIT_PATH" ]; then
  SERVICE_USER="$(systemctl show "$UNIT_NAME" -p User --value)"
  SERVICE_USER="${SERVICE_USER:-root}"
fi
SERVICE_HOME="$(getent passwd "$SERVICE_USER" | cut -d: -f6)"
SERVICE_GROUP="$(id -gn "$SERVICE_USER")"
test -n "$SERVICE_HOME" || { echo "Cannot resolve the service user" >&2; exit 1; }

BINARY="$DIR/tunnel-client"
if [ ! -f "$BINARY" ]; then BINARY="$DIR/1cat-tunnel-client"; fi
test -f "$BINARY" || { echo "Client executable is missing" >&2; exit 1; }
test -x "$BINARY" || { echo "Archive lost executable permissions; re-extract the official tar.gz." >&2; exit 1; }
runuser -u "$SERVICE_USER" -- "$BINARY" -version

CONFIG_PATH="${1:-}"
if [ -z "$CONFIG_PATH" ] && [ -f "$UNIT_PATH" ]; then
  CONFIG_PATH="$(sed -n 's/^ExecStart=.* -config "\([^"]*\)".*/\1/p; s/^ExecStart=.* -config \([^" ][^ ]*\).*/\1/p' "$UNIT_PATH" | head -n 1)"
  test -n "$CONFIG_PATH" || { echo "Existing service uses a custom command; pass its absolute config path as the first argument." >&2; exit 1; }
fi
CONFIG_PATH="${CONFIG_PATH:-$SERVICE_HOME/.config/1cat-tunnel/client-linux.json}"
case "$CONFIG_PATH" in /*) ;; *) echo "Config path must be absolute" >&2; exit 1 ;; esac
CONFIG_DIR="$(dirname -- "$CONFIG_PATH")"
# Never chmod/chown customer-controlled paths with root privileges.
runuser -u "$SERVICE_USER" -- "$BINARY" -initialize-config -config "$CONFIG_PATH"
# The built-in server catalog resolves this optional CA beside the config.
# Create it as the service owner; never overwrite a customer's trust file.
runuser -u "$SERVICE_USER" -- sh -s -- "$DIR/1cat-tunnel-ca.pem" "$CONFIG_DIR/1cat-tunnel-ca.pem" <<'ONECAT_INSTALL_CA'
set -eu
umask 077
[ ! -L "$2" ] || { echo 'Refusing linked CA file' >&2; exit 1; }
if [ -e "$2" ]; then
  [ -f "$2" ] && [ -r "$2" ] || { echo 'Existing CA file is not readable' >&2; exit 1; }
  exit 0
fi
[ -f "$1" ] || { echo 'Bundled TLS CA is missing' >&2; exit 1; }
(set -C; cat -- "$1" > "$2")
ONECAT_INSTALL_CA

STAMP="$(date +%Y%m%d-%H%M%S)-$$"
BACKUP="$INSTALL_DIR/backups/$STAMP"
install -d -m 0700 "$BACKUP"
runuser -u "$SERVICE_USER" -- cat -- "$CONFIG_PATH" > "$BACKUP/client-linux.json"
if [ -f "$UNIT_PATH" ]; then install -m 0600 "$UNIT_PATH" "$BACKUP/previous.service"; fi
WAS_ACTIVE=0
if systemctl is-active --quiet "$UNIT_NAME"; then WAS_ACTIVE=1; fi
WAS_ENABLED=0
if systemctl is-enabled --quiet "$UNIT_NAME"; then WAS_ENABLED=1; fi
if [ -f "$INSTALL_DIR/tunnel-client" ]; then
  install -m 0755 "$INSTALL_DIR/tunnel-client" "$BACKUP/tunnel-client"
fi

# Rename the staged file so an executing client is never truncated.
install -d -m 0755 "$INSTALL_DIR"
install -m 0755 "$BINARY" "$INSTALL_DIR/tunnel-client.next"
rollback_needed=0
restore_on_error() {
  result=$?
  trap - EXIT HUP INT TERM
  if [ "$result" -ne 0 ] && [ "$rollback_needed" -eq 1 ]; then
    systemctl stop "$UNIT_NAME" || true
    if [ "$WAS_ENABLED" -eq 0 ]; then systemctl disable "$UNIT_NAME" || true; fi
    if [ -f "$BACKUP/previous.service" ]; then
      install -m 0644 "$BACKUP/previous.service" "$UNIT_PATH"
    else
      rm -f -- "$UNIT_PATH"
    fi
    if [ -f "$BACKUP/tunnel-client" ]; then
      install -m 0755 "$BACKUP/tunnel-client" "$INSTALL_DIR/tunnel-client.next"
      mv -f "$INSTALL_DIR/tunnel-client.next" "$INSTALL_DIR/tunnel-client"
    fi
    systemctl daemon-reload
    if [ "$WAS_ENABLED" -eq 1 ]; then systemctl enable "$UNIT_NAME" || true; fi
    if [ "$WAS_ACTIVE" -eq 1 ]; then systemctl restart "$UNIT_NAME" || true; fi
    echo "Upgrade failed; previous service restored. Backup: $BACKUP" >&2
  fi
  exit "$result"
}
trap restore_on_error EXIT
trap 'exit 130' HUP INT TERM

unit_quote() {
  printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g; s/%/%%/g'
}
CONFIG_ESCAPED="$(unit_quote "$CONFIG_PATH" | sed 's/\$/$$/g')"
DIRECTORY_ESCAPED="$(unit_quote "$CONFIG_DIR")"
DIRECTORY_RAW="$(printf '%s' "$CONFIG_DIR" | sed 's/%/%%/g')"
HOME_ESCAPED="$(unit_quote "$SERVICE_HOME")"
tee "$UNIT_PATH.next" >/dev/null <<EOF
[Unit]
Description=1Cat Tunnel Client
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
User=$SERVICE_USER
Group=$SERVICE_GROUP
WorkingDirectory=$DIRECTORY_RAW
Environment="HOME=$HOME_ESCAPED"
ExecStart="$INSTALL_DIR/tunnel-client" -managed -service-mode -pause-on-exit=false -config "$CONFIG_ESCAPED"
Restart=always
RestartSec=5
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths="$DIRECTORY_ESCAPED"

[Install]
WantedBy=multi-user.target
EOF
chmod 0644 "$UNIT_PATH.next"
# On a fresh machine the final executable does not exist yet. Verify the
# staged executable, while retaining the canonical path in the installed unit.
sed "s|^ExecStart=\"$INSTALL_DIR/tunnel-client\"|ExecStart=\"$INSTALL_DIR/tunnel-client.next\"|" "$UNIT_PATH.next" > "$BACKUP/candidate.service"
systemd-analyze verify "$BACKUP/candidate.service"
rollback_needed=1
mv -f "$INSTALL_DIR/tunnel-client.next" "$INSTALL_DIR/tunnel-client"
mv -f "$UNIT_PATH.next" "$UNIT_PATH"
systemctl daemon-reload
systemctl enable "$UNIT_NAME"
systemctl restart "$UNIT_NAME"
systemctl is-active --quiet "$UNIT_NAME"
attempt=0
until runuser -u "$SERVICE_USER" -- "$INSTALL_DIR/tunnel-client" -status -config "$CONFIG_PATH" >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  [ "$attempt" -lt 10 ] || { echo "Authenticated health check failed" >&2; exit 1; }
  sleep 1
done
rollback_needed=0
echo "Local management panel: http://127.0.0.1:51888/"
echo "Config: $CONFIG_PATH"
echo "Upgrade backup: $BACKUP"
echo "Use the service owner's account to run: $INSTALL_DIR/tunnel-client -panel -config $CONFIG_PATH"
