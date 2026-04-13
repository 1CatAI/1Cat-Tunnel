#!/bin/sh
set -eu

DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
chmod +x "$DIR/tunnel-server" 2>/dev/null || true

exec "$DIR/tunnel-server" -config "$DIR/server.json" "$@"
