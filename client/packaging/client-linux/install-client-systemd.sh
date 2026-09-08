#!/bin/sh
set -eu

DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
if [ "${1:-}" != "--yes" ]; then
  echo "Install the client with --yes [absolute-config-path]. Run 1cattunnel -setup afterwards to register autostart." >&2
  exit 1
fi
if [ "$(id -u)" -ne 0 ]; then
  exec sudo sh "$0" "$@"
fi
shift
MODE=install
case "${1:-}" in
  --register-only) MODE=register; shift ;;
  --start) MODE=start; shift ;;
esac
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
COMMAND_PATH=/usr/local/bin/1cattunnel
COMMAND_CONFIG_PATH="$INSTALL_DIR/client-config-path"
COMMAND_USER_PATH="$INSTALL_DIR/service-user"
trusted_path "$UNIT_PATH"
trusted_path "$INSTALL_DIR"
if [ -e "$COMMAND_PATH" ] || [ -L "$COMMAND_PATH" ]; then
  trusted_path "$COMMAND_PATH"
  grep -Fq '# 1cattunnel-system-client-launcher' "$COMMAND_PATH" || {
    echo "Refusing to replace an existing 1cattunnel command managed by another installer: $COMMAND_PATH" >&2
    exit 1
  }
fi
SERVICE_USER="${SUDO_USER:-root}"
if [ -f "$COMMAND_USER_PATH" ]; then IFS= read -r SERVICE_USER < "$COMMAND_USER_PATH"; fi
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
if [ -z "$CONFIG_PATH" ] && [ -f "$COMMAND_CONFIG_PATH" ]; then IFS= read -r CONFIG_PATH < "$COMMAND_CONFIG_PATH"; fi
if [ -z "$CONFIG_PATH" ] && [ -f "$UNIT_PATH" ]; then
  CONFIG_PATH="$(sed -n 's/^ExecStart=.* -config "\([^"]*\)".*/\1/p; s/^ExecStart=.* -config \([^" ][^ ]*\).*/\1/p' "$UNIT_PATH" | head -n 1)"
  test -n "$CONFIG_PATH" || { echo "Existing service uses a custom command; pass its absolute config path as the first argument." >&2; exit 1; }
fi
CONFIG_PATH="${CONFIG_PATH:-$SERVICE_HOME/.config/1cat-tunnel/client-linux.json}"
case "$CONFIG_PATH" in /*) ;; *) echo "Config path must be absolute" >&2; exit 1 ;; esac
case "$CONFIG_PATH" in *'
'*) echo "Config path must not contain a newline" >&2; exit 1 ;; esac
CONFIG_DIR="$(dirname -- "$CONFIG_PATH")"
DEFAULT_CONFIG_PATH="$SERVICE_HOME/.config/1cat-tunnel/client-linux.json"

# Repair only the service owner's conventional config directories. Custom
# paths retain strict validation and are never chmodded by this installer.
if [ "$CONFIG_PATH" = "$DEFAULT_CONFIG_PATH" ]; then
  SERVICE_UID="$(id -u "$SERVICE_USER")"
  secure_owned_directory() {
    path="$1"
    mode="$2"
    [ ! -L "$path" ] || { echo "Refusing linked private config directory: $path" >&2; exit 1; }
    if [ -e "$path" ]; then
      [ -d "$path" ] || { echo "Private config path is not a directory: $path" >&2; exit 1; }
      [ "$(stat -c %u -- "$path")" = "$SERVICE_UID" ] || {
        echo "Private config directory is not owned by $SERVICE_USER: $path" >&2
        exit 1
      }
    else
      runuser -u "$SERVICE_USER" -- mkdir -m 0700 -- "$path"
    fi
    if [ "$mode" = private ]; then
      runuser -u "$SERVICE_USER" -- chmod 0700 -- "$path"
    else
      runuser -u "$SERVICE_USER" -- chmod go-w -- "$path"
    fi
  }
  secure_owned_directory "$SERVICE_HOME/.config" parent
  secure_owned_directory "$CONFIG_DIR" private
fi
# Never chmod/chown customer-controlled paths with root privileges.
if ! runuser -u "$SERVICE_USER" -- "$BINARY" -initialize-config -config "$CONFIG_PATH"; then
  echo "Config files and parent directories must belong to $SERVICE_USER and must not be writable by other users." >&2
  exit 1
fi
if [ "$MODE" != install ]; then
  runuser -u "$SERVICE_USER" -- "$BINARY" -validate-config -config "$CONFIG_PATH" || {
    echo 'Complete 1cattunnel -setup before registering or starting a service.' >&2; exit 1;
  }
fi
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
if [ -f "$COMMAND_PATH" ]; then install -m 0755 "$COMMAND_PATH" "$BACKUP/1cattunnel"; fi
if [ -f "$COMMAND_CONFIG_PATH" ]; then install -m 0644 "$COMMAND_CONFIG_PATH" "$BACKUP/client-config-path"; fi
if [ -f "$COMMAND_USER_PATH" ]; then install -m 0644 "$COMMAND_USER_PATH" "$BACKUP/service-user"; fi

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
    else
      rm -f -- "$INSTALL_DIR/tunnel-client"
    fi
    if [ -f "$BACKUP/1cattunnel" ]; then
      install -m 0755 "$BACKUP/1cattunnel" "$COMMAND_PATH"
    else
      rm -f -- "$COMMAND_PATH"
    fi
    if [ -f "$BACKUP/client-config-path" ]; then
      install -m 0644 "$BACKUP/client-config-path" "$COMMAND_CONFIG_PATH"
    else
      rm -f -- "$COMMAND_CONFIG_PATH"
    fi
    if [ -f "$BACKUP/service-user" ]; then
      install -m 0644 "$BACKUP/service-user" "$COMMAND_USER_PATH"
    else
      rm -f -- "$COMMAND_USER_PATH"
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
Restart=on-failure
RestartSec=5
RestartPreventExitStatus=75
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
cat > "$INSTALL_DIR/1cattunnel.next" <<'ONECAT_COMMAND'
#!/bin/sh
# 1cattunnel-system-client-launcher
set -eu
BINARY=/usr/local/lib/1cat-tunnel/tunnel-client
CONFIG_FILE=/usr/local/lib/1cat-tunnel/client-config-path
USER_FILE=/usr/local/lib/1cat-tunnel/service-user
[ -x "$BINARY" ] || { echo "1cattunnel client is not installed correctly: $BINARY" >&2; exit 1; }
[ -r "$CONFIG_FILE" ] && [ -r "$USER_FILE" ] || { echo '1cattunnel service metadata is missing; reinstall the client.' >&2; exit 1; }
IFS= read -r DEFAULT_CONFIG < "$CONFIG_FILE"
IFS= read -r SERVICE_USER < "$USER_FILE"
[ -n "$DEFAULT_CONFIG" ] && [ -n "$SERVICE_USER" ] || { echo '1cattunnel service metadata is invalid; reinstall the client.' >&2; exit 1; }
case "${1:-}" in
  --version|-version) exec "$BINARY" -version ;;
  help|-h|--help)
    cat <<'ONECAT_HELP'
Usage:
  1cattunnel                    Replace the previous client and run in this terminal
  1cattunnel status             Show live connection and mapping status
  1cattunnel panel              Print the private Web management login URL
  1cattunnel -setup             Configure, register autostart, and run in this terminal
  1cattunnel monitor            Watch live status and logs without restarting
  1cattunnel desktop            Open the desktop status/log window
  1cattunnel -config /path.json Run with an explicit config file
  1cattunnel --version          Show the native client version
ONECAT_HELP
    exit 0
    ;;
esac
uses_custom_config=0
for argument do
  case "$argument" in -config|--config|-config=*|--config=*) uses_custom_config=1 ;; esac
done
if [ "$uses_custom_config" -eq 0 ] && [ "$(id -un)" != "$SERVICE_USER" ]; then
  echo "The managed client belongs to user '$SERVICE_USER'. Run this command as that user." >&2
  exit 1
fi
case "${1:-}" in
  panel) shift; exec "$BINARY" -panel -config "$DEFAULT_CONFIG" "$@" ;;
  status) shift; exec "$BINARY" -status -config "$DEFAULT_CONFIG" "$@" ;;
  monitor|logs) shift; exec "$BINARY" -monitor -config "$DEFAULT_CONFIG" "$@" ;;
  desktop) shift; exec "$BINARY" -desktop -config "$DEFAULT_CONFIG" "$@" ;;
  config|setup) shift; exec "$BINARY" -setup -config "$DEFAULT_CONFIG" "$@" ;;
  *) exec "$BINARY" -config "$DEFAULT_CONFIG" "$@" ;;
esac
ONECAT_COMMAND
chmod 0755 "$INSTALL_DIR/1cattunnel.next"
printf '%s\n' "$CONFIG_PATH" > "$INSTALL_DIR/client-config-path.next"
printf '%s\n' "$SERVICE_USER" > "$INSTALL_DIR/service-user.next"
chmod 0644 "$INSTALL_DIR/client-config-path.next" "$INSTALL_DIR/service-user.next"
# On a fresh machine the final executable does not exist yet. Verify the
# staged executable, while retaining the canonical path in the installed unit.
sed "s|^ExecStart=\"$INSTALL_DIR/tunnel-client\"|ExecStart=\"$INSTALL_DIR/tunnel-client.next\"|" "$UNIT_PATH.next" > "$BACKUP/candidate.service"
systemd-analyze verify "$BACKUP/candidate.service"
rollback_needed=1
mv -f "$INSTALL_DIR/tunnel-client.next" "$INSTALL_DIR/tunnel-client"
if [ -f "$BACKUP/previous.service" ] || [ "$MODE" != install ]; then
  mv -f "$UNIT_PATH.next" "$UNIT_PATH"
  systemctl daemon-reload
  if [ "$WAS_ENABLED" -eq 1 ] || [ "$MODE" != install ]; then systemctl enable "$UNIT_NAME"; fi
  if [ "$MODE" = start ] || { [ "$MODE" = install ] && [ "$WAS_ACTIVE" -eq 1 ]; }; then
    systemctl restart "$UNIT_NAME"
    systemctl is-active --quiet "$UNIT_NAME"
    attempt=0
    until runuser -u "$SERVICE_USER" -- "$INSTALL_DIR/tunnel-client" -status -config "$CONFIG_PATH" >/dev/null 2>&1; do
      attempt=$((attempt + 1))
      [ "$attempt" -lt 10 ] || { echo "Authenticated health check failed" >&2; exit 1; }
      sleep 1
    done
  fi
else
  rm -f -- "$UNIT_PATH.next"
fi
mv -f "$INSTALL_DIR/client-config-path.next" "$COMMAND_CONFIG_PATH"
mv -f "$INSTALL_DIR/service-user.next" "$COMMAND_USER_PATH"
install -m 0755 "$INSTALL_DIR/1cattunnel.next" "$COMMAND_PATH.next.$$"
mv -f "$COMMAND_PATH.next.$$" "$COMMAND_PATH"
runuser -u "$SERVICE_USER" -- "$COMMAND_PATH" --version >/dev/null
install -m 0755 "$0" "$INSTALL_DIR/register-service.sh.next"
mv -f "$INSTALL_DIR/register-service.sh.next" "$INSTALL_DIR/register-service.sh"
install -m 0644 "$DIR/1cat-tunnel-ca.pem" "$INSTALL_DIR/1cat-tunnel-ca.pem.next"
mv -f "$INSTALL_DIR/1cat-tunnel-ca.pem.next" "$INSTALL_DIR/1cat-tunnel-ca.pem"
printf '%s\n' "$UNIT_NAME" > "$INSTALL_DIR/service-unit"
chmod 0644 "$INSTALL_DIR/service-unit"
rollback_needed=0
echo "Local management panel: http://127.0.0.1:51888/"
echo "Config: $CONFIG_PATH"
echo "Upgrade backup: $BACKUP"
echo "Command installed: $COMMAND_PATH"
echo "CLI setup: $COMMAND_PATH -setup"
echo "Custom config: $COMMAND_PATH -config /absolute/path/client.json"
if [ "$MODE" = register ]; then
  echo 'Autostart registered. The current terminal will run the client now.'
elif [ ! -f "$BACKUP/previous.service" ] && [ "$MODE" = install ]; then
  echo 'Installed without starting or binding systemd. Next: 1cattunnel -setup'
fi
