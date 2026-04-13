#!/bin/sh
set -eu

DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
chmod +x "$DIR/1cat-tunnel-client" 2>/dev/null || true

exec "$DIR/1cat-tunnel-client" -config "$DIR/client-linux.json" "$@"
